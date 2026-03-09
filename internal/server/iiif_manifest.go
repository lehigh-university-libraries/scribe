package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type canvasInfo struct {
	imageURL  string
	canvasURI string
	label     string
	hocrURL   string // seeAlso hOCR, if present
}

// fetchIIIFManifest fetches and decodes a IIIF Presentation manifest (v2 or v3).
func fetchIIIFManifest(ctx context.Context, manifestURL string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/ld+json;profile=\"http://iiif.io/api/presentation/3/context.json\", application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch manifest: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch manifest: status %d", resp.StatusCode)
	}
	var manifest map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
	return manifest, nil
}

// extractManifestLabel returns the top-level manifest label as a plain string.
func extractManifestLabel(manifest map[string]any) string {
	return extractLabel(manifest)
}

// extractCanvasesFromManifest extracts image URLs, canvas URIs, and labels from
// a IIIF Presentation v2 or v3 manifest.
func extractCanvasesFromManifest(manifest map[string]any) ([]canvasInfo, error) {
	// Detect version by @context or type.
	ctx := manifestStringValue(manifest, "@context")
	if strings.Contains(ctx, "/3/") {
		return extractCanvasesV3(manifest)
	}
	return extractCanvasesV2(manifest)
}

// extractCanvasesV3 handles IIIF Presentation 3.
func extractCanvasesV3(manifest map[string]any) ([]canvasInfo, error) {
	items, _ := manifest["items"].([]any)
	var canvases []canvasInfo
	for _, raw := range items {
		canvas, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		canvasURI := manifestStringValue(canvas, "id")
		label := extractLabel(canvas)
		imageURL := extractImageURLV3(canvas)
		if imageURL == "" {
			continue
		}
		canvases = append(canvases, canvasInfo{
			imageURL:  imageURL,
			canvasURI: canvasURI,
			label:     label,
			hocrURL:   extractHOCRSeeAlso(canvas, "id"),
		})
	}
	if len(canvases) == 0 {
		return nil, fmt.Errorf("no canvases found in manifest")
	}
	return canvases, nil
}

func extractImageURLV3(canvas map[string]any) string {
	// canvas.items[0].items[0].body.id  (painting annotation)
	pageItems, _ := canvas["items"].([]any)
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
			motivation := manifestStringValue(ann, "motivation")
			if motivation != "" && motivation != "painting" {
				continue
			}
			switch body := ann["body"].(type) {
			case map[string]any:
				if id := manifestStringValue(body, "id"); id != "" {
					return id
				}
			case []any:
				for _, rb := range body {
					b, ok := rb.(map[string]any)
					if !ok {
						continue
					}
					if id := manifestStringValue(b, "id"); id != "" {
						return id
					}
				}
			}
		}
	}
	return ""
}

// extractCanvasesV2 handles IIIF Presentation 2.
func extractCanvasesV2(manifest map[string]any) ([]canvasInfo, error) {
	sequences, _ := manifest["sequences"].([]any)
	var canvases []canvasInfo
	for _, rawSeq := range sequences {
		seq, ok := rawSeq.(map[string]any)
		if !ok {
			continue
		}
		rawCanvases, _ := seq["canvases"].([]any)
		for _, rawCanvas := range rawCanvases {
			canvas, ok := rawCanvas.(map[string]any)
			if !ok {
				continue
			}
			canvasURI := manifestStringValue(canvas, "@id")
			label := manifestStringValue(canvas, "label")
			imageURL := extractImageURLV2(canvas)
			if imageURL == "" {
				continue
			}
			canvases = append(canvases, canvasInfo{
				imageURL:  imageURL,
				canvasURI: canvasURI,
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
		// IIIF v3: {"none": ["value"]} or {"en": ["value"]}
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
