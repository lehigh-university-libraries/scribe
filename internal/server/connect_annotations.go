package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	"connectrpc.com/connect"
	hocreditv1 "github.com/lehigh-university-libraries/hOCRedit/proto/hocredit/v1"
)

// --- AnnotationService Connect handlers ---

func (h *Handler) SearchAnnotations(ctx context.Context, req *connect.Request[hocreditv1.SearchAnnotationsRequest]) (*connect.Response[hocreditv1.SearchAnnotationsResponse], error) {
	canvasURI := strings.TrimSpace(req.Msg.GetCanvasUri())
	base := h.annotationBaseURL
	if base == "" {
		base = strings.TrimRight(strings.TrimSpace(os.Getenv("ANNOTATION_API_BASE")), "/")
	}
	if base == "" {
		base = "http://localhost:8080"
	}

	var items []any
	if canvasURI != "" {
		payloads, err := h.annotations.SearchByCanvas(ctx, canvasURI)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		for _, raw := range payloads {
			var obj map[string]any
			if err := json.Unmarshal([]byte(raw), &obj); err != nil {
				continue
			}
			items = append(items, normalizeAnnotation(obj, canvasURI))
		}
		if len(items) == 0 {
			bootstrap, err := h.bootstrapAnnotationsForCanvas(ctx, canvasURI, base)
			if err == nil {
				items = bootstrap
			}
		}
	}

	page := map[string]any{
		"@context": annotationPageContexts(),
		"id":       base + "/v1/annotations/3/search?canvasUri=" + url.QueryEscape(canvasURI),
		"type":     "AnnotationPage",
		"items":    items,
	}
	b, err := json.Marshal(page)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&hocreditv1.SearchAnnotationsResponse{
		AnnotationPageJson: string(b),
	}), nil
}

func (h *Handler) GetAnnotation(ctx context.Context, req *connect.Request[hocreditv1.GetAnnotationRequest]) (*connect.Response[hocreditv1.GetAnnotationResponse], error) {
	id := strings.TrimSpace(req.Msg.GetId())
	if id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("id is required"))
	}
	raw, err := h.annotations.Get(ctx, id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("annotation not found"))
	}
	return connect.NewResponse(&hocreditv1.GetAnnotationResponse{AnnotationJson: raw}), nil
}

func (h *Handler) CreateAnnotation(ctx context.Context, req *connect.Request[hocreditv1.CreateAnnotationRequest]) (*connect.Response[hocreditv1.CreateAnnotationResponse], error) {
	base := h.annotationBaseURL
	if base == "" {
		base = strings.TrimRight(strings.TrimSpace(os.Getenv("ANNOTATION_API_BASE")), "/")
	}
	if base == "" {
		base = "http://localhost:8080"
	}

	var anno map[string]any
	if err := json.Unmarshal([]byte(req.Msg.GetAnnotationJson()), &anno); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid annotation json"))
	}
	anno = normalizeAnnotation(anno, "")
	id := strings.TrimSpace(annStringValue(anno, "id"))
	if id == "" {
		id = base + "/v1/annotations/3/item/" + annRandomID()
		anno["id"] = id
		anno["@id"] = id
	}
	canvasURI := extractCanvasURI(anno)
	if canvasURI == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("annotation target missing canvas uri"))
	}
	payload, err := json.Marshal(anno)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := h.annotations.Upsert(ctx, id, canvasURI, string(payload)); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&hocreditv1.CreateAnnotationResponse{AnnotationJson: string(payload)}), nil
}

func (h *Handler) UpdateAnnotation(ctx context.Context, req *connect.Request[hocreditv1.UpdateAnnotationRequest]) (*connect.Response[hocreditv1.UpdateAnnotationResponse], error) {
	var anno map[string]any
	if err := json.Unmarshal([]byte(req.Msg.GetAnnotationJson()), &anno); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid annotation json"))
	}
	anno = normalizeAnnotation(anno, "")
	id := strings.TrimSpace(annStringValue(anno, "id"))
	if id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("annotation id is required"))
	}
	canvasURI := extractCanvasURI(anno)
	if canvasURI == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("annotation target missing canvas uri"))
	}
	payload, err := json.Marshal(anno)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	found, err := h.annotations.Update(ctx, id, canvasURI, string(payload))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if !found {
		if err := h.annotations.Upsert(ctx, id, canvasURI, string(payload)); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}
	return connect.NewResponse(&hocreditv1.UpdateAnnotationResponse{AnnotationJson: string(payload)}), nil
}

func (h *Handler) DeleteAnnotation(ctx context.Context, req *connect.Request[hocreditv1.DeleteAnnotationRequest]) (*connect.Response[hocreditv1.DeleteAnnotationResponse], error) {
	uri := strings.TrimSpace(req.Msg.GetUri())
	if uri == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("uri is required"))
	}
	if err := h.annotations.Delete(ctx, uri); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&hocreditv1.DeleteAnnotationResponse{}), nil
}

func (h *Handler) EnrichAnnotation(ctx context.Context, req *connect.Request[hocreditv1.EnrichAnnotationRequest]) (*connect.Response[hocreditv1.EnrichAnnotationResponse], error) {
	annotationJSON := strings.TrimSpace(req.Msg.GetAnnotationJson())
	if annotationJSON == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("annotation_json is required"))
	}
	scope := strings.ToLower(strings.TrimSpace(req.Msg.GetScope()))
	if scope == "" {
		scope = "line"
	}

	var enriched string
	var enrichErr error

	if req.Msg.GetContextId() > 0 {
		c, err := h.contexts.Get(ctx, req.Msg.GetContextId())
		if err != nil {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("context not found"))
		}
		if scope == "page" {
			enriched, enrichErr = h.enrichAnnotationPage(ctx, annotationJSON, c)
		} else {
			enriched, enrichErr = h.enrichSingleAnnotation(ctx, annotationJSON, c)
		}
	} else {
		c, _, err := h.contexts.Resolve(ctx, nil)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("resolve context: %w", err))
		}
		if scope == "page" {
			enriched, enrichErr = h.enrichAnnotationPage(ctx, annotationJSON, c)
		} else {
			enriched, enrichErr = h.enrichSingleAnnotation(ctx, annotationJSON, c)
		}
	}

	if enrichErr != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, enrichErr)
	}
	return connect.NewResponse(&hocreditv1.EnrichAnnotationResponse{AnnotationJson: enriched}), nil
}
