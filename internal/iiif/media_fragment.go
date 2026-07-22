package iiif

import (
	"fmt"
	"strings"
)

const fragmentSelectorType = "FragmentSelector"

// TargetCanvas returns the Canvas identity from a compact target, a
// SpecificResource, a Canvas reference, or a single-element target array. A
// compact target's media fragment is never part of the Canvas identity.
func TargetCanvas(target any) string {
	switch target := target.(type) {
	case string:
		canvas, _, _ := strings.Cut(strings.TrimSpace(target), "#")
		return strings.TrimSpace(canvas)
	case map[string]any:
		switch source := target["source"].(type) {
		case string:
			return strings.TrimSpace(source)
		case map[string]any:
			id, _ := source["id"].(string)
			return strings.TrimSpace(id)
		}
		if isCanvasReference(target) {
			id, _ := target["id"].(string)
			return strings.TrimSpace(id)
		}
	case []any:
		// Multiple Web Annotation targets are valid IIIF, but Scribe's
		// canonical OCR model requires one unambiguous Canvas ownership key.
		if len(target) == 1 {
			return TargetCanvas(target[0])
		}
	}
	return ""
}

// TargetPixelXYWH returns the single xywh media-fragment value from a compact
// or SpecificResource target. The returned value keeps an optional "pixel:"
// prefix. Multiple spatial selectors are rejected as ambiguous.
func TargetPixelXYWH(target any) (string, bool, error) {
	switch target := target.(type) {
	case string:
		_, fragment, present := strings.Cut(strings.TrimSpace(target), "#")
		if !present {
			return "", false, nil
		}
		return MediaFragmentPixelXYWH(fragment)
	case map[string]any:
		return selectorPixelXYWH(target["selector"])
	case []any:
		if len(target) == 0 {
			return "", false, nil
		}
		if len(target) != 1 {
			return "", false, fmt.Errorf("canonical annotation must have exactly one target")
		}
		return TargetPixelXYWH(target[0])
	default:
		return "", false, nil
	}
}

// MediaFragmentPixelXYWH finds exactly one xywh parameter among a media
// fragment's other dimensions (for example t and track).
func MediaFragmentPixelXYWH(raw string) (string, bool, error) {
	parameters := mediaFragmentParameters(raw)
	var found string
	count := 0
	for _, parameter := range parameters {
		key, value, present := strings.Cut(parameter, "=")
		if !present || strings.TrimSpace(key) != "xywh" {
			continue
		}
		count++
		found = strings.TrimSpace(value)
	}
	if count > 1 {
		return "", false, fmt.Errorf("media fragment contains multiple xywh parameters")
	}
	if count == 0 {
		return "", false, nil
	}
	if found == "" {
		return "", false, fmt.Errorf("media fragment xywh parameter is empty")
	}
	return found, true, nil
}

// ReplaceMediaFragmentPixelXYWH replaces only the xywh parameter and preserves
// every other media-fragment parameter and its order. If the existing value
// explicitly uses pixel units, the replacement does too.
func ReplaceMediaFragmentPixelXYWH(raw, xywh string) (string, error) {
	if err := validatePixelXYWH(xywh); err != nil {
		return "", err
	}
	parameters := mediaFragmentParameters(raw)
	replaced := false
	for index, parameter := range parameters {
		key, existing, present := strings.Cut(parameter, "=")
		if !present || strings.TrimSpace(key) != "xywh" {
			continue
		}
		if replaced {
			return "", fmt.Errorf("media fragment contains multiple xywh parameters")
		}
		replacement := strings.TrimSpace(xywh)
		if strings.HasPrefix(strings.TrimSpace(existing), "pixel:") && !strings.HasPrefix(replacement, "pixel:") {
			replacement = "pixel:" + replacement
		}
		parameters[index] = key + "=" + replacement
		replaced = true
	}
	if !replaced {
		parameters = append(parameters, "xywh="+strings.TrimSpace(xywh))
	}
	return strings.Join(parameters, "&"), nil
}

// RemoveMediaFragmentPixelXYWH removes only the xywh parameter. It is used to
// compare non-spatial selector semantics before a structural join.
func RemoveMediaFragmentPixelXYWH(raw string) (string, error) {
	parameters := mediaFragmentParameters(raw)
	result := make([]string, 0, len(parameters))
	removed := false
	for _, parameter := range parameters {
		key, _, present := strings.Cut(parameter, "=")
		if present && strings.TrimSpace(key) == "xywh" {
			if removed {
				return "", fmt.Errorf("media fragment contains multiple xywh parameters")
			}
			removed = true
			continue
		}
		result = append(result, parameter)
	}
	return strings.Join(result, "&"), nil
}

// ReplaceTargetPixelXYWH updates one target's Canvas and spatial selector
// without discarding unrelated target/source/selector properties. Compact
// targets become SpecificResources so their non-spatial fragment dimensions
// remain explicit and editable.
func ReplaceTargetPixelXYWH(raw any, canvasURI, xywh string) (map[string]any, error) {
	canvasURI = strings.TrimSpace(canvasURI)
	if err := requireHTTPURL(canvasURI, "canvas uri"); err != nil {
		return nil, err
	}

	var target map[string]any
	switch value := raw.(type) {
	case string:
		_, fragment, _ := strings.Cut(strings.TrimSpace(value), "#")
		updated, err := ReplaceMediaFragmentPixelXYWH(fragment, xywh)
		if err != nil {
			return nil, err
		}
		target = map[string]any{
			"source": canvasURI,
			"selector": map[string]any{
				"type":       fragmentSelectorType,
				"conformsTo": "http://www.w3.org/TR/media-frags/",
				"value":      updated,
			},
		}
	case map[string]any:
		if isCanvasReference(value) {
			target = map[string]any{"type": "SpecificResource", "source": value}
		} else {
			target = value
		}
		setTargetCanvas(target, canvasURI)
		if err := replaceSelectorPixelXYWH(target, xywh); err != nil {
			return nil, err
		}
	default:
		target = map[string]any{
			"source": canvasURI,
			"selector": map[string]any{
				"type":       fragmentSelectorType,
				"conformsTo": "http://www.w3.org/TR/media-frags/",
				"value":      "xywh=" + strings.TrimSpace(xywh),
			},
		}
	}
	return target, nil
}

func isCanvasReference(value map[string]any) bool {
	resourceType, _ := value["type"].(string)
	id, _ := value["id"].(string)
	return resourceType == "Canvas" && strings.TrimSpace(id) != "" && value["source"] == nil
}

func mediaFragmentParameters(raw string) []string {
	raw = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "#"))
	if raw == "" {
		return nil
	}
	return strings.Split(raw, "&")
}

func selectorPixelXYWH(raw any) (string, bool, error) {
	var found string
	count := 0
	var visit func(any) error
	visit = func(value any) error {
		switch selector := value.(type) {
		case map[string]any:
			selectorType, _ := selector["type"].(string)
			if !strings.EqualFold(strings.TrimSpace(selectorType), fragmentSelectorType) {
				return nil
			}
			value, _ := selector["value"].(string)
			xywh, present, err := MediaFragmentPixelXYWH(value)
			if err != nil {
				return err
			}
			if present {
				count++
				found = xywh
			}
		case []any:
			for _, entry := range selector {
				if err := visit(entry); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := visit(raw); err != nil {
		return "", false, err
	}
	if count > 1 {
		return "", false, fmt.Errorf("target contains multiple xywh FragmentSelectors")
	}
	return found, count == 1, nil
}

func setTargetCanvas(target map[string]any, canvasURI string) {
	switch source := target["source"].(type) {
	case string:
		target["source"] = canvasURI
	case map[string]any:
		source["id"] = canvasURI
		if source["type"] == nil {
			source["type"] = "Canvas"
		}
	default:
		target["source"] = canvasURI
	}
}

func replaceSelectorPixelXYWH(target map[string]any, xywh string) error {
	var spatial map[string]any
	fragmentSelectors := make([]map[string]any, 0, 1)
	collect := func(value any) error {
		selector, ok := value.(map[string]any)
		if !ok || !strings.EqualFold(strings.TrimSpace(stringValue(selector["type"])), fragmentSelectorType) {
			return nil
		}
		fragmentSelectors = append(fragmentSelectors, selector)
		fragmentValue, _ := selector["value"].(string)
		_, present, err := MediaFragmentPixelXYWH(fragmentValue)
		if err != nil {
			return err
		}
		if present {
			if spatial != nil {
				return fmt.Errorf("target contains multiple xywh FragmentSelectors")
			}
			spatial = selector
		}
		return nil
	}
	switch selectors := target["selector"].(type) {
	case map[string]any:
		if err := collect(selectors); err != nil {
			return err
		}
	case []any:
		for _, entry := range selectors {
			if err := collect(entry); err != nil {
				return err
			}
		}
	}
	if spatial == nil && len(fragmentSelectors) == 1 {
		spatial = fragmentSelectors[0]
	}
	if spatial != nil {
		value, _ := spatial["value"].(string)
		updated, err := ReplaceMediaFragmentPixelXYWH(value, xywh)
		if err != nil {
			return err
		}
		spatial["type"] = fragmentSelectorType
		spatial["value"] = updated
		if spatial["conformsTo"] == nil {
			spatial["conformsTo"] = "http://www.w3.org/TR/media-frags/"
		}
		return nil
	}

	fragment := map[string]any{
		"type":       fragmentSelectorType,
		"conformsTo": "http://www.w3.org/TR/media-frags/",
		"value":      "xywh=" + strings.TrimSpace(xywh),
	}
	switch selectors := target["selector"].(type) {
	case nil:
		target["selector"] = fragment
	case []any:
		target["selector"] = append(selectors, fragment)
	default:
		target["selector"] = []any{selectors, fragment}
	}
	return nil
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}
