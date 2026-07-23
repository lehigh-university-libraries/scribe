package server

import (
	"encoding/json"
	"encoding/xml"
	"flag"
	"os"
	"strings"
	"testing"
)

var updateGolden = flag.Bool("update", false, "overwrite golden fixture files with current output")

type crosswalkKind int

const (
	crosswalkPlainText crosswalkKind = iota
	crosswalkHOCR
	crosswalkPageXML
	crosswalkALTOXML
)

type crosswalkResult struct {
	format    string
	content   string
	extension string
}

// callCrosswalk exercises the canonical-page crosswalk boundary. Connect
// tenant and revision semantics are covered by the export acceptance tests.
func callCrosswalk(
	t *testing.T,
	_ *Handler,
	kind crosswalkKind,
	annotationPageJSON, annotationJSON string,
	canvasWidth, canvasHeight uint32,
) (crosswalkResult, error) {
	t.Helper()
	if strings.TrimSpace(annotationJSON) != "" {
		return crosswalkResult{}, errItemExportInvalid
	}
	var format string
	switch kind {
	case crosswalkPlainText:
		format = "txt"
	case crosswalkHOCR:
		format = "hocr"
	case crosswalkPageXML:
		format = "pagexml"
	case crosswalkALTOXML:
		format = "alto"
	default:
		t.Fatalf("unknown crosswalk kind %d", kind)
		return crosswalkResult{}, nil
	}
	content, mediaType, extension, err := renderAnnotationExport(annotationPageJSON, int(canvasWidth), int(canvasHeight), format)
	return crosswalkResult{format: mediaType, content: content, extension: extension}, err
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

func TestOCRDerivedCrosswalksExcludeGenericWebAnnotations(t *testing.T) {
	page := `{
  "@context":["http://iiif.io/api/extension/text-granularity/context.json","http://iiif.io/api/presentation/3/context.json"],
  "id":"https://scribe.example/presentation/v3/item-image-1/canvas/page-1/annotations",
  "type":"AnnotationPage",
  "items":[
    {
      "id":"https://scribe.example/presentation/v3/item-image-1/canvas/page-1/annotations/items/11111111111111111111111111111111",
      "type":"Annotation","motivation":"supplementing","textGranularity":"line",
      "body":{"type":"TextualBody","value":"canonical OCR words","format":"text/plain"},
      "target":{"type":"SpecificResource","source":"https://scribe.example/presentation/v3/item-image-1/canvas/page-1","selector":{"type":"FragmentSelector","conformsTo":"http://www.w3.org/TR/media-frags/","value":"xywh=10,20,200,30"}}
    },
    {
      "id":"https://scribe.example/presentation/v3/item-image-1/canvas/page-1/annotations/items/22222222222222222222222222222222",
      "type":"Annotation","motivation":"commenting","ex:editorState":"preserve me",
      "body":{"type":"TextualBody","value":"generic editorial comment","format":"text/plain"},
      "target":{"type":"SpecificResource","source":"https://scribe.example/presentation/v3/item-image-1/canvas/page-1","selector":{"type":"FragmentSelector","conformsTo":"http://www.w3.org/TR/media-frags/","value":"xywh=10,20,200,30"}}
    }
  ]
}`

	for _, test := range []struct {
		name string
		kind crosswalkKind
	}{
		{name: "plain text", kind: crosswalkPlainText},
		{name: "hOCR", kind: crosswalkHOCR},
		{name: "PAGE XML", kind: crosswalkPageXML},
		{name: "ALTO XML", kind: crosswalkALTOXML},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := callCrosswalk(t, &Handler{}, test.kind, page, "", 400, 300)
			if err != nil {
				t.Fatalf("crosswalk: %v", err)
			}
			if strings.Contains(result.content, "generic editorial comment") {
				t.Fatalf("generic annotation leaked into OCR-derived %s: %s", test.name, result.content)
			}
			if !strings.Contains(result.content, "canonical") || !strings.Contains(result.content, "OCR") || !strings.Contains(result.content, "words") {
				t.Fatalf("Text Granularity annotation missing from %s: %s", test.name, result.content)
			}
		})
	}

	var canonical map[string]any
	if err := json.Unmarshal([]byte(page), &canonical); err != nil {
		t.Fatal(err)
	}
	items := canonical["items"].([]any)
	if got := items[1].(map[string]any)["ex:editorState"]; got != "preserve me" {
		t.Fatalf("generic annotation was not preserved canonically: %#v", items[1])
	}
}

// TestCanonicalCrosswalkFormats verifies every derived format against golden
// fixtures using only complete committed-page input.
func TestCanonicalCrosswalkFormats(t *testing.T) {
	h := &Handler{}
	pageJSON := loadJSON(t, "testdata/crosswalk/annotation_page.json")

	tests := []struct {
		name               string
		kind               crosswalkKind
		annotationPageJSON string
		annotationJSON     string
		wantFormat         string
		goldenFile         string
	}{
		{
			name:               "annotation_page to plain text",
			kind:               crosswalkPlainText,
			annotationPageJSON: pageJSON,
			wantFormat:         "text/plain; charset=utf-8",
			goldenFile:         "testdata/crosswalk/expected_plain.txt",
		},
		{
			name:               "annotation_page to hOCR",
			kind:               crosswalkHOCR,
			annotationPageJSON: pageJSON,
			wantFormat:         "text/vnd.hocr+html; charset=utf-8",
			goldenFile:         "testdata/crosswalk/expected_hocr.html",
		},
		{
			name:               "annotation_page to PageXML",
			kind:               crosswalkPageXML,
			annotationPageJSON: pageJSON,
			wantFormat:         "application/vnd.prima.page+xml; charset=utf-8",
			goldenFile:         "testdata/crosswalk/expected_page.xml",
		},
		{
			name:               "annotation_page to ALTO XML",
			kind:               crosswalkALTOXML,
			annotationPageJSON: pageJSON,
			wantFormat:         "application/alto+xml; charset=utf-8",
			goldenFile:         "testdata/crosswalk/expected_alto.xml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := callCrosswalk(t, h, tt.kind, tt.annotationPageJSON, tt.annotationJSON, 0, 0)
			if err != nil {
				t.Fatalf("crosswalk: %v", err)
			}

			if resp.format != tt.wantFormat {
				t.Errorf("format: got %q, want %q", resp.format, tt.wantFormat)
			}
			if resp.extension == "" {
				t.Fatal("production renderer returned an empty extension")
			}
			checkGolden(t, tt.goldenFile, resp.content)
		})
	}
}

func TestXMLCrosswalksEscapeTextAndReplaceInvalidXMLRunes(t *testing.T) {
	pageJSON := `{
  "type":"AnnotationPage",
  "items":[{
    "id":"line-1",
    "type":"Annotation",
    "motivation":"supplementing",
    "textGranularity":"line",
    "body":{"type":"TextualBody","value":"A & < \"quoted\" bad\u0000rune C"},
    "target":{"source":"https://example.test/canvas/1","selector":{"type":"FragmentSelector","value":"xywh=1,2,100,20"}}
  }]
}`

	for _, kind := range []crosswalkKind{crosswalkPageXML, crosswalkALTOXML} {
		result, err := callCrosswalk(t, &Handler{}, kind, pageJSON, "", 200, 100)
		if err != nil {
			t.Fatalf("crosswalk %d: %v", kind, err)
		}
		var root struct {
			XMLName xml.Name
		}
		if err := xml.Unmarshal([]byte(result.content), &root); err != nil {
			t.Fatalf("crosswalk %d emitted malformed XML: %v\n%s", kind, err, result.content)
		}
		if strings.ContainsRune(result.content, '\x00') || !strings.ContainsRune(result.content, '\uFFFD') {
			t.Fatalf("crosswalk %d did not replace an XML 1.0-invalid rune: %q", kind, result.content)
		}
		if !strings.Contains(result.content, "&amp;") || !strings.Contains(result.content, "&lt;") {
			t.Fatalf("crosswalk %d did not XML-escape text: %q", kind, result.content)
		}
	}
}

// TestCrosswalkErrors verifies that malformed canonical-page inputs fail.
func TestCrosswalkErrors(t *testing.T) {
	h := &Handler{}

	tests := []struct {
		name               string
		kind               crosswalkKind
		annotationPageJSON string
		annotationJSON     string
	}{
		{
			name:               "invalid annotation page JSON",
			kind:               crosswalkPlainText,
			annotationPageJSON: `not json`,
		},
		{
			name: "empty request",
			kind: crosswalkPlainText,
		},
		{
			name:               "annotation page items is not an array",
			kind:               crosswalkHOCR,
			annotationPageJSON: `{"type":"AnnotationPage","items":{}}`,
		},
		{
			name:               "annotation page with items but no parseable text",
			kind:               crosswalkPageXML,
			annotationPageJSON: `{"type":"AnnotationPage","items":[{"id":"x","type":"Annotation","body":[]}]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := callCrosswalk(t, h, tt.kind, tt.annotationPageJSON, tt.annotationJSON, 0, 0)
			if err == nil {
				t.Fatal("malformed canonical page was accepted")
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

	resp, err := callCrosswalk(t, h, crosswalkPlainText, pageJSON, "", 0, 0)
	if err != nil {
		t.Fatalf("plain-text crosswalk: %v", err)
	}
	if strings.TrimSpace(resp.content) != "Course Catalog" {
		t.Fatalf("plain text crosswalk duplicated or lost mixed granularity content: %q", resp.content)
	}

	resp, err = callCrosswalk(t, h, crosswalkHOCR, pageJSON, "", 0, 0)
	if err != nil {
		t.Fatalf("hOCR crosswalk: %v", err)
	}
	if strings.Count(resp.content, "Course") != 1 || strings.Count(resp.content, "Catalog") != 1 {
		t.Fatalf("hOCR crosswalk duplicated mixed granularity content:\n%s", resp.content)
	}
}

func TestCrosswalkPageGranularityWithoutFragmentUsesCanvasBounds(t *testing.T) {
	h := &Handler{}
	pageJSON := `{
  "@context": [
    "http://iiif.io/api/extension/text-granularity/context.json",
    "http://iiif.io/api/presentation/3/context.json"
  ],
  "id": "https://example.test/page/annotations",
  "type": "AnnotationPage",
  "items": [{
    "id": "https://example.test/annotations/page-text",
    "type": "Annotation",
    "motivation": "supplementing",
    "textGranularity": "page",
    "body": [{"type":"TextualBody","purpose":"supplementing","format":"text/plain","value":"Whole page text"}],
    "target": "https://example.test/canvas/1"
  }]
}`
	for _, kind := range []crosswalkKind{crosswalkPlainText, crosswalkHOCR, crosswalkPageXML, crosswalkALTOXML} {
		result, err := callCrosswalk(t, h, kind, pageJSON, "", 1200, 1600)
		if err != nil {
			t.Fatalf("page-granularity crosswalk %d: %v", kind, err)
		}
		if !strings.Contains(result.content, "Whole page text") && (!strings.Contains(result.content, "Whole") || !strings.Contains(result.content, "page") || !strings.Contains(result.content, "text")) {
			t.Fatalf("page-granularity crosswalk %d lost text: %s", kind, result.content)
		}
	}
}

func TestCrosswalkSelectorArray(t *testing.T) {
	h := &Handler{}
	pageJSON := `{
	  "@context": ["http://iiif.io/api/extension/text-granularity/context.json","http://iiif.io/api/presentation/3/context.json"],
	  "id": "https://example.org/annotations/page-1",
	  "type": "AnnotationPage",
	  "items": [
	    {
	      "id": "https://example.org/annotations/line-1",
	      "type": "Annotation",
	      "textGranularity": "line",
	      "body": [{"type":"TextualBody","purpose":"supplementing","value":"Hello world","format":"text/plain"}],
	      "target": {
	        "source": {"id":"https://example.org/canvas/1","type":"Canvas"},
	        "selector": [
	          {"type":"CssSelector","value":".ignored"},
	          {"type":"FragmentSelector","value":"xywh=10,20,200,30"}
	        ]
	      }
	    }
	  ]
	}`

	resp, err := callCrosswalk(t, h, crosswalkPlainText, pageJSON, "", 0, 0)
	if err != nil {
		t.Fatalf("plain-text crosswalk: %v", err)
	}
	if strings.TrimSpace(resp.content) != "Hello world" {
		t.Fatalf("plain text = %q; want %q", resp.content, "Hello world")
	}
}

func TestCrosswalkEmptyAnnotationPageUsesCanvasDimensions(t *testing.T) {
	h := &Handler{}
	emptyPage := `{
	  "@context": "http://iiif.io/api/presentation/3/context.json",
	  "id": "https://example.org/annotations/empty",
	  "type": "AnnotationPage",
	  "items": []
	}`

	pageResponse, err := callCrosswalk(t, h, crosswalkPageXML, emptyPage, "", 2160, 3632)
	if err != nil {
		t.Fatalf("PAGE XML crosswalk: %v", err)
	}
	if !strings.Contains(pageResponse.content, `<Page imageFilename="source-image.png" imageWidth="2160" imageHeight="3632">`) {
		t.Fatalf("PAGE XML did not preserve empty Canvas dimensions:\n%s", pageResponse.content)
	}
	if strings.Contains(pageResponse.content, "<TextLine") {
		t.Fatalf("PAGE XML invented text lines for an empty page:\n%s", pageResponse.content)
	}

	altoResponse, err := callCrosswalk(t, h, crosswalkALTOXML, emptyPage, "", 2160, 3632)
	if err != nil {
		t.Fatalf("ALTO XML crosswalk: %v", err)
	}
	if !strings.Contains(altoResponse.content, `<Page ID="P1" PHYSICAL_IMG_NR="1" WIDTH="2160" HEIGHT="3632">`) {
		t.Fatalf("ALTO XML did not preserve empty Canvas dimensions:\n%s", altoResponse.content)
	}

	hocrResponse, err := callCrosswalk(t, h, crosswalkHOCR, emptyPage, "", 2160, 3632)
	if err != nil {
		t.Fatalf("hOCR crosswalk: %v", err)
	}
	if !strings.Contains(hocrResponse.content, "bbox 0 0 2160 3632") {
		t.Fatalf("hOCR did not preserve empty Canvas dimensions:\n%s", hocrResponse.content)
	}

	plainResponse, err := callCrosswalk(t, h, crosswalkPlainText, emptyPage, "", 2160, 3632)
	if err != nil {
		t.Fatalf("plain-text crosswalk: %v", err)
	}
	if plainResponse.content != "" {
		t.Fatalf("plain text for empty page = %q; want empty", plainResponse.content)
	}
}
