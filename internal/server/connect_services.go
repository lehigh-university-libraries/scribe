package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/lehigh-university-libraries/scribe/internal/db"
	ocrhandlers "github.com/lehigh-university-libraries/scribe/internal/handlers"
	"github.com/lehigh-university-libraries/scribe/internal/hocr"
	"github.com/lehigh-university-libraries/scribe/internal/metrics"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
)

func providerFromHeader(h map[string][]string) string {
	return strings.TrimSpace(firstHeaderValue(h, "X-Provider"))
}

func firstHeaderValue(h map[string][]string, key string) string {
	for k, values := range h {
		if strings.EqualFold(k, key) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

type externalProcessingRequest struct {
	source      string
	key         string
	eventHeader string
}

func externalRequestFromHeaders(headers map[string][]string, imageURL string, contextID uint64, metadataJSON string) externalProcessingRequest {
	key := strings.TrimSpace(firstHeaderValue(headers, "X-Idempotency-Key"))
	source := strings.TrimSpace(firstHeaderValue(headers, "X-External-Source"))
	if source == "" {
		source = strings.TrimSpace(firstHeaderValue(headers, "X-Scribe-External-Source"))
	}
	eventHeader := strings.TrimSpace(firstHeaderValue(headers, "X-Islandora-Event"))
	if source == "" && eventHeader != "" {
		source = "islandora"
	}
	if source == "" {
		source = "external"
	}
	if len(eventHeader) > 256*1024 {
		eventHeader = eventHeader[:256*1024]
	}
	if key == "" && eventHeader != "" {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s\n%d\n%s\n%s", imageURL, contextID, strings.TrimSpace(metadataJSON), eventHeader)))
		key = hex.EncodeToString(sum[:])
	}
	if key == "" {
		return externalProcessingRequest{}
	}
	sum := sha256.Sum256([]byte(key))
	return externalProcessingRequest{
		source:      source,
		key:         hex.EncodeToString(sum[:]),
		eventHeader: eventHeader,
	}
}

func (h *Handler) resolveTranscriptionConfig(
	ctx context.Context,
	contextID uint64,
	metadataJSON string,
	headerProvider string,
) (string, string, error) {
	var selectedProvider string
	var selectedModel string

	if contextID > 0 {
		c, err := h.contextForRead(ctx, contextID)
		if err != nil {
			return "", "", fmt.Errorf("context not found")
		}
		selectedProvider = c.TranscriptionProvider
		selectedModel = c.TranscriptionModel
	} else {
		var metadata map[string]any
		raw := strings.TrimSpace(metadataJSON)
		if raw != "" {
			if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
				return "", "", fmt.Errorf("invalid metadata json")
			}
		}
		c, _, err := h.contexts.ResolveForWorkspace(ctx, h.currentWorkspaceID(ctx), metadata)
		if err != nil {
			return "", "", fmt.Errorf("resolve context: %w", err)
		}
		selectedProvider = c.TranscriptionProvider
		selectedModel = c.TranscriptionModel
	}

	headerProvider = strings.TrimSpace(headerProvider)
	if headerProvider != "" {
		// Explicit request override for provider should not inherit a model from a different provider.
		selectedProvider = headerProvider
		selectedModel = ""
	}

	provider := effectiveProvider(selectedProvider)
	model := effectiveModel(provider, selectedModel)
	return provider, model, nil
}

// resolveContext returns the full store.Context for a request, resolving via
// explicit context ID or metadata-based selection rules.
func (h *Handler) resolveContext(ctx context.Context, contextID uint64, metadataJSON string) (store.Context, error) {
	return h.resolveContextForRequest(ctx, contextID, metadataJSON)
}

// processingContextFromStore converts a store.Context into an hocr.ProcessingContext.
func processingContextFromStore(c store.Context, providerOverride string) hocr.ProcessingContext {
	provider := c.TranscriptionProvider
	model := c.TranscriptionModel
	if providerOverride != "" {
		provider = providerOverride
		model = "" // let the hocr service pick the default for this provider
	}
	return hocr.ProcessingContext{
		SegmentationModel:     c.SegmentationModel,
		TranscriptionProvider: effectiveProvider(provider),
		TranscriptionModel:    effectiveModel(effectiveProvider(provider), model),
		TranscriptionBaseURL:  c.TranscriptionBaseURL,
		TranscriptionAudience: c.TranscriptionAudience,
		Temperature:           c.Temperature,
		SystemPrompt:          c.SystemPrompt,
	}
}

func (h *Handler) ProcessImageURL(ctx context.Context, req *connect.Request[scribev1.ProcessImageURLRequest]) (*connect.Response[scribev1.ProcessImageURLResponse], error) {
	providerHeader := providerFromHeader(req.Header())
	imageURL := strings.TrimSpace(req.Msg.GetImageUrl())
	if imageURL == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("image_url is required"))
	}
	externalReq := externalRequestFromHeaders(req.Header(), imageURL, req.Msg.GetContextId(), req.Msg.GetMetadata())
	externalReserved := false
	if externalReq.key != "" && h.transcriptionJobs != nil {
		reservation, created, err := h.transcriptionJobs.ReserveExternalRequest(ctx, h.currentWorkspaceID(ctx), externalReq.source, externalReq.key, externalReq.eventHeader)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("reserve external request: %w", err))
		}
		if !created {
			switch reservation.Status {
			case store.ExternalRequestStatusCompleted:
				if reservation.ItemImageID == 0 {
					return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("external request already completed without an item image"))
				}
				run, err := h.ocrRuns.GetByItemImageID(ctx, reservation.ItemImageID)
				if err != nil {
					return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("external request already completed"))
				}
				return connect.NewResponse(&scribev1.ProcessImageURLResponse{
					ItemId:      reservation.ItemID,
					ItemImageId: reservation.ItemImageID,
					SessionId:   run.SessionID,
					ImageUrl:    run.ImageURL,
					Hocr:        run.OriginalHOCR,
					PlainText:   run.OriginalText,
				}), nil
			case store.ExternalRequestStatusInProgress:
				return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("external request is already in progress"))
			default:
				return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("external request already exists"))
			}
		}
		externalReserved = true
	}
	failExternal := func(err error) {
		if externalReserved && err != nil {
			_ = h.transcriptionJobs.FailExternalRequest(ctx, h.currentWorkspaceID(ctx), externalReq.source, externalReq.key, err.Error())
		}
	}

	resolvedCtx, err := h.resolveContext(ctx, req.Msg.GetContextId(), req.Msg.GetMetadata())
	if err != nil {
		failExternal(err)
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	var result *ocrhandlers.ProcessResult
	var provider, model string
	runAsync := true
	var contextID *uint64
	if resolvedCtx.ID > 0 {
		v := resolvedCtx.ID
		contextID = &v
	}

	seg := strings.ToLower(strings.TrimSpace(resolvedCtx.SegmentationModel))
	if seg != "" && providerHeader == "" {
		// Segmentation model set: detect segments only, then enqueue a batch
		// transcription job. The client is redirected to the editor immediately
		// and the job worker transcribes segment by segment in the background.
		pctx := processingContextFromStore(resolvedCtx, "")
		pctx.SegmentOnly = true
		callCtx := hocr.WithProviderCallMetadata(ctx, "", nil, contextID)
		result, err = h.ocr.ProcessImageURLWithContext(callCtx, imageURL, pctx)
		runAsync = false
	} else {
		// Detection-only hOCR. Transcription now always runs through the durable worker queue.
		provider, model, err = h.resolveTranscriptionConfig(ctx, req.Msg.GetContextId(), req.Msg.GetMetadata(), providerHeader)
		if err != nil {
			failExternal(err)
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		result, err = h.ocr.ProcessImageURLWithProviderAndModelContext(ctx, imageURL, provider, model)
	}

	if err != nil {
		failExternal(err)
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if !runAsync {
		provider = result.Provider
		model = result.Model
	}
	item, itemImage, err := h.createOCRItemAndImage(ctx, "url", result.ImageURL, imageURL, imageURL, resolvedCtx)
	if err != nil {
		failExternal(err)
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	sessionID := item.ID
	if err := h.ocrRuns.Create(ctx, store.OCRRun{
		SessionID:    sessionID,
		ItemImageID:  &itemImage.ID,
		ContextID:    contextID,
		ImageURL:     result.ImageURL,
		Provider:     provider,
		Model:        model,
		OriginalHOCR: result.HOCR,
		OriginalText: result.PlainText,
	}); err != nil {
		failExternal(err)
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := writeSessionHOCR(sessionID, "original.hocr", result.HOCR); err != nil {
		failExternal(err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("persist original hocr: %w", err))
	}
	if err := h.ensureItemImageCanvasAndAnnotations(ctx, store.OCRRun{
		SessionID:    sessionID,
		ItemImageID:  &itemImage.ID,
		OriginalHOCR: result.HOCR,
	}, itemImage.ID); err != nil {
		slog.Warn("Failed to initialize item image canvas/annotations", "item_image_id", itemImage.ID, "error", err)
	}
	var jobID uint64
	if createdJobID, err := h.createTranscriptionJob(ctx, itemImage.ID, contextID); err != nil {
		slog.Warn("Failed to enqueue transcription job", "item_image_id", itemImage.ID, "error", err)
		failExternal(err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("enqueue transcription job: %w", err))
	} else {
		jobID = createdJobID
	}
	if externalReserved {
		if err := h.transcriptionJobs.CompleteExternalRequest(ctx, h.currentWorkspaceID(ctx), externalReq.source, externalReq.key, item.ID, itemImage.ID, jobID); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("complete external request: %w", err))
		}
	}

	return connect.NewResponse(&scribev1.ProcessImageURLResponse{
		ItemId:      item.ID,
		ItemImageId: itemImage.ID,
		SessionId:   sessionID,
		ImageUrl:    result.ImageURL,
		Hocr:        result.HOCR,
		PlainText:   result.PlainText,
	}), nil
}

func (h *Handler) ProcessImageUpload(ctx context.Context, req *connect.Request[scribev1.ProcessImageUploadRequest]) (*connect.Response[scribev1.ProcessImageUploadResponse], error) {
	providerHeader := providerFromHeader(req.Header())
	filename := strings.TrimSpace(req.Msg.GetFilename())
	if filename == "" {
		filename = "upload.jpg"
	}
	if len(req.Msg.GetImageData()) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("image_data is required"))
	}

	resolvedCtx, err := h.resolveContext(ctx, req.Msg.GetContextId(), "")
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	var result *ocrhandlers.ProcessResult
	var provider, model string
	runAsync := true
	var contextID *uint64
	if resolvedCtx.ID > 0 {
		v := resolvedCtx.ID
		contextID = &v
	}

	seg := strings.ToLower(strings.TrimSpace(resolvedCtx.SegmentationModel))
	if seg != "" && providerHeader == "" {
		pctx := processingContextFromStore(resolvedCtx, "")
		pctx.SegmentOnly = true
		callCtx := hocr.WithProviderCallMetadata(ctx, "", nil, contextID)
		result, err = h.ocr.ProcessImageUploadWithContext(callCtx, filename, req.Msg.GetImageData(), pctx)
		runAsync = false
	} else {
		provider, model, err = h.resolveTranscriptionConfig(ctx, req.Msg.GetContextId(), "", providerHeader)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		result, err = h.ocr.ProcessImageUploadWithProviderAndModel(filename, req.Msg.GetImageData(), provider, model)
	}

	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if !runAsync {
		provider = result.Provider
		model = result.Model
	}
	item, itemImage, err := h.createOCRItemAndImage(ctx, "upload", result.ImageURL, "", filename, resolvedCtx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	sessionID := item.ID
	if err := h.ocrRuns.Create(ctx, store.OCRRun{
		SessionID:    sessionID,
		ItemImageID:  &itemImage.ID,
		ContextID:    contextID,
		ImageURL:     result.ImageURL,
		Provider:     provider,
		Model:        model,
		OriginalHOCR: result.HOCR,
		OriginalText: result.PlainText,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := writeSessionHOCR(sessionID, "original.hocr", result.HOCR); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("persist original hocr: %w", err))
	}
	if err := h.ensureItemImageCanvasAndAnnotations(ctx, store.OCRRun{
		SessionID:    sessionID,
		ItemImageID:  &itemImage.ID,
		OriginalHOCR: result.HOCR,
	}, itemImage.ID); err != nil {
		slog.Warn("Failed to initialize item image canvas/annotations", "item_image_id", itemImage.ID, "error", err)
	}
	if _, err := h.createTranscriptionJob(ctx, itemImage.ID, contextID); err != nil {
		slog.Warn("Failed to enqueue transcription job", "item_image_id", itemImage.ID, "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("enqueue transcription job: %w", err))
	}

	return connect.NewResponse(&scribev1.ProcessImageUploadResponse{
		ItemId:      item.ID,
		ItemImageId: itemImage.ID,
		SessionId:   sessionID,
		ImageUrl:    result.ImageURL,
		Hocr:        result.HOCR,
		PlainText:   result.PlainText,
	}), nil
}

func (h *Handler) ProcessHOCR(ctx context.Context, req *connect.Request[scribev1.ProcessHOCRRequest]) (*connect.Response[scribev1.ProcessHOCRResponse], error) {
	hocrXML := strings.TrimSpace(req.Msg.GetHocr())
	if hocrXML == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("hocr is required"))
	}
	if len(hocrXML) > maxInlineHOCRBytes {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("hocr exceeds 10 MiB limit"))
	}

	imageURL := strings.TrimSpace(req.Msg.GetImageUrl())
	if len(req.Msg.GetImageData()) > 0 {
		filename := strings.TrimSpace(req.Msg.GetFilename())
		if filename == "" {
			filename = "upload.jpg"
		}
		storedURL, err := h.ocr.StoreUploadedImage(filename, req.Msg.GetImageData())
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		imageURL = storedURL
	}

	plainText, err := ocrhandlers.HOCRToPlainText(hocrXML)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid hocr"))
	}

	item, itemImage, err := h.createOCRItemAndImage(ctx, "hocr", imageURL, "", imageURL, store.Context{
		Name:                  "Imported hOCR",
		SegmentationModel:     "imported",
		TranscriptionProvider: "custom",
		TranscriptionModel:    "custom",
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	sessionID := item.ID
	if err := h.ocrRuns.Create(ctx, store.OCRRun{
		SessionID:    sessionID,
		ItemImageID:  &itemImage.ID,
		ImageURL:     imageURL,
		Provider:     "custom",
		Model:        "custom",
		OriginalHOCR: hocrXML,
		OriginalText: plainText,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := writeSessionHOCR(sessionID, "original.hocr", hocrXML); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("persist original hocr: %w", err))
	}
	return connect.NewResponse(&scribev1.ProcessHOCRResponse{
		ItemId:      item.ID,
		ItemImageId: itemImage.ID,
		SessionId:   sessionID,
		ImageUrl:    imageURL,
		Hocr:        hocrXML,
		PlainText:   plainText,
	}), nil
}

func (h *Handler) GetOCRRun(ctx context.Context, req *connect.Request[scribev1.GetOCRRunRequest]) (*connect.Response[scribev1.GetOCRRunResponse], error) {
	itemImageID := req.Msg.GetItemImageId()
	if itemImageID == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("item_image_id is required"))
	}
	// Use the on-demand fallback: if no OCR run exists but the item_image
	// has a hocr_url (from a manifest seeAlso), fetch and cache it now.
	if _, authErr := h.itemImageForRequest(ctx, itemImageID); authErr != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("ocr run not found"))
	}
	run, err := h.fetchOrCacheHOCRRun(ctx, itemImageID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("ocr run not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &scribev1.GetOCRRunResponse{
		SessionId:           run.SessionID,
		ImageUrl:            run.ImageURL,
		Model:               run.Model,
		OriginalHocr:        run.OriginalHOCR,
		OriginalText:        run.OriginalText,
		EditCount:           int32FromIntBounded(run.EditCount),
		LevenshteinDistance: int32FromIntBounded(run.LevenshteinDistance),
	}
	if run.ItemImageID != nil {
		resp.ItemImageId = *run.ItemImageID
	}
	if run.CorrectedHOCR != nil {
		resp.CorrectedHocr = *run.CorrectedHOCR
	}
	if strings.TrimSpace(resp.CorrectedHocr) == "" {
		if corrected, ok := readSessionHOCR(run.SessionID, "corrected.hocr"); ok {
			resp.CorrectedHocr = corrected
		}
	}
	if run.CorrectedText != nil {
		resp.CorrectedText = *run.CorrectedText
	}
	return connect.NewResponse(resp), nil
}

func (h *Handler) SaveOCREdits(ctx context.Context, req *connect.Request[scribev1.SaveOCREditsRequest]) (*connect.Response[scribev1.SaveOCREditsResponse], error) {
	itemImageID := req.Msg.GetItemImageId()
	if itemImageID == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("item_image_id is required"))
	}
	correctedHOCR := strings.TrimSpace(req.Msg.GetCorrectedHocr())
	if correctedHOCR == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("corrected_hocr is required"))
	}

	run, err := h.ocrRunForRequest(ctx, "", itemImageID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("ocr run not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	correctedText, err := ocrhandlers.HOCRToPlainText(correctedHOCR)
	if err != nil {
		correctedText = hocrToPlainTextLenient(correctedHOCR)
	}

	lev := metrics.LevenshteinDistance(run.OriginalText, correctedText)
	boxMetrics := calculateBoxEditMetrics(run.OriginalHOCR, correctedHOCR)
	if err := h.ocrRuns.SaveEdits(
		ctx,
		run.SessionID,
		correctedHOCR,
		correctedText,
		int(req.Msg.GetEditCount()),
		lev,
		boxMetrics.ChangedCount,
		boxMetrics.AddedCount,
		boxMetrics.DeletedCount,
		boxMetrics.ChangeScore,
	); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := writeSessionHOCR(run.SessionID, "corrected.hocr", correctedHOCR); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("persist corrected hocr: %w", err))
	}

	return connect.NewResponse(&scribev1.SaveOCREditsResponse{
		SessionId:           run.SessionID,
		ItemImageId:         itemImageID,
		EditCount:           req.Msg.GetEditCount(),
		LevenshteinDistance: int32FromIntBounded(lev),
		CorrectedPlainText:  correctedText,
		OriginalPlainText:   run.OriginalText,
	}), nil
}

func (h *Handler) ReprocessItemImage(ctx context.Context, req *connect.Request[scribev1.ReprocessItemImageRequest]) (*connect.Response[scribev1.ReprocessItemImageResponse], error) {
	if req.Msg.GetItemImageId() == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("item_image_id is required"))
	}
	run, err := h.ocrRunForRequest(ctx, "", req.Msg.GetItemImageId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("ocr run not found"))
	}

	img, err := h.itemImageForRequest(ctx, req.Msg.GetItemImageId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("item image not found"))
	}

	contextIDValue := req.Msg.GetContextId()
	if contextIDValue == 0 && run.ContextID != nil {
		contextIDValue = *run.ContextID
	}
	resolvedCtx, err := h.resolveContext(ctx, contextIDValue, "")
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	var contextID *uint64
	if resolvedCtx.ID > 0 {
		v := resolvedCtx.ID
		contextID = &v
	}

	pctx := processingContextFromStore(resolvedCtx, "")
	callCtx := hocr.WithProviderCallMetadata(ctx, run.SessionID, &img.ID, contextID)

	runAsync := true
	if strings.TrimSpace(pctx.SegmentationModel) != "" {
		pctx.SegmentOnly = true
		runAsync = false
	}

	var (
		result   *ocrhandlers.ProcessResult
		provider string
		model    string
	)
	if runAsync {
		provider = pctx.TranscriptionProvider
		model = pctx.TranscriptionModel
		result, err = h.ocr.ProcessImageURLWithProviderAndModelContext(ctx, run.ImageURL, provider, model)
	} else {
		result, err = h.ocr.ProcessImageURLWithContext(callCtx, run.ImageURL, pctx)
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if !runAsync {
		provider = result.Provider
		model = result.Model
	}

	itemImageID := req.Msg.GetItemImageId()
	if err := h.ocrRuns.Create(ctx, store.OCRRun{
		SessionID:    run.SessionID,
		ItemImageID:  &itemImageID,
		ContextID:    contextID,
		ImageURL:     result.ImageURL,
		Provider:     provider,
		Model:        model,
		OriginalHOCR: result.HOCR,
		OriginalText: result.PlainText,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := writeSessionHOCR(run.SessionID, "original.hocr", result.HOCR); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("persist original hocr: %w", err))
	}

	canvasURI := strings.TrimSpace(img.CanvasURI)
	if canvasURI == "" {
		canvasURI = fmt.Sprintf("%s/v1/item-images/%d/manifest/canvas/page-1", h.internalAnnotationBaseURL(), itemImageID)
		if err := h.items.UpdateImageCanvasURI(ctx, itemImageID, canvasURI); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("persist canvas uri: %w", err))
		}
	}
	annotationScopeID := fmt.Sprintf("item-image-%d", itemImageID)
	if run.ItemImageID != nil {
		annotationScopeID = fmt.Sprintf("item-image-%d", *run.ItemImageID)
	}
	lines, err := hocr.ParseHOCRLines(result.HOCR)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("parse reprocessed hocr: %w", err))
	}
	annotationItems := buildLineAnnotations(annotationScopeID, canvasURI, lines)
	if _, err := h.replaceAnnotationItems(ctx, canvasURI, annotationItems); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("replace annotations: %w", err))
	}
	h.publishEvent("dev.scribe.annotations.created", subjectForItemImage(itemImageID), map[string]any{
		"itemImageId":      itemImageID,
		"canvasUri":        canvasURI,
		"annotationCount":  len(annotationItems),
		"annotationPageId": annotationPageID(canvasURI),
	})
	if _, err := h.createTranscriptionJob(ctx, itemImageID, contextID); err != nil {
		slog.Warn("Failed to enqueue transcription job after reprocess", "item_image_id", itemImageID, "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("enqueue transcription job: %w", err))
	}

	return connect.NewResponse(&scribev1.ReprocessItemImageResponse{
		SessionId:   run.SessionID,
		ItemImageId: req.Msg.GetItemImageId(),
		ContextId:   contextIDValue,
		ImageUrl:    result.ImageURL,
		Hocr:        result.HOCR,
		PlainText:   result.PlainText,
		Provider:    provider,
		Model:       model,
	}), nil
}

func contextProviderLabel(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "gemini":
		return "Google Gemini"
	case "openai":
		return "OpenAI"
	case "ollama":
		return "Ollama"
	case "kraken":
		return "Kraken"
	case "tesseract":
		return "Tesseract"
	default:
		if provider = strings.TrimSpace(provider); provider != "" {
			return provider
		}
		return "OCR"
	}
}

func itemSourceLabel(sourceLabel, imageURL string) string {
	label := strings.TrimSpace(sourceLabel)
	if label == "" {
		label = strings.TrimSpace(imageURL)
	}
	if label == "" {
		return "OCR Item"
	}
	if parsed, err := url.Parse(label); err == nil {
		if base := path.Base(strings.TrimSpace(parsed.Path)); base != "" && base != "." && base != "/" {
			return base
		}
		if host := strings.TrimSpace(parsed.Host); host != "" {
			return host
		}
	}
	return label
}

func itemContextLabel(resolvedCtx store.Context) string {
	provider := contextProviderLabel(resolvedCtx.TranscriptionProvider)
	segmentation := strings.TrimSpace(resolvedCtx.SegmentationModel)
	if segmentation == "" {
		segmentation = "auto"
	}
	model := strings.TrimSpace(resolvedCtx.TranscriptionModel)
	if model == "" {
		model = "default"
	}
	return fmt.Sprintf("%s (%s, %s)", provider, segmentation, model)
}

func (h *Handler) createOCRItemAndImage(ctx context.Context, sourceType, imageURL, sourceURL, sourceLabel string, resolvedCtx store.Context) (store.Item, store.ItemImage, error) {
	itemID := fmt.Sprintf("item_%d", time.Now().UnixNano())
	itemName := fmt.Sprintf("%s %s", itemSourceLabel(sourceLabel, imageURL), itemContextLabel(resolvedCtx))
	item, err := h.items.Create(ctx, db.CreateItemParams{
		ID:          itemID,
		UserID:      h.currentUserID(ctx),
		WorkspaceID: h.currentWorkspaceID(ctx),
		Name:        itemName,
		SourceType:  sourceType,
		SourceURL:   sourceURL,
	})
	if err != nil {
		return store.Item{}, store.ItemImage{}, fmt.Errorf("create item: %w", err)
	}
	itemImage, err := h.items.AddImage(ctx, db.CreateItemImageParams{
		ItemID:   item.ID,
		Sequence: 0,
		ImageURL: imageURL,
	})
	if err != nil {
		return store.Item{}, store.ItemImage{}, fmt.Errorf("add item image: %w", err)
	}
	return item, itemImage, nil
}
