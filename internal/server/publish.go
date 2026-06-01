package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
)

func (h *Handler) annotationPageJSONForItemImage(ctx context.Context, itemImageID uint64) (string, string, int, error) {
	run, err := h.fetchOrCacheHOCRRun(ctx, itemImageID)
	if err != nil {
		return "", "", 0, err
	}
	if err := h.ensureItemImageCanvasAndAnnotations(ctx, run, itemImageID); err != nil {
		return "", "", 0, err
	}
	img, err := h.itemImageForRequest(ctx, itemImageID)
	if err != nil {
		return "", "", 0, err
	}
	canvasURI := strings.TrimSpace(img.CanvasURI)
	if canvasURI == "" {
		return "", "", 0, fmt.Errorf("item image %d canvas URI is not set", itemImageID)
	}

	items, err := h.currentAnnotationItems(ctx, canvasURI, h.internalAnnotationBaseURL())
	if err != nil {
		return "", "", 0, err
	}
	page := map[string]any{
		"@context": annotationPageContexts(),
		"id":       h.tripletAnnotationPageID(canvasURI),
		"type":     "AnnotationPage",
		"items":    items,
	}
	b, err := json.Marshal(page)
	if err != nil {
		return "", "", 0, err
	}
	return string(b), canvasURI, len(items), nil
}

func (h *Handler) PublishItemImageEdits(ctx context.Context, req *connect.Request[scribev1.PublishItemImageEditsRequest]) (*connect.Response[scribev1.PublishItemImageEditsResponse], error) {
	itemImageID := req.Msg.GetItemImageId()
	if itemImageID == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("item_image_id is required"))
	}
	if _, err := h.itemImageForRequest(ctx, itemImageID); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("item image not found"))
	}
	pageJSON, canvasURI, count, err := h.annotationPageJSONForItemImage(ctx, itemImageID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	publishedAt := time.Now().UTC().Format(time.RFC3339Nano)
	h.publishEvent("dev.scribe.annotations.published", subjectForItemImage(itemImageID), map[string]any{
		"itemImageId":        itemImageID,
		"canvasUri":          canvasURI,
		"annotationCount":    count,
		"annotationPageJson": pageJSON,
		"publishedAt":        publishedAt,
	})
	return connect.NewResponse(&scribev1.PublishItemImageEditsResponse{
		ItemImageId:        itemImageID,
		CanvasUri:          canvasURI,
		AnnotationPageJson: pageJSON,
		PublishedAt:        publishedAt,
	}), nil
}
