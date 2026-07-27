package server

import "github.com/lehigh-university-libraries/scribe/internal/store"

func itemEventData(item store.Item, image store.ItemImage, revision uint64) map[string]any {
	data := map[string]any{
		"workspaceId": item.WorkspaceID,
		"itemId":      item.ID,
		"itemImageId": image.ID,
		"canvasUri":   image.CanvasURI,
		"revision":    revision,
	}
	if item.Metadata != nil {
		data["metadata"] = item.Metadata
	}
	if item.CallerIdempotencyKey != "" {
		data["idempotencyKey"] = item.CallerIdempotencyKey
	}
	if item.ExternalReferenceID != "" {
		data["externalReferenceId"] = item.ExternalReferenceID
	}
	return data
}

func mergeEventData(base, extra map[string]any) map[string]any {
	if base == nil {
		base = make(map[string]any, len(extra))
	}
	for key, value := range extra {
		base[key] = value
	}
	return base
}
