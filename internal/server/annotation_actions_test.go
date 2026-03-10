package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
)

// lineAnno builds a minimal IIIF line annotation JSON string.
func lineAnno(id, canvasURI, text string, x, y, w, h int) string {
	return fmt.Sprintf(`{
		"id": %q,
		"type": "Annotation",
		"textGranularity": "line",
		"motivation": "supplementing",
		"body": [{"type":"TextualBody","purpose":"supplementing","format":"text/plain","value":%q}],
		"target": {
			"source": {"id":%q,"type":"Canvas"},
			"selector": {"type":"FragmentSelector","conformsTo":"http://www.w3.org/TR/media-frags/","value":"xywh=%d,%d,%d,%d"}
		}
	}`, id, text, canvasURI, x, y, w, h)
}

const testCanvas = "https://example.org/canvas/1"

// --- parseLineAnnotation ---

func TestParseLineAnnotation(t *testing.T) {
	raw := lineAnno("anno-1", testCanvas, "Hello World", 10, 20, 200, 30)
	anno, text, x1, y1, x2, y2, canvas, err := parseLineAnnotation(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "Hello World" {
		t.Errorf("text: got %q, want %q", text, "Hello World")
	}
	if x1 != 10 || y1 != 20 || x2 != 210 || y2 != 50 {
		t.Errorf("bbox: got (%d,%d,%d,%d), want (10,20,210,50)", x1, y1, x2, y2)
	}
	if canvas != testCanvas {
		t.Errorf("canvas: got %q, want %q", canvas, testCanvas)
	}
	if annStringValue(anno, "id") != "anno-1" {
		t.Errorf("id not preserved in parsed annotation")
	}
}

func TestParseLineAnnotation_Errors(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{"invalid json", `not json`},
		{"missing canvas", `{"id":"x","type":"Annotation","body":[{"type":"TextualBody","value":"hi"}],"target":{"source":{},"selector":{"type":"FragmentSelector","value":"xywh=0,0,10,10"}}}`},
		{"missing fragment", `{"id":"x","type":"Annotation","body":[{"type":"TextualBody","value":"hi"}],"target":{"source":{"id":"https://example.org/canvas/1","type":"Canvas"}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, _, _, _, _, err := parseLineAnnotation(tt.raw)
			if err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

// --- buildLineAnnotation ---

func TestBuildLineAnnotation(t *testing.T) {
	anno := buildLineAnnotation("built-1", testCanvas, 5, 10, 105, 40, "some text")
	if annStringValue(anno, "id") != "built-1" {
		t.Errorf("id: got %q", annStringValue(anno, "id"))
	}
	if annStringValue(anno, "textGranularity") != "line" {
		t.Errorf("granularity: got %q", annStringValue(anno, "textGranularity"))
	}
	text := extractAnnotationText(anno)
	if text != "some text" {
		t.Errorf("text: got %q", text)
	}
	// Round-trip through parseLineAnnotation to verify bbox is correct.
	b, _ := json.Marshal(anno)
	_, _, x1, y1, x2, y2, _, err := parseLineAnnotation(string(b))
	if err != nil {
		t.Fatalf("round-trip parse: %v", err)
	}
	// xywh=5,10,100,30 → x1=5,y1=10,x2=105,y2=40
	if x1 != 5 || y1 != 10 || x2 != 105 || y2 != 40 {
		t.Errorf("round-trip bbox: got (%d,%d,%d,%d), want (5,10,105,40)", x1, y1, x2, y2)
	}
}

// --- SplitLineIntoWords Connect handler ---

func TestSplitLineIntoWords_ExplicitWords(t *testing.T) {
	h := &Handler{}
	raw := lineAnno("line-1", testCanvas, "Hello World", 0, 0, 200, 30)
	req := connect.NewRequest(&scribev1.SplitLineIntoWordsRequest{
		AnnotationJson: raw,
		Words:          []string{"Hello", "World"},
	})
	resp, err := h.SplitLineIntoWords(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var page map[string]any
	if jsonErr := json.Unmarshal([]byte(resp.Msg.GetAnnotationPageJson()), &page); jsonErr != nil {
		t.Fatalf("invalid annotation page json: %v", jsonErr)
	}
	items, _ := page["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("expected 2 word items, got %d", len(items))
	}

	// Verify granularity and proportional widths.
	for i, item := range items {
		anno, _ := item.(map[string]any)
		if annStringValue(anno, "textGranularity") != "word" {
			t.Errorf("item %d: textGranularity: got %q, want %q", i, annStringValue(anno, "textGranularity"), "word")
		}
		// ID should be derived from parent line ID.
		if !strings.HasPrefix(annStringValue(anno, "id"), "line-1-word-") {
			t.Errorf("item %d: id %q doesn't have expected prefix", i, annStringValue(anno, "id"))
		}
	}

	// Word 0 should start at x=0, word 1 at x=100.
	_, _, x10, _, _, _, _, _ := parseLineAnnotation(mustMarshal(t, items[0]))
	_, _, x11, _, _, _, _, _ := parseLineAnnotation(mustMarshal(t, items[1]))
	if x10 != 0 {
		t.Errorf("word 0 x1: got %d, want 0", x10)
	}
	if x11 != 100 {
		t.Errorf("word 1 x1: got %d, want 100", x11)
	}
}

func TestSplitLineIntoWords_TokenizesTextWhenWordsEmpty(t *testing.T) {
	h := &Handler{}
	raw := lineAnno("line-1", testCanvas, "one two three", 0, 0, 300, 20)
	req := connect.NewRequest(&scribev1.SplitLineIntoWordsRequest{
		AnnotationJson: raw,
	})
	resp, err := h.SplitLineIntoWords(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var page map[string]any
	if jsonErr := json.Unmarshal([]byte(resp.Msg.GetAnnotationPageJson()), &page); jsonErr != nil {
		t.Fatalf("invalid annotation page json: %v", jsonErr)
	}
	items, _ := page["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("expected 3 word items, got %d", len(items))
	}
}

func TestSplitLineIntoWords_EmptyLine(t *testing.T) {
	h := &Handler{}
	raw := lineAnno("line-1", testCanvas, "", 0, 0, 200, 30)
	req := connect.NewRequest(&scribev1.SplitLineIntoWordsRequest{
		AnnotationJson: raw,
	})
	_, err := h.SplitLineIntoWords(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for empty line, got nil")
	}
}

// --- SplitLineIntoTwoLines Connect handler ---

func TestSplitLineIntoTwoLines(t *testing.T) {
	h := &Handler{}
	raw := lineAnno("line-1", testCanvas, "Hello World foo bar", 10, 20, 200, 40)
	req := connect.NewRequest(&scribev1.SplitLineIntoTwoLinesRequest{
		AnnotationJson: raw,
		SplitAtWord:    2, // "Hello World" | "foo bar"
	})
	resp, err := h.SplitLineIntoTwoLines(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	parts := resp.Msg.GetAnnotationJsons()
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(parts))
	}

	_, textA, x1A, y1A, x2A, y2A, _, err := parseLineAnnotation(parts[0])
	if err != nil {
		t.Fatalf("part 0 parse: %v", err)
	}
	_, textB, x1B, y1B, x2B, y2B, _, err := parseLineAnnotation(parts[1])
	if err != nil {
		t.Fatalf("part 1 parse: %v", err)
	}

	if textA != "Hello World" {
		t.Errorf("part 0 text: got %q, want %q", textA, "Hello World")
	}
	if textB != "foo bar" {
		t.Errorf("part 1 text: got %q, want %q", textB, "foo bar")
	}
	// Both parts share the same x span.
	if x1A != x1B || x2A != x2B {
		t.Errorf("x spans differ: A=(%d,%d) B=(%d,%d)", x1A, x2A, x1B, x2B)
	}
	// Parts stack vertically and cover the original height.
	totalH := (y2A - y1A) + (y2B - y1B)
	if totalH != 40 {
		t.Errorf("total height: got %d, want 40", totalH)
	}
	// Part B starts where part A ends.
	if y1B != y2A {
		t.Errorf("vertical join: y1B=%d, y2A=%d", y1B, y2A)
	}
}

func TestSplitLineIntoTwoLines_DefaultMidpoint(t *testing.T) {
	h := &Handler{}
	raw := lineAnno("line-1", testCanvas, "one two three four", 0, 0, 400, 20)
	req := connect.NewRequest(&scribev1.SplitLineIntoTwoLinesRequest{
		AnnotationJson: raw,
		SplitAtWord:    0, // default = midpoint
	})
	resp, err := h.SplitLineIntoTwoLines(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Msg.GetAnnotationJsons()) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(resp.Msg.GetAnnotationJsons()))
	}
}

func TestSplitLineIntoTwoLines_SingleWordError(t *testing.T) {
	h := &Handler{}
	raw := lineAnno("line-1", testCanvas, "single", 0, 0, 100, 20)
	req := connect.NewRequest(&scribev1.SplitLineIntoTwoLinesRequest{
		AnnotationJson: raw,
	})
	_, err := h.SplitLineIntoTwoLines(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for single-word line, got nil")
	}
}

// --- JoinLines / JoinWordsIntoLine Connect handlers ---

func TestJoinLines(t *testing.T) {
	h := &Handler{}
	a := lineAnno("line-1", testCanvas, "Hello", 10, 20, 90, 25)
	b := lineAnno("line-2", testCanvas, "World", 110, 20, 90, 25)

	req := connect.NewRequest(&scribev1.JoinAnnotationsRequest{
		AnnotationJsons: []string{a, b},
	})
	resp, err := h.JoinLines(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	merged := resp.Msg.GetAnnotationJson()
	_, text, x1, y1, x2, y2, canvas, err := parseLineAnnotation(merged)
	if err != nil {
		t.Fatalf("parse merged: %v", err)
	}
	if text != "Hello World" {
		t.Errorf("merged text: got %q, want %q", text, "Hello World")
	}
	// Union bbox: x1=10, y1=20, x2=200, y2=45
	if x1 != 10 || y1 != 20 || x2 != 200 || y2 != 45 {
		t.Errorf("union bbox: got (%d,%d,%d,%d), want (10,20,200,45)", x1, y1, x2, y2)
	}
	if canvas != testCanvas {
		t.Errorf("canvas: got %q, want %q", canvas, testCanvas)
	}
}

func TestJoinWordsIntoLine(t *testing.T) {
	h := &Handler{}
	a := lineAnno("word-1", testCanvas, "foo", 0, 0, 50, 20)
	b := lineAnno("word-2", testCanvas, "bar", 60, 0, 50, 20)

	req := connect.NewRequest(&scribev1.JoinAnnotationsRequest{
		AnnotationJsons: []string{a, b},
	})
	resp, err := h.JoinWordsIntoLine(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, text, x1, _, x2, _, _, err := parseLineAnnotation(resp.Msg.GetAnnotationJson())
	if err != nil {
		t.Fatalf("parse merged: %v", err)
	}
	if text != "foo bar" {
		t.Errorf("merged text: got %q, want %q", text, "foo bar")
	}
	if x1 != 0 || x2 != 110 {
		t.Errorf("union x span: got (%d,%d), want (0,110)", x1, x2)
	}
}

func TestJoinLines_TooFewAnnotations(t *testing.T) {
	h := &Handler{}
	req := connect.NewRequest(&scribev1.JoinAnnotationsRequest{
		AnnotationJsons: []string{lineAnno("line-1", testCanvas, "Hello", 0, 0, 100, 20)},
	})
	_, err := h.JoinLines(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for single annotation, got nil")
	}
}

// --- Crosswalk Connect handlers ---

func TestCrosswalkConnectHandlers(t *testing.T) {
	h := &Handler{}
	pageJSON := `{
		"type": "AnnotationPage",
		"items": [{
			"id": "line-1",
			"type": "Annotation",
			"textGranularity": "line",
			"motivation": "supplementing",
			"body": [{"type":"TextualBody","purpose":"supplementing","value":"Hello World"}],
			"target": {
				"source": {"id":"https://example.org/canvas/1","type":"Canvas"},
				"selector": {"type":"FragmentSelector","value":"xywh=10,20,200,30"}
			}
		}]
	}`

	t.Run("plain text", func(t *testing.T) {
		req := connect.NewRequest(&scribev1.CrosswalkRequest{AnnotationPageJson: pageJSON})
		resp, err := h.CrosswalkToPlainText(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.Msg.GetFormat() != "text/plain" {
			t.Errorf("format: got %q", resp.Msg.GetFormat())
		}
		if strings.TrimSpace(resp.Msg.GetContent()) != "Hello World" {
			t.Errorf("content: got %q", resp.Msg.GetContent())
		}
	})

	t.Run("hOCR contains text", func(t *testing.T) {
		req := connect.NewRequest(&scribev1.CrosswalkRequest{AnnotationPageJson: pageJSON})
		resp, err := h.CrosswalkToHOCR(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.Msg.GetFormat() != "text/vnd.hocr+html" {
			t.Errorf("format: got %q", resp.Msg.GetFormat())
		}
		if !strings.Contains(resp.Msg.GetContent(), "Hello") || !strings.Contains(resp.Msg.GetContent(), "World") {
			t.Errorf("hOCR missing text: %s", resp.Msg.GetContent())
		}
	})

	t.Run("PageXML contains text", func(t *testing.T) {
		req := connect.NewRequest(&scribev1.CrosswalkRequest{AnnotationPageJson: pageJSON})
		resp, err := h.CrosswalkToPageXML(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.Msg.GetFormat() != "application/vnd.prima.page+xml" {
			t.Errorf("format: got %q", resp.Msg.GetFormat())
		}
		if !strings.Contains(resp.Msg.GetContent(), "Hello World") {
			t.Errorf("PageXML missing text: %s", resp.Msg.GetContent())
		}
	})

	t.Run("ALTO XML contains text", func(t *testing.T) {
		req := connect.NewRequest(&scribev1.CrosswalkRequest{AnnotationPageJson: pageJSON})
		resp, err := h.CrosswalkToALTOXML(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.Msg.GetFormat() != "application/alto+xml" {
			t.Errorf("format: got %q", resp.Msg.GetFormat())
		}
		if !strings.Contains(resp.Msg.GetContent(), "Hello") || !strings.Contains(resp.Msg.GetContent(), "World") {
			t.Errorf("ALTO XML missing text: %s", resp.Msg.GetContent())
		}
	})
}

// --- Round-trip: split then join ---

func TestSplitLineIntoWords_ThenJoinBack(t *testing.T) {
	h := &Handler{}
	raw := lineAnno("line-1", testCanvas, "one two three", 0, 0, 300, 30)

	// Split into words.
	splitResp, err := h.SplitLineIntoWords(context.Background(), connect.NewRequest(&scribev1.SplitLineIntoWordsRequest{
		AnnotationJson: raw,
	}))
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	var page map[string]any
	if jsonErr := json.Unmarshal([]byte(splitResp.Msg.GetAnnotationPageJson()), &page); jsonErr != nil {
		t.Fatalf("parse page: %v", jsonErr)
	}
	items, _ := page["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("expected 3 words, got %d", len(items))
	}

	// Join them back.
	wordJSONs := make([]string, len(items))
	for i, item := range items {
		wordJSONs[i] = mustMarshal(t, item)
	}
	joinResp, err := h.JoinWordsIntoLine(context.Background(), connect.NewRequest(&scribev1.JoinAnnotationsRequest{
		AnnotationJsons: wordJSONs,
	}))
	if err != nil {
		t.Fatalf("join: %v", err)
	}

	_, text, x1, y1, x2, y2, _, err := parseLineAnnotation(joinResp.Msg.GetAnnotationJson())
	if err != nil {
		t.Fatalf("parse joined: %v", err)
	}
	if text != "one two three" {
		t.Errorf("joined text: got %q, want %q", text, "one two three")
	}
	// Union bbox should recover the original.
	if x1 != 0 || y1 != 0 || x2 != 300 || y2 != 30 {
		t.Errorf("recovered bbox: got (%d,%d,%d,%d), want (0,0,300,30)", x1, y1, x2, y2)
	}
}

// mustMarshal marshals v to JSON or fails the test.
func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}
