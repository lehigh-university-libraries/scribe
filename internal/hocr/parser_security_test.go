package hocr

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lehigh-university-libraries/scribe/internal/iiif"
)

func TestBoundedHOCRParserPreservesNestedGlyphTextAndExactClasses(t *testing.T) {
	fixture := `<?xml version="1.0"?><!DOCTYPE html><html><body><div class="ocr_page" title="image &quot;page.jpg&quot;; bbox 0 0 20 10">
<span class="ocr_line extra" id="line-1" title="bbox 0 0 20 10">
  <span class="ocrx_word" id="word-1" title="bbox 0 0 20 10; x_wconf 99">
    <span class="ocrx_cinfo">H</span><span class="ocrx_cinfo">i</span>
  </span>
  <span class="not_ocrx_word" id="impostor" title="bbox 0 0 1 1">ignored</span>
</span></div></body></html>`
	document, err := ParseDocument(fixture)
	if err != nil || document.PageWidth != 20 || document.PageHeight != 10 || PlainText(document.Lines) != "Hi" {
		t.Fatalf("ParseDocument = %#v, %v", document, err)
	}
	words, err := ParseHOCRWords(fixture)
	if err != nil {
		t.Fatalf("ParseHOCRWords: %v", err)
	}
	if len(words) != 1 || words[0].Text != "Hi" || words[0].LineID != "line-1" {
		t.Fatalf("words = %#v", words)
	}
	lines, err := ParseHOCRLines(fixture)
	if err != nil || len(lines) != 1 || len(lines[0].Words) != 1 || lines[0].Words[0].Text != "Hi" {
		t.Fatalf("lines = %#v, %v", lines, err)
	}
	granular, err := ParseHOCRWordGlyphs(fixture)
	if err != nil || len(granular) != 1 || len(granular[0].Glyphs) != 2 {
		t.Fatalf("word glyphs = %#v, %v", granular, err)
	}
}

func TestBoundedHOCRParserRejectsStructuralAndTextAmplification(t *testing.T) {
	tests := map[string]string{
		"oversized bytes": strings.Repeat(" ", maxHOCRBytes+1),
		"deep nesting":    strings.Repeat("<n>", maxHOCRDepth+1) + strings.Repeat("</n>", maxHOCRDepth+1),
		"multiple roots":  "<a></a><b></b>",
		"nested words":    `<root><span class="ocrx_word" id="a"><span class="ocrx_word" id="b">x</span></span></root>`,
		"nested lines":    `<root><span class="ocr_line" id="a"><span class="ocr_line" id="b"></span></span></root>`,
		"long word":       `<root><span class="ocrx_word" id="a">` + strings.Repeat("x", maxHOCRWordTextBytes+1) + `</span></root>`,
	}
	var attributes strings.Builder
	attributes.WriteString("<root")
	for index := 0; index <= maxHOCRAttributesPerElement; index++ {
		fmt.Fprintf(&attributes, " a%d=\"x\"", index)
	}
	attributes.WriteString("></root>")
	tests["many attributes"] = attributes.String()

	for name, fixture := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseHOCRWords(fixture); err == nil {
				t.Fatal("ParseHOCRWords accepted hostile XML")
			}
		})
	}
}

func TestBoundedHOCRParserRejectsAnnotationFanout(t *testing.T) {
	var fixture strings.Builder
	fixture.WriteString(`<html><body><span class="ocr_line" id="line" title="bbox 0 0 10 10">`)
	for index := 0; index <= iiif.MaxAnnotationsPerPage; index++ {
		fmt.Fprintf(&fixture, `<span class="ocrx_word" id="w%d" title="bbox 0 0 1 1">x</span>`, index)
	}
	fixture.WriteString(`</span></body></html>`)
	if _, err := ParseHOCRWords(fixture.String()); err == nil {
		t.Fatal("ParseHOCRWords accepted annotation fan-out beyond the canonical page limit")
	}
	if _, err := ParseHOCRLines(fixture.String()); err == nil {
		t.Fatal("ParseHOCRLines accepted annotation fan-out beyond the canonical page limit")
	}
}
