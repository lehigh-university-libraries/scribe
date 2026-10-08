package worddetection

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"

	"github.com/lehigh-university-libraries/scribe/internal/safefile"
)

// NewspaperProvider uses PP-DocLayoutV3 reading order to order Kraken BLLA lines.
// Both models and the executable are installed by the reviewed segmentor image.
type NewspaperProvider struct{ lines *KrakenProvider }

func NewNewspaper(model string) *NewspaperProvider {
	return &NewspaperProvider{lines: NewKraken(model)}
}
func (*NewspaperProvider) Name() string { return "newspapers" }

func (p *NewspaperProvider) DetectWords(ctx context.Context, imagePath string) ([]WordBox, error) {
	dir, err := os.MkdirTemp("", "scribe-layout-*")
	if err != nil {
		return nil, errors.New("newspaper layout unavailable")
	}
	defer os.RemoveAll(dir)
	cmd := exec.CommandContext(ctx, "paddleocr", "layout_detection", "-i", imagePath, "--model_name", "PP-DocLayoutV3", "--model_dir", "/models/pp-doclayout-v3", "--device", "cpu", "--save_path", dir) // #nosec G204 -- fixed executable and model; image is a bounded local upload, no shell.
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("newspaper layout failed")
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(files) != 1 {
		return nil, errors.New("newspaper layout output invalid")
	}
	data, err := safefile.ReadFileLimit(files[0], maxKrakenSegmentationBytes)
	if err != nil {
		return nil, errors.New("newspaper layout output invalid")
	}
	regions, err := parseLayoutRegions(data)
	if err != nil {
		return nil, errors.New("newspaper layout output invalid")
	}
	lines, err := p.lines.DetectWords(ctx, imagePath)
	if err != nil {
		return nil, err
	}
	return orderNewspaperLines(lines, regions), nil
}

type layoutRegion struct {
	Label      string    `json:"label"`
	Coordinate []float64 `json:"coordinate"`
	Order      *int      `json:"order"`
}

func parseLayoutRegions(data []byte) ([]layoutRegion, error) {
	var result struct {
		Boxes []layoutRegion `json:"boxes"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	if result.Boxes == nil {
		return nil, errors.New("missing layout boxes")
	}
	if len(result.Boxes) > 4096 {
		return nil, errors.New("too many layout regions")
	}
	regions := make([]layoutRegion, 0, len(result.Boxes))
	for _, region := range result.Boxes {
		switch region.Label {
		case "image", "header_image", "footer_image", "seal", "chart", "table", "figure_title", "vision_footnote", "header", "footer", "footnote", "aside_text":
			// Paddle deliberately leaves auxiliary regions unranked. Their
			// BLLA lines remain in the unassigned group rather than being lost.
			continue
		}
		if len(region.Coordinate) != 4 || region.Order == nil || *region.Order < 0 {
			return nil, errors.New("invalid layout region")
		}
		for _, coordinate := range region.Coordinate {
			if math.IsNaN(coordinate) || math.IsInf(coordinate, 0) || coordinate < 0 || coordinate > 100000 {
				return nil, errors.New("invalid layout coordinate")
			}
		}
		if region.Coordinate[2] <= region.Coordinate[0] || region.Coordinate[3] <= region.Coordinate[1] {
			return nil, errors.New("invalid layout extent")
		}
		regions = append(regions, region)
	}
	sort.SliceStable(regions, func(i, j int) bool { return *regions[i].Order < *regions[j].Order })
	return regions, nil
}

// Keep every BLLA line exactly once, with its original crop. Layout regions
// determine column order; lines within a region retain Kraken's native order.
// Lines not covered by layout detection follow those regions in native order.
func orderNewspaperLines(lines []WordBox, regions []layoutRegion) []WordBox {
	groups := make([][]WordBox, len(regions)+1)
	for _, line := range lines {
		best, bestArea := len(regions), float64(0)
		for index, region := range regions {
			c := region.Coordinate
			width := min(float64(line.X+line.Width), c[2]) - max(float64(line.X), c[0])
			height := min(float64(line.Y+line.Height), c[3]) - max(float64(line.Y), c[1])
			if width > 0 && height > 0 && width*height > bestArea {
				best, bestArea = index, width*height
			}
		}
		groups[best] = append(groups[best], line)
	}
	ordered := make([]WordBox, 0, len(lines))
	for _, group := range groups {
		ordered = append(ordered, group...)
	}
	return ordered
}
