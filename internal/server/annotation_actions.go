package server

import (
	"encoding/json"
	"fmt"
	"strings"
)

func parseLineAnnotation(raw string) (map[string]any, string, int, int, int, int, string, error) {
	var anno map[string]any
	if err := json.Unmarshal([]byte(raw), &anno); err != nil {
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
	body, _ := anno["body"].([]any)
	for _, b := range body {
		obj, _ := b.(map[string]any)
		v := strings.TrimSpace(annStringValue(obj, "value"))
		if v != "" {
			return v
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
