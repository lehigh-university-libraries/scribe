package server

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lehigh-university-libraries/htr/pkg/ocrpage"
)

func annotationLineGeometry(annotation map[string]any) (ocrpage.Line, error) {
	raw, err := json.Marshal(map[string]any{"type": "AnnotationPage", "items": []any{annotation}})
	if err != nil {
		return ocrpage.Line{}, err
	}
	page, err := ocrpage.ReadIIIF(raw, ocrpage.IIIFReadOptions{})
	if err != nil {
		return ocrpage.Line{}, err
	}
	if lines := page.Page.Lines(); len(lines) > 0 {
		return lines[0], nil
	}
	return ocrpage.Line{}, nil
}

func replaceLineGeometry(annotation map[string]any, polygon, baseline []ocrpage.Point) {
	target, ok := annotation["target"].(map[string]any)
	if !ok {
		return
	}
	selectors, ok := target["selector"].([]any)
	if !ok {
		selectors = []any{target["selector"]}
	}
	retained := make([]any, 0, len(selectors)+1)
	var svgSelector map[string]any
	for _, value := range selectors {
		if s, ok := value.(map[string]any); !ok || s["type"] != "SvgSelector" {
			retained = append(retained, value)
		} else if svgSelector == nil {
			svgSelector = s
		}
	}
	format := func(ps []ocrpage.Point) string {
		var parts []string
		for _, p := range ps {
			parts = append(parts, fmt.Sprintf("%d,%d", p.X, p.Y))
		}
		return strings.Join(parts, " ")
	}
	svg := `<svg xmlns="http://www.w3.org/2000/svg">`
	if len(polygon) > 2 {
		svg += `<polygon points="` + format(polygon) + `"/>`
	}
	if len(baseline) > 1 {
		svg += `<polyline class="baseline" points="` + format(baseline) + `"/>`
	}
	if len(polygon) > 2 || len(baseline) > 1 {
		if svgSelector == nil {
			svgSelector = map[string]any{"type": "SvgSelector"}
		}
		delete(svgSelector, "id")
		delete(svgSelector, "@id")
		svgSelector["value"] = svg + `</svg>`
		retained = append(retained, svgSelector)
	}
	if len(retained) == 1 {
		target["selector"] = retained[0]
	} else {
		target["selector"] = retained
	}
}
