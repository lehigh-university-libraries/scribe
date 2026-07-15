//go:build !remoteocr

package worddetection

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/lehigh-university-libraries/scribe/internal/safefile"
)

// KrakenProvider runs kraken segmentation.
// modelID is the kraken model identifier, e.g. "blla.mlmodel" or a full path.
type KrakenProvider struct {
	modelID string
}

// NewKraken creates a Kraken segmentation provider.
// modelID examples: "blla.mlmodel", "en_best.mlmodel", "/path/to/custom.mlmodel"
func NewKraken(modelID string) *KrakenProvider {
	if modelID == "" {
		modelID = "blla.mlmodel"
	}
	return &KrakenProvider{modelID: modelID}
}

// Name returns the provider name including the model.
func (p *KrakenProvider) Name() string {
	return "kraken:" + p.modelID
}

// DetectWords runs kraken segmentation and returns line bounding boxes as WordBox entries.
// Kraken operates at the line level, so each returned WordBox covers a full text line.
func (p *KrakenProvider) DetectWords(ctx context.Context, imagePath string) ([]WordBox, error) {
	output, err := os.CreateTemp("", "kraken-seg-*.json")
	if err != nil {
		return nil, fmt.Errorf("create kraken output: %w", err)
	}
	outputPath := output.Name()
	if err := output.Close(); err != nil {
		_ = os.Remove(outputPath)
		return nil, fmt.Errorf("close kraken output: %w", err)
	}
	defer os.Remove(outputPath)

	// kraken -i <input> <output> segment -bl -i <model>
	// `-bl` selects baseline (BLLA) segmentation; without it, `-i` is read as
	// the legacy box-segmentation model and `-m` becomes --maxcolseps (int).
	// The JSON output format is requested with the .json extension.
	args := []string{
		"-i", imagePath, outputPath,
		"segment",
		"-bl",
		"-i", p.modelID,
	}
	cmd := exec.CommandContext(ctx, "kraken", args...) // #nosec G204 -- kraken is invoked directly without a shell; arguments are file/model paths.
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("kraken segment failed (model=%s): %w\noutput: %s",
			p.modelID, err, strings.TrimSpace(string(out)))
	}

	return parseKrakenJSON(outputPath)
}

// krakenSegOutput is the structure of kraken's JSON segmentation output.
type krakenSegOutput struct {
	Lines []struct {
		Baseline [][]int `json:"baseline"`
		Boundary [][]int `json:"boundary"`
		Tags     struct {
			Type []string `json:"type"`
		} `json:"tags"`
	} `json:"lines"`
	ImageSize []int `json:"image_size"` // [width, height]
}

func parseKrakenJSON(path string) ([]WordBox, error) {
	data, err := safefile.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read kraken output: %w", err)
	}

	var seg krakenSegOutput
	if err := json.Unmarshal(data, &seg); err != nil {
		return nil, fmt.Errorf("parse kraken json: %w", err)
	}

	boxes := make([]WordBox, 0, len(seg.Lines))
	for _, line := range seg.Lines {
		// Derive bounding box from the polygon boundary points.
		box, ok := boundingBoxFromPolygon(line.Boundary)
		if !ok {
			// Fall back to baseline points if boundary is missing.
			box, ok = boundingBoxFromBaseline(line.Baseline)
			if !ok {
				continue
			}
		}
		boxes = append(boxes, box)
	}
	return boxes, nil
}

func boundingBoxFromBaseline(points [][]int) (WordBox, bool) {
	if len(points) == 0 {
		return WordBox{}, false
	}
	minX, minY := points[0][0], points[0][1]
	maxX, maxY := minX, minY
	for _, pt := range points {
		if len(pt) < 2 {
			continue
		}
		if pt[0] < minX {
			minX = pt[0]
		}
		if pt[1] < minY {
			minY = pt[1]
		}
		if pt[0] > maxX {
			maxX = pt[0]
		}
		if pt[1] > maxY {
			maxY = pt[1]
		}
	}
	w := maxX - minX
	if w <= 0 {
		return WordBox{}, false
	}
	h := maxY - minY
	if h < 20 {
		h = 20
	}
	return WordBox{X: minX, Y: minY - h/2, Width: w, Height: h}, true
}

func boundingBoxFromPolygon(points [][]int) (WordBox, bool) {
	if len(points) == 0 {
		return WordBox{}, false
	}
	minX, minY := points[0][0], points[0][1]
	maxX, maxY := minX, minY
	for _, pt := range points {
		if len(pt) < 2 {
			continue
		}
		if pt[0] < minX {
			minX = pt[0]
		}
		if pt[1] < minY {
			minY = pt[1]
		}
		if pt[0] > maxX {
			maxX = pt[0]
		}
		if pt[1] > maxY {
			maxY = pt[1]
		}
	}
	w := maxX - minX
	h := maxY - minY
	if w <= 0 || h <= 0 {
		return WordBox{}, false
	}
	return WordBox{X: minX, Y: minY, Width: w, Height: h}, true
}
