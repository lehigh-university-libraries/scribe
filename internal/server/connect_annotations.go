package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"os"
	"strings"

	"connectrpc.com/connect"
	"github.com/lehigh-university-libraries/scribe/internal/hocr"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
)

// --- AnnotationService Connect handlers ---

func (h *Handler) SearchAnnotations(ctx context.Context, req *connect.Request[scribev1.SearchAnnotationsRequest]) (*connect.Response[scribev1.SearchAnnotationsResponse], error) {
	canvasURI, granularity := parseSearchAnnotationsCanvasURI(req.Msg.GetCanvasUri())
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
				if _, persistErr := h.persistAnnotationItems(ctx, canvasURI, bootstrap); persistErr != nil {
					return nil, connect.NewError(connect.CodeInternal, persistErr)
				}
			}
		}
	}
	items = filterAnnotationsByGranularity(items, granularity)

	page := map[string]any{
		"@context": annotationPageContexts(),
		"id":       annotationPageID(canvasURI),
		"type":     "AnnotationPage",
		"items":    items,
	}
	b, err := json.Marshal(page)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&scribev1.SearchAnnotationsResponse{
		AnnotationPageJson: string(b),
	}), nil
}

func parseSearchAnnotationsCanvasURI(raw string) (string, string) {
	canvasURI := strings.TrimSpace(raw)
	if canvasURI == "" {
		return "", "line"
	}
	parsed, err := url.Parse(canvasURI)
	if err != nil {
		return canvasURI, "line"
	}
	granularity := strings.ToLower(strings.TrimSpace(parsed.Query().Get("textGranularity")))
	switch granularity {
	case "", "line":
		granularity = "line"
	case "word", "glyph", "all":
	default:
		granularity = "line"
	}
	parsed.RawQuery = ""
	return parsed.String(), granularity
}

func filterAnnotationsByGranularity(items []any, granularity string) []any {
	if granularity == "" || granularity == "all" {
		return items
	}
	filtered := make([]any, 0, len(items))
	for _, item := range items {
		anno, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(annStringValue(anno, "textGranularity")), granularity) {
			filtered = append(filtered, anno)
		}
	}
	return filtered
}

func (h *Handler) GetAnnotation(ctx context.Context, req *connect.Request[scribev1.GetAnnotationRequest]) (*connect.Response[scribev1.GetAnnotationResponse], error) {
	id := strings.TrimSpace(req.Msg.GetId())
	if id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("id is required"))
	}
	raw, err := h.annotations.Get(ctx, id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("annotation not found"))
	}
	return connect.NewResponse(&scribev1.GetAnnotationResponse{AnnotationJson: raw}), nil
}

func (h *Handler) CreateAnnotation(ctx context.Context, req *connect.Request[scribev1.CreateAnnotationRequest]) (*connect.Response[scribev1.CreateAnnotationResponse], error) {
	var anno map[string]any
	if err := json.Unmarshal([]byte(req.Msg.GetAnnotationJson()), &anno); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid annotation json"))
	}
	anno = normalizeAnnotation(anno, "")
	id := strings.TrimSpace(annStringValue(anno, "id"))
	if id == "" {
		id = annotationID(annRandomID())
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
	if matches := itemImageFromCanvasPattern.FindStringSubmatch(canvasURI); len(matches) >= 2 {
		if itemImageID, convErr := strconv.ParseUint(strings.TrimSpace(matches[1]), 10, 64); convErr == nil {
			h.publishEvent("dev.scribe.annotations.created", subjectForAnnotation(itemImageID, id), map[string]any{
				"itemImageId":     itemImageID,
				"canvasUri":       canvasURI,
				"annotationId":    id,
				"annotationJson":  string(payload),
				"annotationCount": 1,
			})
		}
	}
	return connect.NewResponse(&scribev1.CreateAnnotationResponse{AnnotationJson: string(payload)}), nil
}

func (h *Handler) UpdateAnnotation(ctx context.Context, req *connect.Request[scribev1.UpdateAnnotationRequest]) (*connect.Response[scribev1.UpdateAnnotationResponse], error) {
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
	return connect.NewResponse(&scribev1.UpdateAnnotationResponse{AnnotationJson: string(payload)}), nil
}

func (h *Handler) DeleteAnnotation(ctx context.Context, req *connect.Request[scribev1.DeleteAnnotationRequest]) (*connect.Response[scribev1.DeleteAnnotationResponse], error) {
	uri := strings.TrimSpace(req.Msg.GetUri())
	if uri == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("uri is required"))
	}
	if err := h.annotations.Delete(ctx, uri); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&scribev1.DeleteAnnotationResponse{}), nil
}

func (h *Handler) SplitLineIntoWords(ctx context.Context, req *connect.Request[scribev1.SplitLineIntoWordsRequest]) (*connect.Response[scribev1.SplitLineIntoWordsResponse], error) {
	anno, text, x1, y1, x2, y2, canvasURI, err := parseLineAnnotation(req.Msg.GetAnnotationJson())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	words := req.Msg.GetWords()
	if len(words) == 0 {
		words = strings.Fields(text)
	}
	if len(words) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("line has no words"))
	}

	lineID := strings.TrimSpace(annStringValue(anno, "id"))
	width := maxInt(1, x2-x1)
	height := maxInt(1, y2-y1)
	wordWidth := maxInt(1, width/len(words))
	items := make([]any, 0, len(words))
	for i, word := range words {
		wx1 := x1 + i*wordWidth
		wx2 := wx1 + wordWidth
		if i == len(words)-1 {
			wx2 = x2
		}
		wordAnno := map[string]any{
			"id":              fmt.Sprintf("%s-word-%d", lineID, i+1),
			"type":            "Annotation",
			"textGranularity": "word",
			"motivation":      "supplementing",
			"body": []any{
				map[string]any{
					"type":    "TextualBody",
					"purpose": "supplementing",
					"format":  "text/plain",
					"value":   strings.TrimSpace(word),
				},
			},
			"target": map[string]any{
				"source": map[string]any{"id": canvasURI, "type": "Canvas"},
				"selector": map[string]any{
					"type":       "FragmentSelector",
					"conformsTo": "http://www.w3.org/TR/media-frags/",
					"value":      fmt.Sprintf("xywh=%d,%d,%d,%d", wx1, y1, maxInt(1, wx2-wx1), height),
				},
			},
		}
		items = append(items, wordAnno)
	}
	page := map[string]any{
		"@context": annotationPageContexts(),
		"type":     "AnnotationPage",
		"items":    items,
	}
	b, _ := json.Marshal(page)
	return connect.NewResponse(&scribev1.SplitLineIntoWordsResponse{AnnotationPageJson: string(b)}), nil
}

func (h *Handler) SplitLineIntoTwoLines(_ context.Context, req *connect.Request[scribev1.SplitLineIntoTwoLinesRequest]) (*connect.Response[scribev1.SplitLineIntoTwoLinesResponse], error) {
	anno, text, x1, y1, x2, y2, canvasURI, err := parseLineAnnotation(req.Msg.GetAnnotationJson())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	words := strings.Fields(text)
	if len(words) < 2 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("line needs at least 2 words to split"))
	}
	splitAt := int(req.Msg.GetSplitAtWord())
	if splitAt <= 0 || splitAt >= len(words) {
		splitAt = len(words) / 2
	}
	textA := strings.Join(words[:splitAt], " ")
	textB := strings.Join(words[splitAt:], " ")

	lineID := strings.TrimSpace(annStringValue(anno, "id"))
	fullHeight := maxInt(2, y2-y1)
	h1 := fullHeight / 2
	h2 := fullHeight - h1
	annoA := buildLineAnnotation(fmt.Sprintf("%s-a", lineID), canvasURI, x1, y1, x2, y1+h1, textA)
	annoB := buildLineAnnotation(fmt.Sprintf("%s-b", lineID), canvasURI, x1, y1+h1, x2, y1+h1+h2, textB)
	b1, _ := json.Marshal(annoA)
	b2, _ := json.Marshal(annoB)
	return connect.NewResponse(&scribev1.SplitLineIntoTwoLinesResponse{AnnotationJsons: []string{string(b1), string(b2)}}), nil
}

func (h *Handler) JoinLines(_ context.Context, req *connect.Request[scribev1.JoinAnnotationsRequest]) (*connect.Response[scribev1.JoinAnnotationsResponse], error) {
	return h.joinAnnotations(req.Msg.GetAnnotationJsons())
}

func (h *Handler) JoinWordsIntoLine(_ context.Context, req *connect.Request[scribev1.JoinAnnotationsRequest]) (*connect.Response[scribev1.JoinAnnotationsResponse], error) {
	return h.joinAnnotations(req.Msg.GetAnnotationJsons())
}

func (h *Handler) joinAnnotations(annotationJSONs []string) (*connect.Response[scribev1.JoinAnnotationsResponse], error) {
	if len(annotationJSONs) < 2 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("at least two annotations are required"))
	}
	var (
		texts     []string
		canvasURI string
		unionX1   = int(^uint(0) >> 1)
		unionY1   = int(^uint(0) >> 1)
		unionX2   = 0
		unionY2   = 0
	)
	for _, raw := range annotationJSONs {
		_, text, x1, y1, x2, y2, c, err := parseLineAnnotation(raw)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		if canvasURI == "" {
			canvasURI = c
		}
		texts = append(texts, strings.TrimSpace(text))
		if x1 < unionX1 {
			unionX1 = x1
		}
		if y1 < unionY1 {
			unionY1 = y1
		}
		if x2 > unionX2 {
			unionX2 = x2
		}
		if y2 > unionY2 {
			unionY2 = y2
		}
	}
	merged := buildLineAnnotation(
		fmt.Sprintf("merged-%s", annStableID(strings.Join(annotationJSONs, "|"))),
		canvasURI,
		unionX1, unionY1, unionX2, unionY2,
		strings.TrimSpace(strings.Join(texts, " ")),
	)
	b, _ := json.Marshal(merged)
	return connect.NewResponse(&scribev1.JoinAnnotationsResponse{AnnotationJson: string(b)}), nil
}

func (h *Handler) CrosswalkToPlainText(_ context.Context, req *connect.Request[scribev1.CrosswalkRequest]) (*connect.Response[scribev1.CrosswalkResponse], error) {
	lines, _, _, err := annotationPayloadToHOCRLines(req.Msg.GetAnnotationPageJson(), req.Msg.GetAnnotationJson())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&scribev1.CrosswalkResponse{
		Format:  "text/plain",
		Content: linesToPlainText(lines),
	}), nil
}

func (h *Handler) CrosswalkToHOCR(_ context.Context, req *connect.Request[scribev1.CrosswalkRequest]) (*connect.Response[scribev1.CrosswalkResponse], error) {
	lines, pageW, pageH, err := annotationPayloadToHOCRLines(req.Msg.GetAnnotationPageJson(), req.Msg.GetAnnotationJson())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	converter := hocr.NewConverter()
	xml := converter.ConvertHOCRLinesToXML(lines, pageW, pageH)
	return connect.NewResponse(&scribev1.CrosswalkResponse{
		Format:  "text/vnd.hocr+html",
		Content: xml,
	}), nil
}

func (h *Handler) CrosswalkToPageXML(_ context.Context, req *connect.Request[scribev1.CrosswalkRequest]) (*connect.Response[scribev1.CrosswalkResponse], error) {
	lines, pageW, pageH, err := annotationPayloadToHOCRLines(req.Msg.GetAnnotationPageJson(), req.Msg.GetAnnotationJson())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&scribev1.CrosswalkResponse{
		Format:  "application/vnd.prima.page+xml",
		Content: linesToPageXML(lines, pageW, pageH),
	}), nil
}

func (h *Handler) CrosswalkToALTOXML(_ context.Context, req *connect.Request[scribev1.CrosswalkRequest]) (*connect.Response[scribev1.CrosswalkResponse], error) {
	lines, pageW, pageH, err := annotationPayloadToHOCRLines(req.Msg.GetAnnotationPageJson(), req.Msg.GetAnnotationJson())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&scribev1.CrosswalkResponse{
		Format:  "application/alto+xml",
		Content: linesToALTOXML(lines, pageW, pageH),
	}), nil
}

func (h *Handler) EnrichAnnotation(ctx context.Context, req *connect.Request[scribev1.EnrichAnnotationRequest]) (*connect.Response[scribev1.EnrichAnnotationResponse], error) {
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
	return connect.NewResponse(&scribev1.EnrichAnnotationResponse{AnnotationJson: enriched}), nil
}
