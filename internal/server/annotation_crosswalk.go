package server

import (
	"fmt"
	"log/slog"

	"github.com/lehigh-university-libraries/htr/pkg/ocrpage"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/models"
	"github.com/lehigh-university-libraries/scribe/internal/ocradapter"
)

func annotationPageToHOCRLines(pageJSON string) ([]models.HOCRLine, int, int, error) {
	return annotationPageToHOCRLinesWithDimensions(pageJSON, 0, 0)
}

// readExportPage keeps text-less pages (covers, blank leaves, boxes awaiting
// transcription) as empty pages so item exports such as PDF still include them.
func readExportPage(pageJSON string, width, height int) (ocrpage.Page, error) {
	page, _, err := readAnnotationPage(pageJSON, width, height)
	return page, err
}

func readAnnotationPage(pageJSON string, width, height int) (ocrpage.Page, int, error) {
	var raw map[string]any
	if err := iiif.DecodeJSON([]byte(pageJSON), &raw); err != nil {
		return ocrpage.Page{}, 0, fmt.Errorf("invalid annotation page json")
	}
	result, err := ocrpage.ReadIIIF([]byte(pageJSON), ocrpage.IIIFReadOptions{CanvasWidth: width, CanvasHeight: height, InferWords: true, SkipEmptyText: true, Limits: ocrpage.Limits{MaxPixelCoordinate: iiif.MaxPixelCoordinate}})
	if err != nil {
		return ocrpage.Page{}, 0, err
	}
	items, _ := raw["items"].([]any)
	for _, skipped := range result.Skipped {
		if skipped.Index >= 0 && skipped.Index < len(items) {
			if annotation, ok := items[skipped.Index].(map[string]any); ok {
				slog.Info("Crosswalk skipping annotation", "reason", skipped.Reason, "annotation", annotationDebugSummary(annotation))
			}
		}
	}
	if len(items) > 0 && len(result.Page.Lines()) == 0 && !onlyTextless(result, len(items)) {
		return ocrpage.Page{}, 0, fmt.Errorf("annotation page has no parseable textual annotations")
	}
	result.Page.Width = max(1, result.Page.Width)
	result.Page.Height = max(1, result.Page.Height)
	// Empty canonical pages retain the same implicit export wrapper as box-only pages.
	if len(result.Page.Regions) == 0 {
		result.Page = ocrpage.FromLines(result.Page.Width, result.Page.Height, nil)
	}
	return result.Page, len(items), nil
}

// onlyTextless reports whether every annotation was well formed but awaiting
// text, as opposed to ignored or structurally invalid.
func onlyTextless(result ocrpage.IIIFReadResult, items int) bool {
	if result.Ignored > 0 || len(result.Skipped) != items {
		return false
	}
	for _, skipped := range result.Skipped {
		if skipped.Reason != "missing text" {
			return false
		}
	}
	return true
}

func annotationPageToHOCRLinesWithDimensions(pageJSON string, width, height int) ([]models.HOCRLine, int, int, error) {
	p, items, err := readAnnotationPage(pageJSON, width, height)
	if err != nil {
		return nil, 0, 0, err
	}
	if items > 0 && len(p.Lines()) == 0 {
		return nil, 0, 0, fmt.Errorf("annotation page has no parseable textual annotations")
	}
	return ocradapter.ToLines(p), p.Width, p.Height, nil
}
func linesToPlainText(lines []models.HOCRLine) string {
	return ocradapter.FromLines(lines, 0, 0).PlainText()
}
func minInt(a, b int) int { return min(a, b) }
