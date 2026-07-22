package server

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lehigh-university-libraries/scribe/internal/iiif"
)

func parseLineAnnotation(raw string) (map[string]any, string, int, int, int, int, string, error) {
	var anno map[string]any
	if err := iiif.DecodeJSON([]byte(raw), &anno); err != nil {
		return nil, "", 0, 0, 0, 0, "", fmt.Errorf("invalid annotation json")
	}
	anno = normalizeAnnotation(anno, "")
	canvasURI := extractCanvasURI(anno)
	if canvasURI == "" {
		return nil, "", 0, 0, 0, 0, "", fmt.Errorf("annotation missing canvas uri")
	}
	fragment := extractFragment(anno)
	if fragment == "" {
		return nil, "", 0, 0, 0, 0, "", fmt.Errorf("annotation missing bbox fragment")
	}
	x1, y1, x2, y2, err := parseXYWH(fragment)
	if err != nil {
		return nil, "", 0, 0, 0, 0, "", err
	}
	text := extractAnnotationText(anno)
	return anno, text, x1, y1, x2, y2, canvasURI, nil
}

func extractAnnotationText(anno map[string]any) string {
	switch body := anno["body"].(type) {
	case map[string]any:
		return strings.TrimSpace(annStringValue(body, "value"))
	case []any:
		for _, entry := range body {
			obj, _ := entry.(map[string]any)
			if value := strings.TrimSpace(annStringValue(obj, "value")); value != "" {
				return value
			}
		}
	}
	return ""
}

func buildLineAnnotation(id, canvasURI string, x1, y1, x2, y2 int, text string) map[string]any {
	return map[string]any{
		"id":              id,
		"type":            "Annotation",
		"textGranularity": "line",
		"motivation":      "supplementing",
		"body": []any{
			map[string]any{
				"type":    "TextualBody",
				"purpose": "supplementing",
				"format":  "text/plain",
				"value":   strings.TrimSpace(text),
			},
		},
		"target": map[string]any{
			"source": map[string]any{"id": canvasURI, "type": "Canvas"},
			"selector": map[string]any{
				"type":       "FragmentSelector",
				"conformsTo": "http://www.w3.org/TR/media-frags/",
				"value":      fmt.Sprintf("xywh=%d,%d,%d,%d", x1, y1, maxInt(1, x2-x1), maxInt(1, y2-y1)),
			},
		},
	}
}

// deriveTextAnnotation changes only Scribe-owned structural fields. All other
// Presentation 3 and extension properties remain attached to the derived
// annotation, body, target, source, and selector.
func deriveTextAnnotation(template map[string]any, id, granularity, canvasURI string, x1, y1, x2, y2 int, text string) (map[string]any, error) {
	annotation, err := cloneAnnotation(template)
	if err != nil {
		return nil, err
	}
	annotation["id"] = id
	annotation["type"] = "Annotation"
	annotation["textGranularity"] = granularity
	if annotation["motivation"] == nil {
		annotation["motivation"] = "supplementing"
	}
	annotation["body"] = textBodyWithValue(annotation["body"], strings.TrimSpace(text))
	target, err := targetWithFragment(annotation["target"], canvasURI, x1, y1, x2, y2)
	if err != nil {
		return nil, err
	}
	annotation["target"] = target
	// A derived annotation changes the embedded textual and selector resources.
	// Reusing their old RDF identifiers would assert that one resource has
	// conflicting values across the split/join outputs. Keep their extension
	// properties, but make the changed embedded resources anonymous; the Canvas
	// source identity remains untouched.
	clearMutableEmbeddedResourceIDs(annotation)
	return annotation, nil
}

func clearMutableEmbeddedResourceIDs(annotation map[string]any) {
	if annotation == nil {
		return
	}
	clearFirstTextualBodyIdentity(annotation["body"])
	clearMutableTargetResourceIDs(annotation)
}

func clearMutableTargetResourceIDs(annotation map[string]any) {
	if annotation == nil {
		return
	}
	target, _ := annotation["target"].(map[string]any)
	if target == nil {
		return
	}
	delete(target, "id")
	delete(target, "@id")
	clearFirstFragmentIdentity(target["selector"])
}

func clearFirstTextualBodyIdentity(raw any) bool {
	switch body := raw.(type) {
	case map[string]any:
		if strings.EqualFold(strings.TrimSpace(annStringValue(body, "type")), "TextualBody") {
			delete(body, "id")
			delete(body, "@id")
			return true
		}
	case []any:
		for _, entry := range body {
			if clearFirstTextualBodyIdentity(entry) {
				return true
			}
		}
	}
	return false
}

func clearFirstFragmentIdentity(raw any) bool {
	switch selector := raw.(type) {
	case map[string]any:
		if strings.EqualFold(strings.TrimSpace(annStringValue(selector, "type")), "FragmentSelector") {
			value, _ := selector["value"].(string)
			_, present, err := iiif.MediaFragmentPixelXYWH(value)
			if err != nil || !present {
				return false
			}
			delete(selector, "id")
			delete(selector, "@id")
			return true
		}
	case []any:
		for _, entry := range selector {
			if clearFirstFragmentIdentity(entry) {
				return true
			}
		}
	}
	return false
}

func cloneAnnotation(annotation map[string]any) (map[string]any, error) {
	raw, err := json.Marshal(annotation)
	if err != nil {
		return nil, fmt.Errorf("encode annotation template: %w", err)
	}
	var clone map[string]any
	if err := iiif.DecodeJSON(raw, &clone); err != nil {
		return nil, fmt.Errorf("decode annotation template: %w", err)
	}
	return clone, nil
}

func textBodyWithValue(raw any, text string) any {
	setValue := func(body map[string]any) {
		body["type"] = "TextualBody"
		body["value"] = text
		if body["purpose"] == nil {
			body["purpose"] = "supplementing"
		}
		if body["format"] == nil {
			body["format"] = "text/plain"
		}
	}
	switch body := raw.(type) {
	case map[string]any:
		if strings.EqualFold(strings.TrimSpace(annStringValue(body, "type")), "TextualBody") {
			setValue(body)
			return body
		}
		textual := map[string]any{}
		setValue(textual)
		return []any{body, textual}
	case []any:
		for _, entry := range body {
			candidate, ok := entry.(map[string]any)
			if ok && strings.EqualFold(strings.TrimSpace(annStringValue(candidate, "type")), "TextualBody") {
				setValue(candidate)
				return body
			}
		}
		textual := map[string]any{}
		setValue(textual)
		return append(body, textual)
	default:
		textual := map[string]any{}
		setValue(textual)
		return []any{textual}
	}
}

func targetWithFragment(raw any, canvasURI string, x1, y1, x2, y2 int) (map[string]any, error) {
	xywh := fmt.Sprintf("%d,%d,%d,%d", x1, y1, maxInt(1, x2-x1), maxInt(1, y2-y1))
	target, err := iiif.ReplaceTargetPixelXYWH(raw, canvasURI, xywh)
	if err != nil {
		return nil, fmt.Errorf("replace annotation target geometry: %w", err)
	}
	return target, nil
}

func structuralPropertyProjection(annotation map[string]any) (map[string]any, error) {
	projection, err := cloneAnnotation(annotation)
	if err != nil {
		return nil, err
	}
	delete(projection, "id")
	delete(projection, "textGranularity")
	clearFirstTextualBodyValue(projection["body"])
	switch target := projection["target"].(type) {
	case string:
		canvas, fragment, hasFragment := strings.Cut(strings.TrimSpace(target), "#")
		if hasFragment {
			withoutXYWH, err := iiif.RemoveMediaFragmentPixelXYWH(fragment)
			if err != nil {
				return nil, err
			}
			projection["target"] = strings.TrimSpace(canvas)
			if withoutXYWH != "" {
				projection["target"] = strings.TrimSpace(canvas) + "#" + withoutXYWH
			}
		}
	case map[string]any:
		delete(target, "id")
		delete(target, "@id")
		if _, err := clearFirstFragmentValue(target["selector"]); err != nil {
			return nil, err
		}
	}
	return projection, nil
}

func clearFirstTextualBodyValue(raw any) bool {
	switch body := raw.(type) {
	case map[string]any:
		if strings.EqualFold(strings.TrimSpace(annStringValue(body, "type")), "TextualBody") {
			delete(body, "value")
			delete(body, "id")
			delete(body, "@id")
			return true
		}
	case []any:
		for _, entry := range body {
			if clearFirstTextualBodyValue(entry) {
				return true
			}
		}
	}
	return false
}

func clearFirstFragmentValue(raw any) (bool, error) {
	switch selector := raw.(type) {
	case map[string]any:
		if strings.EqualFold(strings.TrimSpace(annStringValue(selector, "type")), "FragmentSelector") {
			value, _ := selector["value"].(string)
			_, present, err := iiif.MediaFragmentPixelXYWH(value)
			if err != nil {
				return false, err
			}
			if !present {
				return false, nil
			}
			withoutXYWH, err := iiif.RemoveMediaFragmentPixelXYWH(value)
			if err != nil {
				return false, err
			}
			if withoutXYWH == "" {
				delete(selector, "value")
			} else {
				selector["value"] = withoutXYWH
			}
			delete(selector, "id")
			delete(selector, "@id")
			return true, nil
		}
	case []any:
		for _, entry := range selector {
			cleared, err := clearFirstFragmentValue(entry)
			if err != nil {
				return false, err
			}
			if cleared {
				return true, nil
			}
		}
	}
	return false, nil
}
