package server

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"

	"github.com/lehigh-university-libraries/scribe/internal/hocr"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/safehttp"
)

const maxRemoteManifestBytes int64 = iiif.MaxSourceManifestBytes

type canvasInfo struct {
	imageURL   string
	canvasURI  string
	width      uint32
	height     uint32
	label      string
	hocrURL    string // seeAlso hOCR, if present
	hocrXML    string // bounded content prefetched before tenant writes
	plainText  string // derived once from the prefetched hOCR
	parsedHOCR *hocr.Document
}

// fetchIIIFManifest fetches and decodes a IIIF Presentation manifest (v2 or
// v3). Valid v3 bytes are returned as the bounded source projection retained
// for descriptive and extension-property round trips.
func fetchIIIFManifest(ctx context.Context, manifestURL string, maxCanvases int) (map[string]any, []byte, error) {
	resp, err := safehttp.Get(ctx, manifestURL)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch manifest: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil, fmt.Errorf("fetch manifest: status %d", resp.StatusCode)
	}
	payload, err := safehttp.ReadAllLimit(resp.Body, maxRemoteManifestBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("read manifest: %w", err)
	}
	if len(payload) > iiif.MaxSourceManifestBytes {
		return nil, nil, fmt.Errorf("source manifest exceeds %d bytes", iiif.MaxSourceManifestBytes)
	}
	var manifest map[string]any
	if err := iiif.DecodeJSON(payload, &manifest); err != nil {
		return nil, nil, fmt.Errorf("decode manifest: %w", err)
	}
	if err := enforceManifestCanvasLimit(manifest, maxCanvases); err != nil {
		return nil, nil, err
	}
	if isPresentation3Manifest(manifest) {
		if err := iiif.ValidateSourceManifest(payload); err != nil {
			return nil, nil, err
		}
		return manifest, append([]byte(nil), payload...), nil
	}
	return manifest, nil, nil
}

func enforceManifestCanvasLimit(manifest map[string]any, maxCanvases int) error {
	if maxCanvases < 1 {
		return fmt.Errorf("iiif manifest canvas limit must be positive")
	}
	count := 0
	if isPresentation3Manifest(manifest) {
		items, _ := manifest["items"].([]any)
		count = len(items)
	} else {
		sequences, _ := manifest["sequences"].([]any)
		for _, rawSequence := range sequences {
			sequence, _ := rawSequence.(map[string]any)
			canvases, _ := sequence["canvases"].([]any)
			count += len(canvases)
		}
	}
	if count > maxCanvases {
		return fmt.Errorf("iiif manifest contains %d canvases; configured maximum is %d", count, maxCanvases)
	}
	return nil
}

// extractManifestLabel returns the top-level manifest label as a plain string.
func extractManifestLabel(manifest map[string]any) string {
	return extractLabel(manifest)
}

// extractCanvasesFromManifest extracts image URLs, canvas URIs, and labels from
// a IIIF Presentation v2 or v3 manifest.
func extractCanvasesFromManifest(manifest map[string]any) ([]canvasInfo, error) {
	// Presentation 3 resources use the unprefixed type and items vocabulary.
	// Context is allowed to be either a string or an ordered array when
	// extensions such as Text Granularity are active.
	var (
		canvases []canvasInfo
		err      error
	)
	if isPresentation3Manifest(manifest) {
		canvases, err = extractCanvasesV3(manifest)
	} else {
		if !strings.EqualFold(manifestStringValue(manifest, "@type"), "sc:Manifest") &&
			!manifestContainsString(manifest["@context"], "/presentation/2/") {
			return nil, fmt.Errorf("document is not a IIIF Presentation manifest")
		}
		canvases, err = extractCanvasesV2(manifest)
	}
	if err != nil {
		return nil, err
	}
	seenCanvasIDs := make(map[string]int, len(canvases))
	for index, canvas := range canvases {
		if len(canvas.canvasURI) > iiif.MaxCanvasURIBytes {
			return nil, fmt.Errorf("canvas %d source URI exceeds %d bytes", index+1, iiif.MaxCanvasURIBytes)
		}
		if err := iiif.ValidateCanvasURI(canvas.canvasURI); err != nil {
			return nil, fmt.Errorf("canvas %d source URI is invalid: %w", index+1, err)
		}
		if firstIndex, duplicate := seenCanvasIDs[canvas.canvasURI]; duplicate {
			return nil, fmt.Errorf("canvas %d duplicates the source URI from canvas %d", index+1, firstIndex+1)
		}
		seenCanvasIDs[canvas.canvasURI] = index
		parsed, parseErr := url.Parse(strings.TrimSpace(canvas.imageURL))
		if parseErr != nil {
			return nil, fmt.Errorf("canvas %d image URL is invalid", index+1)
		}
		if validateErr := safehttp.ValidateURL(parsed); validateErr != nil {
			return nil, fmt.Errorf("canvas %d image must use a public HTTP(S) URL: %w", index+1, validateErr)
		}
	}
	return canvases, nil
}

func isPresentation3Manifest(manifest map[string]any) bool {
	return strings.EqualFold(manifestStringValue(manifest, "type"), "Manifest") ||
		manifestContainsString(manifest["@context"], "/presentation/3/") || manifest["items"] != nil
}

// extractCanvasesV3 handles IIIF Presentation 3.
func extractCanvasesV3(manifest map[string]any) ([]canvasInfo, error) {
	items, _ := manifest["items"].([]any)
	var canvases []canvasInfo
	for index, raw := range items {
		canvas, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("canvas %d is not an object", index+1)
		}
		if !strings.EqualFold(manifestStringValue(canvas, "type"), "Canvas") {
			return nil, fmt.Errorf("manifest item %d is not a Canvas", index+1)
		}
		canvasURI := manifestStringValue(canvas, "id")
		label := extractLabel(canvas)
		imageURL, imageErr := extractImageURLV3(canvas, canvasURI)
		if imageErr != nil {
			return nil, fmt.Errorf("canvas %d: %w", index+1, imageErr)
		}
		canvases = append(canvases, canvasInfo{
			imageURL:  imageURL,
			canvasURI: canvasURI,
			width:     manifestDimension(canvas, "width"),
			height:    manifestDimension(canvas, "height"),
			label:     label,
			hocrURL:   extractHOCRSeeAlso(canvas, "id"),
		})
	}
	if len(canvases) == 0 {
		return nil, fmt.Errorf("no canvases found in manifest")
	}
	return canvases, nil
}

func extractImageURLV3(canvas map[string]any, canvasURI string) (string, error) {
	// canvas.items[0].items[0].body.id  (painting annotation)
	pageItems, _ := canvas["items"].([]any)
	foundSupportedBody := false
	foundPaintingMotivation := false
	for _, rawPage := range pageItems {
		page, ok := rawPage.(map[string]any)
		if !ok {
			continue
		}
		annItems, _ := page["items"].([]any)
		for _, rawAnn := range annItems {
			ann, ok := rawAnn.(map[string]any)
			if !ok {
				continue
			}
			id := extractSupportedPaintingImageID(ann["body"])
			if id == "" {
				continue
			}
			foundSupportedBody = true
			if !manifestValueContainsExact(ann["motivation"], "painting") {
				continue
			}
			foundPaintingMotivation = true
			// Presentation 3 painting annotations identify their enclosing Canvas
			// directly. A fragment or SpecificResource target is not the same
			// resource and could silently attach the imported image to another
			// Canvas.
			if target, ok := ann["target"].(string); !ok || strings.TrimSpace(target) != strings.TrimSpace(canvasURI) {
				continue
			}
			return id, nil
		}
	}
	switch {
	case !foundSupportedBody:
		return "", fmt.Errorf("does not contain a supported public painting Image")
	case !foundPaintingMotivation:
		return "", fmt.Errorf("supported Image annotation must declare painting motivation")
	default:
		return "", fmt.Errorf("painting annotation target must exactly match the enclosing Canvas ID")
	}
}

// extractCanvasesV2 handles IIIF Presentation 2.
func extractCanvasesV2(manifest map[string]any) ([]canvasInfo, error) {
	sequences, _ := manifest["sequences"].([]any)
	var canvases []canvasInfo
	canvasIndex := 0
	for sequenceIndex, rawSeq := range sequences {
		seq, ok := rawSeq.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("sequence %d is not an object", sequenceIndex+1)
		}
		rawCanvases, _ := seq["canvases"].([]any)
		for _, rawCanvas := range rawCanvases {
			canvasIndex++
			canvas, ok := rawCanvas.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("canvas %d is not an object", canvasIndex)
			}
			canvasURI := manifestStringValue(canvas, "@id")
			label := manifestStringValue(canvas, "label")
			imageURL := extractImageURLV2(canvas)
			if imageURL == "" {
				return nil, fmt.Errorf("canvas %d does not contain a supported painting Image", canvasIndex)
			}
			canvases = append(canvases, canvasInfo{
				imageURL:  imageURL,
				canvasURI: canvasURI,
				width:     manifestDimension(canvas, "width"),
				height:    manifestDimension(canvas, "height"),
				label:     label,
				hocrURL:   extractHOCRSeeAlso(canvas, "@id"),
			})
		}
	}
	if len(canvases) == 0 {
		return nil, fmt.Errorf("no canvases found in manifest")
	}
	return canvases, nil
}

func extractImageURLV2(canvas map[string]any) string {
	images, _ := canvas["images"].([]any)
	for _, rawImg := range images {
		img, ok := rawImg.(map[string]any)
		if !ok {
			continue
		}
		resource, _ := img["resource"].(map[string]any)
		if resource == nil {
			continue
		}
		if id := manifestStringValue(resource, "@id"); id != "" {
			return id
		}
	}
	return ""
}

// extractLabel returns a plain string label from a v3 label object or v2 label string.
func extractLabel(obj map[string]any) string {
	raw, ok := obj["label"]
	if !ok {
		return ""
	}
	switch v := raw.(type) {
	case string:
		return v
	case map[string]any:
		// Prefer language-neutral and English values before other language maps.
		for _, language := range []string{"none", "en"} {
			vals, ok := v[language]
			if !ok {
				continue
			}
			if arr, ok := vals.([]any); ok && len(arr) > 0 {
				if s, ok := arr[0].(string); ok {
					return s
				}
			}
		}
		for _, vals := range v {
			if arr, ok := vals.([]any); ok && len(arr) > 0 {
				if s, ok := arr[0].(string); ok {
					return s
				}
			}
		}
	}
	return ""
}

// extractHOCRSeeAlso returns the URL of a seeAlso entry whose format is
// text/vnd.hocr+html. idKey is "@id" for v2 or "id" for v3.
// seeAlso may be a single object or an array.
func extractHOCRSeeAlso(canvas map[string]any, idKey string) string {
	raw, ok := canvas["seeAlso"]
	if !ok {
		return ""
	}
	check := func(obj map[string]any) string {
		if manifestStringValue(obj, "format") != "text/vnd.hocr+html" {
			return ""
		}
		return manifestStringValue(obj, idKey)
	}
	switch v := raw.(type) {
	case map[string]any:
		return check(v)
	case []any:
		for _, item := range v {
			if obj, ok := item.(map[string]any); ok {
				if u := check(obj); u != "" {
					return u
				}
			}
		}
	}
	return ""
}

func manifestStringValue(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

// manifestDimension normalizes the positive integral Canvas dimensions emitted
// by encoding/json and by programmatically constructed test/import payloads.
// Zero means the source Canvas did not provide a usable dimension.
func manifestDimension(m map[string]any, key string) uint32 {
	if m == nil {
		return 0
	}
	var value uint64
	switch raw := m[key].(type) {
	case float64:
		if raw <= 0 || raw != math.Trunc(raw) || raw > math.MaxUint32 {
			return 0
		}
		value = uint64(raw)
	case float32:
		asFloat := float64(raw)
		if asFloat <= 0 || asFloat != math.Trunc(asFloat) || asFloat > math.MaxUint32 {
			return 0
		}
		value = uint64(asFloat)
	case int:
		if raw <= 0 {
			return 0
		}
		value = uint64(raw)
	case int32:
		if raw <= 0 {
			return 0
		}
		value = uint64(raw)
	case int64:
		if raw <= 0 {
			return 0
		}
		value = uint64(raw)
	case uint:
		value = uint64(raw)
	case uint32:
		value = uint64(raw)
	case uint64:
		value = raw
	case json.Number:
		parsed, err := strconv.ParseUint(string(raw), 10, 32)
		if err != nil {
			return 0
		}
		value = parsed
	default:
		return 0
	}
	if value == 0 || value > math.MaxUint32 {
		return 0
	}
	return uint32(value)
}

func manifestContainsString(value any, substring string) bool {
	substring = strings.ToLower(strings.TrimSpace(substring))
	switch typed := value.(type) {
	case string:
		return strings.Contains(strings.ToLower(typed), substring)
	case []any:
		for _, item := range typed {
			if manifestContainsString(item, substring) {
				return true
			}
		}
	}
	return false
}

func manifestValueContainsExact(value any, expected string) bool {
	expected = strings.TrimSpace(expected)
	switch typed := value.(type) {
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), expected)
	case []any:
		for _, item := range typed {
			if manifestValueContainsExact(item, expected) {
				return true
			}
		}
	}
	return false
}

func extractSupportedPaintingImageID(value any) string {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			if id := extractSupportedPaintingImageID(item); id != "" {
				return id
			}
		}
	case map[string]any:
		typeName := strings.ToLower(manifestStringValue(typed, "type"))
		switch typeName {
		case "choice":
			return extractSupportedPaintingImageID(typed["items"])
		case "specificresource":
			return extractSupportedPaintingImageID(typed["source"])
		case "image":
			if !supportedPaintingImageFormat(manifestStringValue(typed, "format")) {
				return ""
			}
			id := manifestStringValue(typed, "id")
			if id == "" {
				id = manifestStringValue(typed, "@id")
			}
			parsed, err := url.Parse(id)
			if err != nil || safehttp.ValidateURL(parsed) != nil {
				return ""
			}
			return id
		}
		// Some producers omit the Choice type but still provide its items.
		return extractSupportedPaintingImageID(typed["items"])
	}
	return ""
}

func supportedPaintingImageFormat(format string) bool {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "image/jpeg", "image/png", "image/gif", "image/webp", "image/tiff", "image/tif", "image/jp2", "image/jpx", "image/jpm", "image/jpeg2000":
		return true
	default:
		return false
	}
}
