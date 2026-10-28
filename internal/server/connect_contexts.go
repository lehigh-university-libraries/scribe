package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect/v2"
	"github.com/lehigh-university-libraries/htr/pkg/segeval"
	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/providerregistry"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// --- ContextService Connect handlers ---

func (h *Handler) ListContexts(ctx context.Context, req *scribev1.ListContextsRequest) (*scribev1.ListContextsResponse, error) {
	if h.itemPageTokens == nil {
		return nil, connect.Errorf(connect.CodeInternal, "context pagination is not configured")
	}
	workspaceID := h.currentWorkspaceID(ctx)
	pageSize, cursor, err := normalizeContextPageRequest(req.GetPageSize(), req.GetPageToken(), workspaceID, req.GetSystemOnly(), h.itemPageTokens)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error()).WithCause(err)
	}
	page, err := h.contexts.ListPageForWorkspace(ctx, workspaceID, req.GetSystemOnly(), pageSize, cursor)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error()).WithCause(err)
	}
	nextPageToken, err := h.itemPageTokens.encodeContextPage(page.NextCursor, workspaceID, req.GetSystemOnly())
	if err != nil {
		return nil, connect.Errorf(connect.CodeInternal, "encode context page token")
	}
	resp := &scribev1.ListContextsResponse{
		Contexts:      make([]*scribev1.Context, 0, len(page.Contexts)),
		NextPageToken: nextPageToken,
	}
	for _, c := range page.Contexts {
		resp.Contexts = append(resp.Contexts, storeContextToProto(c))
	}
	return resp, nil
}

func (h *Handler) GetContext(ctx context.Context, req *scribev1.GetContextRequest) (*scribev1.GetContextResponse, error) {
	c, err := h.contextForRead(ctx, req.GetContextId())
	if err != nil {
		return nil, connect.Errorf(connect.CodeNotFound, "context not found")
	}
	return &scribev1.GetContextResponse{Context: storeContextToProto(c)}, nil
}

func (h *Handler) CreateContext(ctx context.Context, req *scribev1.CreateContextRequest) (*scribev1.CreateContextResponse, error) {
	contextValue := protoContextToStore(req.GetContext())
	if err := validateContextSelection(contextValue); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error()).WithCause(err)
	}
	userID := h.currentUserID(ctx)
	workspaceID := h.currentWorkspaceID(ctx)
	contextValue.UserID = &userID
	contextValue.WorkspaceID = &workspaceID
	c, err := h.contexts.Create(ctx, contextValue)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error()).WithCause(err)
	}
	return &scribev1.CreateContextResponse{Context: storeContextToProto(c)}, nil
}

func (h *Handler) UpdateContext(ctx context.Context, req *scribev1.UpdateContextRequest) (*scribev1.UpdateContextResponse, error) {
	contextValue := protoContextToStore(req.GetContext())
	if err := validateContextSelection(contextValue); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error()).WithCause(err)
	}
	c, err := h.contexts.UpdateForWorkspace(ctx, contextValue, h.currentWorkspaceID(ctx), h.currentUserID(ctx))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error()).WithCause(err)
	}
	return &scribev1.UpdateContextResponse{Context: storeContextToProto(c)}, nil
}

func (h *Handler) DeleteContext(ctx context.Context, req *scribev1.DeleteContextRequest) (*scribev1.DeleteContextResponse, error) {
	if err := h.contexts.DeleteForWorkspace(ctx, req.GetContextId(), h.currentWorkspaceID(ctx)); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.Errorf(connect.CodeNotFound, "context not found")
		}
		return nil, connect.NewError(connect.CodeInternal, err.Error()).WithCause(err)
	}
	return &scribev1.DeleteContextResponse{}, nil
}

func (h *Handler) ListSelectionRules(ctx context.Context, req *scribev1.ListSelectionRulesRequest) (*scribev1.ListSelectionRulesResponse, error) {
	if h.itemPageTokens == nil {
		return nil, connect.Errorf(connect.CodeInternal, "selection rule pagination is not configured")
	}
	workspaceID := h.currentWorkspaceID(ctx)
	pageSize, cursor, err := normalizeSelectionRulePageRequest(req.GetPageSize(), req.GetPageToken(), workspaceID, req.GetContextId(), h.itemPageTokens)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error()).WithCause(err)
	}
	page, err := h.contexts.ListRulePageForWorkspace(ctx, workspaceID, req.GetContextId(), pageSize, cursor)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error()).WithCause(err)
	}
	nextPageToken, err := h.itemPageTokens.encodeSelectionRulePage(page.NextCursor, workspaceID, req.GetContextId())
	if err != nil {
		return nil, connect.Errorf(connect.CodeInternal, "encode selection rule page token")
	}
	resp := &scribev1.ListSelectionRulesResponse{
		Rules:         make([]*scribev1.ContextSelectionRule, 0, len(page.Rules)),
		NextPageToken: nextPageToken,
	}
	for _, r := range page.Rules {
		resp.Rules = append(resp.Rules, storeRuleToProto(r))
	}
	return resp, nil
}

func (h *Handler) CreateSelectionRule(ctx context.Context, req *scribev1.CreateSelectionRuleRequest) (*scribev1.CreateSelectionRuleResponse, error) {
	r, err := h.contexts.CreateRuleForWorkspace(ctx, h.currentWorkspaceID(ctx), protoRuleToStore(req.GetRule()))
	if err != nil {
		if errors.Is(err, store.ErrSelectionRuleLimit) {
			return nil, connect.Errorf(connect.CodeResourceExhausted, "workspace selection rule limit reached")
		}
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.Errorf(connect.CodeNotFound, "context not found")
		}
		return nil, connect.NewError(connect.CodeInternal, err.Error()).WithCause(err)
	}
	return &scribev1.CreateSelectionRuleResponse{Rule: storeRuleToProto(r)}, nil
}

func (h *Handler) DeleteSelectionRule(ctx context.Context, req *scribev1.DeleteSelectionRuleRequest) (*scribev1.DeleteSelectionRuleResponse, error) {
	if err := h.contexts.DeleteRuleForWorkspace(ctx, h.currentWorkspaceID(ctx), req.GetRuleId()); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error()).WithCause(err)
	}
	return &scribev1.DeleteSelectionRuleResponse{}, nil
}

func (h *Handler) ResolveContext(ctx context.Context, req *scribev1.ResolveContextRequest) (*scribev1.ResolveContextResponse, error) {
	metadata, err := decodeContextMetadataJSON(req.GetMetadataJson())
	if err != nil {
		return nil, connect.Errorf(connect.CodeInvalidArgument, "metadata_json must be a bounded flat JSON object")
	}
	c, isDefault, err := h.contexts.ResolveForWorkspace(ctx, h.currentWorkspaceID(ctx), metadata)
	if err != nil {
		return nil, resolveContextConnectError(err)
	}
	return &scribev1.ResolveContextResponse{
		Context:   storeContextToProto(c),
		IsDefault: isDefault,
	}, nil
}

func (h *Handler) GetModelCatalog(ctx context.Context, req *scribev1.GetModelCatalogRequest) (*scribev1.GetModelCatalogResponse, error) {
	registry := providerregistry.New(config.Get().Config)
	catalog := registry.Catalog()
	response := &scribev1.GetModelCatalogResponse{}
	for _, p := range segeval.Profiles() {
		response.DocumentTypes = append(response.DocumentTypes, &scribev1.DocumentTypeDescriptor{Id: p.Name, Description: p.Description, Calibrated: p.Calibrated})
	}
	for _, descriptor := range catalog.TranscriptionProviders {
		provider := &scribev1.ProviderDescriptor{
			Id:                   descriptor.ID,
			Label:                descriptor.Label,
			RequiresApiKey:       descriptor.RequiresAPIKey,
			SupportsSystemPrompt: descriptor.SupportsSystemPrompt,
		}
		for _, model := range descriptor.Models {
			provider.Models = append(provider.Models, modelDescriptorToProto(model))
		}
		response.TranscriptionProviders = append(response.TranscriptionProviders, provider)
	}
	for _, model := range catalog.SegmentationModels {
		response.SegmentationModels = append(response.SegmentationModels, modelDescriptorToProto(model))
	}
	return response, nil
}

func (h *Handler) GetContextMetrics(ctx context.Context, req *scribev1.GetContextMetricsRequest) (*scribev1.GetContextMetricsResponse, error) {
	contextID := req.GetContextId()
	if _, err := h.contextForRead(ctx, contextID); err != nil {
		return nil, connect.Errorf(connect.CodeNotFound, "context not found")
	}
	metric, err := h.ocrRuns.GetContextMetrics(ctx, h.currentWorkspaceID(ctx), contextID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get context metrics: %w", err).Error()).WithCause(err)
	}
	totalRuns, err := uint64FromNonNegativeInt64(metric.TotalRuns)
	if err != nil {
		return nil, connect.Errorf(connect.CodeInternal, "get context metrics: invalid total run count")
	}
	correctedRuns, err := uint64FromNonNegativeInt64(metric.CorrectedRuns)
	if err != nil {
		return nil, connect.Errorf(connect.CodeInternal, "get context metrics: invalid corrected run count")
	}
	return &scribev1.GetContextMetricsResponse{Metrics: &scribev1.ContextMetrics{
		InputTokens: metric.InputTokens, OutputTokens: metric.OutputTokens, ThoughtTokens: metric.ThoughtTokens, BilledOutputTokens: metric.OutputTokens + metric.ThoughtTokens,
		ContextId:              metric.ContextID,
		TotalRuns:              totalRuns,
		CorrectedRuns:          correctedRuns,
		AvgLevenshteinDistance: metric.AvgLevenshteinDistance,
	}}, nil
}

func modelDescriptorToProto(model providerregistry.Model) *scribev1.ModelDescriptor {
	return &scribev1.ModelDescriptor{
		Id:                  model.ID,
		Label:               model.Label,
		IsDefault:           model.IsDefault,
		SupportsTemperature: model.SupportsTemperature,
	}
}

func validateContextSelection(contextValue store.Context) error {
	if _, ok := segeval.Lookup(contextValue.DocumentProfile()); !ok {
		return fmt.Errorf("unknown document type")
	}
	registry := providerregistry.New(config.Get().Config)
	if err := registry.ValidateSegmentation(contextValue.SegmentationModel); err != nil {
		return err
	}
	if segmentor, _, _, err := registry.ResolveSegmentor(contextValue.SegmentationModel); err == nil && segmentor.Capabilities.OutputGranularity == "region" {
		return fmt.Errorf("this segmentor provides regions only; select layout+kraken for line transcription")
	}
	if strings.EqualFold(contextValue.TranscriptionProvider, "kraken") && contextValue.SegmentationModel != "kraken" {
		return fmt.Errorf("native Kraken recognition requires BLLA segmentation")
	}
	return registry.ValidateSelection(
		contextValue.TranscriptionProvider,
		contextValue.TranscriptionModel,
		contextValue.SystemPrompt,
		contextValue.Temperature,
	)
}

// --- proto ↔ store conversion ---

func storeContextToProto(c store.Context) *scribev1.Context {
	proto := &scribev1.Context{
		DocumentType:          c.DocumentProfile(),
		Id:                    c.ID,
		Name:                  c.Name,
		Description:           c.Description,
		IsDefault:             c.IsDefault,
		SegmentationModel:     c.SegmentationModel,
		TranscriptionProvider: c.TranscriptionProvider,
		TranscriptionModel:    c.TranscriptionModel,
		SystemPrompt:          c.SystemPrompt,
		Temperature:           c.Temperature,
		CreatedAt:             timestamppb.New(c.CreatedAt).AsTime().String(),
		UpdatedAt:             timestamppb.New(c.UpdatedAt).AsTime().String(),
	}
	if c.UserID != nil {
		proto.UserId = *c.UserID
	}
	return proto
}

func protoContextToStore(p *scribev1.Context) store.Context {
	if p == nil {
		return store.Context{}
	}
	c := store.Context{
		DocumentType:          p.GetDocumentType(),
		ID:                    p.GetId(),
		Name:                  p.GetName(),
		Description:           p.GetDescription(),
		IsDefault:             p.GetIsDefault(),
		SegmentationModel:     p.GetSegmentationModel(),
		TranscriptionProvider: p.GetTranscriptionProvider(),
		TranscriptionModel:    p.GetTranscriptionModel(),
		SystemPrompt:          p.GetSystemPrompt(),
	}
	if p.GetUserId() > 0 {
		uid := p.GetUserId()
		c.UserID = &uid
	}
	if p.Temperature != nil {
		t := *p.Temperature
		c.Temperature = &t
	}
	return c
}

func storeRuleToProto(r store.ContextSelectionRule) *scribev1.ContextSelectionRule {
	proto := &scribev1.ContextSelectionRule{
		Id:        r.ID,
		ContextId: r.ContextID,
		Priority:  r.Priority,
	}
	for _, cond := range r.Conditions {
		proto.Conditions = append(proto.Conditions, &scribev1.RuleCondition{
			Field:    cond.Field,
			Operator: cond.Operator,
			Value:    cond.Value,
		})
	}
	return proto
}

func protoRuleToStore(p *scribev1.ContextSelectionRule) store.ContextSelectionRule {
	if p == nil {
		return store.ContextSelectionRule{}
	}
	r := store.ContextSelectionRule{
		ID:        p.GetId(),
		ContextID: p.GetContextId(),
		Priority:  p.GetPriority(),
	}
	for _, c := range p.GetConditions() {
		r.Conditions = append(r.Conditions, store.RuleCondition{
			Field:    c.GetField(),
			Operator: c.GetOperator(),
			Value:    c.GetValue(),
		})
	}
	return r
}
