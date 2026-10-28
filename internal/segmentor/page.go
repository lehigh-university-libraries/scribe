package segmentor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/lehigh-university-libraries/htr/pkg/ocrpage"
	"github.com/lehigh-university-libraries/htr/pkg/segeval"
	"github.com/lehigh-university-libraries/htr/pkg/segment"
	"github.com/lehigh-university-libraries/htr/pkg/segment/kraken"
	"github.com/lehigh-university-libraries/htr/pkg/segment/layoutkraken"
	"github.com/lehigh-university-libraries/htr/pkg/segment/ppdoclayout"
	ocrcatalog "github.com/lehigh-university-libraries/scribe/config"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
)

// pageSegmenter resolves public model keys to server-owned local model paths.
func pageSegmenter(model string) (segment.Segmenter, string, error) {
	modelDir := os.Getenv("PADDLE_LAYOUT_MODEL_DIR")
	if modelDir == "" {
		modelDir = ocrcatalog.LayoutModelDir()
	}
	layout := ppdoclayout.New(ppdoclayout.Options{ModelDir: modelDir})
	if model == "ppdoclayout" {
		return layout, "PP-DocLayoutV3", nil
	}
	route, err := configuredKrakenModelRoute("KRAKEN_SEGMENTATION_MODEL_ID", "KRAKEN_SEGMENTATION_MODEL")
	if err != nil {
		return nil, "", err
	}
	if model == "newspapers" || model == "layout+kraken" {
		return &layoutkraken.Segmenter{Layout: layout, Lines: kraken.New(route.path)}, route.path, nil
	}
	if model != route.id {
		return nil, "", fmt.Errorf("unsupported segmentation model")
	}
	return kraken.New(route.path), route.path, nil
}
func handleCapabilities(w http.ResponseWriter, r *http.Request) {
	model := r.URL.Query().Get("model")
	if model == "" {
		model = "kraken"
	}
	provider, native, err := pageSegmenter(model)
	if err != nil {
		http.Error(w, "unsupported segmentation model", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(provider.Capabilities(native))
}
func segmentPreparedPage(ctx context.Context, path, model, hintsJSON string) (segment.Result, error) {
	provider, native, err := pageSegmenter(model)
	if err != nil {
		return segment.Result{}, err
	}
	hints := segment.Hints{DocumentType: "generic"}
	if len(hintsJSON) > 4096 {
		return segment.Result{}, fmt.Errorf("segmentation hints exceed limit")
	}
	if hintsJSON != "" {
		if err = json.Unmarshal([]byte(hintsJSON), &hints); err != nil {
			return segment.Result{}, fmt.Errorf("invalid segmentation hints")
		}
	}
	if hints.DocumentType == "" {
		hints.DocumentType = "generic"
	}
	if _, ok := segeval.Lookup(hints.DocumentType); !ok {
		return segment.Result{}, fmt.Errorf("unknown document type")
	}
	if len(hints.Languages) > 32 {
		return segment.Result{}, fmt.Errorf("too many language hints")
	}
	for _, s := range hints.Languages {
		if len(s) > 64 {
			return segment.Result{}, fmt.Errorf("invalid language hint")
		}
	}
	file, ok := provider.(segment.FileSegmenter)
	if !ok {
		return segment.Result{}, fmt.Errorf("local segmentation requires file support")
	}
	result, err := file.SegmentFile(ctx, path, segment.Request{Model: native, Hints: hints})
	if err != nil {
		return result, err
	}
	if err = validateSegmentedPage(result.Page); err != nil {
		return segment.Result{}, err
	}
	// Public provenance must never expose an operator's filesystem model paths.
	result.Page.Provenance = ocrpage.Provenance{Provider: provider.Name(), Model: model}
	result.EffectiveModel = model
	return result, nil
}

func validateSegmentedPage(page ocrpage.Page) error {
	if err := ocrpage.Validate(page, ocrpage.Limits{}); err != nil {
		return err
	}
	count := 0
	for _, level := range []ocrpage.Level{ocrpage.LevelRegion, ocrpage.LevelLine, ocrpage.LevelWord} {
		count += len(page.Elements(level))
	}
	if count > iiif.MaxAnnotationsPerPage {
		return fmt.Errorf("segmentation annotation count exceeds limit")
	}
	return nil
}
