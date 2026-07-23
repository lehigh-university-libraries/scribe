package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/providerregistry"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// --- ContextService Connect handlers ---

func (h *Handler) ListContexts(ctx context.Context, req *connect.Request[scribev1.ListContextsRequest]) (*connect.Response[scribev1.ListContextsResponse], error) {
	if h.itemPageTokens == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("context pagination is not configured"))
	}
	workspaceID := h.currentWorkspaceID(ctx)
	pageSize, cursor, err := normalizeContextPageRequest(req.Msg.GetPageSize(), req.Msg.GetPageToken(), workspaceID, req.Msg.GetSystemOnly(), h.itemPageTokens)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	page, err := h.contexts.ListPageForWorkspace(ctx, workspaceID, req.Msg.GetSystemOnly(), pageSize, cursor)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	nextPageToken, err := h.itemPageTokens.encodeContextPage(page.NextCursor, workspaceID, req.Msg.GetSystemOnly())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("encode context page token"))
	}
	resp := &scribev1.ListContextsResponse{
		Contexts:      make([]*scribev1.Context, 0, len(page.Contexts)),
		NextPageToken: nextPageToken,
	}
	for _, c := range page.Contexts {
		resp.Contexts = append(resp.Contexts, storeContextToProto(c))
	}
	return connect.NewResponse(resp), nil
}

func (h *Handler) GetContext(ctx context.Context, req *connect.Request[scribev1.GetContextRequest]) (*connect.Response[scribev1.GetContextResponse], error) {
	c, err := h.contextForRead(ctx, req.Msg.GetContextId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("context not found"))
	}
	return connect.NewResponse(&scribev1.GetContextResponse{Context: storeContextToProto(c)}), nil
}

func (h *Handler) CreateContext(ctx context.Context, req *connect.Request[scribev1.CreateContextRequest]) (*connect.Response[scribev1.CreateContextResponse], error) {
	contextValue := protoContextToStore(req.Msg.GetContext())
	if err := validateContextSelection(contextValue); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	userID := h.currentUserID(ctx)
	workspaceID := h.currentWorkspaceID(ctx)
	contextValue.UserID = &userID
	contextValue.WorkspaceID = &workspaceID
	c, err := h.contexts.Create(ctx, contextValue)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&scribev1.CreateContextResponse{Context: storeContextToProto(c)}), nil
}

func (h *Handler) UpdateContext(ctx context.Context, req *connect.Request[scribev1.UpdateContextRequest]) (*connect.Response[scribev1.UpdateContextResponse], error) {
	contextValue := protoContextToStore(req.Msg.GetContext())
	if err := validateContextSelection(contextValue); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	c, err := h.contexts.UpdateForWorkspace(ctx, contextValue, h.currentWorkspaceID(ctx), h.currentUserID(ctx))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&scribev1.UpdateContextResponse{Context: storeContextToProto(c)}), nil
}

func (h *Handler) DeleteContext(ctx context.Context, req *connect.Request[scribev1.DeleteContextRequest]) (*connect.Response[scribev1.DeleteContextResponse], error) {
	if err := h.contexts.DeleteForWorkspace(ctx, req.Msg.GetContextId(), h.currentWorkspaceID(ctx)); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("context not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&scribev1.DeleteContextResponse{}), nil
}

func (h *Handler) ListSelectionRules(ctx context.Context, req *connect.Request[scribev1.ListSelectionRulesRequest]) (*connect.Response[scribev1.ListSelectionRulesResponse], error) {
	if h.itemPageTokens == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("selection rule pagination is not configured"))
	}
	workspaceID := h.currentWorkspaceID(ctx)
	pageSize, cursor, err := normalizeSelectionRulePageRequest(req.Msg.GetPageSize(), req.Msg.GetPageToken(), workspaceID, req.Msg.GetContextId(), h.itemPageTokens)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	page, err := h.contexts.ListRulePageForWorkspace(ctx, workspaceID, req.Msg.GetContextId(), pageSize, cursor)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	nextPageToken, err := h.itemPageTokens.encodeSelectionRulePage(page.NextCursor, workspaceID, req.Msg.GetContextId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("encode selection rule page token"))
	}
	resp := &scribev1.ListSelectionRulesResponse{
		Rules:         make([]*scribev1.ContextSelectionRule, 0, len(page.Rules)),
		NextPageToken: nextPageToken,
	}
	for _, r := range page.Rules {
		resp.Rules = append(resp.Rules, storeRuleToProto(r))
	}
	return connect.NewResponse(resp), nil
}

func (h *Handler) CreateSelectionRule(ctx context.Context, req *connect.Request[scribev1.CreateSelectionRuleRequest]) (*connect.Response[scribev1.CreateSelectionRuleResponse], error) {
	r, err := h.contexts.CreateRuleForWorkspace(ctx, h.currentWorkspaceID(ctx), protoRuleToStore(req.Msg.GetRule()))
	if err != nil {
		if errors.Is(err, store.ErrSelectionRuleLimit) {
			return nil, connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("workspace selection rule limit reached"))
		}
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("context not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&scribev1.CreateSelectionRuleResponse{Rule: storeRuleToProto(r)}), nil
}

func (h *Handler) DeleteSelectionRule(ctx context.Context, req *connect.Request[scribev1.DeleteSelectionRuleRequest]) (*connect.Response[scribev1.DeleteSelectionRuleResponse], error) {
	if err := h.contexts.DeleteRuleForWorkspace(ctx, h.currentWorkspaceID(ctx), req.Msg.GetRuleId()); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&scribev1.DeleteSelectionRuleResponse{}), nil
}

func (h *Handler) ResolveContext(ctx context.Context, req *connect.Request[scribev1.ResolveContextRequest]) (*connect.Response[scribev1.ResolveContextResponse], error) {
	metadata, err := decodeContextMetadataJSON(req.Msg.GetMetadataJson())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("metadata_json must be a bounded flat JSON object"))
	}
	c, isDefault, err := h.contexts.ResolveForWorkspace(ctx, h.currentWorkspaceID(ctx), metadata)
	if err != nil {
		return nil, resolveContextConnectError(err)
	}
	return connect.NewResponse(&scribev1.ResolveContextResponse{
		Context:   storeContextToProto(c),
		IsDefault: isDefault,
	}), nil
}

func (h *Handler) GetModelCatalog(ctx context.Context, req *connect.Request[scribev1.GetModelCatalogRequest]) (*connect.Response[scribev1.GetModelCatalogResponse], error) {
	registry := providerregistry.New(config.Get().Config)
	catalog := registry.Catalog()
	response := &scribev1.GetModelCatalogResponse{}
	for _, descriptor := range catalog.TranscriptionProviders {
		provider := &scribev1.ProviderDescriptor{
			Id:                   descriptor.ID,
			Label:                descriptor.Label,
			RequiresApiKey:       descriptor.RequiresAPIKey,
			SupportsSystemPrompt: descriptor.SupportsSystemPrompt,
			SupportsTemperature:  descriptor.SupportsTemperature,
		}
		for _, model := range descriptor.Models {
			provider.Models = append(provider.Models, modelDescriptorToProto(model))
		}
		response.TranscriptionProviders = append(response.TranscriptionProviders, provider)
	}
	for _, model := range catalog.SegmentationModels {
		response.SegmentationModels = append(response.SegmentationModels, modelDescriptorToProto(model))
	}
	return connect.NewResponse(response), nil
}

func (h *Handler) GetContextMetrics(ctx context.Context, req *connect.Request[scribev1.GetContextMetricsRequest]) (*connect.Response[scribev1.GetContextMetricsResponse], error) {
	contextID := req.Msg.GetContextId()
	if _, err := h.contextForRead(ctx, contextID); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("context not found"))
	}
	metric, err := h.ocrRuns.GetContextMetrics(ctx, h.currentWorkspaceID(ctx), contextID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get context metrics: %w", err))
	}
	totalRuns, err := uint64FromNonNegativeInt64(metric.TotalRuns)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get context metrics: invalid total run count"))
	}
	correctedRuns, err := uint64FromNonNegativeInt64(metric.CorrectedRuns)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get context metrics: invalid corrected run count"))
	}
	return connect.NewResponse(&scribev1.GetContextMetricsResponse{Metrics: &scribev1.ContextMetrics{
		ContextId:              metric.ContextID,
		TotalRuns:              totalRuns,
		CorrectedRuns:          correctedRuns,
		AvgLevenshteinDistance: metric.AvgLevenshteinDistance,
	}}), nil
}

func modelDescriptorToProto(model providerregistry.Model) *scribev1.ModelDescriptor {
	return &scribev1.ModelDescriptor{Id: model.ID, Label: model.Label, IsDefault: model.IsDefault}
}

func validateContextSelection(contextValue store.Context) error {
	registry := providerregistry.New(config.Get().Config)
	if err := registry.ValidateSegmentation(contextValue.SegmentationModel); err != nil {
		return err
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
