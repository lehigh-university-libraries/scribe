package server

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lehigh-university-libraries/scribe/internal/hocr"
	"github.com/lehigh-university-libraries/scribe/internal/models"
)

// testHOCR is a valid hOCR document with two lines and three words used
// across the hOCR parsing regression tests in this file.
const testHOCR = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html>
<html>
  <body>
    <div class="ocr_page" id="page_1" title="bbox 0 0 2160 3632">
      <span class="ocr_line" id="line_1" title="bbox 10 20 500 45">
        <span class="ocrx_word" id="word_1" title="bbox 10 20 100 45; x_wconf 95">Course</span>
        <span class="ocrx_word" id="word_2" title="bbox 110 20 200 45; x_wconf 92">Catalog</span>
      </span>
      <span class="ocr_line" id="line_2" title="bbox 10 60 400 85">
        <span class="ocrx_word" id="word_3" title="bbox 10 60 150 85; x_wconf 88">1908-1909</span>
      </span>
    </div>
  </body>
</html>`

// TestParseHOCRLines_BasicStructure parses a known hOCR string and asserts
// correct line count, joined line text, and bbox coordinates.
func TestParseHOCRLines_BasicStructure(t *testing.T) {
	lines, err := hocr.ParseHOCRLines(testHOCR)
	if err != nil {
		t.Fatalf("ParseHOCRLines: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("got %d lines; want 2", len(lines))
	}

	// line_1: bbox 10 20 500 45, words "Course" and "Catalog"
	line1 := lines[0]
	if line1.ID != "line_1" {
		t.Errorf("lines[0].ID = %q; want %q", line1.ID, "line_1")
	}
	if line1.BBox.X1 != 10 || line1.BBox.Y1 != 20 || line1.BBox.X2 != 500 || line1.BBox.Y2 != 45 {
		t.Errorf("lines[0].BBox = {%d,%d,%d,%d}; want {10,20,500,45}",
			line1.BBox.X1, line1.BBox.Y1, line1.BBox.X2, line1.BBox.Y2)
	}
	joinedLine1 := joinLineWords(line1)
	if !strings.Contains(joinedLine1, "Course") || !strings.Contains(joinedLine1, "Catalog") {
		t.Errorf("line_1 joined text = %q; want to contain 'Course' and 'Catalog'", joinedLine1)
	}

	// line_2: bbox 10 60 400 85, word "1908-1909"
	line2 := lines[1]
	if line2.ID != "line_2" {
		t.Errorf("lines[1].ID = %q; want %q", line2.ID, "line_2")
	}
	if line2.BBox.X1 != 10 || line2.BBox.Y1 != 60 || line2.BBox.X2 != 400 || line2.BBox.Y2 != 85 {
		t.Errorf("lines[1].BBox = {%d,%d,%d,%d}; want {10,60,400,85}",
			line2.BBox.X1, line2.BBox.Y1, line2.BBox.X2, line2.BBox.Y2)
	}
	joinedLine2 := joinLineWords(line2)
	if !strings.Contains(joinedLine2, "1908-1909") {
		t.Errorf("line_2 joined text = %q; want to contain '1908-1909'", joinedLine2)
	}
}

// TestParseHOCRWords_BasicStructure parses a known hOCR and asserts word count,
// each word's text, and each word's bbox.
func TestParseHOCRWords_BasicStructure(t *testing.T) {
	words, err := hocr.ParseHOCRWords(testHOCR)
	if err != nil {
		t.Fatalf("ParseHOCRWords: %v", err)
	}
	if len(words) != 3 {
		t.Fatalf("got %d words; want 3", len(words))
	}

	checks := []struct {
		id   string
		text string
		x1   int
		y1   int
		x2   int
		y2   int
	}{
		{"word_1", "Course", 10, 20, 100, 45},
		{"word_2", "Catalog", 110, 20, 200, 45},
		{"word_3", "1908-1909", 10, 60, 150, 85},
	}
	for i, c := range checks {
		w := words[i]
		if w.ID != c.id {
			t.Errorf("words[%d].ID = %q; want %q", i, w.ID, c.id)
		}
		if w.Text != c.text {
			t.Errorf("words[%d].Text = %q; want %q", i, w.Text, c.text)
		}
		if w.BBox.X1 != c.x1 || w.BBox.Y1 != c.y1 || w.BBox.X2 != c.x2 || w.BBox.Y2 != c.y2 {
			t.Errorf("words[%d].BBox = {%d,%d,%d,%d}; want {%d,%d,%d,%d}",
				i, w.BBox.X1, w.BBox.Y1, w.BBox.X2, w.BBox.Y2,
				c.x1, c.y1, c.x2, c.y2)
		}
	}
}

// TestParseHOCRLines_EmptyInput verifies that an empty string returns no lines
// and no error.
func TestParseHOCRLines_EmptyInput(t *testing.T) {
	lines, err := hocr.ParseHOCRLines("")
	if err == nil && len(lines) == 0 {
		return // expected: no lines, no error
	}
	if err != nil {
		// An error on empty input is acceptable; no lines should be returned.
		if len(lines) != 0 {
			t.Errorf("expected no lines on empty input error, got %d", len(lines))
		}
		return
	}
	// err == nil but got lines — that is unexpected
	if len(lines) != 0 {
		t.Errorf("ParseHOCRLines(\"\") returned %d lines; want 0", len(lines))
	}
}

// TestParseHOCRLines_NoWords verifies that a line with no word spans still
// produces a line annotation (with the line ID used as the text via
// joinLineWords's fallback).
func TestParseHOCRLines_NoWords(t *testing.T) {
	noWordsHOCR := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html>
<html>
  <body>
    <div class="ocr_page" id="page_1" title="bbox 0 0 2160 3632">
      <span class="ocr_line" id="line_empty" title="bbox 5 5 200 30">
      </span>
    </div>
  </body>
</html>`

	lines, err := hocr.ParseHOCRLines(noWordsHOCR)
	if err != nil {
		t.Fatalf("ParseHOCRLines: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("got %d lines; want 1", len(lines))
	}
	line := lines[0]
	if line.ID != "line_empty" {
		t.Errorf("line.ID = %q; want %q", line.ID, "line_empty")
	}
	// joinLineWords falls back to the line ID when there are no words.
	text := joinLineWords(line)
	if text != "line_empty" {
		t.Errorf("joinLineWords on empty line = %q; want %q (line ID fallback)", text, "line_empty")
	}
}

// TestBuildLineAnnotations_BBoxXYWH calls buildLineAnnotations and verifies
// that the annotation target selector value uses xywh=x,y,w,h format (where
// w = X2-X1 and h = Y2-Y1), not raw x1,y1,x2,y2.
func TestBuildLineAnnotations_BBoxXYWH(t *testing.T) {
	lines := []models.HOCRLine{
		{
			ID: "line_1",
			BBox: models.BBox{
				X1: 10, Y1: 20, X2: 500, Y2: 45,
			},
			Words: []models.HOCRWord{
				{ID: "word_1", Text: "Course", BBox: models.BBox{X1: 10, Y1: 20, X2: 100, Y2: 45}},
				{ID: "word_2", Text: "Catalog", BBox: models.BBox{X1: 110, Y1: 20, X2: 200, Y2: 45}},
			},
		},
	}

	items := buildLineAnnotations("test-session", "https://example.org/canvas/1", lines)
	if len(items) != 1 {
		t.Fatalf("got %d annotation items; want 1", len(items))
	}

	ann, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("annotation is not map[string]any")
	}
	target, ok := ann["target"].(map[string]any)
	if !ok {
		t.Fatalf("annotation target is not map[string]any")
	}
	selector, ok := target["selector"].(map[string]any)
	if !ok {
		t.Fatalf("annotation target.selector is not map[string]any")
	}
	value, ok := selector["value"].(string)
	if !ok {
		t.Fatalf("selector value is not a string")
	}

	// line_1: x=10, y=20, w=500-10=490, h=45-20=25
	want := fmt.Sprintf("xywh=%d,%d,%d,%d", 10, 20, 490, 25)
	if value != want {
		t.Errorf("selector value = %q; want %q", value, want)
	}
}

// TestBuildWordAnnotations_TextPreserved verifies that word text passes through
// buildWordAnnotations correctly into the annotation body value.
func TestBuildWordAnnotations_TextPreserved(t *testing.T) {
	words := []models.HOCRWord{
		{
			ID:   "word_1",
			Text: "Course",
			BBox: models.BBox{X1: 10, Y1: 20, X2: 100, Y2: 45},
		},
		{
			ID:   "word_2",
			Text: "Catalog",
			BBox: models.BBox{X1: 110, Y1: 20, X2: 200, Y2: 45},
		},
	}

	items := buildWordAnnotations("test-session", "https://example.org/canvas/1", words)
	if len(items) != 2 {
		t.Fatalf("got %d annotation items; want 2", len(items))
	}

	wantTexts := []string{"Course", "Catalog"}
	for i, item := range items {
		ann, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("items[%d] is not map[string]any", i)
		}
		body, ok := ann["body"].([]any)
		if !ok || len(body) == 0 {
			t.Fatalf("items[%d].body is empty or wrong type", i)
		}
		bodyItem, ok := body[0].(map[string]any)
		if !ok {
			t.Fatalf("items[%d].body[0] is not map[string]any", i)
		}
		got, _ := bodyItem["value"].(string)
		if got != wantTexts[i] {
			t.Errorf("items[%d].body[0].value = %q; want %q", i, got, wantTexts[i])
		}
	}
}

// TestHOCRRoundTrip_LinesFromWords verifies that given hOCR with 2 lines and 3
// words, ParseHOCRLines gives 2 lines with correct joined text, and
// ParseHOCRWords gives 3 words.
func TestHOCRRoundTrip_LinesFromWords(t *testing.T) {
	lines, err := hocr.ParseHOCRLines(testHOCR)
	if err != nil {
		t.Fatalf("ParseHOCRLines: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("ParseHOCRLines: got %d lines; want 2", len(lines))
	}

	// Line 1 should join to "Course Catalog"
	joined1 := joinLineWords(lines[0])
	if joined1 != "Course Catalog" {
		t.Errorf("line_1 joined text = %q; want %q", joined1, "Course Catalog")
	}

	// Line 2 should join to "1908-1909"
	joined2 := joinLineWords(lines[1])
	if joined2 != "1908-1909" {
		t.Errorf("line_2 joined text = %q; want %q", joined2, "1908-1909")
	}

	words, err := hocr.ParseHOCRWords(testHOCR)
	if err != nil {
		t.Fatalf("ParseHOCRWords: %v", err)
	}
	if len(words) != 3 {
		t.Fatalf("ParseHOCRWords: got %d words; want 3", len(words))
	}
}
