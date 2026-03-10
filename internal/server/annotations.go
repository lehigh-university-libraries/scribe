package server

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	"github.com/lehigh-university-libraries/scribe/internal/db"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
)

const (
	iiifTextGranularityContext = "http://iiif.io/api/extension/text-granularity/context.json"
	iiifPresentationContext    = "http://iiif.io/api/presentation/3/context.json"
)

func annotationPageContexts() []string {
	return []string{
		iiifTextGranularityContext,
		iiifPresentationContext,
	}
}

// annotationBaseURL returns the scheme+host to use as base for annotation IDs.
func (h *Handler) annBase(r *http.Request) string {
	if h.annotationBaseURL != "" {
		return h.annotationBaseURL
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (h *Handler) handleAnnotationSearch(w http.ResponseWriter, r *http.Request) {
	canvasURI := strings.TrimSpace(r.URL.Query().Get("canvasUri"))
	resp, err := h.SearchAnnotations(r.Context(), connect.NewRequest(&scribev1.SearchAnnotationsRequest{
		CanvasUri: canvasURI,
	}))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var page map[string]any
	if err := json.Unmarshal([]byte(resp.Msg.GetAnnotationPageJson()), &page); err != nil {
		writeError(w, http.StatusInternalServerError, "invalid annotation page json")
		return
	}
	// Keep HTTP path identity for annotation/IIIF clients.
	base := h.annBase(r)
	page["id"] = base + "/v1/annotations/3/search?canvasUri=" + url.QueryEscape(canvasURI)
	writeJSON(w, http.StatusOK, page)
}

var (
	itemImageFromCanvasPattern = regexp.MustCompile(`/v1/item-images/([0-9]+)/manifest/`)
)

// bootstrapAnnotationsFromHOCR fetches the hOCR annotation page from the
// core API for a canvas that has no saved annotations yet.
func (h *Handler) bootstrapAnnotationsFromHOCR(ctx context.Context, canvasURI, base string) ([]any, error) {
	return h.bootstrapAnnotationsForGranularities(ctx, canvasURI, base, annotationBootstrapURL, "line", "word")
}

func bindAnnotationToCanvas(anno map[string]any, canvasURI string) map[string]any {
	if anno == nil || strings.TrimSpace(canvasURI) == "" {
		return anno
	}

	selector := map[string]any(nil)
	switch target := anno["target"].(type) {
	case map[string]any:
		if s, ok := target["selector"].(map[string]any); ok {
			selector = s
		}
	case string:
		if idx := strings.Index(target, "#xywh="); idx >= 0 {
			selector = map[string]any{
				"type":       "FragmentSelector",
				"conformsTo": "http://www.w3.org/TR/media-frags/",
				"value":      "xywh=" + roundXYWHFragment(target[idx+6:]),
			}
		}
	}

	newTarget := map[string]any{
		"source": map[string]any{
			"id":   canvasURI,
			"type": "Canvas",
		},
	}
	if selector != nil {
		newTarget["selector"] = selector
	}
	anno["target"] = newTarget
	delete(anno, "on")
	return anno
}

// bootstrapAnnotationsForCanvas returns bootstrapped annotations for any canvas URI.
// For our own item-image manifests it delegates to bootstrapAnnotationsFromHOCR.
// For external canvases it looks up (or auto-ingests) the item_image by canvas_uri.
func (h *Handler) bootstrapAnnotationsForCanvas(ctx context.Context, canvasURI, base string) ([]any, error) {
	// Fast path: canvas belongs to our own manifest.
	if itemImageFromCanvasPattern.MatchString(canvasURI) {
		return h.bootstrapAnnotationsFromHOCR(ctx, canvasURI, base)
	}

	// Look up item_image by canvas_uri.
	img, err := h.items.GetImageByCanvasURI(ctx, canvasURI)
	if err != nil {
		// Not yet registered — try to ingest the manifest.
		candidates := manifestURLCandidatesFromCanvasURI(canvasURI)
		if len(candidates) == 0 {
			return nil, fmt.Errorf("cannot determine manifest URL for canvas %q", canvasURI)
		}
		var lastIngestErr error
		for _, manifestURL := range candidates {
			if ingestErr := h.autoIngestManifest(ctx, manifestURL); ingestErr == nil {
				lastIngestErr = nil
				break
			} else {
				lastIngestErr = ingestErr
			}
		}
		if lastIngestErr != nil {
			return nil, fmt.Errorf("auto-ingest for canvas %q: %w", canvasURI, lastIngestErr)
		}
		img, err = h.items.GetImageByCanvasURI(ctx, canvasURI)
		if err != nil {
			return nil, fmt.Errorf("canvas not found after ingest: %w", err)
		}
	}

	// Build a bootstrap URL using our internal item-image route.
	internalBase := base
	return h.bootstrapAnnotationsForGranularities(ctx, canvasURI, base, func(_ string, _ string) (string, error) {
		return internalBase + "/v1/item-images/" + fmt.Sprintf("%d", img.ID) + "/annotations", nil
	}, "line", "word")
}

func (h *Handler) bootstrapAnnotationsForGranularities(
	ctx context.Context,
	canvasURI, base string,
	urlBuilder func(string, string) (string, error),
	granularities ...string,
) ([]any, error) {
	type result struct {
		order []string
		items map[string]map[string]any
	}

	merged := result{
		order: make([]string, 0),
		items: make(map[string]map[string]any),
	}
	seen := make(map[string]struct{})

	for _, granularity := range granularities {
		rawItems, err := h.fetchBootstrapAnnotationItems(ctx, canvasURI, base, granularity, urlBuilder)
		if err != nil {
			if granularity == "line" {
				return nil, err
			}
			continue
		}
		for _, item := range rawItems {
			anno, ok := item.(map[string]any)
			if !ok {
				continue
			}
			id := strings.TrimSpace(annStringValue(anno, "id"))
			if id == "" {
				id = strings.TrimSpace(annStringValue(anno, "@id"))
			}
			if id == "" {
				continue
			}
			if _, ok := seen[id]; !ok {
				seen[id] = struct{}{}
				merged.order = append(merged.order, id)
			}
			merged.items[id] = anno
		}
	}

	items := make([]any, 0, len(merged.order))
	for _, id := range merged.order {
		if anno, ok := merged.items[id]; ok {
			items = append(items, anno)
		}
	}
	return items, nil
}

func (h *Handler) fetchBootstrapAnnotationItems(
	ctx context.Context,
	canvasURI, base, granularity string,
	urlBuilder func(string, string) (string, error),
) ([]any, error) {
	reqURL, err := urlBuilder(base, canvasURI)
	if err != nil {
		return nil, err
	}
	if granularity != "" {
		parsed, parseErr := url.Parse(reqURL)
		if parseErr != nil {
			return nil, parseErr
		}
		query := parsed.Query()
		query.Set("textGranularity", granularity)
		parsed.RawQuery = query.Encode()
		reqURL = parsed.String()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("annotation bootstrap failed: %s", resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	rawItems, _ := payload["items"].([]any)
	for i, item := range rawItems {
		anno, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id := strings.TrimSpace(annStringValue(anno, "id"))
		if id == "" {
			id = strings.TrimSpace(annStringValue(anno, "@id"))
		}
		if id != "" && !strings.HasPrefix(id, base+"/v1/annotations/3/item/") {
			stable := annStableID(id)
			full := base + "/v1/annotations/3/item/" + stable
			anno["id"] = full
			anno["@id"] = full
		}
		rawItems[i] = bindAnnotationToCanvas(normalizeAnnotation(anno, canvasURI), canvasURI)
	}
	return rawItems, nil
}

// manifestURLCandidatesFromCanvasURI returns manifest URL candidates derived from a
// IIIF canvas URI by stripping the "/canvas/..." suffix and trying common patterns.
// The most common IIIF pattern ("{base}/manifest") is tried first, followed by the
// bare base URL for servers that serve the manifest directly at the node path.
func manifestURLCandidatesFromCanvasURI(canvasURI string) []string {
	if idx := strings.Index(canvasURI, "/canvas/"); idx >= 0 {
		base := canvasURI[:idx]
		return []string{base + "/manifest", base}
	}
	return nil
}

// autoIngestManifest fetches a IIIF manifest and creates an item + item_images for it.
func (h *Handler) autoIngestManifest(ctx context.Context, manifestURL string) error {
	manifest, err := fetchIIIFManifest(ctx, manifestURL)
	if err != nil {
		return fmt.Errorf("fetch manifest: %w", err)
	}
	label := extractManifestLabel(manifest)
	if label == "" {
		label = manifestURL
	}
	itemID := fmt.Sprintf("auto-%x", sha1.Sum([]byte(manifestURL)))[:20]
	it, err := h.items.Create(ctx, db.CreateItemParams{
		ID:         itemID,
		UserID:     store.AnonymousUserID,
		Name:       label,
		SourceType: "iiif_manifest",
		SourceURL:  manifestURL,
	})
	if err != nil {
		return fmt.Errorf("create item: %w", err)
	}
	_, err = h.ingestParsedManifest(ctx, it.ID, manifest)
	return err
}

func annotationBootstrapURL(base, canvasURI string) (string, error) {
	if matches := itemImageFromCanvasPattern.FindStringSubmatch(canvasURI); len(matches) >= 2 {
		itemImageID := strings.TrimSpace(matches[1])
		if itemImageID == "" {
			return "", fmt.Errorf("empty item image id in canvas uri")
		}
		return base + "/v1/item-images/" + url.PathEscape(itemImageID) + "/annotations", nil
	}
	return "", fmt.Errorf("cannot extract item image reference from canvas uri")
}

func (h *Handler) handleAnnotationCreate(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	resp, err := h.CreateAnnotation(r.Context(), connect.NewRequest(&scribev1.CreateAnnotationRequest{
		AnnotationJson: string(body),
	}))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var anno map[string]any
	if err := json.Unmarshal([]byte(resp.Msg.GetAnnotationJson()), &anno); err != nil {
		writeError(w, http.StatusInternalServerError, "invalid annotation json")
		return
	}
	writeJSON(w, http.StatusOK, anno)
}

func (h *Handler) handleAnnotationUpdate(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	resp, err := h.UpdateAnnotation(r.Context(), connect.NewRequest(&scribev1.UpdateAnnotationRequest{
		AnnotationJson: string(body),
	}))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var anno map[string]any
	if err := json.Unmarshal([]byte(resp.Msg.GetAnnotationJson()), &anno); err != nil {
		writeError(w, http.StatusInternalServerError, "invalid annotation json")
		return
	}
	writeJSON(w, http.StatusOK, anno)
}

func (h *Handler) handleAnnotationDelete(w http.ResponseWriter, r *http.Request) {
	uri := strings.TrimSpace(r.URL.Query().Get("uri"))
	if uri == "" {
		writeError(w, http.StatusBadRequest, "uri is required")
		return
	}
	_, err := h.DeleteAnnotation(r.Context(), connect.NewRequest(&scribev1.DeleteAnnotationRequest{Uri: uri}))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleAnnotationGet(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	base := h.annBase(r)
	fullID := base + "/v1/annotations/3/item/" + id
	resp, err := h.GetAnnotation(r.Context(), connect.NewRequest(&scribev1.GetAnnotationRequest{Id: fullID}))
	if err != nil {
		writeError(w, http.StatusNotFound, "annotation not found")
		return
	}
	var anno map[string]any
	if err := json.Unmarshal([]byte(resp.Msg.GetAnnotationJson()), &anno); err != nil {
		writeError(w, http.StatusInternalServerError, "stored annotation is invalid json")
		return
	}
	writeJSON(w, http.StatusOK, normalizeAnnotation(anno, ""))
}

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
		anno["@id"] = id
	}
	annoType := strings.TrimSpace(annStringValue(anno, "type"))
	if annoType == "" {
		annoType = strings.TrimSpace(annStringValue(anno, "@type"))
	}
	if annoType == "" {
		annoType = "Annotation"
	}
	anno["type"] = annoType
	anno["@type"] = annoType

	bodyValue := strings.TrimSpace(annStringValue(anno, "bodyValue"))
	if bodyValue == "" {
		if resource, ok := anno["resource"].(map[string]any); ok {
			bodyValue = strings.TrimSpace(annStringValue(resource, "chars"))
			if bodyValue == "" {
				bodyValue = strings.TrimSpace(annStringValue(resource, "value"))
			}
		}
	}
	var bodyList []any
	switch b := anno["body"].(type) {
	case []any:
		bodyList = b
	case map[string]any:
		bodyList = []any{b}
	case string:
		if trimmed := strings.TrimSpace(b); trimmed != "" {
			bodyList = []any{map[string]any{
				"type": "TextualBody", "purpose": "supplementing",
				"value": trimmed, "format": "text/plain",
			}}
		}
	}
	if len(bodyList) == 0 && bodyValue != "" {
		bodyList = []any{map[string]any{
			"type": "TextualBody", "purpose": "supplementing",
			"value": bodyValue, "format": "text/plain",
		}}
	}
	if len(bodyList) > 0 {
		anno["body"] = bodyList
		if strings.TrimSpace(annStringValue(anno, "textGranularity")) == "" {
			anno["textGranularity"] = "line"
		}
	}

	canvasURI := strings.TrimSpace(defaultCanvasURI)
	if canvasURI == "" {
		canvasURI = extractCanvasURI(anno)
	}
	var fragment string
	switch on := anno["on"].(type) {
	case string:
		if idx := strings.Index(on, "#"); idx >= 0 {
			fragment = strings.TrimPrefix(on[idx+1:], "xywh=")
		}
	case map[string]any:
		if selector, ok := on["selector"].(map[string]any); ok {
			fragment = strings.TrimPrefix(strings.TrimSpace(annStringValue(selector, "value")), "xywh=")
		}
	}

	var target map[string]any
	switch tv := anno["target"].(type) {
	case map[string]any:
		target = tv
	case string:
		// Some clients send target as a plain string: "canvasURI#xywh=x,y,w,h"
		if canvasURI == "" {
			if idx := strings.Index(tv, "#"); idx >= 0 {
				canvasURI = tv[:idx]
			} else {
				canvasURI = tv
			}
		}
		if fragment == "" {
			if idx := strings.Index(tv, "#xywh="); idx >= 0 {
				fragment = roundXYWHFragment(tv[idx+6:])
			}
		}
		target = map[string]any{}
	default:
		target = map[string]any{}
	}
	if canvasURI != "" {
		target["source"] = map[string]any{"id": canvasURI, "type": "Canvas"}
	}
	if fragment != "" {
		target["selector"] = map[string]any{
			"type":       "FragmentSelector",
			"conformsTo": "http://www.w3.org/TR/media-frags/",
			"value":      "xywh=" + fragment,
		}
	}
	if len(target) > 0 {
		anno["target"] = target
	}
	delete(anno, "resource")
	delete(anno, "on")
	delete(anno, "bodyValue")
	return anno
}

func extractCanvasURI(anno map[string]any) string {
	on := strings.TrimSpace(annStringValue(anno, "on"))
	if on == "" {
		switch tv := anno["target"].(type) {
		case string:
			on = strings.TrimSpace(tv)
		case map[string]any:
			switch source := tv["source"].(type) {
			case string:
				on = strings.TrimSpace(source)
			case map[string]any:
				on = strings.TrimSpace(annStringValue(source, "id"))
			}
		}
	}
	if on == "" {
		return ""
	}
	if idx := strings.Index(on, "#"); idx >= 0 {
		return on[:idx]
	}
	return on
}

func emptyAnnotationPage(id string) map[string]any {
	return map[string]any{
		"@context": annotationPageContexts(),
		"id":       id,
		"type":     "AnnotationPage",
		"items":    []any{},
	}
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

func annRandomID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "anno-fallback"
	}
	return hex.EncodeToString(buf)
}

func annStableID(raw string) string {
	sum := sha1.Sum([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// handleAnnotationEnrich accepts a single IIIF Annotation (scope=line) or a
// full AnnotationPage (scope=page) as JSON, re-transcribes the image region(s)
// using the chosen context, and returns the enriched annotation JSON.
func (h *Handler) handleAnnotationEnrich(w http.ResponseWriter, r *http.Request) {
	var req scribev1.EnrichAnnotationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	resp, err := h.EnrichAnnotation(r.Context(), connect.NewRequest(&req))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"annotation_json": resp.Msg.GetAnnotationJson()})
}

// enrichSingleAnnotation re-transcribes the image region referenced by a single
// IIIF annotation and returns the updated annotation JSON.
func (h *Handler) enrichSingleAnnotation(ctx context.Context, annotationJSON string, pctx store.Context) (string, error) {
	var anno map[string]any
	if err := json.Unmarshal([]byte(annotationJSON), &anno); err != nil {
		return "", fmt.Errorf("parse annotation json: %w", err)
	}
	anno = normalizeAnnotation(anno, "")

	canvasURI := extractCanvasURI(anno)
	fragment := extractFragment(anno)
	if canvasURI == "" || fragment == "" {
		return "", fmt.Errorf("annotation must have a canvas uri and bbox fragment")
	}

	x1, y1, x2, y2, err := parseXYWH(fragment)
	if err != nil {
		return "", fmt.Errorf("parse fragment: %w", err)
	}

	// Extract IIIF identifier from canvas URI → manifest → image.
	iiifID, err := h.iiifIDFromCanvasURI(ctx, canvasURI)
	if err != nil {
		return "", fmt.Errorf("iiif id from canvas: %w", err)
	}

	imagePath, cleanup, err := fetchIIIFRegionToTemp(iiifID, x1, y1, x2, y2)
	if err != nil {
		return "", fmt.Errorf("fetch image region: %w", err)
	}
	defer cleanup()

	text, err := h.ocr.TranscribeImageRegion(
		imagePath, 0, 0, x2-x1, y2-y1,
		pctx.TranscriptionProvider, pctx.TranscriptionModel,
	)
	if err != nil {
		return "", fmt.Errorf("transcribe region: %w", err)
	}

	// Replace body with enriched text.
	anno["body"] = []any{map[string]any{
		"type":    "TextualBody",
		"purpose": "supplementing",
		"format":  "text/plain",
		"value":   strings.TrimSpace(text),
	}}
	if strings.TrimSpace(annStringValue(anno, "textGranularity")) == "" {
		anno["textGranularity"] = "line"
	}

	b, _ := json.Marshal(anno)
	return string(b), nil
}

// enrichAnnotationPage re-transcribes all annotations in a IIIF AnnotationPage.
func (h *Handler) enrichAnnotationPage(ctx context.Context, pageJSON string, pctx store.Context) (string, error) {
	var page map[string]any
	if err := json.Unmarshal([]byte(pageJSON), &page); err != nil {
		return "", fmt.Errorf("parse annotation page: %w", err)
	}
	rawItems, _ := page["items"].([]any)
	for i, item := range rawItems {
		anno, ok := item.(map[string]any)
		if !ok {
			continue
		}
		b, _ := json.Marshal(anno)
		enriched, err := h.enrichSingleAnnotation(ctx, string(b), pctx)
		if err != nil {
			slog.Warn("enrich annotation item failed", "index", i, "error", err)
			continue
		}
		var enrichedAnno map[string]any
		if err := json.Unmarshal([]byte(enriched), &enrichedAnno); err == nil {
			rawItems[i] = enrichedAnno
		}
	}
	page["items"] = rawItems
	b, _ := json.Marshal(page)
	return string(b), nil
}

// extractFragment returns the xywh fragment value from an annotation target selector.
func extractFragment(anno map[string]any) string {
	target, _ := anno["target"].(map[string]any)
	if target == nil {
		return ""
	}
	selector, _ := target["selector"].(map[string]any)
	if selector == nil {
		return ""
	}
	val := strings.TrimSpace(annStringValue(selector, "value"))
	return strings.TrimPrefix(val, "xywh=")
}

// roundXYWHFragment rounds each float component of an "x,y,w,h" string to the
// nearest integer, returning a new "x,y,w,h" string with integer values.
func roundXYWHFragment(raw string) string {
	parts := strings.Split(raw, ",")
	if len(parts) != 4 {
		return raw
	}
	var rounded [4]int
	for i, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return raw
		}
		rounded[i] = int(math.Round(f))
	}
	return fmt.Sprintf("%d,%d,%d,%d", rounded[0], rounded[1], rounded[2], rounded[3])
}

// parseXYWH parses "x,y,w,h" (integer or float) into x1,y1,x2,y2 coordinates.
func parseXYWH(fragment string) (x1, y1, x2, y2 int, err error) {
	parts := strings.Split(fragment, ",")
	if len(parts) != 4 {
		return 0, 0, 0, 0, fmt.Errorf("invalid xywh fragment %q", fragment)
	}
	vals := make([]int, 4)
	for i, p := range parts {
		f, e := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if e != nil {
			return 0, 0, 0, 0, fmt.Errorf("invalid xywh fragment %q: %w", fragment, e)
		}
		vals[i] = int(math.Round(f))
	}
	return vals[0], vals[1], vals[0] + vals[2], vals[1] + vals[3], nil
}

// iiifIDFromCanvasURI extracts the source image IIIF identifier from canvas URI by
// resolving the underlying OCR run through the item_image_id path.
func (h *Handler) iiifIDFromCanvasURI(ctx context.Context, canvasURI string) (string, error) {
	if matches := itemImageFromCanvasPattern.FindStringSubmatch(canvasURI); len(matches) >= 2 {
		itemImageIDRaw := strings.TrimSpace(matches[1])
		itemImageID, err := strconv.ParseUint(itemImageIDRaw, 10, 64)
		if err != nil {
			return "", fmt.Errorf("invalid item image id in canvas uri %q", canvasURI)
		}
		run, err := h.ocrRuns.GetByItemImageID(ctx, itemImageID)
		if err != nil {
			return "", fmt.Errorf("lookup run by item image id %s: %w", itemImageIDRaw, err)
		}
		return iiifIdentifierFromImageURL(run.ImageURL)
	}
	return "", fmt.Errorf("cannot extract item image reference from canvas uri %q", canvasURI)
}
