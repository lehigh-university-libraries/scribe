package server

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

var updateGolden = flag.Bool("update", false, "overwrite golden fixture files with current output")

// postCrosswalkHandler POSTs a JSON body to an http.HandlerFunc and returns the recorder.
func postCrosswalkHandler(t *testing.T, fn http.HandlerFunc, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	fn(rec, req)
	return rec
}

// decodeCrosswalkResponse decodes the standard annotationCrosswalkResponse JSON.
func decodeCrosswalkResponse(t *testing.T, rec *httptest.ResponseRecorder) annotationCrosswalkResponse {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp annotationCrosswalkResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp
}

// checkGolden compares content against a golden file, or writes it when -update is set.
func checkGolden(t *testing.T, path, got string) {
	t.Helper()
	if *updateGolden {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		t.Logf("updated golden: %s", path)
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (run with -update to create): %v", path, err)
	}
	want := string(raw)
	// Normalise trailing newlines so editors that add one don't break tests.
	if strings.TrimRight(got, "\n") != strings.TrimRight(want, "\n") {
		t.Errorf("content mismatch for golden %s\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}

func loadJSON(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("load %s: %v", path, err)
	}
	return string(b)
}

// TestCrosswalkRoutes verifies every crosswalk handler against golden fixtures,
// for both the full annotation-page path and the single-annotation path.
func TestCrosswalkRoutes(t *testing.T) {
	h := &Handler{}
	pageJSON := loadJSON(t, "testdata/crosswalk/annotation_page.json")
	singleJSON := loadJSON(t, "testdata/crosswalk/single_annotation.json")

	tests := []struct {
		name       string
		fn         http.HandlerFunc
		inputKey   string
		inputVal   string
		wantFormat string
		goldenFile string
	}{
		{
			name:       "annotation_page to plain text",
			fn:         h.handleCrosswalkToPlainText,
			inputKey:   "annotation_page_json",
			inputVal:   pageJSON,
			wantFormat: "text/plain",
			goldenFile: "testdata/crosswalk/expected_plain.txt",
		},
		{
			name:       "annotation_page to hOCR",
			fn:         h.handleCrosswalkToHOCR,
			inputKey:   "annotation_page_json",
			inputVal:   pageJSON,
			wantFormat: "text/vnd.hocr+html",
			goldenFile: "testdata/crosswalk/expected_hocr.html",
		},
		{
			name:       "annotation_page to PageXML",
			fn:         h.handleCrosswalkToPageXML,
			inputKey:   "annotation_page_json",
			inputVal:   pageJSON,
			wantFormat: "application/vnd.prima.page+xml",
			goldenFile: "testdata/crosswalk/expected_page.xml",
		},
		{
			name:       "annotation_page to ALTO XML",
			fn:         h.handleCrosswalkToALTOXML,
			inputKey:   "annotation_page_json",
			inputVal:   pageJSON,
			wantFormat: "application/alto+xml",
			goldenFile: "testdata/crosswalk/expected_alto.xml",
		},
		{
			name:       "single annotation to plain text",
			fn:         h.handleCrosswalkToPlainText,
			inputKey:   "annotation_json",
			inputVal:   singleJSON,
			wantFormat: "text/plain",
			goldenFile: "testdata/crosswalk/expected_single_plain.txt",
		},
		{
			name:       "single annotation to hOCR",
			fn:         h.handleCrosswalkToHOCR,
			inputKey:   "annotation_json",
			inputVal:   singleJSON,
			wantFormat: "text/vnd.hocr+html",
			goldenFile: "testdata/crosswalk/expected_single_hocr.html",
		},
		{
			name:       "single annotation to PageXML",
			fn:         h.handleCrosswalkToPageXML,
			inputKey:   "annotation_json",
			inputVal:   singleJSON,
			wantFormat: "application/vnd.prima.page+xml",
			goldenFile: "testdata/crosswalk/expected_single_page.xml",
		},
		{
			name:       "single annotation to ALTO XML",
			fn:         h.handleCrosswalkToALTOXML,
			inputKey:   "annotation_json",
			inputVal:   singleJSON,
			wantFormat: "application/alto+xml",
			goldenFile: "testdata/crosswalk/expected_single_alto.xml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := postCrosswalkHandler(t, tt.fn, map[string]string{tt.inputKey: tt.inputVal})
			resp := decodeCrosswalkResponse(t, rec)

			if resp.Format != tt.wantFormat {
				t.Errorf("format: got %q, want %q", resp.Format, tt.wantFormat)
			}
			checkGolden(t, tt.goldenFile, resp.Content)
		})
	}
}

// TestCrosswalkErrors verifies that bad inputs return 400 with an error body.
func TestCrosswalkErrors(t *testing.T) {
	h := &Handler{}

	tests := []struct {
		name string
		fn   http.HandlerFunc
		body string
	}{
		{
			name: "invalid JSON body",
			fn:   h.handleCrosswalkToPlainText,
			body: `not json`,
		},
		{
			name: "empty object — no annotation fields",
			fn:   h.handleCrosswalkToPlainText,
			body: `{}`,
		},
		{
			name: "annotation page with no items",
			fn:   h.handleCrosswalkToHOCR,
			body: buildCrosswalkBody(t, "annotation_page_json", `{"type":"AnnotationPage","items":[]}`),
		},
		{
			name: "annotation page with items but no parseable text",
			fn:   h.handleCrosswalkToPageXML,
			body: buildCrosswalkBody(t, "annotation_page_json", `{"type":"AnnotationPage","items":[{"id":"x","type":"Annotation","body":[]}]}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			tt.fn(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
			}
			var errResp map[string]string
			if err := json.NewDecoder(rec.Body).Decode(&errResp); err != nil {
				t.Errorf("error response is not valid JSON: %v", err)
			}
			if errResp["error"] == "" {
				t.Errorf("error response missing 'error' field: %v", errResp)
			}
		})
	}
}

func TestCrosswalkMixedLineAndWordGranularity(t *testing.T) {
	h := &Handler{}
	pageJSON := `{
	  "type": "AnnotationPage",
	  "items": [
	    {
	      "id": "line-1",
	      "type": "Annotation",
	      "textGranularity": "line",
	      "motivation": "supplementing",
	      "body": [{"type":"TextualBody","purpose":"supplementing","value":"Course Catalog"}],
	      "target": {"source":{"id":"https://example.org/canvas/1","type":"Canvas"},"selector":{"type":"FragmentSelector","value":"xywh=10,20,490,25"}}
	    },
	    {
	      "id": "word-1",
	      "type": "Annotation",
	      "textGranularity": "word",
	      "motivation": "supplementing",
	      "body": [{"type":"TextualBody","purpose":"supplementing","value":"Course"}],
	      "target": {"source":{"id":"https://example.org/canvas/1","type":"Canvas"},"selector":{"type":"FragmentSelector","value":"xywh=10,20,90,25"}}
	    },
	    {
	      "id": "word-2",
	      "type": "Annotation",
	      "textGranularity": "word",
	      "motivation": "supplementing",
	      "body": [{"type":"TextualBody","purpose":"supplementing","value":"Catalog"}],
	      "target": {"source":{"id":"https://example.org/canvas/1","type":"Canvas"},"selector":{"type":"FragmentSelector","value":"xywh=110,20,90,25"}}
	    }
	  ]
	}`

	rec := postCrosswalkHandler(t, h.handleCrosswalkToPlainText, map[string]string{"annotation_page_json": pageJSON})
	resp := decodeCrosswalkResponse(t, rec)
	if strings.TrimSpace(resp.Content) != "Course Catalog" {
		t.Fatalf("plain text crosswalk duplicated or lost mixed granularity content: %q", resp.Content)
	}

	rec = postCrosswalkHandler(t, h.handleCrosswalkToHOCR, map[string]string{"annotation_page_json": pageJSON})
	resp = decodeCrosswalkResponse(t, rec)
	if strings.Count(resp.Content, "Course") != 1 || strings.Count(resp.Content, "Catalog") != 1 {
		t.Fatalf("hOCR crosswalk duplicated mixed granularity content:\n%s", resp.Content)
	}
}

// buildCrosswalkBody JSON-encodes a crosswalk request with a single string field.
func buildCrosswalkBody(t *testing.T, key, value string) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{key: value})
	if err != nil {
		t.Fatalf("build body: %v", err)
	}
	return string(b)
}
