package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"

	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/hocr"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

var (
	errEmptyAnnotationTranscription     = errors.New("transcription provider returned no text")
	errInvalidAnnotationEnrichmentInput = errors.New("invalid annotation enrichment input")
)

// --- IIIF annotation normalisation (ported from annotationserver) ---

func normalizeAnnotation(anno map[string]any, defaultCanvasURI string) map[string]any {
	if anno == nil {
		return map[string]any{}
	}
	id := strings.TrimSpace(annStringValue(anno, "id"))
	if id == "" {
		id = strings.TrimSpace(annStringValue(anno, "@id"))
	}
	if id != "" {
		anno["id"] = id
	}
	delete(anno, "@id")
	annoType := strings.TrimSpace(annStringValue(anno, "type"))
	if annoType == "" {
		annoType = strings.TrimSpace(annStringValue(anno, "@type"))
	}
	if annoType == "" {
		annoType = "Annotation"
	}
	anno["type"] = annoType
	delete(anno, "@type")

	bodyValue := strings.TrimSpace(annStringValue(anno, "bodyValue"))
	if bodyValue == "" {
		if resource, ok := anno["resource"].(map[string]any); ok {
			bodyValue = strings.TrimSpace(annStringValue(resource, "chars"))
			if bodyValue == "" {
				bodyValue = strings.TrimSpace(annStringValue(resource, "value"))
			}
		}
	}
	var normalizedBody any
	hasBody := false
	switch b := anno["body"].(type) {
	case []any:
		normalizeTextualBodyPurpose(b)
		normalizedBody = b
		hasBody = len(b) > 0
	case map[string]any:
		normalizeTextualBodyPurpose(b)
		normalizedBody = b
		hasBody = true
	case string:
		if trimmed := strings.TrimSpace(b); trimmed != "" {
			normalizedBody = map[string]any{
				"type": "TextualBody", "purpose": "supplementing",
				"value": trimmed, "format": "text/plain",
			}
			hasBody = true
		}
	}
	if !hasBody && bodyValue != "" {
		normalizedBody = map[string]any{
			"type": "TextualBody", "purpose": "supplementing",
			"value": bodyValue, "format": "text/plain",
		}
		hasBody = true
	}
	if hasBody {
		anno["body"] = normalizedBody
		if strings.TrimSpace(annStringValue(anno, "textGranularity")) == "" {
			anno["textGranularity"] = "line"
		}
		if anno["motivation"] == nil {
			anno["motivation"] = "supplementing"
		}
	}

	canvasURI := strings.TrimSpace(defaultCanvasURI)
	targetValue, hasTarget := anno["target"]
	if !hasTarget || targetValue == nil {
		targetValue = anno["on"]
	}
	if canvasURI == "" {
		canvasURI = iiif.TargetCanvas(targetValue)
	}
	fragment, hasFragment, fragmentErr := iiif.TargetPixelXYWH(targetValue)
	if fragmentErr == nil && hasFragment && canvasURI != "" {
		if rounded := roundXYWHFragment(fragment); rounded != "" {
			if target, err := iiif.ReplaceTargetPixelXYWH(targetValue, canvasURI, rounded); err == nil {
				anno["target"] = target
			}
		}
	} else if canvasURI != "" {
		switch target := targetValue.(type) {
		case string:
			// Compact targets without geometry are already valid and retain their
			// complete fragment rather than being rewritten.
			anno["target"] = target
		case map[string]any:
			switch source := target["source"].(type) {
			case string:
				target["source"] = canvasURI
			case map[string]any:
				source["id"] = canvasURI
				if strings.TrimSpace(annStringValue(source, "type")) == "" {
					source["type"] = "Canvas"
				}
			default:
				target["source"] = map[string]any{"id": canvasURI, "type": "Canvas"}
			}
			anno["target"] = target
		default:
			anno["target"] = map[string]any{"source": map[string]any{"id": canvasURI, "type": "Canvas"}}
		}
	}
	delete(anno, "resource")
	delete(anno, "on")
	delete(anno, "bodyValue")
	return anno
}

func normalizeTextualBodyPurpose(raw any) {
	switch body := raw.(type) {
	case map[string]any:
		if strings.EqualFold(strings.TrimSpace(annStringValue(body, "type")), "TextualBody") && body["purpose"] == nil {
			body["purpose"] = "supplementing"
		}
	case []any:
		for _, entry := range body {
			normalizeTextualBodyPurpose(entry)
		}
	}
}

func extractCanvasURI(anno map[string]any) string {
	on := strings.TrimSpace(annStringValue(anno, "on"))
	if on == "" {
		on = iiif.TargetCanvas(anno["target"])
	}
	if on == "" {
		return ""
	}
	if idx := strings.Index(on, "#"); idx >= 0 {
		return on[:idx]
	}
	return on
}

func annStringValue(v map[string]any, key string) string {
	if v == nil {
		return ""
	}
	raw, ok := v[key]
	if !ok || raw == nil {
		return ""
	}
	switch t := raw.(type) {
	case string:
		return t
	case fmt.Stringer:
		return t.String()
	default:
		return ""
	}
}

func annotationID(parts ...string) string {
	escaped := make([]string, 0, len(parts))
	for _, part := range parts {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		escaped = append(escaped, url.QueryEscape(p))
	}
	return "urn:scribe:annotation:" + strings.Join(escaped, ":")
}

// enrichSingleAnnotation re-transcribes the image region referenced by a single
// IIIF annotation and returns the updated annotation JSON.
func (h *Handler) enrichSingleAnnotation(ctx context.Context, itemImageID uint64, annotationJSON string, pctx store.Context) (string, error) {
	workspaceID := h.currentWorkspaceID(ctx)
	var userID *uint64
	if currentUserID := h.currentUserID(ctx); currentUserID > 0 {
		userID = &currentUserID
	}
	return h.enrichSingleAnnotationInWorkspace(ctx, itemImageID, annotationJSON, pctx, workspaceID, userID)
}

// enrichSingleAnnotationInWorkspace is the shared request/worker use case. Its
// tenant and credential ownership are explicit because queue contexts do not
// contain an authenticated HTTP principal.
func (h *Handler) enrichSingleAnnotationInWorkspace(
	ctx context.Context,
	itemImageID uint64,
	annotationJSON string,
	pctx store.Context,
	workspaceID uint64,
	userID *uint64,
) (string, error) {
	if workspaceID == 0 || itemImageID == 0 {
		return "", fmt.Errorf("workspace and item image are required")
	}
	var anno map[string]any
	if err := iiif.DecodeJSON([]byte(annotationJSON), &anno); err != nil {
		return "", fmt.Errorf("%w: annotation_json must contain one JSON object", errInvalidAnnotationEnrichmentInput)
	}
	anno = normalizeAnnotation(anno, "")

	canvasURI := extractCanvasURI(anno)
	fragment := extractFragment(anno)
	if canvasURI == "" || fragment == "" {
		return "", fmt.Errorf("%w: annotation must have a canvas uri and bbox fragment", errInvalidAnnotationEnrichmentInput)
	}

	x1, y1, x2, y2, err := parseXYWH(fragment)
	if err != nil {
		return "", fmt.Errorf("%w: invalid annotation bbox fragment", errInvalidAnnotationEnrichmentInput)
	}

	image, err := h.items.GetImageForWorkspace(ctx, itemImageID, workspaceID)
	if err != nil {
		return "", fmt.Errorf("resolve source image %d: %w", itemImageID, err)
	}
	if strings.TrimSpace(image.CanvasURI) != canvasURI {
		return "", fmt.Errorf("annotation target does not match item image %d", itemImageID)
	}
	var contextID *uint64
	if pctx.ID > 0 {
		contextID = &pctx.ID
	}
	ctx = hocr.WithProviderCallMetadata(ctx, workspaceID, "", &itemImageID, contextID)
	ctx = h.contextWithProviderSecret(ctx, workspaceID, userID, pctx.TranscriptionProvider)

	fetchRegion := h.imageRegionFetcher
	if fetchRegion == nil {
		fetchRegion = fetchImageRegionToTemp
	}
	imagePath, cleanup, err := fetchRegion(ctx, image.ImageURL, x1, y1, x2, y2)
	if err != nil {
		return "", fmt.Errorf("fetch image region for item image id %d: %w", image.ID, err)
	}
	defer cleanup()

	text, err := h.ocr.TranscribeImageFileWithContext(
		hocr.WithTranscriptionOptions(ctx, pctx.SystemPrompt, pctx.Temperature),
		imagePath,
		pctx.TranscriptionProvider, pctx.TranscriptionModel,
	)
	if err != nil {
		if errors.Is(err, hocr.ErrNoTranscription) {
			return "", fmt.Errorf("transcribe region: %w", errEmptyAnnotationTranscription)
		}
		return "", fmt.Errorf("transcribe region: %w", err)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("transcribe region: %w", errEmptyAnnotationTranscription)
	}

	// Change the first TextualBody in place so language, service, confidence,
	// and extension properties survive the model operation. Its old RDF
	// identity cannot survive a changed value, so the mutated body is anonymous.
	anno["body"] = textBodyWithValue(anno["body"], text)
	clearFirstTextualBodyIdentity(anno["body"])
	if strings.TrimSpace(annStringValue(anno, "textGranularity")) == "" {
		anno["textGranularity"] = "line"
	}

	b, _ := json.Marshal(anno)
	return string(b), nil
}

// enrichAnnotationPage re-transcribes every line in a IIIF AnnotationPage.
// Finer-granularity words remain in the draft and are reconciled from the
// changed line text by SaveAnnotationPage, avoiding duplicate provider calls.
func (h *Handler) enrichAnnotationPage(ctx context.Context, itemImageID uint64, pageJSON string, pctx store.Context) (string, error) {
	return enrichAnnotationPageWithLimit(ctx, pageJSON, pctx, config.Get().Config.Processing.MaxPageEnrichmentLines, func(callCtx context.Context, annotationJSON string, callContext store.Context) (string, error) {
		return h.enrichSingleAnnotation(callCtx, itemImageID, annotationJSON, callContext)
	})
}

func enrichAnnotationPageWith(
	ctx context.Context,
	pageJSON string,
	pctx store.Context,
	enrich func(context.Context, string, store.Context) (string, error),
) (string, error) {
	return enrichAnnotationPageWithLimit(ctx, pageJSON, pctx, config.DefaultMaxPageEnrichmentLines, enrich)
}

func enrichAnnotationPageWithLimit(
	ctx context.Context,
	pageJSON string,
	pctx store.Context,
	maxLines int,
	enrich func(context.Context, string, store.Context) (string, error),
) (string, error) {
	var page map[string]any
	if err := iiif.DecodeJSON([]byte(pageJSON), &page); err != nil {
		return "", fmt.Errorf("%w: annotation_json must contain an AnnotationPage", errInvalidAnnotationEnrichmentInput)
	}
	rawItems, ok := page["items"].([]any)
	if !ok {
		return "", fmt.Errorf("%w: annotation page items must be an array", errInvalidAnnotationEnrichmentInput)
	}
	if maxLines < 1 {
		maxLines = config.DefaultMaxPageEnrichmentLines
	}
	lineCount := 0
	for i, item := range rawItems {
		anno, ok := item.(map[string]any)
		if !ok {
			return "", fmt.Errorf("%w: annotation page item %d must be an object", errInvalidAnnotationEnrichmentInput, i)
		}
		if strings.EqualFold(strings.TrimSpace(annStringValue(anno, "textGranularity")), "line") {
			lineCount++
			if lineCount > maxLines {
				return "", fmt.Errorf("%w: annotation page contains %d or more lines; page enrichment limit is %d", errInvalidAnnotationEnrichmentInput, lineCount, maxLines)
			}
		}
	}
	enrichedItems := make([]any, len(rawItems))
	for i, item := range rawItems {
		anno, ok := item.(map[string]any)
		if !ok {
			return "", fmt.Errorf("annotation page item %d must be an object", i)
		}
		if !strings.EqualFold(strings.TrimSpace(annStringValue(anno, "textGranularity")), "line") {
			enrichedItems[i] = anno
			continue
		}
		b, err := json.Marshal(anno)
		if err != nil {
			return "", fmt.Errorf("encode annotation page item %d: %w", i, err)
		}
		enriched, err := enrich(ctx, string(b), pctx)
		if err != nil {
			return "", fmt.Errorf("enrich annotation page item %d: %w", i, err)
		}
		var enrichedAnno map[string]any
		if err := iiif.DecodeJSON([]byte(enriched), &enrichedAnno); err != nil {
			return "", fmt.Errorf("decode enriched annotation page item %d: %w", i, err)
		}
		enrichedItems[i] = enrichedAnno
	}
	page["items"] = enrichedItems
	b, err := json.Marshal(page)
	if err != nil {
		return "", fmt.Errorf("encode enriched annotation page: %w", err)
	}
	return string(b), nil
}

// extractFragment returns the xywh fragment value from an annotation target selector.
func extractFragment(anno map[string]any) string {
	value, present, err := iiif.TargetPixelXYWH(anno["target"])
	if err != nil || !present {
		return ""
	}
	return value
}

func annotationDebugSummary(anno map[string]any) map[string]any {
	if anno == nil {
		return map[string]any{"nil": true}
	}
	granularity := strings.ToLower(strings.TrimSpace(annStringValue(anno, "textGranularity")))
	if granularity != "" && !iiif.IsTextGranularity(granularity) {
		granularity = "other"
	}
	target, _ := anno["target"].(map[string]any)
	return map[string]any{
		"hasId":           strings.TrimSpace(annStringValue(anno, "id")) != "",
		"textGranularity": granularity,
		"hasCanvas":       strings.TrimSpace(extractCanvasURI(anno)) != "",
		"hasFragment":     strings.TrimSpace(extractFragment(anno)) != "",
		"hasText":         strings.TrimSpace(extractAnnotationText(anno)) != "",
		"targetType":      annotationJSONValueType(anno["target"]),
		"selectorType":    annotationJSONValueType(target["selector"]),
	}
}

func annotationJSONValueType(value any) string {
	switch value.(type) {
	case nil:
		return "missing"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case float64, json.Number:
		return "number"
	case bool:
		return "boolean"
	default:
		return "other"
	}
}

// roundXYWHFragment rounds each float component of an "x,y,w,h" string to the
// nearest integer, returning a new "x,y,w,h" string with integer values.
func roundXYWHFragment(raw string) string {
	rounded, err := parseRoundedXYWH(raw)
	if err != nil {
		return raw
	}
	return fmt.Sprintf("%d,%d,%d,%d", rounded[0], rounded[1], rounded[2], rounded[3])
}

// parseXYWH parses "x,y,w,h" (integer or float) into x1,y1,x2,y2 coordinates.
func parseXYWH(fragment string) (x1, y1, x2, y2 int, err error) {
	vals, err := parseRoundedXYWH(fragment)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	if vals[0] < 0 || vals[1] < 0 || vals[2] <= 0 || vals[3] <= 0 || vals[0] > iiif.MaxPixelCoordinate-vals[2] || vals[1] > iiif.MaxPixelCoordinate-vals[3] {
		return 0, 0, 0, 0, fmt.Errorf("invalid xywh fragment %q: geometry is outside processing limits", fragment)
	}
	return vals[0], vals[1], vals[0] + vals[2], vals[1] + vals[3], nil
}

func parseRoundedXYWH(fragment string) ([4]int, error) {
	fragment = strings.TrimSpace(fragment)
	if strings.HasPrefix(fragment, "pixel:") {
		fragment = strings.TrimSpace(strings.TrimPrefix(fragment, "pixel:"))
	}
	if strings.HasPrefix(fragment, "percent:") {
		return [4]int{}, fmt.Errorf("invalid xywh fragment %q: percent coordinates are not supported", fragment)
	}
	parts := strings.Split(fragment, ",")
	if len(parts) != 4 {
		return [4]int{}, fmt.Errorf("invalid xywh fragment %q", fragment)
	}
	var vals [4]int
	for i, p := range parts {
		f, e := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if e != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f > float64(iiif.MaxPixelCoordinate) {
			return [4]int{}, fmt.Errorf("invalid xywh fragment %q", fragment)
		}
		vals[i] = int(math.Round(f))
	}
	return vals, nil
}
