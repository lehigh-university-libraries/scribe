package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
)

// --- AnnotationService Connect handlers ---

func (h *Handler) GetAnnotationPage(ctx context.Context, req *connect.Request[scribev1.GetAnnotationPageRequest]) (*connect.Response[scribev1.GetAnnotationPageResponse], error) {
	page, err := h.currentAnnotationPage(ctx, req.Msg.GetItemImageId())
	if err != nil {
		return nil, annotationConnectError(err)
	}
	return connect.NewResponse(&scribev1.GetAnnotationPageResponse{
		ItemImageId:        page.ItemImageID,
		CanvasUri:          page.CanvasURI,
		AnnotationPageJson: page.Payload,
		Revision:           page.Revision,
		UpdatedAt:          page.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}), nil
}

func (h *Handler) SaveAnnotationPage(ctx context.Context, req *connect.Request[scribev1.SaveAnnotationPageRequest]) (*connect.Response[scribev1.SaveAnnotationPageResponse], error) {
	page, err := h.saveCanonicalAnnotationPage(
		ctx,
		req.Msg.GetItemImageId(),
		req.Msg.GetAnnotationPageJson(),
		req.Msg.GetExpectedRevision(),
	)
	if err != nil {
		return nil, annotationConnectError(err)
	}
	return connect.NewResponse(&scribev1.SaveAnnotationPageResponse{
		ItemImageId:        page.ItemImageID,
		CanvasUri:          page.CanvasURI,
		AnnotationPageJson: page.Payload,
		Revision:           page.Revision,
		UpdatedAt:          page.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}), nil
}

func annotationConnectError(err error) error {
	switch {
	case errors.Is(err, store.ErrAnnotationRevisionConflict):
		return connect.NewError(connect.CodeAborted, err)
	case errors.Is(err, store.ErrAnnotationPageNotFound), errors.Is(err, store.ErrAnnotationPageResource):
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("annotation page not found"))
	case errors.Is(err, errInvalidAnnotationPage):
		return connect.NewError(connect.CodeInvalidArgument, err)
	default:
		return connect.NewError(connect.CodeInternal, fmt.Errorf("annotation persistence failed"))
	}
}

func (h *Handler) SearchAnnotations(ctx context.Context, req *connect.Request[scribev1.SearchAnnotationsRequest]) (*connect.Response[scribev1.SearchAnnotationsResponse], error) {
	canvasURI := strings.TrimSpace(req.Msg.GetCanvasUri())
	itemImageID := req.Msg.GetItemImageId()
	page, err := h.currentAnnotationPage(ctx, itemImageID)
	if err != nil {
		return nil, annotationConnectError(err)
	}
	if canvasURI != "" && canvasURI != page.CanvasURI {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("annotation page not found"))
	}
	payload := page.Payload
	granularity := annotationGranularityName(req.Msg.GetGranularity())
	if granularity != "all" {
		var document map[string]any
		if err := iiif.DecodeJSON([]byte(page.Payload), &document); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		items, _ := document["items"].([]any)
		document["items"] = filterAnnotationsByGranularity(items, granularity)
		filtered, err := json.Marshal(document)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		payload = string(filtered)
	}
	return connect.NewResponse(&scribev1.SearchAnnotationsResponse{
		AnnotationPageJson: payload,
		Revision:           page.Revision,
	}), nil
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

func (h *Handler) GetAnnotation(ctx context.Context, req *connect.Request[scribev1.GetAnnotationRequest]) (*connect.Response[scribev1.GetAnnotationResponse], error) {
	itemImageID := req.Msg.GetItemImageId()
	if _, err := h.itemImageForRequest(ctx, itemImageID); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("annotation not found"))
	}
	id := strings.TrimSpace(req.Msg.GetId())
	if id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("id is required"))
	}
	entry, err := h.annotations.GetIndexEntry(ctx, h.currentWorkspaceID(ctx), id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("annotation not found"))
	}
	if entry.ItemImageID != itemImageID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("annotation not found"))
	}
	return connect.NewResponse(&scribev1.GetAnnotationResponse{AnnotationJson: entry.Payload}), nil
}

func (h *Handler) SplitLineIntoWords(ctx context.Context, req *connect.Request[scribev1.SplitLineIntoWordsRequest]) (*connect.Response[scribev1.SplitLineIntoWordsResponse], error) {
	transformed, err := h.transformAnnotationDraft(ctx, req.Msg.GetItemImageId(), req.Msg.GetAnnotationPageJson(), func(draft *annotationDraft) error {
		return splitDraftLineIntoWords(draft, req.Msg.GetSelectedAnnotationId(), req.Msg.GetWords())
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&scribev1.SplitLineIntoWordsResponse{AnnotationPageJson: transformed}), nil
}

func (h *Handler) SplitPageIntoWords(ctx context.Context, req *connect.Request[scribev1.SplitPageIntoWordsRequest]) (*connect.Response[scribev1.SplitPageIntoWordsResponse], error) {
	transformed, err := h.transformAnnotationDraft(ctx, req.Msg.GetItemImageId(), req.Msg.GetAnnotationPageJson(), splitDraftPageIntoWords)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&scribev1.SplitPageIntoWordsResponse{AnnotationPageJson: transformed}), nil
}

func (h *Handler) SplitLineIntoTwoLines(ctx context.Context, req *connect.Request[scribev1.SplitLineIntoTwoLinesRequest]) (*connect.Response[scribev1.SplitLineIntoTwoLinesResponse], error) {
	transformed, err := h.transformAnnotationDraft(ctx, req.Msg.GetItemImageId(), req.Msg.GetAnnotationPageJson(), func(draft *annotationDraft) error {
		return splitDraftLineIntoTwo(draft, req.Msg.GetSelectedAnnotationId(), int(req.Msg.GetSplitAtWord()))
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&scribev1.SplitLineIntoTwoLinesResponse{AnnotationPageJson: transformed}), nil
}

func (h *Handler) JoinLines(ctx context.Context, req *connect.Request[scribev1.JoinLinesRequest]) (*connect.Response[scribev1.JoinLinesResponse], error) {
	transformed, err := h.transformAnnotationDraft(ctx, req.Msg.GetItemImageId(), req.Msg.GetAnnotationPageJson(), func(draft *annotationDraft) error {
		return joinDraftLines(draft, req.Msg.GetSelectedAnnotationIds())
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&scribev1.JoinLinesResponse{AnnotationPageJson: transformed}), nil
}

func (h *Handler) JoinWordsIntoLine(ctx context.Context, req *connect.Request[scribev1.JoinWordsIntoLineRequest]) (*connect.Response[scribev1.JoinWordsIntoLineResponse], error) {
	transformed, err := h.transformAnnotationDraft(ctx, req.Msg.GetItemImageId(), req.Msg.GetAnnotationPageJson(), func(draft *annotationDraft) error {
		return joinDraftWordsIntoLine(draft, req.Msg.GetSelectedAnnotationIds())
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&scribev1.JoinWordsIntoLineResponse{AnnotationPageJson: transformed}), nil
}

func (h *Handler) transformAnnotationDraft(ctx context.Context, itemImageID uint64, raw string, transform func(*annotationDraft) error) (string, error) {
	if h.items == nil {
		return "", connect.NewError(connect.CodeInternal, fmt.Errorf("annotation repository is not configured"))
	}
	image, err := h.itemImageForRequest(ctx, itemImageID)
	if err != nil {
		return "", connect.NewError(connect.CodeNotFound, fmt.Errorf("annotation page not found"))
	}
	canvasURI := strings.TrimSpace(image.CanvasURI)
	if canvasURI == "" {
		return "", connect.NewError(connect.CodeNotFound, fmt.Errorf("annotation page not found"))
	}
	draft, err := decodeAnnotationDraft(raw, iiif.PageIdentity{
		PublicBaseURL: h.publicAnnotationBaseURL(),
		ItemImageID:   itemImageID,
		CanvasURI:     canvasURI,
	})
	if err != nil {
		return "", connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := transform(draft); err != nil {
		return "", connect.NewError(connect.CodeInvalidArgument, err)
	}
	transformed, err := draft.encode()
	if err != nil {
		return "", connect.NewError(connect.CodeInvalidArgument, err)
	}
	return transformed, nil
}

func (h *Handler) ExportAnnotationPage(ctx context.Context, req *connect.Request[scribev1.ExportAnnotationPageRequest]) (*connect.Response[scribev1.ExportAnnotationPageResponse], error) {
	itemImageID := req.Msg.GetItemImageId()
	format, err := annotationExportFormatName(req.Msg.GetFormat())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	ctx, cancel := context.WithTimeout(ctx, maxPreparedExportDuration)
	defer cancel()
	release, allowed := h.exportLimiter.TryAcquire(fmt.Sprintf("workspace:%d", h.currentWorkspaceID(ctx)))
	if !allowed {
		return nil, connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("export concurrency limit exceeded"))
	}
	defer release()
	exportPage, err := h.loadCanonicalExportPage(ctx, itemImageID, req.Msg.GetExpectedRevision())
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled):
			return nil, connect.NewError(connect.CodeCanceled, fmt.Errorf("canonical annotation export canceled"))
		case errors.Is(err, context.DeadlineExceeded):
			return nil, connect.NewError(connect.CodeDeadlineExceeded, fmt.Errorf("canonical annotation export timed out"))
		case errors.Is(err, errItemExportRevisionConflict):
			return nil, connect.NewError(connect.CodeAborted, fmt.Errorf("canonical annotations changed; reload before exporting"))
		case errors.Is(err, store.ErrAnnotationPageNotFound):
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("annotation page not found"))
		default:
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("canonical annotation export failed"))
		}
	}
	content, mediaType, extension, err := renderAnnotationExport(exportPage.Page.Payload, int(exportPage.Image.Width), int(exportPage.Image.Height), format)
	if err != nil {
		if errors.Is(err, errItemExportOutputLimit) {
			return nil, connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("canonical annotation export exceeds the output-byte limit"))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("canonical annotation export failed"))
	}
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, connect.NewError(connect.CodeDeadlineExceeded, fmt.Errorf("canonical annotation export timed out"))
		}
		return nil, connect.NewError(connect.CodeCanceled, fmt.Errorf("canonical annotation export canceled"))
	}
	return connect.NewResponse(&scribev1.ExportAnnotationPageResponse{
		ItemImageId: itemImageID,
		Revision:    exportPage.Page.Revision,
		MediaType:   mediaType,
		Content:     []byte(content),
		Filename:    fmt.Sprintf("item-%d.%s", itemImageID, extension),
	}), nil
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

func (h *Handler) EnrichAnnotation(ctx context.Context, req *connect.Request[scribev1.EnrichAnnotationRequest]) (*connect.Response[scribev1.EnrichAnnotationResponse], error) {
	itemImageID := req.Msg.GetItemImageId()
	if itemImageID == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("item_image_id is required"))
	}
	annotationJSON := strings.TrimSpace(req.Msg.GetAnnotationJson())
	if annotationJSON == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("annotation_json is required"))
	}
	scope := strings.ToLower(strings.TrimSpace(req.Msg.GetScope()))
	if scope == "" {
		scope = "line"
	}
	switch scope {
	case "page":
		if err := h.authorizeAnnotationPageJSON(ctx, itemImageID, annotationJSON); err != nil {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("annotation page not found"))
		}
	case "line":
		if err := h.authorizeAnnotationJSON(ctx, itemImageID, annotationJSON); err != nil {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("annotation not found"))
		}
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("scope must be line or page"))
	}

	var processingContext store.Context
	if req.Msg.GetContextId() > 0 {
		c, err := h.contextForRead(ctx, req.Msg.GetContextId())
		if err != nil {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("context not found"))
		}
		processingContext = c
	} else {
		c, _, err := h.contexts.ResolveForWorkspace(ctx, h.currentWorkspaceID(ctx), nil)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("resolve context: %w", err))
		}
		processingContext = c
	}

	releaseProcessing, err := h.acquireProcessingSlot(ctx, h.currentWorkspaceID(ctx), processingContext)
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
	return connect.NewResponse(&scribev1.EnrichAnnotationResponse{AnnotationJson: enriched}), nil
}

func annotationEnrichmentConnectError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return connect.NewError(connect.CodeCanceled, fmt.Errorf("annotation enrichment canceled"))
	case errors.Is(err, context.DeadlineExceeded):
		return connect.NewError(connect.CodeDeadlineExceeded, fmt.Errorf("annotation enrichment deadline exceeded"))
	case errors.Is(err, errInvalidAnnotationEnrichmentInput):
		return connect.NewError(connect.CodeInvalidArgument, err)
	default:
		return connect.NewError(connect.CodeInternal, fmt.Errorf("annotation enrichment failed"))
	}
}
