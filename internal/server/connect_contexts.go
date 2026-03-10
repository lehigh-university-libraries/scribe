package server

import (
	"context"
	"encoding/json"
	"fmt"

	"connectrpc.com/connect"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// --- ContextService Connect handlers ---

func (h *Handler) ListContexts(ctx context.Context, req *connect.Request[scribev1.ListContextsRequest]) (*connect.Response[scribev1.ListContextsResponse], error) {
	contexts, err := h.contexts.List(ctx, req.Msg.GetSystemOnly())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &scribev1.ListContextsResponse{
		Contexts: make([]*scribev1.Context, 0, len(contexts)),
	}
	for _, c := range contexts {
		resp.Contexts = append(resp.Contexts, storeContextToProto(c))
	}
	return connect.NewResponse(resp), nil
}

func (h *Handler) GetContext(ctx context.Context, req *connect.Request[scribev1.GetContextRequest]) (*connect.Response[scribev1.GetContextResponse], error) {
	c, err := h.contexts.Get(ctx, req.Msg.GetContextId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("context not found"))
	}
	return connect.NewResponse(&scribev1.GetContextResponse{Context: storeContextToProto(c)}), nil
}

func (h *Handler) CreateContext(ctx context.Context, req *connect.Request[scribev1.CreateContextRequest]) (*connect.Response[scribev1.CreateContextResponse], error) {
	c, err := h.contexts.Create(ctx, protoContextToStore(req.Msg.GetContext()))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&scribev1.CreateContextResponse{Context: storeContextToProto(c)}), nil
}

func (h *Handler) UpdateContext(ctx context.Context, req *connect.Request[scribev1.UpdateContextRequest]) (*connect.Response[scribev1.UpdateContextResponse], error) {
	c, err := h.contexts.Update(ctx, protoContextToStore(req.Msg.GetContext()))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&scribev1.UpdateContextResponse{Context: storeContextToProto(c)}), nil
}

func (h *Handler) DeleteContext(ctx context.Context, req *connect.Request[scribev1.DeleteContextRequest]) (*connect.Response[scribev1.DeleteContextResponse], error) {
	if err := h.contexts.Delete(ctx, req.Msg.GetContextId()); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&scribev1.DeleteContextResponse{}), nil
}

func (h *Handler) ListSelectionRules(ctx context.Context, req *connect.Request[scribev1.ListSelectionRulesRequest]) (*connect.Response[scribev1.ListSelectionRulesResponse], error) {
	rules, err := h.contexts.ListRules(ctx, req.Msg.GetContextId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &scribev1.ListSelectionRulesResponse{
		Rules: make([]*scribev1.ContextSelectionRule, 0, len(rules)),
	}
	for _, r := range rules {
		resp.Rules = append(resp.Rules, storeRuleToProto(r))
	}
	return connect.NewResponse(resp), nil
}

func (h *Handler) CreateSelectionRule(ctx context.Context, req *connect.Request[scribev1.CreateSelectionRuleRequest]) (*connect.Response[scribev1.CreateSelectionRuleResponse], error) {
	r, err := h.contexts.CreateRule(ctx, protoRuleToStore(req.Msg.GetRule()))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&scribev1.CreateSelectionRuleResponse{Rule: storeRuleToProto(r)}), nil
}

func (h *Handler) DeleteSelectionRule(ctx context.Context, req *connect.Request[scribev1.DeleteSelectionRuleRequest]) (*connect.Response[scribev1.DeleteSelectionRuleResponse], error) {
	if err := h.contexts.DeleteRule(ctx, req.Msg.GetRuleId()); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&scribev1.DeleteSelectionRuleResponse{}), nil
}

func (h *Handler) ResolveContext(ctx context.Context, req *connect.Request[scribev1.ResolveContextRequest]) (*connect.Response[scribev1.ResolveContextResponse], error) {
	var metadata map[string]any
	if raw := req.Msg.GetMetadataJson(); raw != "" {
		if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid metadata json"))
		}
	}
	c, isDefault, err := h.contexts.Resolve(ctx, metadata)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&scribev1.ResolveContextResponse{
		Context:   storeContextToProto(c),
		IsDefault: isDefault,
	}), nil
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
		PostProcessingSteps:   c.PostProcessingSteps,
		CreatedAt:             timestamppb.New(c.CreatedAt).AsTime().String(),
		UpdatedAt:             timestamppb.New(c.UpdatedAt).AsTime().String(),
	}
	if c.UserID != nil {
		proto.UserId = *c.UserID
	}
	if c.Temperature != nil {
		proto.Temperature = *c.Temperature
	} else {
		proto.Temperature = -1
	}
	for _, pp := range c.ImagePreprocessors {
		proto.ImagePreprocessors = append(proto.ImagePreprocessors, &scribev1.ImagePreprocessor{
			Type:   pp.Type,
			Params: marshalJSONOrEmpty(pp.Params),
		})
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
		PostProcessingSteps:   p.GetPostProcessingSteps(),
	}
	if p.GetUserId() > 0 {
		uid := p.GetUserId()
		c.UserID = &uid
	}
	if p.GetTemperature() >= 0 {
		t := p.GetTemperature()
		c.Temperature = &t
	}
	for _, pp := range p.GetImagePreprocessors() {
		var params map[string]any
		if pp.GetParams() != "" {
			_ = json.Unmarshal([]byte(pp.GetParams()), &params)
		}
		c.ImagePreprocessors = append(c.ImagePreprocessors, store.ImagePreprocessor{
			Type:   pp.GetType(),
			Params: params,
		})
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

func marshalJSONOrEmpty(v any) string {
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
