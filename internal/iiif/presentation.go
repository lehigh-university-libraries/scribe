// Package iiif owns Presentation 3 identifiers, schema validation, and
// Scribe's Text Granularity extension invariants.
package iiif

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	textgranularityschema "github.com/libops/iiif-spec/extension/textgranularity/schema"
	presentationschema "github.com/libops/iiif-spec/presentation/v3/schema"
)

const (
	// PresentationContext is the IIIF Presentation 3 JSON-LD context.
	PresentationContext = presentationschema.Context
	// TextGranularityContext is the IIIF Text Granularity extension context.
	TextGranularityContext = textgranularityschema.Context
	// MaxAnnotationPageBytes is the canonical encoded-page admission ceiling.
	MaxAnnotationPageBytes = 8 << 20
	// MaxAnnotationsPerPage bounds schema work and derived index fan-out.
	MaxAnnotationsPerPage = 10_000
	// MaxPixelCoordinate bounds canonical geometry and every computed extent to
	// signed 32-bit space, matching browser/cropper interoperability limits.
	MaxPixelCoordinate      = 1<<31 - 1
	maxAnnotationIDBytes    = 512
	maxCanonicalPageIDBytes = 512
	maxResourceIDBytes      = 2 << 10
	maxAnnotationBodyBytes  = 1 << 20
	maxSelectorBytes        = 64 << 10
	maxSelectorValueBytes   = 4 << 10
)

// PageIdentity defines Scribe's canonical identity for an AnnotationPage.
type PageIdentity struct {
	PublicBaseURL string
	ItemImageID   uint64
	CanvasURI     string
}

// AnnotationPageID returns the dereferenceable public ID for an image's page.
func AnnotationPageID(publicBaseURL string, itemImageID uint64) (string, error) {
	base, err := itemImageResourceBase(publicBaseURL, itemImageID)
	if err != nil {
		return "", err
	}
	return base + "/canvas/page-1/annotations", nil
}

// CanonicalPageID is the application-facing name for AnnotationPageID.
func CanonicalPageID(publicBaseURL string, itemImageID uint64) (string, error) {
	return AnnotationPageID(publicBaseURL, itemImageID)
}

// ItemImageCanvasID returns the Scribe-owned Canvas ID for an upload or other
// image that did not arrive with an imported Canvas identity.
func ItemImageCanvasID(publicBaseURL string, itemImageID uint64) (string, error) {
	base, err := itemImageResourceBase(publicBaseURL, itemImageID)
	if err != nil {
		return "", err
	}
	return base + "/canvas/page-1", nil
}

func itemImageResourceBase(publicBaseURL string, itemImageID uint64) (string, error) {
	base, err := validHTTPBase(publicBaseURL)
	if err != nil {
		return "", err
	}
	if itemImageID == 0 {
		return "", fmt.Errorf("item image id is required")
	}
	return base + "/item-image-" + strconv.FormatUint(itemImageID, 10), nil
}

// ValidateCanvasURI verifies a canonical or imported Canvas resource identity.
func ValidateCanvasURI(canvasURI string) error {
	return requireHTTPURL(strings.TrimSpace(canvasURI), "canvas uri")
}

// PageIdentityFromPageID parses a canonical Scribe AnnotationPage ID without
// assuming a deployment origin or base path.
func PageIdentityFromPageID(pageID, canvasURI string) (PageIdentity, error) {
	base, itemImageID, err := parseAnnotationPageID(pageID)
	if err != nil {
		return PageIdentity{}, err
	}
	identity := PageIdentity{
		PublicBaseURL: base,
		ItemImageID:   itemImageID,
		CanvasURI:     strings.TrimSpace(canvasURI),
	}
	if err := requireHTTPURL(identity.CanvasURI, "canvas uri"); err != nil {
		return PageIdentity{}, err
	}
	return identity, nil
}

// ItemImageIDFromAnnotationPageID returns the owning image identifier from a
// canonical Scribe page ID in the configured Triplet Presentation namespace.
func ItemImageIDFromAnnotationPageID(pageID string) (uint64, error) {
	_, itemImageID, err := parseAnnotationPageID(pageID)
	return itemImageID, err
}

func parseAnnotationPageID(pageID string) (string, uint64, error) {
	pageID = strings.TrimSpace(pageID)
	parsed, err := url.Parse(pageID)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", 0, fmt.Errorf("annotation page id must be an absolute HTTP(S) URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", 0, fmt.Errorf("annotation page id must be a canonical Scribe resource")
	}
	const marker = "/item-image-"
	markerIndex := strings.LastIndex(parsed.Path, marker)
	if markerIndex < 0 {
		return "", 0, fmt.Errorf("annotation page id must be a canonical Scribe resource")
	}
	parts := strings.Split(parsed.Path[markerIndex+len(marker):], "/")
	if len(parts) != 4 || parts[0] == "" || parts[1] != "canvas" || parts[2] != "page-1" || parts[3] != "annotations" {
		return "", 0, fmt.Errorf("annotation page id must be a canonical Scribe resource")
	}
	itemImageID, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil || itemImageID == 0 {
		return "", 0, fmt.Errorf("annotation page id has an invalid item image id")
	}
	base := *parsed
	base.Path = parsed.Path[:markerIndex]
	base.RawPath = ""
	base.RawQuery = ""
	base.Fragment = ""
	publicBaseURL := strings.TrimRight(base.String(), "/")
	expected, err := AnnotationPageID(publicBaseURL, itemImageID)
	if err != nil || expected != pageID {
		return "", 0, fmt.Errorf("annotation page id must be a canonical Scribe resource")
	}
	return publicBaseURL, itemImageID, nil
}

// AnnotationID returns a stable dereferenceable child resource ID.
func AnnotationID(pageID, seed string) (string, error) {
	if err := requireResourceURL(pageID, "annotation page id", false); err != nil {
		return "", err
	}
	seed = strings.TrimSpace(seed)
	if seed == "" {
		return "", fmt.Errorf("annotation id seed is required")
	}
	digest := sha256.Sum256([]byte(seed))
	return strings.TrimRight(pageID, "/") + "/items/" + hex.EncodeToString(digest[:16]), nil
}

// PageIdentityFromAnnotationID recovers the owning Scribe page from one of its
// canonical child annotation IDs. Structural editor operations use this to
// keep their result IDs dereferenceable without accepting a second, possibly
// contradictory resource identifier from the client.
func PageIdentityFromAnnotationID(annotationID, canvasURI string) (PageIdentity, error) {
	annotationID = strings.TrimSpace(annotationID)
	parsed, err := url.Parse(annotationID)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return PageIdentity{}, fmt.Errorf("annotation id must be an absolute HTTP(S) URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return PageIdentity{}, fmt.Errorf("annotation id must be a canonical Scribe child resource")
	}

	const marker = "/item-image-"
	markerIndex := strings.LastIndex(parsed.Path, marker)
	if markerIndex < 0 {
		return PageIdentity{}, fmt.Errorf("annotation id must be a canonical Scribe child resource")
	}
	parts := strings.Split(parsed.Path[markerIndex+len(marker):], "/")
	if len(parts) != 6 || parts[0] == "" || parts[1] != "canvas" || parts[2] != "page-1" || parts[3] != "annotations" || parts[4] != "items" || parts[5] == "" {
		return PageIdentity{}, fmt.Errorf("annotation id must be a canonical Scribe child resource")
	}
	itemImageID, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil || itemImageID == 0 {
		return PageIdentity{}, fmt.Errorf("annotation id has an invalid item image id")
	}

	base := *parsed
	base.Path = parsed.Path[:markerIndex]
	base.RawPath = ""
	base.RawQuery = ""
	base.Fragment = ""
	identity := PageIdentity{
		PublicBaseURL: strings.TrimRight(base.String(), "/"),
		ItemImageID:   itemImageID,
		CanvasURI:     strings.TrimSpace(canvasURI),
	}
	pageID, err := AnnotationPageID(identity.PublicBaseURL, identity.ItemImageID)
	if err != nil {
		return PageIdentity{}, err
	}
	if !isCanonicalAnnotationID(annotationID, pageID) {
		return PageIdentity{}, fmt.Errorf("annotation id must be a canonical Scribe child resource")
	}
	if err := requireHTTPURL(identity.CanvasURI, "canvas uri"); err != nil {
		return PageIdentity{}, err
	}
	return identity, nil
}

// NormalizeAnnotationPage applies Scribe-owned resource identity while
// preserving extension and presentation properties, then validates the result.
func NormalizeAnnotationPage(raw []byte, identity PageIdentity) ([]byte, error) {
	if err := validateAnnotationPageCost(raw, false); err != nil {
		return nil, err
	}
	pageID, err := AnnotationPageID(identity.PublicBaseURL, identity.ItemImageID)
	if err != nil {
		return nil, err
	}
	if err := requireHTTPURL(identity.CanvasURI, "canvas uri"); err != nil {
		return nil, err
	}
	var page map[string]any
	if err := DecodeJSON(raw, &page); err != nil {
		return nil, fmt.Errorf("decode annotation page: %w", err)
	}
	page["@context"] = annotationPageContexts(page["@context"])
	page["id"] = pageID
	page["type"] = "AnnotationPage"
	delete(page, "@id")
	delete(page, "@type")

	itemsValue, present := page["items"]
	if !present || itemsValue == nil {
		page["items"] = []any{}
	} else {
		items, ok := itemsValue.([]any)
		if !ok {
			return nil, fmt.Errorf("annotation page items must be an array")
		}
		seenIDs := make(map[string]struct{}, len(items))
		for position, value := range items {
			annotation, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("annotation page item %d must be an object", position)
			}
			annotation["type"] = "Annotation"
			delete(annotation, "@id")
			delete(annotation, "@type")
			id, _ := annotation["id"].(string)
			_, duplicate := seenIDs[id]
			if !isCanonicalAnnotationID(id, pageID) || duplicate {
				seed, marshalErr := json.Marshal(annotation)
				if marshalErr != nil {
					return nil, fmt.Errorf("encode annotation item %d: %w", position, marshalErr)
				}
				id, err = AnnotationID(pageID, strconv.Itoa(position)+"\x00"+string(seed))
				if err != nil {
					return nil, err
				}
				annotation["id"] = id
			}
			seenIDs[id] = struct{}{}
			targetCanvas := annotationTargetCanvas(annotation["target"])
			if targetCanvas != strings.TrimSpace(identity.CanvasURI) {
				return nil, fmt.Errorf("annotation item %d targets canvas %q, want %q", position, targetCanvas, identity.CanvasURI)
			}
		}
	}

	normalized, err := json.Marshal(page)
	if err != nil {
		return nil, fmt.Errorf("encode annotation page: %w", err)
	}
	if err := ValidateCanonicalAnnotationPage(normalized, identity); err != nil {
		return nil, err
	}
	return normalized, nil
}

// NewAnnotationPage builds and validates a canonical page from raw items.
func NewAnnotationPage(identity PageIdentity, items []any) ([]byte, error) {
	raw, err := json.Marshal(map[string]any{
		"@context": []string{TextGranularityContext, PresentationContext},
		"type":     "AnnotationPage",
		"items":    items,
	})
	if err != nil {
		return nil, fmt.Errorf("encode annotation page: %w", err)
	}
	return NormalizeAnnotationPage(raw, identity)
}

// ValidateAnnotationPage validates the IIIF Presentation 3 schema and the Text
// Granularity semantics used by Scribe.
func ValidateAnnotationPage(raw []byte) error {
	if err := validateAnnotationPageCost(raw, true); err != nil {
		return err
	}
	var page map[string]any
	if err := DecodeJSON(raw, &page); err != nil {
		return fmt.Errorf("decode annotation page semantics: %w", err)
	}
	if err := textgranularityschema.ValidateAnnotationPageBytes(raw); err != nil {
		return fmt.Errorf("invalid IIIF Presentation 3 AnnotationPage: %w", err)
	}
	if err := validatePresentationContext(page["@context"]); err != nil {
		return fmt.Errorf("invalid annotation page context: %w", err)
	}
	if err := validateAnnotationPageExtensionTree(page, nil); err != nil {
		return err
	}
	items, _ := page["items"].([]any)
	for position, value := range items {
		annotation, _ := value.(map[string]any)
		granularity, _ := annotation["textGranularity"].(string)
		granularity = strings.TrimSpace(granularity)
		if granularity == "" {
			continue
		}
		if !textgranularityschema.IsKnownLevel(granularity) {
			return fmt.Errorf("annotation item %d has unsupported textGranularity %q", position, granularity)
		}
		if !containsString(annotation["motivation"], "supplementing") {
			return fmt.Errorf("annotation item %d with textGranularity must have supplementing motivation", position)
		}
		if !containsString(page["@context"], TextGranularityContext) {
			return fmt.Errorf("annotation item %d uses textGranularity without the Text Granularity context", position)
		}
		body, exists := annotation["body"]
		if !exists || body == nil || !hasTextualBody(body) {
			return fmt.Errorf("annotation item %d with textGranularity must use a TextualBody", position)
		}
		canvas := annotationTargetCanvas(annotation["target"])
		if err := requireResourceURL(canvas, fmt.Sprintf("annotation item %d target canvas", position), true); err != nil {
			return err
		}
		fragment, hasFragment, fragmentErr := annotationTargetFragment(annotation["target"])
		if fragmentErr != nil {
			return fmt.Errorf("annotation item %d has invalid FragmentSelector: %w", position, fragmentErr)
		}
		if granularity != "page" && !hasFragment {
			return fmt.Errorf("annotation item %d with textGranularity %q requires an xywh FragmentSelector", position, granularity)
		}
		if hasFragment {
			if err := validatePixelXYWH(fragment); err != nil {
				return fmt.Errorf("annotation item %d has invalid FragmentSelector: %w", position, err)
			}
		}
	}
	return nil
}

var standardAnnotationPageProperties = map[string]struct{}{
	"@context": {}, "id": {}, "type": {}, "rendering": {}, "label": {},
	"service": {}, "thumbnail": {}, "items": {}, "partOf": {}, "next": {},
	"prev": {}, "first": {}, "last": {},
}

func validateAnnotationPageExtensionContext(page map[string]any, context any) error {
	hasExtensionTerm := false
	for key := range page {
		if _, standard := standardAnnotationPageProperties[key]; standard {
			continue
		}
		if strings.HasPrefix(key, "@") {
			return fmt.Errorf("annotation page contains unsupported JSON-LD keyword %q", key)
		}
		hasExtensionTerm = true
	}
	if !hasExtensionTerm {
		return nil
	}
	contexts, ok := context.([]any)
	if !ok {
		return fmt.Errorf("annotation page extension properties require an extension context")
	}
	for _, value := range contexts {
		switch contextValue := value.(type) {
		case string:
			if contextValue != PresentationContext && contextValue != TextGranularityContext {
				return nil
			}
		case map[string]any:
			return nil
		}
	}
	return fmt.Errorf("annotation page extension properties require an extension context")
}

func validateAnnotationPageExtensionTree(value, inheritedContext any) error {
	switch typed := value.(type) {
	case []any:
		for _, child := range typed {
			if err := validateAnnotationPageExtensionTree(child, inheritedContext); err != nil {
				return err
			}
		}
	case map[string]any:
		context := inheritedContext
		if ownContext, exists := typed["@context"]; exists {
			context = ownContext
		}
		if resourceType, _ := typed["type"].(string); resourceType == "AnnotationPage" {
			if err := validateAnnotationPageExtensionContext(typed, context); err != nil {
				return err
			}
		}
		for _, child := range typed {
			if err := validateAnnotationPageExtensionTree(child, context); err != nil {
				return err
			}
		}
	}
	return nil
}

// ValidateCanonicalAnnotationPage validates both the IIIF resource and the
// Scribe-owned identity boundary. Every child must belong to the exact page,
// use the fixed 32-lowerhex suffix, be unique, and target the owning Canvas.
func ValidateCanonicalAnnotationPage(raw []byte, identity PageIdentity) error {
	if err := ValidateAnnotationPage(raw); err != nil {
		return err
	}
	pageID, err := AnnotationPageID(identity.PublicBaseURL, identity.ItemImageID)
	if err != nil {
		return err
	}
	canvasURI := strings.TrimSpace(identity.CanvasURI)
	if err := requireHTTPURL(canvasURI, "canvas uri"); err != nil {
		return err
	}
	var page struct {
		ID    string           `json:"id"`
		Items []map[string]any `json:"items"`
	}
	if err := DecodeJSON(raw, &page); err != nil {
		return fmt.Errorf("decode canonical annotation page: %w", err)
	}
	if page.ID != pageID {
		return fmt.Errorf("annotation page id %q does not match canonical resource %q", page.ID, pageID)
	}
	seen := make(map[string]struct{}, len(page.Items))
	for position, annotation := range page.Items {
		id, _ := annotation["id"].(string)
		if !isCanonicalAnnotationID(id, pageID) {
			return fmt.Errorf("annotation item %d id %q is not a canonical child of %q", position, id, pageID)
		}
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("annotation item %d duplicates id %q", position, id)
		}
		seen[id] = struct{}{}
		if targetCanvas := annotationTargetCanvas(annotation["target"]); targetCanvas != canvasURI {
			return fmt.Errorf("annotation item %d targets canvas %q, want %q", position, targetCanvas, canvasURI)
		}
	}
	return nil
}

func validateAnnotationPageCost(raw []byte, enforceCanonicalIDs bool) error {
	if len(raw) > MaxAnnotationPageBytes {
		return fmt.Errorf("annotation page exceeds %d bytes", MaxAnnotationPageBytes)
	}
	var page struct {
		ID    string            `json:"id"`
		Items []json.RawMessage `json:"items"`
	}
	if err := DecodeJSON(raw, &page); err != nil {
		return fmt.Errorf("decode annotation page admission fields: %w", err)
	}
	if len(page.Items) > MaxAnnotationsPerPage {
		return fmt.Errorf("annotation page contains %d items; maximum is %d", len(page.Items), MaxAnnotationsPerPage)
	}
	if enforceCanonicalIDs && len(page.ID) > maxCanonicalPageIDBytes {
		return fmt.Errorf("annotation page id exceeds %d bytes", maxCanonicalPageIDBytes)
	}
	for position, encoded := range page.Items {
		var annotation map[string]any
		if err := DecodeJSON(encoded, &annotation); err != nil {
			return fmt.Errorf("decode annotation item %d admission fields: %w", position, err)
		}
		if enforceCanonicalIDs {
			id, _ := annotation["id"].(string)
			if len(id) > maxAnnotationIDBytes {
				return fmt.Errorf("annotation item %d id exceeds %d bytes", position, maxAnnotationIDBytes)
			}
		}
		if body, ok := annotation["body"]; ok {
			if err := validateEncodedValueLimit(body, maxAnnotationBodyBytes, fmt.Sprintf("annotation item %d body", position)); err != nil {
				return err
			}
			if err := validateResourceIDLengths(body, fmt.Sprintf("annotation item %d body", position)); err != nil {
				return err
			}
		}
		if target, ok := annotation["target"]; ok {
			if err := validateResourceIDLengths(target, fmt.Sprintf("annotation item %d target", position)); err != nil {
				return err
			}
			if targetObject, ok := target.(map[string]any); ok {
				if selector, exists := targetObject["selector"]; exists {
					if err := validateEncodedValueLimit(selector, maxSelectorBytes, fmt.Sprintf("annotation item %d selector", position)); err != nil {
						return err
					}
					if err := validateSelectorValueLengths(selector, position); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func validateEncodedValueLimit(value any, limit int, label string) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode %s: %w", label, err)
	}
	if len(encoded) > limit {
		return fmt.Errorf("%s exceeds %d bytes", label, limit)
	}
	return nil
}

func validateResourceIDLengths(value any, path string) error {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			childPath := path + "." + key
			if key == "id" || key == "@id" {
				if id, ok := child.(string); ok && len(id) > maxResourceIDBytes {
					return fmt.Errorf("%s exceeds %d bytes", childPath, maxResourceIDBytes)
				}
			}
			if err := validateResourceIDLengths(child, childPath); err != nil {
				return err
			}
		}
	case []any:
		for index, child := range value {
			if err := validateResourceIDLengths(child, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateSelectorValueLengths(value any, position int) error {
	switch value := value.(type) {
	case map[string]any:
		if selectorValue, ok := value["value"].(string); ok && len(selectorValue) > maxSelectorValueBytes {
			return fmt.Errorf("annotation item %d selector value exceeds %d bytes", position, maxSelectorValueBytes)
		}
		for _, child := range value {
			if err := validateSelectorValueLengths(child, position); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range value {
			if err := validateSelectorValueLengths(child, position); err != nil {
				return err
			}
		}
	}
	return nil
}

// ValidateManifest validates a IIIF Presentation 3 Manifest.
func ValidateManifest(raw []byte) error {
	var manifest map[string]any
	if err := DecodeJSON(raw, &manifest); err != nil {
		return fmt.Errorf("decode manifest context: %w", err)
	}
	if err := presentationschema.ValidateExtensibleManifestBytes(raw); err != nil {
		return fmt.Errorf("invalid IIIF Presentation 3 Manifest: %w", err)
	}
	return nil
}

func annotationPageContexts(existing any) []any {
	// Presentation 3 requires its context to be the final array entry. Keep the
	// Text Granularity context first, preserve other extension contexts between
	// them, and discard duplicate copies of the two contexts Scribe owns.
	contexts := []any{TextGranularityContext}
	seen := map[string]struct{}{TextGranularityContext: {}}
	appendContext := func(value any) {
		text, ok := value.(string)
		if ok {
			if text == PresentationContext {
				return
			}
			if _, duplicate := seen[text]; duplicate {
				return
			}
			seen[text] = struct{}{}
		}
		contexts = append(contexts, value)
	}
	switch value := existing.(type) {
	case []any:
		for _, contextValue := range value {
			appendContext(contextValue)
		}
	case nil:
	default:
		appendContext(value)
	}
	contexts = append(contexts, PresentationContext)
	return contexts
}

func validatePresentationContext(raw any) error {
	switch contextValue := raw.(type) {
	case string:
		if contextValue != PresentationContext {
			return fmt.Errorf("iiif Presentation 3 context is required")
		}
		return nil
	case []any:
		if len(contextValue) == 0 {
			return fmt.Errorf("iiif Presentation 3 context is required")
		}
		count := 0
		for _, entry := range contextValue {
			if text, ok := entry.(string); ok && text == PresentationContext {
				count++
			}
		}
		last, ok := contextValue[len(contextValue)-1].(string)
		if !ok || last != PresentationContext || count != 1 {
			return fmt.Errorf("iiif Presentation 3 context must occur once as the final @context entry")
		}
		return nil
	default:
		return fmt.Errorf("iiif Presentation 3 context is required")
	}
}

func containsString(value any, want string) bool {
	switch value := value.(type) {
	case string:
		return value == want
	case []any:
		for _, entry := range value {
			if text, ok := entry.(string); ok && text == want {
				return true
			}
		}
	}
	return false
}

func annotationTargetCanvas(target any) string {
	return TargetCanvas(target)
}

func annotationTargetFragment(target any) (string, bool, error) {
	return TargetPixelXYWH(target)
}

func validatePixelXYWH(raw string) error {
	_, err := parsePixelXYWH(raw)
	return err
}

func parsePixelXYWH(raw string) ([4]uint64, error) {
	var values [4]uint64
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "pixel:") {
		raw = strings.TrimSpace(strings.TrimPrefix(raw, "pixel:"))
	}
	if strings.HasPrefix(raw, "percent:") {
		return values, fmt.Errorf("percent coordinates are not supported for canonical OCR geometry")
	}
	parts := strings.Split(raw, ",")
	if len(parts) != 4 {
		return values, fmt.Errorf("xywh must contain four coordinates")
	}
	for index, part := range parts {
		component := strings.TrimSpace(part)
		value, err := strconv.ParseUint(component, 10, 64)
		if err != nil {
			return values, fmt.Errorf("xywh coordinate %d must be a non-negative integer", index+1)
		}
		values[index] = value
	}
	if values[2] == 0 || values[3] == 0 {
		return values, fmt.Errorf("xywh width and height must be positive")
	}
	for index, value := range values {
		if value > MaxPixelCoordinate {
			return values, fmt.Errorf("xywh coordinate %d exceeds %d", index+1, MaxPixelCoordinate)
		}
	}
	if values[2] > MaxPixelCoordinate-values[0] || values[3] > MaxPixelCoordinate-values[1] {
		return values, fmt.Errorf("xywh extent exceeds %d", MaxPixelCoordinate)
	}
	return values, nil
}

// ValidateAnnotationPageGeometry binds canonical pixel selectors to the
// authoritative image dimensions. It is shared by editor, import, reprocess,
// and worker commits so no persistence path can create an out-of-bounds page.
func ValidateAnnotationPageGeometry(raw []byte, width, height uint32) error {
	var document map[string]any
	if err := DecodeJSON(raw, &document); err != nil {
		return fmt.Errorf("decode annotation page geometry: %w", err)
	}
	items, ok := document["items"].([]any)
	if !ok {
		return fmt.Errorf("annotation page items must be an array")
	}
	for index, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			return fmt.Errorf("annotation item %d must be an object", index)
		}
		fragment, present, err := TargetPixelXYWH(item["target"])
		if err != nil {
			return fmt.Errorf("annotation item %d geometry: %w", index, err)
		}
		if !present {
			continue
		}
		values, err := parsePixelXYWH(fragment)
		if err != nil {
			return fmt.Errorf("annotation item %d geometry: %w", index, err)
		}
		if width == 0 || height == 0 {
			return fmt.Errorf("annotation item %d geometry requires known image dimensions", index)
		}
		if values[0] >= uint64(width) || values[1] >= uint64(height) ||
			values[2] > uint64(width)-values[0] || values[3] > uint64(height)-values[1] {
			return fmt.Errorf("annotation item %d geometry exceeds image dimensions %dx%d", index, width, height)
		}
	}
	return nil
}

func hasTextualBody(raw any) bool {
	switch body := raw.(type) {
	case map[string]any:
		bodyType, _ := body["type"].(string)
		return strings.EqualFold(strings.TrimSpace(bodyType), "TextualBody")
	case []any:
		for _, entry := range body {
			if hasTextualBody(entry) {
				return true
			}
		}
	}
	return false
}

func isCanonicalAnnotationID(raw, pageID string) bool {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	prefix := strings.TrimRight(pageID, "/") + "/items/"
	if !strings.HasPrefix(raw, prefix) {
		return false
	}
	suffix := strings.TrimPrefix(raw, prefix)
	if len(suffix) != 32 || suffix != strings.ToLower(suffix) || strings.Contains(suffix, "/") {
		return false
	}
	_, err = hex.DecodeString(suffix)
	return err == nil
}

func validHTTPBase(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if err := requireResourceURL(raw, "public base url", false); err != nil {
		return "", err
	}
	const longestChildSuffix = "/item-image-18446744073709551615/canvas/page-1/annotations/items/0123456789abcdef0123456789abcdef"
	if len(raw)+len(longestChildSuffix) > maxAnnotationIDBytes {
		return "", fmt.Errorf("public base url is too long for the %d-byte canonical resource id contract", maxAnnotationIDBytes)
	}
	return raw, nil
}

// ValidatePublicBaseURL verifies that every worst-case canonical child ID can
// fit the persisted 512-byte page/annotation identity contract.
func ValidatePublicBaseURL(raw string) error {
	_, err := validHTTPBase(raw)
	return err
}

func requireHTTPURL(raw, label string) error {
	return requireResourceURL(raw, label, true)
}

func requireResourceURL(raw, label string, allowQuery bool) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" || (!allowQuery && parsed.RawQuery != "") {
		return fmt.Errorf("%s must be an absolute HTTP(S) URL", label)
	}
	return nil
}
