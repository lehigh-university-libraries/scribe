package worddetection

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"unicode"

	"github.com/lehigh-university-libraries/htr/pkg/remoteocr"
	"github.com/lehigh-university-libraries/scribe/internal/safefile"
)

// RecognizeKraken runs native baseline segmentation and recognition together.
func RecognizeKraken(ctx context.Context, imagePath, segmentationModel, recognitionModel string) ([]remoteocr.Line, error) {
	output, err := os.CreateTemp("", "kraken-ocr-*.json")
	if err != nil {
		return nil, errors.New("kraken OCR unavailable")
	}
	path := output.Name()
	defer os.Remove(path)
	if err = output.Close(); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "kraken", "--threads", "1", "--no-subline-segmentation", "-t", "/app/kraken-ocr.json.jinja", "-i", imagePath, path, "segment", "-bl", "-i", segmentationModel, "ocr", "-m", recognitionModel) // #nosec G204 -- executable and model paths are server-owned; no shell.
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err = cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("kraken OCR failed")
	}
	data, err := safefile.ReadFileLimit(path, maxKrakenSegmentationBytes)
	if err != nil {
		return nil, errors.New("kraken OCR output invalid")
	}
	return parseKrakenOCR(data)
}

type krakenOCREntity struct {
	ID          string            `json:"id"`
	Type        string            `json:"type"`
	Text        string            `json:"text"`
	Boundary    [][]int           `json:"boundary"`
	Baseline    [][]int           `json:"baseline"`
	Cuts        [][][]float64     `json:"cuts"`
	Confidences []float64         `json:"confidences"`
	Lines       []krakenOCREntity `json:"lines"`
}

func parseKrakenOCR(data []byte) ([]remoteocr.Line, error) {
	var page struct {
		Entities []krakenOCREntity `json:"entities"`
		Orders   [][]string        `json:"line_orders"`
	}
	if err := json.Unmarshal(data, &page); err != nil {
		return nil, errors.New("invalid kraken OCR JSON")
	}
	entities := []krakenOCREntity{}
	for _, e := range page.Entities {
		if e.Type == "line" {
			entities = append(entities, e)
		} else if e.Type == "region" {
			entities = append(entities, e.Lines...)
		}
	}
	if len(page.Orders) > 0 && len(page.Orders[0]) > 0 {
		byID := make(map[string]krakenOCREntity, len(entities))
		for _, e := range entities {
			byID[e.ID] = e
		}
		ordered := make([]krakenOCREntity, 0, len(entities))
		seen := map[string]bool{}
		for _, id := range page.Orders[0] {
			e, ok := byID[id]
			if !ok || seen[id] {
				return nil, errors.New("invalid kraken reading order")
			}
			ordered = append(ordered, e)
			seen[id] = true
		}
		for _, e := range entities {
			if !seen[e.ID] {
				ordered = append(ordered, e)
			}
		}
		entities = ordered
	}
	lines := make([]remoteocr.Line, 0, len(entities))
	for _, e := range entities {
		box, ok := boundingBoxFromPolygon(e.Boundary)
		if !ok {
			return nil, errors.New("invalid kraken line boundary")
		}
		chars := []rune(e.Text)
		if len(chars) != len(e.Cuts) || len(chars) != len(e.Confidences) {
			return nil, errors.New("invalid kraken character cuts")
		}
		line := remoteocr.Line{Box: remoteocr.Box{X: box.X, Y: box.Y, Width: box.Width, Height: box.Height, Text: e.Text}, Words: []remoteocr.Box{}}
		for _, c := range e.Confidences {
			if c < 0 || c > 1 {
				return nil, errors.New("invalid kraken confidence")
			}
			line.Confidence += c
		}
		if len(chars) > 0 {
			line.Confidence /= float64(len(chars))
		}
		for start := 0; start < len(chars); {
			if unicode.IsSpace(chars[start]) {
				start++
				continue
			}
			end := start + 1
			for end < len(chars) && !unicode.IsSpace(chars[end]) {
				end++
			}
			points := [][]int{}
			confidence := 0.0
			for i := start; i < end; i++ {
				for _, point := range e.Cuts[i] {
					if len(point) != 2 || math.IsNaN(point[0]) || math.IsNaN(point[1]) || point[0] < 0 || point[1] < 0 || point[0] > 1e8 || point[1] > 1e8 {
						return nil, errors.New("invalid character cut")
					}
					points = append(points, []int{int(math.Floor(point[0])), int(math.Floor(point[1]))}, []int{int(math.Ceil(point[0])), int(math.Ceil(point[1]))})
				}
				confidence += e.Confidences[i]
			}
			word, ok := boundingBoxFromPolygon(points)
			if !ok {
				return nil, errors.New("invalid kraken word cut")
			}
			// Native cuts can cross the polygon's integer envelope by a pixel. Clamp
			// to the owning line before exposing the remote geometry contract.
			x1, y1 := max(box.X, word.X), max(box.Y, word.Y)
			x2, y2 := min(box.X+box.Width, word.X+word.Width), min(box.Y+box.Height, word.Y+word.Height)
			if x2 <= x1 || y2 <= y1 {
				return nil, errors.New("kraken word lies outside line")
			}
			line.Words = append(line.Words, remoteocr.Box{X: x1, Y: y1, Width: x2 - x1, Height: y2 - y1, Text: string(chars[start:end]), Confidence: confidence / float64(end-start)})
			start = end
		}
		lines = append(lines, line)
	}
	return lines, nil
}
