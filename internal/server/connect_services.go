package server

import (
	"context"
	"database/sql"
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

func progressIDFromHeader(h map[string][]string) string {
	return strings.TrimSpace(firstHeaderValue(h, "X-Progress-ID"))
}

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

func (h *Handler) ProcessImageURL(ctx context.Context, req *connect.Request[scribev1.ProcessImageURLRequest]) (*connect.Response[scribev1.ProcessImageResponse], error) {
	progressID := progressIDFromHeader(req.Header())
	providerHeader := providerFromHeader(req.Header())
	imageURL := strings.TrimSpace(req.Msg.GetImageUrl())
	if imageURL == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("image_url is required"))
	}

	if progressID != "" {
		startProgress(progressID, "processing", "Running OCR")
		defer startProgressHeartbeat(progressID)()
	}

	resolvedCtx, err := h.resolveContext(ctx, req.Msg.GetContextId(), req.Msg.GetMetadata())
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
		// Segmentation model set: detect segments only, then enqueue a batch
		// transcription job. The client is redirected to the editor immediately
		// and the job worker transcribes segment by segment in the background.
		pctx := processingContextFromStore(resolvedCtx, "")
		pctx.SegmentOnly = true
		callCtx := hocr.WithProviderCallMetadata(ctx, "", nil, contextID)
		result, err = h.ocr.ProcessImageURLWithContext(callCtx, imageURL, pctx)
		runAsync = false
	} else {
		// Legacy path: detection-only hOCR + async LLM transcription.
		provider, model, err = h.resolveTranscriptionConfig(ctx, req.Msg.GetContextId(), req.Msg.GetMetadata(), providerHeader)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		result, err = h.ocr.ProcessImageURLWithProviderAndModel(imageURL, provider, model)
	}

	if err != nil {
		if progressID != "" {
			finishProgress(progressID, "failed", "OCR processing failed", err.Error())
		}
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if !runAsync {
		provider = result.Provider
		model = result.Model
	}
	item, itemImage, err := h.createOCRItemAndImage(ctx, "url", result.ImageURL, imageURL, imageURL, resolvedCtx)
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
		if progressID != "" {
			finishProgress(progressID, "failed", "Failed to save OCR run", err.Error())
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := writeSessionHOCR(sessionID, "original.hocr", result.HOCR); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("persist original hocr: %w", err))
	}
	if !runAsync {
		if err := h.ensureItemImageCanvasAndAnnotations(ctx, store.OCRRun{
			SessionID:    sessionID,
			ItemImageID:  &itemImage.ID,
			OriginalHOCR: result.HOCR,
		}, itemImage.ID); err != nil {
			slog.Warn("Failed to initialize item image canvas/annotations", "item_image_id", itemImage.ID, "error", err)
		}
	}
	if progressID != "" {
		finishProgress(progressID, "done", "Completed", "")
	}
	if runAsync {
		h.startAsyncTranscription(sessionID, result.ImageURL, provider, model, h.currentWorkspaceID(ctx), h.currentUserIDPtr(ctx))
	} else {
		// Segment-only path: enqueue a batch transcription job so the worker
		// transcribes annotations in the background and the editor can stream progress.
		if _, err := h.transcriptionJobs.Create(ctx, itemImage.ID, contextID); err != nil {
			slog.Warn("Failed to enqueue transcription job", "item_image_id", itemImage.ID, "error", err)
		}
	}

	return connect.NewResponse(&scribev1.ProcessImageResponse{
		ItemId:      item.ID,
		ItemImageId: itemImage.ID,
		SessionId:   sessionID,
		ImageUrl:    result.ImageURL,
		Hocr:        result.HOCR,
		PlainText:   result.PlainText,
	}), nil
}

func (h *Handler) ProcessImageUpload(ctx context.Context, req *connect.Request[scribev1.ProcessImageUploadRequest]) (*connect.Response[scribev1.ProcessImageResponse], error) {
	progressID := progressIDFromHeader(req.Header())
	providerHeader := providerFromHeader(req.Header())
	filename := strings.TrimSpace(req.Msg.GetFilename())
	if filename == "" {
		filename = "upload.jpg"
	}
	if len(req.Msg.GetImageData()) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("image_data is required"))
	}

	if progressID != "" {
		startProgress(progressID, "processing", "Running OCR")
		defer startProgressHeartbeat(progressID)()
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
		if progressID != "" {
			finishProgress(progressID, "failed", "OCR processing failed", err.Error())
		}
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
		if progressID != "" {
			finishProgress(progressID, "failed", "Failed to save OCR run", err.Error())
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := writeSessionHOCR(sessionID, "original.hocr", result.HOCR); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("persist original hocr: %w", err))
	}
	if !runAsync {
		if err := h.ensureItemImageCanvasAndAnnotations(ctx, store.OCRRun{
			SessionID:    sessionID,
			ItemImageID:  &itemImage.ID,
			OriginalHOCR: result.HOCR,
		}, itemImage.ID); err != nil {
			slog.Warn("Failed to initialize item image canvas/annotations", "item_image_id", itemImage.ID, "error", err)
		}
	}
	if progressID != "" {
		finishProgress(progressID, "done", "Completed", "")
	}
	if runAsync {
		h.startAsyncTranscription(sessionID, result.ImageURL, provider, model, h.currentWorkspaceID(ctx), h.currentUserIDPtr(ctx))
	} else {
		if _, err := h.transcriptionJobs.Create(ctx, itemImage.ID, contextID); err != nil {
			slog.Warn("Failed to enqueue transcription job", "item_image_id", itemImage.ID, "error", err)
		}
	}

	return connect.NewResponse(&scribev1.ProcessImageResponse{
		ItemId:      item.ID,
		ItemImageId: itemImage.ID,
		SessionId:   sessionID,
		ImageUrl:    result.ImageURL,
		Hocr:        result.HOCR,
		PlainText:   result.PlainText,
	}), nil
}

func (h *Handler) ProcessHOCR(ctx context.Context, req *connect.Request[scribev1.ProcessHOCRRequest]) (*connect.Response[scribev1.ProcessImageResponse], error) {
	progressID := progressIDFromHeader(req.Header())
	if progressID != "" {
		startProgress(progressID, "processing", "Processing supplied hOCR")
		defer startProgressHeartbeat(progressID)()
	}

	hocrXML := strings.TrimSpace(req.Msg.GetHocr())
	if hocrXML == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("hocr is required"))
	}

	imageURL := strings.TrimSpace(req.Msg.GetImageUrl())
	if len(req.Msg.GetImageData()) > 0 {
		filename := strings.TrimSpace(req.Msg.GetFilename())
		if filename == "" {
			filename = "upload.jpg"
		}
		storedURL, err := h.ocr.StoreUploadedImage(filename, req.Msg.GetImageData())
		if err != nil {
			if progressID != "" {
				finishProgress(progressID, "failed", "Failed to store uploaded image", err.Error())
			}
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		imageURL = storedURL
	}

	plainText, err := ocrhandlers.HOCRToPlainText(hocrXML)
	if err != nil {
		if progressID != "" {
			finishProgress(progressID, "failed", "invalid hocr", err.Error())
		}
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
		if progressID != "" {
			finishProgress(progressID, "failed", "Failed to save OCR run", err.Error())
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := writeSessionHOCR(sessionID, "original.hocr", hocrXML); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("persist original hocr: %w", err))
	}
	if progressID != "" {
		finishProgress(progressID, "done", "Completed", "")
	}

	return connect.NewResponse(&scribev1.ProcessImageResponse{
		ItemId:      item.ID,
		ItemImageId: itemImage.ID,
		SessionId:   sessionID,
		ImageUrl:    imageURL,
		Hocr:        hocrXML,
		PlainText:   plainText,
	}), nil
}

func (h *Handler) GetOCRRun(ctx context.Context, req *connect.Request[scribev1.GetOCRRunRequest]) (*connect.Response[scribev1.OCRRun], error) {
	var (
		run store.OCRRun
		err error
	)
	if req.Msg.GetItemImageId() > 0 {
		// Use the on-demand fallback: if no OCR run exists but the item_image
		// has a hocr_url (from a manifest seeAlso), fetch and cache it now.
		if _, authErr := h.itemImageForRequest(ctx, req.Msg.GetItemImageId()); authErr != nil {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("ocr run not found"))
		}
		run, err = h.fetchOrCacheHOCRRun(ctx, req.Msg.GetItemImageId())
	} else {
		run, err = h.ocrRunForRequest(ctx, req.Msg.GetSessionId(), 0)
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("ocr run not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &scribev1.OCRRun{
		SessionId:           run.SessionID,
		ImageUrl:            run.ImageURL,
		Model:               run.Model,
		OriginalHocr:        run.OriginalHOCR,
		OriginalText:        run.OriginalText,
		EditCount:           int32(run.EditCount),
		LevenshteinDistance: int32(run.LevenshteinDistance),
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
	sessionID := req.Msg.GetSessionId()
	correctedHOCR := strings.TrimSpace(req.Msg.GetCorrectedHocr())
	if correctedHOCR == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("corrected_hocr is required"))
	}

	var (
		run         store.OCRRun
		err         error
		itemImageID uint64
	)
	if req.Msg.GetItemImageId() > 0 {
		itemImageID = req.Msg.GetItemImageId()
		run, err = h.ocrRunForRequest(ctx, "", itemImageID)
		if err == nil {
			sessionID = run.SessionID
		}
	} else {
		run, err = h.ocrRunForRequest(ctx, sessionID, 0)
		if run.ItemImageID != nil {
			itemImageID = *run.ItemImageID
		}
	}
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
		sessionID,
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
	if err := writeSessionHOCR(sessionID, "corrected.hocr", correctedHOCR); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("persist corrected hocr: %w", err))
	}

	return connect.NewResponse(&scribev1.SaveOCREditsResponse{
		SessionId:           sessionID,
		ItemImageId:         itemImageID,
		EditCount:           req.Msg.GetEditCount(),
		LevenshteinDistance: int32(lev),
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
		result, err = h.ocr.ProcessImageURLWithProviderAndModel(run.ImageURL, provider, model)
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
	annotationScopeID := run.SessionID
	if run.ItemImageID != nil {
		annotationScopeID = fmt.Sprintf("item-image-%d", *run.ItemImageID)
	} else {
		annotationScopeID = fmt.Sprintf("item-image-%d", itemImageID)
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
	if !runAsync {
		if _, err := h.transcriptionJobs.Create(ctx, itemImageID, contextID); err != nil {
			slog.Warn("Failed to enqueue transcription job after reprocess", "item_image_id", itemImageID, "error", err)
		}
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
