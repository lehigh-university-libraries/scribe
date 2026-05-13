package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/lehigh-university-libraries/scribe/internal/auth"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

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
	var metadata map[string]any
	if raw := strings.TrimSpace(metadataJSON); raw != "" {
		if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
			return store.Context{}, fmt.Errorf("invalid metadata json: %w", err)
		}
	}
	returnContext, _, err := h.contexts.ResolveForWorkspace(ctx, h.currentWorkspaceID(ctx), metadata)
	return returnContext, err
}

func (h *Handler) authorizeCanvasRead(ctx context.Context, canvasURI string) error {
	canvasURI = strings.TrimSpace(canvasURI)
	if canvasURI == "" {
		return fmt.Errorf("canvas uri is required")
	}
	if matches := itemImageFromCanvasPattern.FindStringSubmatch(canvasURI); len(matches) >= 2 {
		itemImageID, err := strconv.ParseUint(strings.TrimSpace(matches[1]), 10, 64)
		if err != nil {
			return sql.ErrNoRows
		}
		_, err = h.itemImageForRequest(ctx, itemImageID)
		return err
	}
	_, err := h.items.GetImageByCanvasURIForWorkspace(ctx, canvasURI, h.currentWorkspaceID(ctx))
	if err == nil {
		return nil
	}
	if err != sql.ErrNoRows {
		return err
	}
	for _, manifestURL := range manifestURLCandidatesFromCanvasURI(canvasURI) {
		if ingestErr := h.autoIngestManifest(ctx, manifestURL); ingestErr != nil {
			continue
		}
		if _, retryErr := h.items.GetImageByCanvasURIForWorkspace(ctx, canvasURI, h.currentWorkspaceID(ctx)); retryErr == nil {
			return nil
		}
	}
	return sql.ErrNoRows
}

func (h *Handler) authorizeAnnotationJSON(ctx context.Context, raw string) error {
	var annotation map[string]any
	if err := json.Unmarshal([]byte(raw), &annotation); err != nil {
		return err
	}
	return h.authorizeCanvasRead(ctx, extractCanvasURI(annotation))
}

func (h *Handler) ocrRunForRequest(ctx context.Context, sessionID string, itemImageID uint64) (store.OCRRun, error) {
	var (
		run store.OCRRun
		err error
	)
	switch {
	case itemImageID > 0:
		if _, err = h.itemImageForRequest(ctx, itemImageID); err != nil {
			return store.OCRRun{}, err
		}
		run, err = h.ocrRuns.GetByItemImageID(ctx, itemImageID)
	case strings.TrimSpace(sessionID) != "":
		run, err = h.ocrRuns.Get(ctx, sessionID)
		if err == nil && run.ItemImageID != nil {
			if _, authErr := h.itemImageForRequest(ctx, *run.ItemImageID); authErr != nil {
				return store.OCRRun{}, authErr
			}
		}
	default:
		return store.OCRRun{}, sql.ErrNoRows
	}
	return run, err
}
