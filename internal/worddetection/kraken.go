package worddetection

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
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
	outputPath := filepath.Join(os.TempDir(),
		fmt.Sprintf("kraken_seg_%d.json", time.Now().UnixNano()))
	defer os.Remove(outputPath)

	// kraken -i <input> <output> segment -m <model>
	// The JSON output format is requested with the .json extension.
	args := []string{
		"-i", imagePath, outputPath,
		"segment",
		"-m", p.modelID,
	}
	cmd := exec.CommandContext(ctx, "kraken", args...)
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
	data, err := os.ReadFile(path)
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
			box, ok = boundingBoxFromPolygon(line.Baseline)
			if !ok {
				continue
			}
			// Inflate the baseline box vertically to approximate line height.
			h := box.Height
			if h < 20 {
				h = 20
			}
			box.Y -= h / 2
			box.Height = h
		}
		boxes = append(boxes, box)
	}
	return boxes, nil
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
