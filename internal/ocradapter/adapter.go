// Package ocradapter translates Scribe's legacy hOCR view to the shared htr page.
package ocradapter

import (
	"strings"

	"github.com/lehigh-university-libraries/htr/pkg/ocrpage"
	"github.com/lehigh-university-libraries/scribe/internal/models"
)

// FromLines wraps a box-only Scribe export in one implicit region.
func FromLines(lines []models.HOCRLine, width, height int) ocrpage.Page {
	out := make([]ocrpage.Line, len(lines))
	for i, line := range lines {
		out[i] = ocrpage.Line{ID: line.ID, Box: ocrpage.Rect(line.BBox)}
		for _, word := range line.Words {
			out[i].Words = append(out[i].Words, ocrpage.Word{ID: word.ID, Text: strings.TrimSpace(word.Text), Box: ocrpage.Rect(word.BBox), Confidence: word.Confidence / 100})
		}
	}
	return ocrpage.FromLines(width, height, out)
}

// ToLines flattens the shared page into the legacy box-only derived view.
func ToLines(page ocrpage.Page) []models.HOCRLine {
	var lines []models.HOCRLine
	for _, line := range page.Lines() {
		out := models.HOCRLine{ID: line.ID, BBox: models.BBox(line.Box)}
		for _, word := range line.Words {
			out.Words = append(out.Words, models.HOCRWord{ID: word.ID, LineID: line.ID, Text: word.Text, BBox: models.BBox(word.Box), Confidence: word.Confidence * 100})
		}
		lines = append(lines, out)
	}
	return lines
}
