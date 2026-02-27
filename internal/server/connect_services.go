package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	legacyhandlers "github.com/lehigh-university-libraries/hOCRedit/internal/handlers"
	"github.com/lehigh-university-libraries/hOCRedit/internal/metrics"
	"github.com/lehigh-university-libraries/hOCRedit/internal/store"
	hocreditv1 "github.com/lehigh-university-libraries/hOCRedit/proto/hocredit/v1"
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

func (h *Handler) ListSessions(ctx context.Context, _ *connect.Request[hocreditv1.ListSessionsRequest]) (*connect.Response[hocreditv1.ListSessionsResponse], error) {
	sessions, err := h.sessions.List(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &hocreditv1.ListSessionsResponse{
		Sessions: make([]*hocreditv1.Session, 0, len(sessions)),
	}
	for _, s := range sessions {
		resp.Sessions = append(resp.Sessions, &hocreditv1.Session{
			Id:        s.ID,
			Name:      s.Name,
			CreatedAt: s.CreatedAt.UTC().Format(time.RFC3339),
			UpdatedAt: s.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	return connect.NewResponse(resp), nil
}

func (h *Handler) CreateSession(ctx context.Context, req *connect.Request[hocreditv1.CreateSessionRequest]) (*connect.Response[hocreditv1.CreateSessionResponse], error) {
	id := strings.TrimSpace(req.Msg.GetId())
	name := strings.TrimSpace(req.Msg.GetName())
	if id == "" {
		id = time.Now().UTC().Format("20060102150405")
	}
	if name == "" {
		name = "Untitled Session"
	}
	session, err := h.sessions.Create(ctx, id, name)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&hocreditv1.CreateSessionResponse{
		Session: &hocreditv1.Session{
			Id:        session.ID,
			Name:      session.Name,
			CreatedAt: session.CreatedAt.UTC().Format(time.RFC3339),
			UpdatedAt: session.UpdatedAt.UTC().Format(time.RFC3339),
		},
	}), nil
}

func (h *Handler) ProcessImageURL(ctx context.Context, req *connect.Request[hocreditv1.ProcessImageURLRequest]) (*connect.Response[hocreditv1.ProcessImageResponse], error) {
	progressID := progressIDFromHeader(req.Header())
	provider := providerFromHeader(req.Header())
	model := strings.TrimSpace(req.Msg.GetModel())
	imageURL := strings.TrimSpace(req.Msg.GetImageUrl())
	if imageURL == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("image_url is required"))
	}

	if progressID != "" {
		startProgress(progressID, "processing", "Running OCR")
		defer startProgressHeartbeat(progressID)()
	}

	result, err := h.legacy.ProcessImageURLWithProviderAndModel(imageURL, provider, model)
	if err != nil {
		if progressID != "" {
			finishProgress(progressID, "failed", "OCR processing failed", err.Error())
		}
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := h.ocrRuns.Create(ctx, store.OCRRun{
		SessionID:    result.SessionID,
		ImageURL:     result.ImageURL,
		Provider:     effectiveProvider(provider),
		Model:        effectiveModel(provider, model),
		OriginalHOCR: result.HOCR,
		OriginalText: result.PlainText,
	}); err != nil {
		if progressID != "" {
			finishProgress(progressID, "failed", "Failed to save OCR run", err.Error())
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if progressID != "" {
		finishProgress(progressID, "done", "Completed", "")
	}

	return connect.NewResponse(&hocreditv1.ProcessImageResponse{
		SessionId: result.SessionID,
		ImageUrl:  result.ImageURL,
		Hocr:      result.HOCR,
		PlainText: result.PlainText,
	}), nil
}

func (h *Handler) ProcessImageUpload(ctx context.Context, req *connect.Request[hocreditv1.ProcessImageUploadRequest]) (*connect.Response[hocreditv1.ProcessImageResponse], error) {
	progressID := progressIDFromHeader(req.Header())
	provider := providerFromHeader(req.Header())
	model := strings.TrimSpace(req.Msg.GetModel())
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

	result, err := h.legacy.ProcessImageUploadWithProviderAndModel(filename, req.Msg.GetImageData(), provider, model)
	if err != nil {
		if progressID != "" {
			finishProgress(progressID, "failed", "OCR processing failed", err.Error())
		}
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := h.ocrRuns.Create(ctx, store.OCRRun{
		SessionID:    result.SessionID,
		ImageURL:     result.ImageURL,
		Provider:     effectiveProvider(provider),
		Model:        effectiveModel(provider, model),
		OriginalHOCR: result.HOCR,
		OriginalText: result.PlainText,
	}); err != nil {
		if progressID != "" {
			finishProgress(progressID, "failed", "Failed to save OCR run", err.Error())
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if progressID != "" {
		finishProgress(progressID, "done", "Completed", "")
	}

	return connect.NewResponse(&hocreditv1.ProcessImageResponse{
		SessionId: result.SessionID,
		ImageUrl:  result.ImageURL,
		Hocr:      result.HOCR,
		PlainText: result.PlainText,
	}), nil
}

func (h *Handler) ProcessHOCR(ctx context.Context, req *connect.Request[hocreditv1.ProcessHOCRRequest]) (*connect.Response[hocreditv1.ProcessImageResponse], error) {
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
		storedURL, err := h.legacy.StoreUploadedImage(filename, req.Msg.GetImageData())
		if err != nil {
			if progressID != "" {
				finishProgress(progressID, "failed", "Failed to store uploaded image", err.Error())
			}
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		imageURL = storedURL
	}

	plainText, err := legacyhandlers.HOCRToPlainText(hocrXML)
	if err != nil {
		if progressID != "" {
			finishProgress(progressID, "failed", "invalid hocr", err.Error())
		}
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid hocr"))
	}

	sessionID := fmt.Sprintf("hocr_%d", time.Now().UnixNano())
	if err := h.ocrRuns.Create(ctx, store.OCRRun{
		SessionID:    sessionID,
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
	if progressID != "" {
		finishProgress(progressID, "done", "Completed", "")
	}

	return connect.NewResponse(&hocreditv1.ProcessImageResponse{
		SessionId: sessionID,
		ImageUrl:  imageURL,
		Hocr:      hocrXML,
		PlainText: plainText,
	}), nil
}

func (h *Handler) GetOCRRun(ctx context.Context, req *connect.Request[hocreditv1.GetOCRRunRequest]) (*connect.Response[hocreditv1.OCRRun], error) {
	run, err := h.ocrRuns.Get(ctx, req.Msg.GetSessionId())
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no rows") {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("session not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &hocreditv1.OCRRun{
		SessionId:           run.SessionID,
		ImageUrl:            run.ImageURL,
		Model:               run.Model,
		OriginalHocr:        run.OriginalHOCR,
		OriginalText:        run.OriginalText,
		EditCount:           int32(run.EditCount),
		LevenshteinDistance: int32(run.LevenshteinDistance),
	}
	if run.CorrectedHOCR != nil {
		resp.CorrectedHocr = *run.CorrectedHOCR
	}
	if run.CorrectedText != nil {
		resp.CorrectedText = *run.CorrectedText
	}
	return connect.NewResponse(resp), nil
}

func (h *Handler) SaveOCREdits(ctx context.Context, req *connect.Request[hocreditv1.SaveOCREditsRequest]) (*connect.Response[hocreditv1.SaveOCREditsResponse], error) {
	sessionID := req.Msg.GetSessionId()
	correctedHOCR := strings.TrimSpace(req.Msg.GetCorrectedHocr())
	if correctedHOCR == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("corrected_hocr is required"))
	}

	run, err := h.ocrRuns.Get(ctx, sessionID)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no rows") {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("session not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	correctedText, err := legacyhandlers.HOCRToPlainText(correctedHOCR)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid corrected_hocr"))
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

	return connect.NewResponse(&hocreditv1.SaveOCREditsResponse{
		SessionId:           sessionID,
		EditCount:           req.Msg.GetEditCount(),
		LevenshteinDistance: int32(lev),
		CorrectedPlainText:  correctedText,
		OriginalPlainText:   run.OriginalText,
	}), nil
}
