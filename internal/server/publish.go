package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type publishItemImageEditsRequest struct {
	ItemImageID uint64 `json:"itemImageId"`
}

type publishItemImageEditsResponse struct {
	ItemImageID         uint64 `json:"itemImageId"`
	CanvasURI           string `json:"canvasUri"`
	AnnotationPageJSON  string `json:"annotationPageJson"`
	PublishedAt         string `json:"publishedAt"`
}

func (h *Handler) annotationPageJSONForItemImage(ctx context.Context, itemImageID uint64) (string, string, int, error) {
	run, err := h.fetchOrCacheHOCRRun(ctx, itemImageID)
	if err != nil {
		return "", "", 0, err
	}
	if err := h.ensureItemImageCanvasAndAnnotations(ctx, run, itemImageID); err != nil {
		return "", "", 0, err
	}
	img, err := h.items.GetImage(ctx, itemImageID)
	if err != nil {
		return "", "", 0, err
	}
	canvasURI := strings.TrimSpace(img.CanvasURI)
	if canvasURI == "" {
		return "", "", 0, fmt.Errorf("item image %d canvas URI is not set", itemImageID)
	}

	payloads, err := h.annotations.SearchByCanvas(ctx, canvasURI)
	if err != nil {
		return "", "", 0, err
	}
	items := make([]any, 0, len(payloads))
	for _, raw := range payloads {
		var obj map[string]any
		if err := json.Unmarshal([]byte(raw), &obj); err != nil {
			continue
		}
		items = append(items, normalizeAnnotation(obj, canvasURI))
	}
	page := map[string]any{
		"@context": annotationPageContexts(),
		"id":       annotationPageID(canvasURI),
		"type":     "AnnotationPage",
		"items":    items,
	}
	b, err := json.Marshal(page)
	if err != nil {
		return "", "", 0, err
	}
	return string(b), canvasURI, len(items), nil
}

func (h *Handler) handlePublishItemImageEdits(w http.ResponseWriter, r *http.Request) {
	var req publishItemImageEditsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.ItemImageID == 0 {
		writeError(w, http.StatusBadRequest, "itemImageId is required")
		return
	}

	pageJSON, canvasURI, count, err := h.annotationPageJSONForItemImage(r.Context(), req.ItemImageID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	publishedAt := time.Now().UTC().Format(time.RFC3339Nano)
	h.publishEvent("dev.scribe.annotations.published", subjectForItemImage(req.ItemImageID), map[string]any{
		"itemImageId":        req.ItemImageID,
		"canvasUri":          canvasURI,
		"annotationCount":    count,
		"annotationPageJson": pageJSON,
		"publishedAt":        publishedAt,
	})

	writeJSON(w, http.StatusOK, publishItemImageEditsResponse{
		ItemImageID:        req.ItemImageID,
		CanvasURI:          canvasURI,
		AnnotationPageJSON: pageJSON,
		PublishedAt:        publishedAt,
	})
}
