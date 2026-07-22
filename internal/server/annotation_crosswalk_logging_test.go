package server

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestCrosswalkSkipLogsRedactAnnotationContentAndResourceURIs(t *testing.T) {
	const (
		secretText     = "PRIVATE_OCR_TEXT_DO_NOT_LOG"
		secretCanvas   = "https://private.example/canvas/PRIVATE_CANVAS_DO_NOT_LOG"
		secretID       = "https://private.example/annotation/PRIVATE_ID_DO_NOT_LOG"
		secretFragment = "PRIVATE_FRAGMENT_DO_NOT_LOG"
	)
	page := `{
  "type":"AnnotationPage",
  "items":[
    {
      "id":"` + secretID + `-missing-fragment", "type":"Annotation", "textGranularity":"line",
      "body":{"type":"TextualBody","value":"` + secretText + `"},
      "target":{"type":"SpecificResource","source":"` + secretCanvas + `"}
    },
    {
      "id":"` + secretID + `-invalid-fragment", "type":"Annotation", "textGranularity":"line",
      "body":{"type":"TextualBody","value":"` + secretText + `"},
      "target":{"type":"SpecificResource","source":"` + secretCanvas + `","selector":{"type":"FragmentSelector","value":"xywh=` + secretFragment + `"}}
    },
    {
      "id":"` + secretID + `-missing-text", "type":"Annotation", "textGranularity":"line",
      "body":{"type":"TextualBody","value":""},
      "target":{"type":"SpecificResource","source":"` + secretCanvas + `","selector":{"type":"FragmentSelector","value":"xywh=1,2,30,40"}}
    }
  ]
}`

	var captured bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&captured, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	// Every fixture annotation is deliberately skipped, so the crosswalk's
	// terminal "no parseable annotations" error is expected. This test captures
	// only the preceding diagnostic records.
	_, _, _, _ = annotationPageToHOCRLines(page)
	logs := captured.String()
	for _, secret := range []string{secretText, secretCanvas, secretID, secretFragment} {
		if strings.Contains(logs, secret) {
			t.Fatalf("crosswalk skip log exposed annotation content %q: %s", secret, logs)
		}
	}
	for _, metadata := range []string{`"hasCanvas":true`, `"hasText":true`, `"targetType":"object"`} {
		if !strings.Contains(logs, metadata) {
			t.Fatalf("crosswalk skip log omitted bounded diagnostic metadata %s: %s", metadata, logs)
		}
	}
}
