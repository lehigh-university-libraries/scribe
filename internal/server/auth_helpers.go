package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/lehigh-university-libraries/scribe/internal/auth"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

var errInvalidContextMetadata = errors.New("invalid context metadata")

const maxContextMetadataJSONBytes = 64 << 10

func (h *Handler) currentUserID(ctx context.Context) uint64 {
	if h.auth == nil {
		return store.AnonymousUserID
	}
	return auth.UserIDFromContext(ctx)
}

func (h *Handler) currentWorkspaceID(ctx context.Context) uint64 {
	if h.auth == nil {
		return store.AnonymousWorkspaceID
	}
	return auth.WorkspaceIDFromContext(ctx)
}

func (h *Handler) itemForRequest(ctx context.Context, itemID string) (store.Item, error) {
	return h.items.GetForWorkspace(ctx, itemID, h.currentWorkspaceID(ctx))
}

func (h *Handler) itemImageForRequest(ctx context.Context, itemImageID uint64) (store.ItemImage, error) {
	return h.items.GetImageForWorkspace(ctx, itemImageID, h.currentWorkspaceID(ctx))
}

func (h *Handler) contextForRead(ctx context.Context, contextID uint64) (store.Context, error) {
	return h.contexts.GetForWorkspace(ctx, contextID, h.currentWorkspaceID(ctx))
}

func (h *Handler) resolveContextForRequest(ctx context.Context, contextID uint64, metadataJSON string) (store.Context, error) {
	if contextID > 0 {
		return h.contextForRead(ctx, contextID)
	}
	metadata, err := decodeContextMetadataJSON(metadataJSON)
	if err != nil {
		return store.Context{}, err
	}
	returnContext, _, err := h.contexts.ResolveForWorkspace(ctx, h.currentWorkspaceID(ctx), metadata)
	return returnContext, err
}

func decodeContextMetadataJSON(raw string) (map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	if len(raw) > maxContextMetadataJSONBytes {
		return nil, fmt.Errorf("%w: metadata_json exceeds %d bytes", errInvalidContextMetadata, maxContextMetadataJSONBytes)
	}
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.UseNumber()
	var metadata map[string]any
	if err := decoder.Decode(&metadata); err != nil || metadata == nil {
		return nil, fmt.Errorf("%w: metadata_json must be a JSON object", errInvalidContextMetadata)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("%w: metadata_json contains trailing data", errInvalidContextMetadata)
	}
	return metadata, nil
}

func (h *Handler) authorizeAnnotationJSON(ctx context.Context, itemImageID uint64, raw string) error {
	var annotation map[string]any
	if err := iiif.DecodeJSON([]byte(raw), &annotation); err != nil {
		return fmt.Errorf("invalid annotation json: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(annStringValue(annotation, "type")), "Annotation") {
		return fmt.Errorf("resource is not an Annotation")
	}
	if !strings.EqualFold(strings.TrimSpace(annStringValue(annotation, "textGranularity")), "line") {
		return fmt.Errorf("resource is not a line annotation")
	}
	canonical, err := h.annotations.LoadPage(ctx, h.currentWorkspaceID(ctx), itemImageID)
	if err != nil {
		return err
	}
	canvasURI := extractCanvasURI(annotation)
	identity, err := iiif.PageIdentityFromAnnotationID(strings.TrimSpace(annStringValue(annotation, "id")), canvasURI)
	if err != nil || identity.ItemImageID != itemImageID {
		return fmt.Errorf("annotation id is not a child of the requested item image")
	}
	pageID, err := iiif.CanonicalPageID(identity.PublicBaseURL, identity.ItemImageID)
	if err != nil || pageID != canonical.PageID {
		return fmt.Errorf("annotation id is not a canonical child of the requested page")
	}
	if canvasURI != canonical.CanvasURI {
		return fmt.Errorf("annotation targets a different canvas")
	}
	return nil
}

// authorizeAnnotationPageJSON verifies the identity and every target on a
// client-supplied canonical page against the tenant-scoped page repository.
// The client may submit a local draft, so authorization deliberately does not
// require byte equality with the persisted payload.
func (h *Handler) authorizeAnnotationPageJSON(ctx context.Context, itemImageID uint64, raw string) error {
	var page map[string]any
	if err := iiif.DecodeJSON([]byte(raw), &page); err != nil {
		return fmt.Errorf("invalid annotation page json: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(annStringValue(page, "type")), "AnnotationPage") {
		return fmt.Errorf("resource is not an AnnotationPage")
	}
	pageID := strings.TrimSpace(annStringValue(page, "id"))
	payloadItemImageID, err := itemImageIDFromAnnotationPageID(pageID)
	if err != nil || payloadItemImageID != itemImageID {
		return fmt.Errorf("annotation page does not belong to the requested item image")
	}
	if _, err := h.itemImageForRequest(ctx, itemImageID); err != nil {
		return err
	}
	canonical, err := h.annotations.LoadPage(ctx, h.currentWorkspaceID(ctx), itemImageID)
	if err != nil {
		return err
	}
	if pageID != canonical.PageID {
		return fmt.Errorf("annotation page id is not canonical")
	}
	identity, err := iiif.PageIdentityFromPageID(canonical.PageID, canonical.CanvasURI)
	if err != nil {
		return err
	}
	return iiif.ValidateCanonicalAnnotationPage([]byte(raw), identity)
}

func itemImageIDFromAnnotationPageID(raw string) (uint64, error) {
	return iiif.ItemImageIDFromAnnotationPageID(raw)
}
