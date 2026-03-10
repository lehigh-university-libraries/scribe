package server

import (
	"context"
	"encoding/json"
	"fmt"
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
		c, err := h.contexts.Get(ctx, contextID)
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
		c, _, err := h.contexts.Resolve(ctx, metadata)
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
	if contextID > 0 {
		return h.contexts.Get(ctx, contextID)
	}
	var metadata map[string]any
	if raw := strings.TrimSpace(metadataJSON); raw != "" {
		if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
			return store.Context{}, fmt.Errorf("invalid metadata json: %w", err)
		}
	}
	c, _, err := h.contexts.Resolve(ctx, metadata)
	return c, err
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

	seg := strings.ToLower(strings.TrimSpace(resolvedCtx.SegmentationModel))
	if seg != "" && providerHeader == "" {
		// Context has a segmentation model set: run the full synchronous pipeline
		// (segmentation + transcription in one shot, no async step needed).
		pctx := processingContextFromStore(resolvedCtx, "")
		result, err = h.ocr.ProcessImageURLWithContext(imageURL, pctx)
		provider = pctx.TranscriptionProvider
		model = pctx.TranscriptionModel
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
	item, itemImage, err := h.createOCRItemAndImage(ctx, "url", result.ImageURL, imageURL)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	sessionID := item.ID
	var contextID *uint64
	if req.Msg.GetContextId() > 0 {
		v := req.Msg.GetContextId()
		contextID = &v
	}
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
	if progressID != "" {
		finishProgress(progressID, "done", "Completed", "")
	}
	if runAsync {
		h.startAsyncTranscription(sessionID, result.ImageURL, provider, model)
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

	seg := strings.ToLower(strings.TrimSpace(resolvedCtx.SegmentationModel))
	if seg != "" && providerHeader == "" {
		pctx := processingContextFromStore(resolvedCtx, "")
		result, err = h.ocr.ProcessImageUploadWithContext(filename, req.Msg.GetImageData(), pctx)
		provider = pctx.TranscriptionProvider
		model = pctx.TranscriptionModel
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
	item, itemImage, err := h.createOCRItemAndImage(ctx, "upload", result.ImageURL, "")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	sessionID := item.ID
	var contextID *uint64
	if req.Msg.GetContextId() > 0 {
		v := req.Msg.GetContextId()
		contextID = &v
	}
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
	if progressID != "" {
		finishProgress(progressID, "done", "Completed", "")
	}
	if runAsync {
		h.startAsyncTranscription(sessionID, result.ImageURL, provider, model)
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

	item, itemImage, err := h.createOCRItemAndImage(ctx, "hocr", imageURL, "")
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
		run, err = h.fetchOrCacheHOCRRun(ctx, req.Msg.GetItemImageId())
	} else {
		run, err = h.ocrRuns.Get(ctx, req.Msg.GetSessionId())
	}
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no rows") || strings.Contains(strings.ToLower(err.Error()), "not found") {
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
		run, err = h.ocrRuns.GetByItemImageID(ctx, itemImageID)
		sessionID = run.SessionID
	} else {
		run, err = h.ocrRuns.Get(ctx, sessionID)
		if run.ItemImageID != nil {
			itemImageID = *run.ItemImageID
		}
	}
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no rows") {
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

func (h *Handler) createOCRItemAndImage(ctx context.Context, sourceType, imageURL, sourceURL string) (store.Item, store.ItemImage, error) {
	itemID := fmt.Sprintf("item_%d", time.Now().UnixNano())
	itemName := "OCR Item " + time.Now().UTC().Format(time.RFC3339)
	item, err := h.items.Create(ctx, db.CreateItemParams{
		ID:         itemID,
		UserID:     store.AnonymousUserID,
		Name:       itemName,
		SourceType: sourceType,
		SourceURL:  sourceURL,
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
