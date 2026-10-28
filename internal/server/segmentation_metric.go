package server

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/lehigh-university-libraries/htr/pkg/ocrpage"
	"github.com/lehigh-university-libraries/htr/pkg/segeval"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

var segmentationMetricSlots = make(chan struct{}, 2)

func baselinePage(run store.OCRRun, width, height int) (ocrpage.Page, error) {
	if run.OriginalPage != "" {
		return ocrpage.ParseFormat([]byte(run.OriginalPage), ocrpage.FormatHTRJSON, ocrpage.ReadOptions{})
	}
	return ocrpage.ParseFormat([]byte(run.OriginalHOCR), ocrpage.FormatHOCR, ocrpage.ReadOptions{CanvasWidth: width, CanvasHeight: height})
}

func segmentationCorrection(ctx context.Context, run store.OCRRun, corrected []byte, width, height int, name string) string {
	select {
	case segmentationMetricSlots <- struct{}{}:
		defer func() { <-segmentationMetricSlots }()
	case <-ctx.Done():
		return `{"unavailable":"request canceled"}`
	}
	profile, ok := segeval.Lookup(name)
	if !ok {
		return `{"unavailable":"unknown profile"}`
	}
	gt, err := ocrpage.ReadIIIF(corrected, ocrpage.IIIFReadOptions{CanvasWidth: width, CanvasHeight: height})
	if err != nil {
		return `{"unavailable":"invalid corrected geometry"}`
	}
	hyp, err := baselinePage(run, width, height)
	if err != nil {
		return `{"unavailable":"no baseline geometry"}`
	}
	// ponytail: cBAD's all-pairs distances need a smaller synchronous budget.
	// Larger pages remain editable and can be scored offline from GT exports.
	gs, hs := gt.Page.Elements(profile.Level), hyp.Elements(profile.Level)
	limit := 256
	if profile.Level == ocrpage.LevelLine {
		for _, elements := range [][]ocrpage.Element{gs, hs} {
			points := 0
			sampledPoints := int64(0)
			for _, line := range elements {
				points += len(line.Baseline)
				if len(line.Baseline) < 2 {
					continue
				}
				dense := int64(1)
				for i := 1; i < len(line.Baseline); i++ {
					a, b := line.Baseline[i-1], line.Baseline[i]
					dx, dy := max(a.X, b.X)-min(a.X, b.X), max(a.Y, b.Y)-min(a.Y, b.Y)
					dense += int64(max(dx, dy))
				}
				if dense <= 20 {
					sampledPoints += dense
				} else {
					sampledPoints += max(20, (dense-1)/5+1)
				}
			}
			if points > 0 {
				limit = 32
			}
			if points > 2048 {
				return `{"unavailable":"offline scoring required above 2048 baseline points"}`
			}
			if sampledPoints > 4096 {
				return `{"unavailable":"offline scoring required above 4096 sampled baseline points"}`
			}
		}
	}
	if len(gs) > limit || len(hs) > limit {
		return fmt.Sprintf(`{"unavailable":"offline scoring required above %d elements"}`, limit)
	}
	report, err := segeval.Evaluate(gt.Page, hyp, segeval.Options{Profile: profile})
	if err != nil {
		return `{"unavailable":"incompatible geometry levels"}`
	}
	data, err := json.Marshal(report)
	if err != nil {
		return `{"unavailable":"invalid metric result"}`
	}
	return string(data)
}
