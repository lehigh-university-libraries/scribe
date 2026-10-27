package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/hocr"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/providerregistry"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
)

// --- AnnotationService Connect handlers ---

func (h *Handler) GetAnnotationPage(ctx context.Context, req *scribev1.GetAnnotationPageRequest) (*scribev1.GetAnnotationPageResponse, error) {
	page, err := h.currentAnnotationPage(ctx, req.GetItemImageId())
	if err != nil {
		return nil, annotationConnectError(err)
	}
	return &scribev1.GetAnnotationPageResponse{
		ItemImageId:        page.ItemImageID,
		CanvasUri:          page.CanvasURI,
		AnnotationPageJson: page.Payload,
		Revision:           page.Revision,
		UpdatedAt:          page.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}, nil
}

func (h *Handler) SaveAnnotationPage(ctx context.Context, req *scribev1.SaveAnnotationPageRequest) (*scribev1.SaveAnnotationPageResponse, error) {
	page, metric, err := h.saveCanonicalAnnotationPage(
		ctx,
		req.GetItemImageId(),
		req.GetAnnotationPageJson(),
		req.GetExpectedRevision(),
	)
	if err != nil {
		return nil, annotationConnectError(err)
	}
	return &scribev1.SaveAnnotationPageResponse{
		ItemImageId:        page.ItemImageID,
		CanvasUri:          page.CanvasURI,
		AnnotationPageJson: page.Payload,
		Revision:           page.Revision,
		UpdatedAt:          page.UpdatedAt.UTC().Format(time.RFC3339Nano),
		Correction:         annotationCorrectionMetricResponse(metric),
	}, nil
}

func annotationConnectError(err error) error {
	switch {
	case errors.Is(err, store.ErrAnnotationRevisionConflict):
		return connect.NewError(connect.CodeAborted, err.Error()).WithCause(err)
	case errors.Is(err, store.ErrAnnotationPageNotFound), errors.Is(err, store.ErrAnnotationPageResource):
		return connect.Errorf(connect.CodeNotFound, "annotation page not found")
	case errors.Is(err, errInvalidAnnotationPage):
		return connect.NewError(connect.CodeInvalidArgument, err.Error()).WithCause(err)
	default:
		return connect.Errorf(connect.CodeInternal, "annotation persistence failed")
	}
}

func (h *Handler) SearchAnnotations(ctx context.Context, req *scribev1.SearchAnnotationsRequest) (*scribev1.SearchAnnotationsResponse, error) {
	canvasURI := strings.TrimSpace(req.GetCanvasUri())
	itemImageID := req.GetItemImageId()
	page, err := h.currentAnnotationPage(ctx, itemImageID)
	if err != nil {
		return nil, annotationConnectError(err)
	}
	if canvasURI != "" && canvasURI != page.CanvasURI {
		return nil, connect.Errorf(connect.CodeNotFound, "annotation page not found")
	}
	payload := page.Payload
	granularity := annotationGranularityName(req.GetGranularity())
	if granularity != "all" {
		var document map[string]any
		if err := iiif.DecodeJSON([]byte(page.Payload), &document); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err.Error()).WithCause(err)
		}
		items, _ := document["items"].([]any)
		document["items"] = filterAnnotationsByGranularity(items, granularity)
		filtered, err := json.Marshal(document)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err.Error()).WithCause(err)
		}
		payload = string(filtered)
	}
	return &scribev1.SearchAnnotationsResponse{
		AnnotationPageJson: payload,
		Revision:           page.Revision,
	}, nil
}

func annotationGranularityName(value scribev1.AnnotationGranularity) string {
	switch value {
	case scribev1.AnnotationGranularity_ANNOTATION_GRANULARITY_PAGE:
		return "page"
	case scribev1.AnnotationGranularity_ANNOTATION_GRANULARITY_BLOCK:
		return "block"
	case scribev1.AnnotationGranularity_ANNOTATION_GRANULARITY_PARAGRAPH:
		return "paragraph"
	case scribev1.AnnotationGranularity_ANNOTATION_GRANULARITY_LINE:
		return "line"
	case scribev1.AnnotationGranularity_ANNOTATION_GRANULARITY_WORD:
		return "word"
	case scribev1.AnnotationGranularity_ANNOTATION_GRANULARITY_GLYPH:
		return "glyph"
	default:
		return "all"
	}
}

func filterAnnotationsByGranularity(items []any, granularity string) []any {
	if granularity == "" || granularity == "all" {
		return items
	}
	filtered := make([]any, 0, len(items))
	for _, item := range items {
		anno, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(annStringValue(anno, "textGranularity")), granularity) {
			filtered = append(filtered, anno)
		}
	}
	return filtered
}

func (h *Handler) GetAnnotation(ctx context.Context, req *scribev1.GetAnnotationRequest) (*scribev1.GetAnnotationResponse, error) {
	itemImageID := req.GetItemImageId()
	if _, err := h.itemImageForRequest(ctx, itemImageID); err != nil {
		return nil, connect.Errorf(connect.CodeNotFound, "annotation not found")
	}
	id := strings.TrimSpace(req.GetId())
	if id == "" {
		return nil, connect.Errorf(connect.CodeInvalidArgument, "id is required")
	}
	entry, err := h.annotations.GetIndexEntry(ctx, h.currentWorkspaceID(ctx), id)
	if err != nil {
		return nil, connect.Errorf(connect.CodeNotFound, "annotation not found")
	}
	if entry.ItemImageID != itemImageID {
		return nil, connect.Errorf(connect.CodeNotFound, "annotation not found")
	}
	return &scribev1.GetAnnotationResponse{AnnotationJson: entry.Payload}, nil
}

func (h *Handler) SplitLineIntoWords(ctx context.Context, req *scribev1.SplitLineIntoWordsRequest) (*scribev1.SplitLineIntoWordsResponse, error) {
	transformed, err := h.transformAnnotationDraft(ctx, req.GetItemImageId(), req.GetAnnotationPageJson(), func(draft *annotationDraft) error {
		return splitDraftLineIntoWords(draft, req.GetSelectedAnnotationId(), req.GetWords())
	})
	if err != nil {
		return nil, err
	}
	return &scribev1.SplitLineIntoWordsResponse{AnnotationPageJson: transformed}, nil
}

func (h *Handler) SplitPageIntoWords(ctx context.Context, req *scribev1.SplitPageIntoWordsRequest) (*scribev1.SplitPageIntoWordsResponse, error) {
	transformed, err := h.transformAnnotationDraft(ctx, req.GetItemImageId(), req.GetAnnotationPageJson(), splitDraftPageIntoWords)
	if err != nil {
		return nil, err
	}
	return &scribev1.SplitPageIntoWordsResponse{AnnotationPageJson: transformed}, nil
}

func (h *Handler) SplitLineIntoTwoLines(ctx context.Context, req *scribev1.SplitLineIntoTwoLinesRequest) (*scribev1.SplitLineIntoTwoLinesResponse, error) {
	transformed, err := h.transformAnnotationDraft(ctx, req.GetItemImageId(), req.GetAnnotationPageJson(), func(draft *annotationDraft) error {
		return splitDraftLineIntoTwo(draft, req.GetSelectedAnnotationId(), int(req.GetSplitAtWord()))
	})
	if err != nil {
		return nil, err
	}
	return &scribev1.SplitLineIntoTwoLinesResponse{AnnotationPageJson: transformed}, nil
}

func (h *Handler) JoinLines(ctx context.Context, req *scribev1.JoinLinesRequest) (*scribev1.JoinLinesResponse, error) {
	transformed, err := h.transformAnnotationDraft(ctx, req.GetItemImageId(), req.GetAnnotationPageJson(), func(draft *annotationDraft) error {
		return joinDraftLines(draft, req.GetSelectedAnnotationIds())
	})
	if err != nil {
		return nil, err
	}
	return &scribev1.JoinLinesResponse{AnnotationPageJson: transformed}, nil
}

func (h *Handler) JoinWordsIntoLine(ctx context.Context, req *scribev1.JoinWordsIntoLineRequest) (*scribev1.JoinWordsIntoLineResponse, error) {
	transformed, err := h.transformAnnotationDraft(ctx, req.GetItemImageId(), req.GetAnnotationPageJson(), func(draft *annotationDraft) error {
		return joinDraftWordsIntoLine(draft, req.GetSelectedAnnotationIds())
	})
	if err != nil {
		return nil, err
	}
	return &scribev1.JoinWordsIntoLineResponse{AnnotationPageJson: transformed}, nil
}

func (h *Handler) transformAnnotationDraft(ctx context.Context, itemImageID uint64, raw string, transform func(*annotationDraft) error) (string, error) {
	if h.items == nil {
		return "", connect.Errorf(connect.CodeInternal, "annotation repository is not configured")
	}
	image, err := h.itemImageForRequest(ctx, itemImageID)
	if err != nil {
		return "", connect.Errorf(connect.CodeNotFound, "annotation page not found")
	}
	canvasURI := strings.TrimSpace(image.CanvasURI)
	if canvasURI == "" {
		return "", connect.Errorf(connect.CodeNotFound, "annotation page not found")
	}
	draft, err := decodeAnnotationDraft(raw, iiif.PageIdentity{
		PublicBaseURL: h.publicAnnotationBaseURL(),
		ItemImageID:   itemImageID,
		CanvasURI:     canvasURI,
	})
	if err != nil {
		return "", connect.NewError(connect.CodeInvalidArgument, err.Error()).WithCause(err)
	}
	if err := transform(draft); err != nil {
		return "", connect.NewError(connect.CodeInvalidArgument, err.Error()).WithCause(err)
	}
	transformed, err := draft.encode()
	if err != nil {
		return "", connect.NewError(connect.CodeInvalidArgument, err.Error()).WithCause(err)
	}
	return transformed, nil
}

func (h *Handler) ExportAnnotationPage(ctx context.Context, req *scribev1.ExportAnnotationPageRequest) (*scribev1.ExportAnnotationPageResponse, error) {
	itemImageID := req.GetItemImageId()
	format, err := annotationExportFormatName(req.GetFormat())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error()).WithCause(err)
	}
	ctx, cancel := context.WithTimeout(ctx, maxPreparedExportDuration)
	defer cancel()
	release, allowed := h.exportLimiter.TryAcquire(fmt.Sprintf("workspace:%d", h.currentWorkspaceID(ctx)))
	if !allowed {
		return nil, connect.Errorf(connect.CodeResourceExhausted, "export concurrency limit exceeded")
	}
	defer release()
	exportPage, err := h.loadCanonicalExportPage(ctx, itemImageID, req.GetExpectedRevision())
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled):
			return nil, connect.Errorf(connect.CodeCanceled, "canonical annotation export canceled")
		case errors.Is(err, context.DeadlineExceeded):
			return nil, connect.Errorf(connect.CodeDeadlineExceeded, "canonical annotation export timed out")
		case errors.Is(err, errItemExportRevisionConflict):
			return nil, connect.Errorf(connect.CodeAborted, "canonical annotations changed; reload before exporting")
		case errors.Is(err, store.ErrAnnotationPageNotFound):
			return nil, connect.Errorf(connect.CodeNotFound, "annotation page not found")
		default:
			return nil, connect.Errorf(connect.CodeInternal, "canonical annotation export failed")
		}
	}
	content, mediaType, extension, err := renderAnnotationExport(exportPage.Page.Payload, int(exportPage.Image.Width), int(exportPage.Image.Height), format)
	if err != nil {
		if errors.Is(err, errItemExportOutputLimit) {
			return nil, connect.Errorf(connect.CodeResourceExhausted, "canonical annotation export exceeds the output-byte limit")
		}
		return nil, connect.Errorf(connect.CodeInternal, "canonical annotation export failed")
	}
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, connect.Errorf(connect.CodeDeadlineExceeded, "canonical annotation export timed out")
		}
		return nil, connect.Errorf(connect.CodeCanceled, "canonical annotation export canceled")
	}
	return &scribev1.ExportAnnotationPageResponse{
		ItemImageId: itemImageID,
		Revision:    exportPage.Page.Revision,
		MediaType:   mediaType,
		Content:     []byte(content),
		Filename:    fmt.Sprintf("item-%d.%s", itemImageID, extension),
	}, nil
}

func annotationExportFormatName(format scribev1.AnnotationExportFormat) (string, error) {
	switch format {
	case scribev1.AnnotationExportFormat_ANNOTATION_EXPORT_FORMAT_PLAIN_TEXT:
		return "txt", nil
	case scribev1.AnnotationExportFormat_ANNOTATION_EXPORT_FORMAT_HOCR:
		return "hocr", nil
	case scribev1.AnnotationExportFormat_ANNOTATION_EXPORT_FORMAT_PAGE_XML:
		return "pagexml", nil
	case scribev1.AnnotationExportFormat_ANNOTATION_EXPORT_FORMAT_ALTO_XML:
		return "alto", nil
	default:
		return "", fmt.Errorf("format must be plain text, hOCR, PAGE XML, or ALTO XML")
	}
}

func (h *Handler) EnrichAnnotation(ctx context.Context, req *scribev1.EnrichAnnotationRequest) (*scribev1.EnrichAnnotationResponse, error) {
	itemImageID := req.GetItemImageId()
	if itemImageID == 0 {
		return nil, connect.Errorf(connect.CodeInvalidArgument, "item_image_id is required")
	}
	annotationJSON := strings.TrimSpace(req.GetAnnotationJson())
	if annotationJSON == "" {
		return nil, connect.Errorf(connect.CodeInvalidArgument, "annotation_json is required")
	}
	scope := strings.ToLower(strings.TrimSpace(req.GetScope()))
	if scope == "" {
		scope = "line"
	}
	switch scope {
	case "page":
		if err := h.authorizeAnnotationPageJSON(ctx, itemImageID, annotationJSON); err != nil {
			return nil, connect.Errorf(connect.CodeNotFound, "annotation page not found")
		}
	case "line":
		if err := h.authorizeAnnotationJSON(ctx, itemImageID, annotationJSON); err != nil {
			return nil, connect.Errorf(connect.CodeNotFound, "annotation not found")
		}
	default:
		return nil, connect.Errorf(connect.CodeInvalidArgument, "scope must be line or page")
	}

	var processingContext store.Context
	if req.GetContextId() > 0 {
		c, err := h.contextForRead(ctx, req.GetContextId())
		if err != nil {
			return nil, connect.Errorf(connect.CodeNotFound, "context not found")
		}
		processingContext = c
	} else {
		c, _, err := h.contexts.ResolveForWorkspace(ctx, h.currentWorkspaceID(ctx), nil)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("resolve context: %w", err).Error()).WithCause(err)
		}
		processingContext = c
	}
	processingContext, err := normalizeContextForExecution(
		processingContext,
		providerregistry.New(config.Get().Config),
	)
	if err != nil {
		return nil, connect.Errorf(connect.CodeFailedPrecondition, "processing context is not executable")
	}

	if processingContext.TranscriptionProvider == "kraken" {
		return nil, connect.Errorf(connect.CodeFailedPrecondition, "kraken contexts require whole-page reprocessing")
	}

	releaseProcessing, err := h.acquireTranscriptionProcessingSlot(ctx, h.currentWorkspaceID(ctx), processingContext)
	if err != nil {
		return nil, err
	}
	defer releaseProcessing()
	var enriched string
	var enrichErr error
	if scope == "page" {
		enriched, enrichErr = h.enrichAnnotationPage(ctx, itemImageID, annotationJSON, processingContext)
	} else {
		enriched, enrichErr = h.enrichSingleAnnotation(ctx, itemImageID, annotationJSON, processingContext)
	}

	if enrichErr != nil {
		return nil, annotationEnrichmentConnectError(enrichErr)
	}
	return &scribev1.EnrichAnnotationResponse{AnnotationJson: enriched}, nil
}

func annotationEnrichmentConnectError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return connect.Errorf(connect.CodeCanceled, "annotation enrichment canceled")
	case errors.Is(err, context.DeadlineExceeded):
		return connect.Errorf(connect.CodeDeadlineExceeded, "annotation enrichment deadline exceeded")
	case errors.Is(err, errInvalidAnnotationEnrichmentInput):
		return connect.NewError(connect.CodeInvalidArgument, err.Error()).WithCause(err)
	case errors.Is(err, errEmptyAnnotationTranscription):
		return connect.NewError(connect.CodeFailedPrecondition, errEmptyAnnotationTranscription.Error()).WithCause(errEmptyAnnotationTranscription)
	case errors.Is(err, hocr.ErrPermanentProviderRequest):
		return connect.Errorf(connect.CodeFailedPrecondition, "transcription provider rejected the configured credential or context")
	case errors.Is(err, hocr.ErrRetryableProviderRequest):
		return connect.Errorf(connect.CodeUnavailable, "transcription provider is temporarily unavailable")
	default:
		return connect.Errorf(connect.CodeInternal, "annotation enrichment failed")
	}
}

// annotationCorrectionMetricResponse maps the store metric onto the API. A
// nil metric means the page has no OCR baseline, so the field is omitted
// rather than reported as a zero distance.
func annotationCorrectionMetricResponse(metric *store.AnnotationCorrectionMetric) *scribev1.AnnotationCorrectionMetric {
	if metric == nil {
		return nil
	}
	return &scribev1.AnnotationCorrectionMetric{
		LevenshteinDistance: int32FromIntBounded(metric.LevenshteinDistance),
		BaselineCharacters:  int32FromIntBounded(metric.BaselineCharacters),
		CorrectedCharacters: int32FromIntBounded(metric.CorrectedCharacters),
	}
}
