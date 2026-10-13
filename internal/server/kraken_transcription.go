package server

import (
	"context"
	"fmt"
	"time"

	"github.com/lehigh-university-libraries/htr/pkg/remoteocr"
	"github.com/lehigh-university-libraries/scribe/internal/models"
	"github.com/lehigh-university-libraries/scribe/internal/providerregistry"
	"github.com/lehigh-university-libraries/scribe/internal/segmentor"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

func (h *Handler) recognizeKrakenJobPage(ctx context.Context, job *store.TranscriptionJob, pctx store.Context, img store.ItemImage, registry providerregistry.Registry) ([]any, error) {
	descriptor, ok := registry.Provider("kraken")
	if !ok {
		return nil, permanentTranscriptionFailure("Kraken provider unavailable")
	}
	endpoint := descriptor.EndpointForModel(pctx.TranscriptionModel)
	client, err := segmentor.NewClientForEndpoint(endpoint.URL, endpoint.Audience)
	if err != nil {
		return nil, err
	}
	path, local := localUploadPathFromImageURL(img.ImageURL)
	cleanup := func() {}
	if h.imageRegionFetcher != nil {
		path, cleanup, err = h.imageRegionFetcher(ctx, img.ImageURL, 0, 0, int(img.Width), int(img.Height))
	} else if !local {
		path, cleanup, err = fetchImageURLToTemp(ctx, img.ImageURL, "scribe-native-ocr-*")
	}
	if err != nil {
		return nil, err
	}
	defer cleanup()
	started := time.Now()
	result, err := client.OCR(ctx, path, pctx.TranscriptionModel)
	if h.providerCallAudits != nil {
		record := store.ProviderCallAudit{WorkspaceID: job.WorkspaceID, ItemImageID: &img.ID, ContextID: &pctx.ID, Provider: "kraken", Model: pctx.TranscriptionModel, Operation: "page_ocr", DurationMS: time.Since(started).Milliseconds()}
		if err != nil {
			record.ErrorMessage = "full-page OCR failed"
		}
		_ = h.providerCallAudits.Create(ctx, record)
	}
	if err != nil {
		return nil, fmt.Errorf("full-page Kraken OCR: %w", err)
	}
	if result.Provider != "kraken" || result.Model != pctx.TranscriptionModel || len(result.Lines) > transcriptionJobSegmentLimit() {
		return nil, permanentTranscriptionFailure("invalid Kraken OCR response")
	}
	return krakenAnnotations(fmt.Sprintf("transcription-job-%d-recognition", job.ID), img.CanvasURI, result.Lines), nil
}

func krakenAnnotations(scope, canvas string, lines []remoteocr.Line) []any {
	items := make([]any, 0, len(lines))
	for i, line := range lines {
		lineID := annotationID(scope, "line", fmt.Sprintf("line-%d", i+1))
		box := func(b remoteocr.Box) models.BBox {
			return models.BBox{X1: b.X, Y1: b.Y, X2: b.X + b.Width, Y2: b.Y + b.Height}
		}
		items = append(items, transcriptionAnnotation(lineID, "line", line.Text, canvas, box(line.Box)))
		for j, word := range line.Words {
			annotation := transcriptionAnnotation(annotationID(scope, "word", fmt.Sprintf("line-%d-word-%d", i+1, j+1)), "word", word.Text, canvas, box(word))
			annotation["partOf"] = map[string]any{"id": lineID, "type": "Annotation"}
			items = append(items, annotation)
		}
	}
	return items
}
