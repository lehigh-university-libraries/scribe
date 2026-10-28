package worddetection

import (
	"context"
	"image"

	"github.com/lehigh-university-libraries/htr/pkg/ocrpage"
	"github.com/lehigh-university-libraries/htr/pkg/remoteocr"
	"github.com/lehigh-university-libraries/htr/pkg/segment/kraken"
)

// RecognizeKraken delegates native cuts, baselines and reading order to htr.
func RecognizeKraken(ctx context.Context, path, segmentationModel, recognitionModel string) ([]remoteocr.Line, error) {
	result, err := kraken.RecognizeFile(ctx, path, kraken.NativeOptions{SegmentationModel: segmentationModel, RecognitionModel: recognitionModel, TemplatePath: "/app/kraken-ocr.json.jinja"})
	if err != nil {
		return nil, err
	}
	return legacyOCRLines(result.Page), nil
}
func parseKrakenOCR(data []byte) ([]remoteocr.Line, error) {
	page, err := kraken.ParseOCR(data, image.Point{})
	if err != nil {
		return nil, err
	}
	return legacyOCRLines(page), nil
}
func legacyOCRLines(page ocrpage.Page) []remoteocr.Line {
	lines := make([]remoteocr.Line, 0, len(page.Lines()))
	for _, line := range page.Lines() {
		out := remoteocr.Line{Box: remoteocr.Box{X: line.Box.X1, Y: line.Box.Y1, Width: line.Box.X2 - line.Box.X1, Height: line.Box.Y2 - line.Box.Y1, Text: line.Text, Confidence: line.Confidence}, Words: []remoteocr.Box{}}
		for _, word := range line.Words {
			out.Words = append(out.Words, remoteocr.Box{X: word.Box.X1, Y: word.Box.Y1, Width: word.Box.X2 - word.Box.X1, Height: word.Box.Y2 - word.Box.Y1, Text: word.Text, Confidence: word.Confidence})
		}
		lines = append(lines, out)
	}
	return lines
}
