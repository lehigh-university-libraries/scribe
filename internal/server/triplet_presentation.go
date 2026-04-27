package server

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/lehigh-university-libraries/scribe/internal/config"
)

type annotationPageDocument struct {
	Context any              `json:"@context"`
	ID      string           `json:"id"`
	Type    string           `json:"type"`
	Items   []map[string]any `json:"items"`
	ETag    string           `json:"-"`
}

type tripletAnnotationRoute struct {
	ItemID   string
	CanvasID string
}

func (h *Handler) tripletPresentationInternalBase() string {
	return strings.TrimRight(strings.TrimSpace(config.Get().Config.Annotation.TripletPresentationInternalBase), "/")
}

func (h *Handler) tripletPresentationPublicBase() string {
	base := strings.TrimRight(strings.TrimSpace(config.Get().Config.Annotation.TripletPresentationBase), "/")
	if base == "" {
		base = h.tripletPresentationInternalBase()
	}
	return base
}

func (h *Handler) tripletPresentationEnabled() bool {
	return h.tripletPresentationInternalBase() != ""
}

func tripletRouteForCanvasURI(canvasURI string) tripletAnnotationRoute {
	canvasURI = strings.TrimSpace(canvasURI)
	if matches := itemImageFromCanvasPattern.FindStringSubmatch(canvasURI); len(matches) >= 2 {
		canvasID := "page-1"
		if u, err := url.Parse(canvasURI); err == nil {
			parts := strings.Split(strings.Trim(u.Path, "/"), "/")
			for i := 0; i+1 < len(parts); i++ {
				if parts[i] == "canvas" && strings.TrimSpace(parts[i+1]) != "" {
					canvasID = parts[i+1]
					break
				}
			}
		}
		return tripletAnnotationRoute{
			ItemID:   "item-image-" + strings.TrimSpace(matches[1]),
			CanvasID: canvasID,
		}
	}
	sum := sha1.Sum([]byte(canvasURI))
	return tripletAnnotationRoute{
		ItemID:   "canvas-" + hex.EncodeToString(sum[:])[:24],
		CanvasID: "annotations",
	}
}

func (h *Handler) tripletAnnotationPageURL(canvasURI string) (string, bool) {
	base := h.tripletPresentationInternalBase()
	if base == "" {
		return "", false
	}
	route := tripletRouteForCanvasURI(canvasURI)
	u := base + "/" + url.PathEscape(route.ItemID) + "/canvas/" + url.PathEscape(route.CanvasID) + "/annotations"
	return u, true
}

func (h *Handler) tripletAnnotationPageID(canvasURI string) string {
	base := h.tripletPresentationPublicBase()
	if base == "" {
		return annotationPageID(canvasURI)
	}
	route := tripletRouteForCanvasURI(canvasURI)
	return base + "/" + url.PathEscape(route.ItemID) + "/canvas/" + url.PathEscape(route.CanvasID) + "/annotations"
}

func (h *Handler) loadTripletAnnotationPage(ctx context.Context, canvasURI string) (*annotationPageDocument, bool, error) {
	reqURL, ok := h.tripletAnnotationPageURL(canvasURI)
	if !ok {
		return nil, false, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, true, err
	}
	client := h.webhookClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, true, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, true, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, true, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, true, fmt.Errorf("triplet presentation GET status %d: %s", resp.StatusCode, string(body))
	}
	var page annotationPageDocument
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, true, err
	}
	if page.Type != "AnnotationPage" {
		return nil, true, fmt.Errorf("triplet presentation response type %q", page.Type)
	}
	page.ETag = strings.TrimSpace(resp.Header.Get("ETag"))
	return &page, true, nil
}

func (h *Handler) putTripletAnnotationPage(ctx context.Context, canvasURI string, items []any, etag string) error {
	reqURL, ok := h.tripletAnnotationPageURL(canvasURI)
	if !ok {
		return nil
	}
	pageID := h.tripletAnnotationPageID(canvasURI)
	page := map[string]any{
		"@context": annotationPageContexts(),
		"id":       pageID,
		"type":     "AnnotationPage",
		"items":    items,
	}
	body, err := json.Marshal(page)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, reqURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/ld+json")
	token := strings.TrimSpace(config.Get().Config.Annotation.TripletPresentationWriteToken)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if strings.TrimSpace(etag) != "" {
		req.Header.Set("If-Match", strings.TrimSpace(etag))
	}
	client := h.webhookClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("triplet presentation PUT status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return nil
}

func (h *Handler) currentAnnotationItems(ctx context.Context, canvasURI, base string) ([]any, error) {
	if page, ok, err := h.loadTripletAnnotationPage(ctx, canvasURI); ok {
		if err != nil {
			return nil, err
		}
		if page != nil {
			items := make([]any, 0, len(page.Items))
			for _, item := range page.Items {
				items = append(items, normalizeAnnotation(item, canvasURI))
			}
			if err := h.indexAnnotationItems(ctx, canvasURI, items); err != nil {
				return nil, err
			}
			return items, nil
		}
	}

	payloads, err := h.annotations.SearchByCanvas(ctx, canvasURI)
	if err != nil {
		return nil, err
	}
	items := make([]any, 0, len(payloads))
	for _, raw := range payloads {
		var obj map[string]any
		if err := json.Unmarshal([]byte(raw), &obj); err != nil {
			continue
		}
		items = append(items, normalizeAnnotation(obj, canvasURI))
	}
	if len(items) > 0 {
		return items, nil
	}
	bootstrapped, err := h.bootstrapAnnotationsForCanvas(ctx, canvasURI, base)
	if err != nil {
		return nil, err
	}
	if _, err := h.saveAnnotationPage(ctx, canvasURI, bootstrapped); err != nil {
		return nil, err
	}
	return bootstrapped, nil
}

func (h *Handler) saveAnnotationPage(ctx context.Context, canvasURI string, items []any) ([]string, error) {
	var etag string
	if page, ok, err := h.loadTripletAnnotationPage(ctx, canvasURI); ok {
		if err != nil {
			return nil, err
		}
		if page != nil {
			etag = page.ETag
		}
	}
	return h.saveAnnotationPageWithETag(ctx, canvasURI, items, etag)
}

func (h *Handler) saveAnnotationPageWithETag(ctx context.Context, canvasURI string, items []any, etag string) ([]string, error) {
	normalizedItems, payloads, err := normalizeAnnotationItemsForCanvas(canvasURI, items)
	if err != nil {
		return nil, err
	}

	if err := h.putTripletAnnotationPage(ctx, canvasURI, normalizedItems, etag); err != nil {
		return nil, err
	}

	if err := h.writeAnnotationIndex(ctx, canvasURI, normalizedItems, payloads); err != nil {
		if h.tripletPresentationEnabled() {
			return payloads, nil
		}
		return nil, err
	}
	return payloads, nil
}

func (h *Handler) indexAnnotationItems(ctx context.Context, canvasURI string, items []any) error {
	normalizedItems, payloads, err := normalizeAnnotationItemsForCanvas(canvasURI, items)
	if err != nil {
		return err
	}
	return h.writeAnnotationIndex(ctx, canvasURI, normalizedItems, payloads)
}

func normalizeAnnotationItemsForCanvas(canvasURI string, items []any) ([]any, []string, error) {
	payloads := make([]string, 0, len(items))
	normalizedItems := make([]any, 0, len(items))
	seen := map[string]struct{}{}
	for _, item := range items {
		anno, ok := item.(map[string]any)
		if !ok {
			continue
		}
		anno = normalizeAnnotation(anno, canvasURI)
		id := strings.TrimSpace(annStringValue(anno, "id"))
		if id == "" {
			id = strings.TrimSpace(annStringValue(anno, "@id"))
		}
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		normalizedItems = append(normalizedItems, anno)
		b, err := json.Marshal(anno)
		if err != nil {
			return nil, nil, err
		}
		payloads = append(payloads, string(b))
	}
	return normalizedItems, payloads, nil
}

func (h *Handler) writeAnnotationIndex(ctx context.Context, canvasURI string, normalizedItems []any, payloads []string) error {
	if h.annotations == nil {
		return nil
	}
	if err := h.annotations.DeleteByCanvas(ctx, canvasURI); err != nil {
		return err
	}
	for i, item := range normalizedItems {
		anno, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id := strings.TrimSpace(annStringValue(anno, "id"))
		if id == "" {
			continue
		}
		if err := h.annotations.Upsert(ctx, id, canvasURI, payloads[i]); err != nil {
			return err
		}
	}
	return nil
}

func upsertAnnotationItem(items []any, anno map[string]any) []any {
	id := strings.TrimSpace(annStringValue(anno, "id"))
	replaced := false
	for i, item := range items {
		existing, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if strings.TrimSpace(annStringValue(existing, "id")) == id {
			items[i] = anno
			replaced = true
			break
		}
	}
	if !replaced {
		items = append(items, anno)
	}
	return items
}

func deleteAnnotationItem(items []any, id string) []any {
	out := make([]any, 0, len(items))
	for _, item := range items {
		anno, ok := item.(map[string]any)
		if !ok || strings.TrimSpace(annStringValue(anno, "id")) == id {
			continue
		}
		out = append(out, item)
	}
	return out
}

func extractCanvasURIFromRawAnnotation(raw string) string {
	var anno map[string]any
	if err := json.Unmarshal([]byte(raw), &anno); err != nil {
		return ""
	}
	return extractCanvasURI(anno)
}

func annotationItemByID(items []any, id string) map[string]any {
	id = strings.TrimSpace(id)
	for _, item := range items {
		anno, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if strings.TrimSpace(annStringValue(anno, "id")) == id || strings.TrimSpace(annStringValue(anno, "@id")) == id {
			return anno
		}
	}
	return nil
}

func canvasURIFromAnnotationID(id string) string {
	id = strings.TrimSpace(id)
	const prefix = "urn:scribe:annotation:item-image-"
	if !strings.HasPrefix(id, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(id, prefix)
	parts := strings.Split(rest, ":")
	if len(parts) == 0 {
		return ""
	}
	itemImageID := strings.TrimSpace(parts[0])
	if itemImageID == "" {
		return ""
	}
	return "/v1/item-images/" + itemImageID + "/manifest/canvas/page-1"
}
