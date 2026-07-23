package iiif

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	textgranularityschema "github.com/libops/iiif-spec/extension/textgranularity/schema"
	presentationschema "github.com/libops/iiif-spec/presentation/v3/schema"
)

const (
	// MaxSourceManifestBytes bounds the raw Presentation 3 projection retained
	// for lossless descriptive and extension-property round trips.
	MaxSourceManifestBytes = 20 << 20
	// MaxCanvasURIBytes matches item_images.canvas_uri. Imported Canvas IDs are
	// canonical targets/provenance; uploaded images receive shorter Scribe IDs.
	MaxCanvasURIBytes = 1024
)

// CanvasBuildInput contains the Scribe-owned state needed to build one Canvas
// and its painting resources. ManifestSource may contain a validated upstream
// Presentation 3 Manifest; descriptive and extension properties are copied
// from its matching Canvas while the fields below always win.
type CanvasBuildInput struct {
	PublicBaseURL    string
	ItemImageID      uint64
	CanvasID         string
	Width            uint32
	Height           uint32
	Label            string
	ImageBody        map[string]any
	AnnotationPageID string
	SeeAlso          []any
	ManifestSource   *ManifestSource
}

// ManifestSource is one bounded, indexed source Manifest projection prepared
// for resource emission. Its maps are private and cloned at the builder
// boundary so callers cannot mutate the retained provenance. Preparing it once
// keeps aggregate Manifest emission O(n) instead of decoding and scanning the
// entire source separately for every Canvas.
type ManifestSource struct {
	manifest map[string]any
	canvases map[string]map[string]any
}

// ParseManifestSource decodes source bytes that passed ValidateSourceManifest
// and indexes their Canvases by exact ID. Empty bytes represent a file upload
// with no imported Manifest provenance.
func ParseManifestSource(raw []byte) (*ManifestSource, error) {
	source := &ManifestSource{canvases: make(map[string]map[string]any)}
	if len(raw) == 0 {
		return source, nil
	}
	if len(raw) > MaxSourceManifestBytes {
		return nil, fmt.Errorf("source manifest exceeds %d bytes", MaxSourceManifestBytes)
	}
	if err := DecodeJSON(raw, &source.manifest); err != nil {
		return nil, fmt.Errorf("decode source manifest projection: %w", err)
	}
	if resourceStringValue(source.manifest, "type") != "Manifest" {
		return nil, fmt.Errorf("source manifest projection must be a Presentation 3 Manifest")
	}
	items, ok := source.manifest["items"].([]any)
	if !ok {
		return nil, fmt.Errorf("source manifest projection must contain a Canvas items array")
	}
	for index, value := range items {
		canvas, ok := value.(map[string]any)
		if !ok || resourceStringValue(canvas, "type") != "Canvas" {
			return nil, fmt.Errorf("source manifest item %d must be a Canvas", index+1)
		}
		canvasID := resourceStringValue(canvas, "id")
		if canvasID == "" {
			return nil, fmt.Errorf("source manifest Canvas %d must have an id", index+1)
		}
		if _, duplicate := source.canvases[canvasID]; duplicate {
			return nil, fmt.Errorf("source manifest contains duplicate Canvas id %q", canvasID)
		}
		source.canvases[canvasID] = canvas
	}
	return source, nil
}

func (source *ManifestSource) context() any {
	if source == nil || source.manifest == nil {
		return nil
	}
	return cloneValue(source.manifest["@context"])
}

func (source *ManifestSource) canvas(canvasID string) map[string]any {
	if source == nil {
		return nil
	}
	return cloneObject(source.canvases[canvasID])
}

// CanvasResources are standalone Presentation 3 resources. Nested resources
// in Canvas and PaintingPage omit @context as required by JSON-LD embedding;
// each top-level map is independently schema-valid. Painting resources use
// Scribe routes; an imported Canvas intentionally retains its external ID.
type CanvasResources struct {
	Canvas             map[string]any
	PaintingPage       map[string]any
	PaintingAnnotation map[string]any
}

// ResourceDocument is one independently dereferenceable Presentation 3
// resource encoded with its inherited top-level JSON-LD context restored.
type ResourceDocument struct {
	ID      string
	Payload []byte
}

// EmbeddedResource returns a copy suitable for nesting in another IIIF
// Presentation resource. A nested resource inherits the top-level @context.
func EmbeddedResource(resource map[string]any) map[string]any {
	return embeddedResource(resource)
}

// PaintingPageID returns the Scribe-owned painting AnnotationPage identity.
func PaintingPageID(publicBaseURL string, itemImageID uint64) (string, error) {
	base, err := ItemImageCanvasID(publicBaseURL, itemImageID)
	if err != nil {
		return "", err
	}
	return base + "/painting", nil
}

// PaintingAnnotationID returns the Scribe-owned painting Annotation identity.
func PaintingAnnotationID(publicBaseURL string, itemImageID uint64) (string, error) {
	pageID, err := PaintingPageID(publicBaseURL, itemImageID)
	if err != nil {
		return "", err
	}
	return pageID + "/items/image", nil
}

// ItemImageManifestID returns the stable Presentation 3 Manifest identity for
// one image. Triplet serves this resource when the image is published; the
// editor may use the same identity in a private Connect-delivered draft.
func ItemImageManifestID(publicBaseURL string, itemImageID uint64) (string, error) {
	base, err := itemImageResourceBase(publicBaseURL, itemImageID)
	if err != nil {
		return "", err
	}
	return base + "/manifest", nil
}

// ItemManifestID returns the stable aggregate Presentation 3 Manifest
// identity for a Scribe item.
func ItemManifestID(publicBaseURL, itemID string) (string, error) {
	base, err := validHTTPBase(publicBaseURL)
	if err != nil {
		return "", err
	}
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		return "", fmt.Errorf("item id is required")
	}
	return base + "/item-" + url.PathEscape(itemID) + "/manifest", nil
}

// IsTextGranularity reports whether value is an IIIF Text Granularity term
// supported by Scribe's OCR-derived projections.
func IsTextGranularity(value string) bool {
	return textgranularityschema.IsKnownLevel(strings.TrimSpace(value))
}

// BuildCanvasResources constructs independently valid Canvas, painting page,
// and painting annotation documents. Triplet serves Scribe-minted resources;
// the Canvas ID is the preserved import target or a hosted upload ID.
func BuildCanvasResources(input CanvasBuildInput) (CanvasResources, error) {
	if input.ItemImageID == 0 {
		return CanvasResources{}, fmt.Errorf("item image id is required")
	}
	canvasID := strings.TrimSpace(input.CanvasID)
	if err := requireHTTPURL(canvasID, "canvas id"); err != nil {
		return CanvasResources{}, err
	}
	if input.Width == 0 || input.Height == 0 {
		return CanvasResources{}, fmt.Errorf("canvas width and height must be positive")
	}
	if err := requireHTTPURL(strings.TrimSpace(input.AnnotationPageID), "annotation page id"); err != nil {
		return CanvasResources{}, err
	}
	paintingPageID, err := PaintingPageID(input.PublicBaseURL, input.ItemImageID)
	if err != nil {
		return CanvasResources{}, err
	}
	paintingAnnotationID, err := PaintingAnnotationID(input.PublicBaseURL, input.ItemImageID)
	if err != nil {
		return CanvasResources{}, err
	}
	sourceContext := input.ManifestSource.context()
	resourceContext := presentationContexts(sourceContext)
	sourceCanvas := input.ManifestSource.canvas(canvasID)

	body := cloneObject(input.ImageBody)
	if body == nil {
		return CanvasResources{}, fmt.Errorf("painting annotation image body is required")
	}
	if sourceCanvas != nil {
		if sourceBody := sourceImageBody(sourceCanvas, resourceStringValue(body, "id")); sourceBody != nil {
			merged := cloneObject(sourceBody)
			for key, value := range body {
				// Imported, schema-validated format and Image API service
				// declarations are more authoritative than URL inference. Scribe
				// still owns the selected identity and current dimensions.
				if (key == "format" || key == "service") && merged[key] != nil {
					continue
				}
				merged[key] = value
			}
			body = merged
		}
	}

	paintingAnnotation := map[string]any{
		"@context":   resourceContext,
		"id":         paintingAnnotationID,
		"type":       "Annotation",
		"motivation": "painting",
		"target":     canvasID,
		"body":       body,
	}
	paintingPage := map[string]any{
		"@context": resourceContext,
		"id":       paintingPageID,
		"type":     "AnnotationPage",
		"items":    []any{embeddedResource(paintingAnnotation)},
	}

	canvas := sourceCanvas
	if canvas == nil {
		canvas = make(map[string]any)
	}
	canvas["@context"] = presentationContexts(sourceContext, canvas["@context"])
	canvas["id"] = canvasID
	canvas["type"] = "Canvas"
	canvas["width"] = input.Width
	canvas["height"] = input.Height
	canvas["items"] = []any{embeddedResource(paintingPage)}
	delete(canvas, "@id")
	delete(canvas, "@type")
	if !validLanguageMap(canvas["label"]) {
		label := strings.TrimSpace(input.Label)
		if label == "" {
			label = canvasID
		}
		canvas["label"] = map[string]any{"none": []any{label}}
	}
	canvas["annotations"] = appendUniqueReference(canvas["annotations"], strings.TrimSpace(input.AnnotationPageID), "AnnotationPage")
	for _, value := range input.SeeAlso {
		canvas["seeAlso"] = appendResourceValue(canvas["seeAlso"], cloneValue(value))
	}

	resources := CanvasResources{Canvas: canvas, PaintingPage: paintingPage, PaintingAnnotation: paintingAnnotation}
	if err := validateResourceMap("Canvas", resources.Canvas); err != nil {
		return CanvasResources{}, err
	}
	if err := validateResourceMap("AnnotationPage", resources.PaintingPage); err != nil {
		return CanvasResources{}, err
	}
	if err := validateResourceMap("Annotation", resources.PaintingAnnotation); err != nil {
		return CanvasResources{}, err
	}
	return resources, nil
}

// BuildManifest merges a bounded source Presentation 3 Manifest projection
// with Scribe-owned identity and Canvas state. When preserveStructures is
// false, ranges are omitted because a partial publication cannot safely emit
// references to unpublished source Canvases, and start is retained only when
// it references an emitted Canvas.
func BuildManifest(source *ManifestSource, id, fallbackLabel string, canvases []any, preserveStructures bool) ([]byte, error) {
	manifest := make(map[string]any)
	canvasIDMap := make(map[string]string)
	if source != nil && source.manifest != nil {
		manifest = cloneObject(source.manifest)
		delete(manifest, "items")
		sourceCanvases, _ := source.manifest["items"].([]any)
		if len(sourceCanvases) == len(canvases) {
			for index := range sourceCanvases {
				sourceCanvas, sourceOK := sourceCanvases[index].(map[string]any)
				canonicalCanvas, canonicalOK := canvases[index].(map[string]any)
				if sourceOK && canonicalOK {
					canvasIDMap[resourceStringValue(sourceCanvas, "id")] = resourceStringValue(canonicalCanvas, "id")
				}
			}
		}
	}
	if err := requireHTTPURL(strings.TrimSpace(id), "manifest id"); err != nil {
		return nil, err
	}
	manifest["@context"] = presentationContexts(manifest["@context"])
	manifest["id"] = strings.TrimSpace(id)
	manifest["type"] = "Manifest"
	manifest["items"] = canvases
	delete(manifest, "@id")
	delete(manifest, "@type")
	if !validLanguageMap(manifest["label"]) {
		label := strings.TrimSpace(fallbackLabel)
		if label == "" {
			label = "Untitled Item"
		}
		manifest["label"] = map[string]any{"none": []any{label}}
	}
	if !preserveStructures {
		delete(manifest, "structures")
		if start, ok := manifest["start"]; ok && !referencesEmittedCanvas(start, canvases) {
			delete(manifest, "start")
		}
	} else if structures, ok := manifest["structures"]; ok {
		manifest["structures"] = rewriteCanvasReferences(structures, canvasIDMap)
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("encode IIIF Presentation 3 Manifest: %w", err)
	}
	if err := ValidateManifest(encoded); err != nil {
		return nil, err
	}
	return encoded, nil
}

// ValidateSourceManifest validates a complete extension-aware Presentation 3
// Manifest. The same bounded raw document is retained so descriptive and
// extension properties survive import and publication.
func ValidateSourceManifest(raw []byte) error {
	if len(raw) > MaxSourceManifestBytes {
		return fmt.Errorf("source manifest exceeds %d bytes", MaxSourceManifestBytes)
	}
	var source map[string]any
	if err := DecodeJSON(raw, &source); err != nil {
		return fmt.Errorf("decode source manifest: %w", err)
	}
	if !strings.EqualFold(resourceStringValue(source, "type"), "Manifest") {
		return fmt.Errorf("source document is not a Presentation 3 Manifest")
	}
	return ValidateManifest(raw)
}

func rewriteCanvasReferences(value any, replacements map[string]string) any {
	switch typed := value.(type) {
	case []any:
		out := make([]any, len(typed))
		for index, entry := range typed {
			out[index] = rewriteCanvasReferences(entry, replacements)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, entry := range typed {
			out[key] = rewriteCanvasReferences(entry, replacements)
		}
		if strings.EqualFold(resourceStringValue(out, "type"), "Canvas") {
			if replacement := replacements[resourceStringValue(out, "id")]; replacement != "" {
				out["id"] = replacement
			}
		}
		return out
	default:
		return typed
	}
}

// AnnotationFromPage returns one canonical child as a standalone resource,
// preserving the page's extension contexts and unknown annotation properties.
func AnnotationFromPage(pageRaw []byte, annotationID string) ([]byte, error) {
	want := strings.TrimSpace(annotationID)
	resources, err := AnnotationsFromPage(pageRaw)
	if err != nil {
		return nil, err
	}
	for _, resource := range resources {
		if resource.ID == want {
			return resource.Payload, nil
		}
	}
	return nil, fmt.Errorf("annotation %q not found", want)
}

// AnnotationsFromPage materializes every embedded Annotation as a standalone
// resource in one bounded pass. It preserves unknown properties and restores
// the page's extension contexts without repeatedly rescanning the page.
func AnnotationsFromPage(pageRaw []byte) ([]ResourceDocument, error) {
	if err := ValidateAnnotationPage(pageRaw); err != nil {
		return nil, err
	}
	var page map[string]any
	if err := DecodeJSON(pageRaw, &page); err != nil {
		return nil, fmt.Errorf("decode annotation page: %w", err)
	}
	items, _ := page["items"].([]any)
	resources := make([]ResourceDocument, 0, len(items))
	for index, value := range items {
		annotation, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("annotation page item %d must be an object", index)
		}
		id := resourceStringValue(annotation, "id")
		if id == "" {
			return nil, fmt.Errorf("annotation page item %d has no id", index)
		}
		standalone := cloneObject(annotation)
		standalone["@context"] = presentationContexts(page["@context"])
		encoded, err := json.Marshal(standalone)
		if err != nil {
			return nil, fmt.Errorf("encode annotation item %d: %w", index, err)
		}
		if err := ValidateAnnotation(encoded); err != nil {
			return nil, fmt.Errorf("validate annotation item %d: %w", index, err)
		}
		resources = append(resources, ResourceDocument{ID: id, Payload: encoded})
	}
	return resources, nil
}

// ValidateCanvas validates a standalone Presentation 3 Canvas document.
func ValidateCanvas(raw []byte) error {
	var resource map[string]any
	if err := DecodeJSON(raw, &resource); err != nil {
		return fmt.Errorf("decode canvas context: %w", err)
	}
	if resourceStringValue(resource, "type") != "Canvas" {
		return fmt.Errorf("presentation document type is %q, want %q", resourceStringValue(resource, "type"), "Canvas")
	}
	if err := validatePresentationContext(resource["@context"]); err != nil {
		return err
	}
	if err := presentationschema.ValidateExtensibleCanvasBytes(raw); err != nil {
		return fmt.Errorf("invalid IIIF Presentation 3 Canvas: %w", err)
	}
	return nil
}

// ValidateAnnotation validates a standalone Presentation 3 Annotation and the
// Text Granularity semantics used by canonical OCR annotations.
func ValidateAnnotation(raw []byte) error {
	if err := textgranularityschema.ValidateAnnotationBytes(raw); err != nil {
		return fmt.Errorf("invalid IIIF Presentation 3 Annotation: %w", err)
	}
	var annotation map[string]any
	if err := DecodeJSON(raw, &annotation); err != nil {
		return fmt.Errorf("decode annotation: %w", err)
	}
	if err := validatePresentationContext(annotation["@context"]); err != nil {
		return fmt.Errorf("invalid annotation context: %w", err)
	}
	granularity := strings.TrimSpace(resourceStringValue(annotation, "textGranularity"))
	if granularity == "" {
		return nil
	}
	if !IsTextGranularity(granularity) {
		return fmt.Errorf("annotation has unsupported textGranularity %q", granularity)
	}
	if !containsString(annotation["motivation"], "supplementing") {
		return fmt.Errorf("annotation with textGranularity must have supplementing motivation")
	}
	if !containsString(annotation["@context"], TextGranularityContext) {
		return fmt.Errorf("annotation uses textGranularity without the Text Granularity context")
	}
	if !hasTextualBody(annotation["body"]) {
		return fmt.Errorf("annotation with textGranularity must use a TextualBody")
	}
	fragment, hasFragment, err := annotationTargetFragment(annotation["target"])
	if err != nil {
		return fmt.Errorf("annotation has invalid FragmentSelector: %w", err)
	}
	if granularity != "page" && !hasFragment {
		return fmt.Errorf("annotation with textGranularity %q requires an xywh FragmentSelector", granularity)
	}
	if hasFragment {
		return validatePixelXYWH(fragment)
	}
	return nil
}

func validateResourceMap(kind string, resource map[string]any) error {
	encoded, err := json.Marshal(resource)
	if err != nil {
		return fmt.Errorf("encode %s: %w", kind, err)
	}
	switch kind {
	case "Canvas":
		return ValidateCanvas(encoded)
	case "AnnotationPage":
		return ValidateAnnotationPage(encoded)
	case "Annotation":
		return ValidateAnnotation(encoded)
	default:
		return fmt.Errorf("unsupported IIIF resource type %q", kind)
	}
}

func sourceImageBody(canvas map[string]any, imageID string) map[string]any {
	var walk func(any) map[string]any
	walk = func(value any) map[string]any {
		switch typed := value.(type) {
		case []any:
			for _, entry := range typed {
				if found := walk(entry); found != nil {
					return found
				}
			}
		case map[string]any:
			if strings.EqualFold(resourceStringValue(typed, "type"), "Image") && resourceStringValue(typed, "id") == imageID {
				return typed
			}
			for _, key := range []string{"items", "body", "source"} {
				if found := walk(typed[key]); found != nil {
					return found
				}
			}
		}
		return nil
	}
	return walk(canvas["items"])
}

func referencesEmittedCanvas(value any, canvases []any) bool {
	emitted := make(map[string]struct{}, len(canvases))
	for _, value := range canvases {
		canvas, ok := value.(map[string]any)
		if ok {
			emitted[resourceStringValue(canvas, "id")] = struct{}{}
		}
	}
	var referencedCanvasID func(any) string
	referencedCanvasID = func(value any) string {
		switch typed := value.(type) {
		case string:
			return strings.TrimSpace(typed)
		case map[string]any:
			if resourceStringValue(typed, "type") == "Canvas" {
				return resourceStringValue(typed, "id")
			}
			if resourceStringValue(typed, "type") == "SpecificResource" {
				return referencedCanvasID(typed["source"])
			}
		}
		return ""
	}
	_, ok := emitted[referencedCanvasID(value)]
	return ok
}

func presentationContexts(existingValues ...any) any {
	contexts := make([]any, 0, 4)
	seenStrings := make(map[string]struct{})
	appendValue := func(value any) {
		if text, ok := value.(string); ok {
			if text == PresentationContext {
				return
			}
			if _, duplicate := seenStrings[text]; duplicate {
				return
			}
			seenStrings[text] = struct{}{}
		}
		contexts = append(contexts, cloneValue(value))
	}
	var appendExisting func(any)
	appendExisting = func(existing any) {
		switch typed := existing.(type) {
		case []any:
			for _, value := range typed {
				appendExisting(value)
			}
		case nil:
		default:
			appendValue(typed)
		}
	}
	for _, existing := range existingValues {
		appendExisting(existing)
	}
	if len(contexts) == 0 {
		return PresentationContext
	}
	contexts = append(contexts, PresentationContext)
	return contexts
}

func appendUniqueReference(existing any, id, resourceType string) []any {
	values := resourceValues(existing)
	for _, value := range values {
		if object, ok := value.(map[string]any); ok && resourceStringValue(object, "id") == id {
			return values
		}
	}
	return append(values, map[string]any{"id": id, "type": resourceType})
}

func appendResourceValue(existing, value any) []any {
	values := resourceValues(existing)
	if value == nil {
		return values
	}
	return append(values, value)
}

func resourceValues(value any) []any {
	switch typed := value.(type) {
	case []any:
		return append([]any(nil), typed...)
	case nil:
		return []any{}
	default:
		return []any{typed}
	}
}

func embeddedResource(resource map[string]any) map[string]any {
	copy := cloneObject(resource)
	delete(copy, "@context")
	return copy
}

func validLanguageMap(value any) bool {
	languageMap, ok := value.(map[string]any)
	if !ok || len(languageMap) == 0 {
		return false
	}
	for _, rawValues := range languageMap {
		values, ok := rawValues.([]any)
		if !ok || len(values) == 0 {
			return false
		}
		for _, value := range values {
			if _, ok := value.(string); !ok {
				return false
			}
		}
	}
	return true
}

func resourceStringValue(object map[string]any, key string) string {
	value, _ := object[key].(string)
	return strings.TrimSpace(value)
}

func cloneObject(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	cloned, _ := cloneValue(value).(map[string]any)
	return cloned
}

func cloneValue(value any) any {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var cloned any
	if err := DecodeJSON(encoded, &cloned); err != nil {
		return nil
	}
	return cloned
}
