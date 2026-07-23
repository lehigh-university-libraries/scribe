package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

// tripletPresentationResource is one desired resource in Triplet's generic
// Presentation store. Ordering is intentional: standalone children precede
// their image-scoped parents. The shared item Manifest is projected by the
// independently fenced item-level outbox.
type tripletPresentationResource struct {
	ID      string
	Payload []byte
	// Parallel is limited to standalone child Annotations, which have no
	// interdependencies. Every parent resource remains ordered after them.
	Parallel bool
}

func (h *Handler) buildPublishedPresentationResources(ctx context.Context, itemImageID uint64) ([]tripletPresentationResource, error) {
	if h == nil || h.items == nil || h.annotations == nil || itemImageID == 0 {
		return nil, fmt.Errorf("published Presentation repositories and item image are required")
	}
	publication, err := h.annotations.LoadPublishedPage(ctx, itemImageID)
	if err != nil {
		return nil, err
	}
	if err := iiif.ValidateAnnotationPage([]byte(publication.Payload)); err != nil {
		return nil, fmt.Errorf("validate published AnnotationPage: %w", err)
	}
	image, err := h.items.GetImage(ctx, itemImageID)
	if err != nil {
		return nil, fmt.Errorf("load published item image: %w", err)
	}
	item, err := h.items.Get(ctx, image.ItemID)
	if err != nil {
		return nil, fmt.Errorf("load published item: %w", err)
	}
	if publication.WorkspaceID != item.WorkspaceID || strings.TrimSpace(publication.CanvasURI) != strings.TrimSpace(image.CanvasURI) {
		return nil, fmt.Errorf("published page ownership does not match its item image")
	}
	manifestSource, err := iiif.ParseManifestSource([]byte(item.SourceManifest))
	if err != nil {
		return nil, fmt.Errorf("parse retained Manifest source: %w", err)
	}
	presentationBase := h.publicAnnotationBaseURL()
	imageBase, err := publicIIIFImageBaseURL(config.Get().Config.PublicBaseURL, config.Get().Config.IIIF.Base)
	if err != nil {
		return nil, err
	}

	currentReference := store.AnnotationManifestReference{
		WorkspaceID: publication.WorkspaceID,
		ItemImageID: publication.ItemImageID,
		PageID:      publication.PageID,
		CanvasURI:   publication.CanvasURI,
		Revision:    publication.PublishedRevision,
		ModifiedAt:  publication.PublishedAt,
		Published:   true,
	}

	currentCanvas, err := h.buildPresentationCanvas(item, image, currentReference, manifestSource, presentationBase, imageBase)
	if err != nil {
		return nil, err
	}
	resources := make([]tripletPresentationResource, 0, 8)
	children, err := iiif.AnnotationsFromPage([]byte(publication.Payload))
	if err != nil {
		return nil, err
	}
	for _, child := range children {
		resources = append(resources, tripletPresentationResource{ID: child.ID, Payload: child.Payload, Parallel: true})
	}
	for _, child := range []map[string]any{currentCanvas.PaintingAnnotation, currentCanvas.PaintingPage} {
		resource, err := encodedTripletResource(child)
		if err != nil {
			return nil, err
		}
		resources = append(resources, resource)
	}
	resources = append(resources, tripletPresentationResource{ID: publication.PageID, Payload: []byte(publication.Payload)})
	expectedCanvasID, err := iiif.ItemImageCanvasID(presentationBase, itemImageID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(image.CanvasURI) == expectedCanvasID {
		resource, err := encodedTripletResource(currentCanvas.Canvas)
		if err != nil {
			return nil, err
		}
		resources = append(resources, resource)
	}

	singleManifestID, err := iiif.ItemImageManifestID(presentationBase, itemImageID)
	if err != nil {
		return nil, err
	}
	singleManifest, err := iiif.BuildManifest(
		manifestSource,
		singleManifestID,
		presentationLabel(item, image),
		[]any{iiif.EmbeddedResource(currentCanvas.Canvas)},
		false,
	)
	if err != nil {
		return nil, err
	}
	resources = append(resources, tripletPresentationResource{ID: singleManifestID, Payload: singleManifest})

	if err := validateTripletResourceSet(resources, presentationBase); err != nil {
		return nil, err
	}
	return resources, nil
}

func (h *Handler) buildPublishedItemManifestResource(
	item store.Item,
	references map[uint64]store.AnnotationManifestReference,
	manifestSource *iiif.ManifestSource,
	presentationBase string,
	imageBase string,
) (*tripletPresentationResource, error) {
	if len(references) == 0 {
		return nil, nil
	}
	canvases := make([]any, 0, len(references))
	for _, image := range item.Images {
		reference, published := references[image.ID]
		if !published {
			continue
		}
		canvas, err := h.buildPresentationCanvas(item, image, reference, manifestSource, presentationBase, imageBase)
		if err != nil {
			return nil, err
		}
		canvases = append(canvases, iiif.EmbeddedResource(canvas.Canvas))
	}
	if len(canvases) != len(references) {
		return nil, fmt.Errorf("published Manifest references do not match item images")
	}
	manifestID, err := iiif.ItemManifestID(presentationBase, item.ID)
	if err != nil {
		return nil, err
	}
	payload, err := iiif.BuildManifest(
		manifestSource,
		manifestID,
		item.Name,
		canvases,
		len(canvases) == len(item.Images),
	)
	if err != nil {
		return nil, err
	}
	return &tripletPresentationResource{ID: manifestID, Payload: payload}, nil
}

// buildPublishedItemManifest loads one aggregate public projection after an
// image deletion. A nil resource means the item exists but has no published
// Canvas and its formerly published Manifest should be removed.
func (h *Handler) buildPublishedItemManifest(ctx context.Context, itemID string) (*tripletPresentationResource, error) {
	if h == nil || h.items == nil || h.annotations == nil || strings.TrimSpace(itemID) == "" {
		return nil, fmt.Errorf("published Manifest repositories and item are required")
	}
	item, err := h.items.Get(ctx, strings.TrimSpace(itemID))
	if err != nil {
		return nil, err
	}
	referenceRows, err := h.annotations.ListPublishedItemManifestReferences(ctx, item.ID)
	if err != nil {
		return nil, err
	}
	references := make(map[uint64]store.AnnotationManifestReference, len(referenceRows))
	for _, reference := range referenceRows {
		if reference.WorkspaceID != item.WorkspaceID || !reference.Published {
			return nil, fmt.Errorf("published Manifest reference ownership is inconsistent")
		}
		references[reference.ItemImageID] = reference
	}
	manifestSource, err := iiif.ParseManifestSource([]byte(item.SourceManifest))
	if err != nil {
		return nil, fmt.Errorf("parse retained Manifest source: %w", err)
	}
	presentationBase := h.publicAnnotationBaseURL()
	imageBase, err := publicIIIFImageBaseURL(config.Get().Config.PublicBaseURL, config.Get().Config.IIIF.Base)
	if err != nil {
		return nil, err
	}
	return h.buildPublishedItemManifestResource(item, references, manifestSource, presentationBase, imageBase)
}

func (h *Handler) buildPresentationCanvas(
	item store.Item,
	image store.ItemImage,
	reference store.AnnotationManifestReference,
	manifestSource *iiif.ManifestSource,
	presentationBase string,
	imageBase string,
) (iiif.CanvasResources, error) {
	if reference.WorkspaceID != item.WorkspaceID || reference.ItemImageID != image.ID || strings.TrimSpace(reference.CanvasURI) != strings.TrimSpace(image.CanvasURI) {
		return iiif.CanvasResources{}, fmt.Errorf("item image %d has an inconsistent published page reference", image.ID)
	}
	width, height := itemImageDimensions(image)
	return iiif.BuildCanvasResources(iiif.CanvasBuildInput{
		PublicBaseURL:    presentationBase,
		ItemImageID:      image.ID,
		CanvasID:         image.CanvasURI,
		Width:            uint32(width),  // #nosec G115 -- persisted dimensions are uint32 and clamped to at least one.
		Height:           uint32(height), // #nosec G115 -- persisted dimensions are uint32 and clamped to at least one.
		Label:            presentationLabel(item, image),
		ImageBody:        buildImageBody(image.ImageURL, h.internalAnnotationBaseURL(), imageBase, width, height),
		AnnotationPageID: reference.PageID,
		ManifestSource:   manifestSource,
	})
}

func presentationLabel(item store.Item, image store.ItemImage) string {
	for _, candidate := range []string{image.Label, item.Name, image.ImageURL} {
		if value := strings.TrimSpace(candidate); value != "" {
			return value
		}
	}
	return fmt.Sprintf("item-image-%d", image.ID)
}

func encodedTripletResource(resource map[string]any) (tripletPresentationResource, error) {
	id, _ := resource["id"].(string)
	id = strings.TrimSpace(id)
	if id == "" {
		return tripletPresentationResource{}, fmt.Errorf("presentation resource has no id")
	}
	payload, err := json.Marshal(resource)
	if err != nil {
		return tripletPresentationResource{}, fmt.Errorf("encode Presentation resource: %w", err)
	}
	return tripletPresentationResource{ID: id, Payload: payload}, nil
}

func validateTripletResourceSet(resources []tripletPresentationResource, presentationBase string) error {
	base, err := parseTripletBaseURL(presentationBase)
	if err != nil {
		return fmt.Errorf("invalid Triplet Presentation base")
	}
	basePath := strings.TrimRight(base.EscapedPath(), "/")
	seen := make(map[string]struct{}, len(resources))
	finishedParallelStage := false
	for index, resource := range resources {
		parsed, err := url.Parse(resource.ID)
		if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
			!sameHTTPOrigin(base, parsed) || !strings.HasPrefix(parsed.EscapedPath(), basePath+"/") || len(resource.Payload) == 0 {
			return fmt.Errorf("presentation resource %d is outside the Triplet namespace", index)
		}
		if finishedParallelStage && resource.Parallel {
			return fmt.Errorf("presentation resource %d re-enters the parallel child stage", index)
		}
		if !resource.Parallel {
			finishedParallelStage = true
		}
		if _, duplicate := seen[resource.ID]; duplicate {
			return fmt.Errorf("presentation resource %d duplicates id %q", index, resource.ID)
		}
		var identity struct {
			ID string `json:"id"`
		}
		if err := iiif.DecodeJSON(resource.Payload, &identity); err != nil || strings.TrimSpace(identity.ID) != resource.ID {
			return fmt.Errorf("presentation resource %d payload id does not match its route", index)
		}
		seen[resource.ID] = struct{}{}
	}
	return nil
}
