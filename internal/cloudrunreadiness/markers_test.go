package cloudrunreadiness

import (
	"fmt"
	"testing"
)

func TestBrowserReadinessMarkerVocabularyIsExhaustive(t *testing.T) {
	t.Parallel()
	categories := []string{
		"home", "context", "upload", "handoff", "transcription", "annotations",
		"editor", "overlay", "retranscribe", "structure", "save", "publish",
		"responsive", "token", "manifest", "cleanup", "network", "csp", "rate",
		"network-document-client", "network-document-server",
		"network-auth-client", "network-auth-server",
		"network-workspace-client", "network-workspace-server",
		"network-item-client", "network-item-server",
		"network-context-client", "network-context-server",
		"network-annotation-client", "network-annotation-server",
		"network-processing-client", "network-processing-server",
		"network-transcription-client", "network-transcription-server",
		"network-events-client", "network-events-server",
		"network-presentation-client", "network-presentation-server",
		"network-iiif-client", "network-iiif-server",
		"network-asset-client", "network-asset-server",
		"network-other-client", "network-other-server",
		"network-document-transport", "network-api-transport",
		"network-events-transport", "network-image-transport",
		"network-asset-transport", "network-other-transport",
		"initial-ingress-forbidden", "initial-ingress-not-found",
	}
	if len(categories) != 53 {
		t.Fatalf("browser category fixture has %d entries, want 53", len(categories))
	}
	for _, category := range categories {
		line := "browser readiness failed: " + category
		if !ReadinessMarkerAllowed(KindBrowser, line) {
			t.Errorf("browser category %q was rejected", category)
		}
		if ReadinessMarkerAllowed(KindBackend, line) || ReadinessMarkerAllowed(KindOCR, line) {
			t.Errorf("browser category %q crossed kind boundary", category)
		}
	}
}

func TestBrowserUploadSubstageMarkerVocabularyIsExhaustive(t *testing.T) {
	t.Parallel()
	substages := []string{
		"start-response", "start-transport", "image-terminal", "image-retry",
		"image-transport", "handoff-timeout", "handoff-terminal", "response-contract",
	}
	for _, substage := range substages {
		line := "browser readiness upload substage: " + substage
		if !ReadinessMarkerAllowed(KindBrowser, line) {
			t.Errorf("browser upload substage %q was rejected", substage)
		}
		if ReadinessMarkerAllowed(KindBackend, line) || ReadinessMarkerAllowed(KindOCR, line) {
			t.Errorf("browser upload substage %q crossed kind boundary", substage)
		}
	}
}

func TestBrowserUploadDurableFailureMarkerVocabularyIsExhaustive(t *testing.T) {
	t.Parallel()
	categories := []string{
		"segmentation-canceled", "segmentation-timeout", "segmentation-failed",
		"provider-authentication", "provider-failed", "admission-failed",
		"upload-storage-failed", "segmentation-output-failed", "quota-resize-failed",
		"lease-renewal-failed", "image-commit-failed", "ocr-run-commit-failed",
		"annotation-commit-failed", "transcription-enqueue-failed", "item-reload-failed",
		"batch-commit-failed", "unknown",
	}
	for _, category := range categories {
		line := "browser readiness upload durable failure: " + category
		if !ReadinessMarkerAllowed(KindBrowser, line) {
			t.Errorf("browser durable upload failure %q was rejected", category)
		}
		if ReadinessMarkerAllowed(KindBackend, line) || ReadinessMarkerAllowed(KindOCR, line) {
			t.Errorf("browser durable upload failure %q crossed kind boundary", category)
		}
	}
}

func TestBrowserUploadRetryableResponseMarkerVocabularyIsExhaustive(t *testing.T) {
	t.Parallel()
	kinds := []string{
		"connect-aborted", "connect-already-exists", "connect-deadline-exceeded",
		"connect-internal", "connect-resource-exhausted", "connect-unavailable",
		"connect-unknown", "http-408", "http-409", "http-425", "http-429",
		"http-500", "http-502", "http-503", "http-504",
	}
	for _, kind := range kinds {
		line := "browser readiness upload retryable response: " + kind
		if !ReadinessMarkerAllowed(KindBrowser, line) {
			t.Errorf("browser upload retryable response %q was rejected", kind)
		}
		if ReadinessMarkerAllowed(KindBackend, line) || ReadinessMarkerAllowed(KindOCR, line) {
			t.Errorf("browser upload retryable response %q crossed kind boundary", kind)
		}
	}
}

func TestBrowserStructureSubstageMarkerVocabularyIsExhaustive(t *testing.T) {
	t.Parallel()
	substages := []string{
		"draw-mode", "centered-line", "undo-redo", "delete-line", "line-edit",
		"split-words", "add-word", "word-history", "join-words", "split-line",
		"join-lines", "snapshot",
	}
	for _, substage := range substages {
		line := "browser readiness structure substage: " + substage
		if !ReadinessMarkerAllowed(KindBrowser, line) {
			t.Errorf("browser structure substage %q was rejected", substage)
		}
		if ReadinessMarkerAllowed(KindBackend, line) || ReadinessMarkerAllowed(KindOCR, line) {
			t.Errorf("browser structure substage %q crossed kind boundary", substage)
		}
	}
}

func TestBrowserTokenSubstageMarkerVocabularyIsExhaustive(t *testing.T) {
	t.Parallel()
	substages := []string{
		"post-home-presentation", "settings-open", "key-creation", "key-display",
		"key-deletion", "logout-proof", "final-cleanup",
	}
	for _, substage := range substages {
		line := "browser readiness token substage: " + substage
		if !ReadinessMarkerAllowed(KindBrowser, line) {
			t.Errorf("browser token substage %q was rejected", substage)
		}
		if ReadinessMarkerAllowed(KindBackend, line) || ReadinessMarkerAllowed(KindOCR, line) {
			t.Errorf("browser token substage %q crossed kind boundary", substage)
		}
	}
}

func TestBrowserRateLimitMarkerVocabularyIsExhaustive(t *testing.T) {
	t.Parallel()
	families := []string{
		"document", "auth", "workspace", "item", "context", "annotation",
		"processing", "transcription", "events", "presentation", "iiif", "asset", "other",
	}
	for _, family := range families {
		line := "browser readiness rate limit: " + family
		if !ReadinessMarkerAllowed(KindBrowser, line) {
			t.Errorf("browser rate limit family %q was rejected", family)
		}
		if ReadinessMarkerAllowed(KindBackend, line) || ReadinessMarkerAllowed(KindOCR, line) {
			t.Errorf("browser rate limit family %q crossed kind boundary", family)
		}
	}
}

func TestParseLogMarkersRetainsOnlyFixedBrowserUploadDiagnostics(t *testing.T) {
	t.Parallel()
	const execution = "scribe-browser-deadbeef-abcde"
	data := []byte(`[
		{"textPayload":"browser readiness failed: upload\n","labels":{"run.googleapis.com/execution_name":"scribe-browser-deadbeef-abcde"}},
		{"textPayload":"browser readiness upload substage: image-retry\n","labels":{"run.googleapis.com/execution_name":"scribe-browser-deadbeef-abcde"}},
		{"textPayload":"browser readiness upload retryable response: connect-already-exists\n","labels":{"run.googleapis.com/execution_name":"scribe-browser-deadbeef-abcde"}},
		{"textPayload":"browser readiness upload durable failure: provider-authentication\n","labels":{"run.googleapis.com/execution_name":"scribe-browser-deadbeef-abcde"}},
		{"textPayload":"browser readiness upload substage: raw-error\n","labels":{"run.googleapis.com/execution_name":"scribe-browser-deadbeef-abcde"}},
		{"textPayload":"browser readiness upload retryable response: gateway-502\n","labels":{"run.googleapis.com/execution_name":"scribe-browser-deadbeef-abcde"}},
		{"textPayload":"browser readiness upload durable failure: private-response\n","labels":{"run.googleapis.com/execution_name":"scribe-browser-deadbeef-abcde"}},
		{"textPayload":"browser readiness upload substage: start-response\n","labels":{"run.googleapis.com/execution_name":"other-execution"}}
	]`)

	markers, err := parseLogMarkers(data, execution, KindBrowser)
	if err != nil {
		t.Fatalf("parseLogMarkers: %v", err)
	}
	if len(markers) != 4 ||
		markers[0] != "browser readiness failed: upload" ||
		markers[1] != "browser readiness upload substage: image-retry" ||
		markers[2] != "browser readiness upload retryable response: connect-already-exists" ||
		markers[3] != "browser readiness upload durable failure: provider-authentication" {
		t.Fatalf("markers = %#v", markers)
	}
}

func TestParseLogMarkersRetainsOnlyFixedBrowserStructureDiagnostics(t *testing.T) {
	t.Parallel()
	const execution = "scribe-browser-deadbeef-abcde"
	data := []byte(`[
		{"textPayload":"browser readiness failed: structure\n","labels":{"run.googleapis.com/execution_name":"scribe-browser-deadbeef-abcde"}},
		{"textPayload":"browser readiness structure substage: join-words\n","labels":{"run.googleapis.com/execution_name":"scribe-browser-deadbeef-abcde"}},
		{"textPayload":"browser readiness structure substage: raw-error\n","labels":{"run.googleapis.com/execution_name":"scribe-browser-deadbeef-abcde"}},
		{"textPayload":"browser readiness structure substage: split-line\n","labels":{"run.googleapis.com/execution_name":"other-execution"}}
	]`)

	markers, err := parseLogMarkers(data, execution, KindBrowser)
	if err != nil {
		t.Fatalf("parseLogMarkers: %v", err)
	}
	if len(markers) != 2 ||
		markers[0] != "browser readiness failed: structure" ||
		markers[1] != "browser readiness structure substage: join-words" {
		t.Fatalf("markers = %#v", markers)
	}
}

func TestOCRReadinessMarkerVocabularyIsExhaustive(t *testing.T) {
	t.Parallel()
	categories := []string{"image-contract"}
	for _, operation := range []string{"segment", "transcribe", "ollama"} {
		for _, failure := range []string{"token", "request", "timeout", "contract"} {
			categories = append(categories, operation+"-"+failure)
		}
	}
	if len(categories) != 13 {
		t.Fatalf("OCR category fixture has %d entries, want 13", len(categories))
	}
	for _, category := range categories {
		line := "ocr readiness failed: " + category
		if !ReadinessMarkerAllowed(KindOCR, line) {
			t.Errorf("OCR category %q was rejected", category)
		}
		if ReadinessMarkerAllowed(KindBackend, line) || ReadinessMarkerAllowed(KindBrowser, line) {
			t.Errorf("OCR category %q crossed kind boundary", category)
		}
	}
}

func TestBackendReadinessMarkerVocabularyBranches(t *testing.T) {
	t.Parallel()
	lines := []string{
		"frontend readiness failed: frontend-server-exited",
		"frontend readiness failed: frontend did not respond",
		"frontend readiness failed: HTTP 503 (invalid-json)",
		"frontend readiness failed: transport-TypeError/ECONNRESET",
		"frontend readiness failed: internal-Error",
		"frontend proxy request failed [TimeoutError/ETIMEDOUT]",
		"frontend backend startup gate failed [Error; readiness-contract; HTTP 503 (invalid-payload)]",
		"frontend backend startup gate failed [Error; startup-deadline; backend did not report ready]",
		"frontend backend startup gate failed [Error; startup-deadline; HTTP 503 (non-ready-status)]",
		"frontend backend startup gate failed [Error; startup-deadline; transport-Error/ECONNREFUSED]",
		"frontend backend startup gate failed [Error; startup-deadline; transport-AbortError]",
		"frontend readiness failed: transport-AbortError",
		"frontend backend network probe [dns-match; tcp-timeout; http-timeout]",
		"frontend backend network probe [dns-match; tcp-open; http-ready]",
		"frontend backend network probe [dns-timeout; tcp-timeout; http-timeout]",
		"frontend backend network probe [dns-invalid-origin; tcp-skipped; http-skipped]",
	}
	for _, line := range lines {
		if !ReadinessMarkerAllowed(KindBackend, line) {
			t.Errorf("backend marker was rejected: %q", line)
		}
		if ReadinessMarkerAllowed(KindBrowser, line) || ReadinessMarkerAllowed(KindOCR, line) {
			t.Errorf("backend marker crossed kind boundary: %q", line)
		}
	}
}

func TestReadinessMarkerRejectsUnknownOrNonExactLines(t *testing.T) {
	t.Parallel()
	tests := []struct {
		kind Kind
		line string
	}{
		{KindBrowser, "browser readiness failed: unknown"},
		{KindBrowser, "browser readiness failed: home extra"},
		{KindBrowser, "browser readiness failed: home\n"},
		{KindBrowser, "browser readiness upload substage: raw-error"},
		{KindBrowser, "browser readiness upload retryable response: gateway-502"},
		{KindBrowser, "browser readiness upload retryable response: http-418"},
		{KindBrowser, "browser readiness upload retryable response: connect-private"},
		{KindBrowser, "browser readiness upload durable failure: canonical-commit-failed"},
		{KindBrowser, "browser readiness upload durable failure: processing-failed"},
		{KindBrowser, "browser readiness upload durable failure: private-response"},
		{KindBrowser, "browser readiness structure substage: raw-error"},
		{KindOCR, "ocr readiness failed: segment-timeout secret"},
		{KindOCR, "ocr readiness failed: segment-timeout\r"},
		{KindBackend, "frontend readiness failed: HTTP 503 (invalid-json) extra"},
		{KindBackend, " frontend readiness failed: frontend-server-exited"},
		{Kind("unknown"), "browser readiness failed: home"},
	}
	for index, test := range tests {
		t.Run(fmt.Sprintf("case-%d", index), func(t *testing.T) {
			if ReadinessMarkerAllowed(test.kind, test.line) {
				t.Fatalf("accepted non-exact marker %q for %q", test.line, test.kind)
			}
		})
	}
}
