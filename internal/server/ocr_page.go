package server

import "github.com/lehigh-university-libraries/htr/pkg/ocrpage"

func originalPageJSON(page *ocrpage.Page) string {
	if page == nil {
		return ""
	}
	data, err := ocrpage.WriteJSON(*page)
	if err != nil {
		return ""
	} // Page was validated before canonical persistence.
	return string(data)
}

// pageAnnotations is the canonical geometry boundary for new segmentation.
// Stable Scribe identities and its text-body convention remain application
// policy; polygons, baselines and region classifications come from htr.
func pageAnnotations(scope, canvas string, page ocrpage.Page, levels ...ocrpage.Level) ([]any, error) {
	if len(levels) == 0 {
		levels = []ocrpage.Level{ocrpage.LevelRegion, ocrpage.LevelLine, ocrpage.LevelWord}
	}
	annotations, err := ocrpage.WriteIIIF(page, ocrpage.IIIFWriteOptions{
		CanvasID: canvas,
		ID: func(level ocrpage.Level, id string) string {
			granularity := string(level)
			if level == ocrpage.LevelRegion {
				granularity = "block"
			}
			return annotationID(scope, granularity, id)
		},
		Levels: levels,
	})
	if err != nil {
		return nil, err
	}
	items := make([]any, len(annotations))
	for i, annotation := range annotations {
		bodies := annotation["body"].([]map[string]any)
		bodyValues := make([]any, len(bodies))
		for j, body := range bodies {
			if body["purpose"] == "transcribing" {
				body["purpose"] = "supplementing"
				body["format"] = "text/plain"
			}
			bodyValues[j] = body
		}
		annotation["body"] = bodyValues
		target := annotation["target"].(map[string]any)
		target["source"] = map[string]any{"id": canvas, "type": "Canvas"}
		items[i] = annotation
	}
	return items, nil
}
