package server

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"connectrpc.com/connect"
	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
)

// GetEditorManifest returns a private draft Manifest through Connect. The
// browser loads these bytes from an object URL, so Scribe does not implement a
// second Presentation HTTP surface beside Triplet.
func (h *Handler) GetEditorManifest(ctx context.Context, req *connect.Request[scribev1.GetEditorManifestRequest]) (*connect.Response[scribev1.GetEditorManifestResponse], error) {
	itemImageID := req.Msg.GetItemImageId()
	image, err := h.itemImageForRequest(ctx, itemImageID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("item image not found"))
	}
	item, err := h.itemForRequest(ctx, image.ItemID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("item not found"))
	}
	manifest, selectedCanvasID, err := h.buildEditorManifest(ctx, item, itemImageID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("build editor manifest"))
	}
	return connect.NewResponse(&scribev1.GetEditorManifestResponse{
		Item:             storeItemToProto(item),
		ManifestJson:     string(manifest),
		SelectedCanvasId: selectedCanvasID,
	}), nil
}

func (h *Handler) buildEditorManifest(ctx context.Context, item store.Item, selectedItemImageID uint64) ([]byte, string, error) {
	if h == nil || h.annotations == nil || h.items == nil {
		return nil, "", fmt.Errorf("editor manifest repositories are not configured")
	}
	if item.ID == "" || item.WorkspaceID == 0 || len(item.Images) == 0 {
		return nil, "", fmt.Errorf("editor manifest item is incomplete")
	}
	referenceRows, err := h.annotations.ListItemManifestReferences(ctx, item.WorkspaceID, item.ID)
	if err != nil {
		return nil, "", err
	}
	references := make(map[uint64]store.AnnotationManifestReference, len(referenceRows))
	for _, reference := range referenceRows {
		if reference.WorkspaceID != item.WorkspaceID {
			return nil, "", fmt.Errorf("canonical annotation workspace does not match item")
		}
		references[reference.ItemImageID] = reference
	}
	manifestSource, err := iiif.ParseManifestSource([]byte(item.SourceManifest))
	if err != nil {
		return nil, "", err
	}
	presentationBase := h.publicAnnotationBaseURL()
	imageBase, err := publicIIIFImageBaseURL(config.Get().Config.PublicBaseURL, config.Get().Config.IIIF.Base)
	if err != nil {
		return nil, "", err
	}
	canvases := make([]any, 0, len(item.Images))
	selectedCanvasID := ""
	for _, image := range item.Images {
		reference, ok := references[image.ID]
		if !ok || strings.TrimSpace(reference.PageID) == "" || strings.TrimSpace(reference.CanvasURI) != strings.TrimSpace(image.CanvasURI) {
			return nil, "", fmt.Errorf("item image %d has no consistent canonical annotation page", image.ID)
		}
		width, height := itemImageDimensions(image)
		label := strings.TrimSpace(image.Label)
		if label == "" {
			label = strings.TrimSpace(image.ImageURL)
		}
		resources, err := iiif.BuildCanvasResources(iiif.CanvasBuildInput{
			PublicBaseURL:    presentationBase,
			ItemImageID:      image.ID,
			CanvasID:         image.CanvasURI,
			Width:            uint32(width),  // #nosec G115 -- persisted dimensions are uint32 and clamped to at least one.
			Height:           uint32(height), // #nosec G115 -- persisted dimensions are uint32 and clamped to at least one.
			Label:            label,
			ImageBody:        buildImageBody(image.ImageURL, h.internalAnnotationBaseURL(), imageBase, width, height),
			AnnotationPageID: reference.PageID,
			ManifestSource:   manifestSource,
		})
		if err != nil {
			return nil, "", fmt.Errorf("build Canvas for item image %d: %w", image.ID, err)
		}
		canvases = append(canvases, iiif.EmbeddedResource(resources.Canvas))
		if image.ID == selectedItemImageID {
			selectedCanvasID = strings.TrimSpace(image.CanvasURI)
		}
	}
	if selectedCanvasID == "" {
		return nil, "", fmt.Errorf("selected item image is not part of item")
	}
	manifestID, err := iiif.ItemManifestID(presentationBase, item.ID)
	if err != nil {
		return nil, "", err
	}
	manifest, err := iiif.BuildManifest(manifestSource, manifestID, item.Name, canvases, true)
	if err != nil {
		return nil, "", err
	}
	return manifest, selectedCanvasID, nil
}

func publicIIIFImageBaseURL(publicBaseURL, configuredBase string) (string, error) {
	configuredBase = strings.TrimSpace(configuredBase)
	if parsed, err := url.Parse(configuredBase); err == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https") {
		return strings.TrimRight(configuredBase, "/"), nil
	}
	publicBaseURL = strings.TrimRight(strings.TrimSpace(publicBaseURL), "/")
	parsed, err := url.Parse(publicBaseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("public base URL must be an absolute HTTP(S) URL")
	}
	if configuredBase == "" {
		configuredBase = "/iiif/3"
	}
	if !strings.HasPrefix(configuredBase, "/") {
		configuredBase = "/" + configuredBase
	}
	return publicBaseURL + strings.TrimRight(configuredBase, "/"), nil
}
