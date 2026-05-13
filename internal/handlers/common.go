package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/hocr"
	"github.com/lehigh-university-libraries/scribe/internal/models"
	"github.com/lehigh-university-libraries/scribe/internal/storage"
	"github.com/lehigh-university-libraries/scribe/internal/uploadblob"
	"github.com/lehigh-university-libraries/scribe/internal/utils"
)

type Handler struct {
	sessionStore *storage.SessionStore
	hocrService  *hocr.Service
}

type ImageProcessResult struct {
	ImageFilename string
	ImageFilePath string
	HOCRXML       string
	Width         int
	Height        int
	ContentHash   string
}

type SessionConfig struct {
	Model       string
	Prompt      string
	Temperature float64
	Prefix      string
}

func New() *Handler {
	return &Handler{
		sessionStore: storage.New(),
		hocrService:  hocr.NewService(),
	}
}

func (h *Handler) SetProviderCallAuditLogger(logger hocr.ProviderCallAuditLogger) {
	if h == nil || h.hocrService == nil {
		return
	}
	h.hocrService.SetProviderCallAuditLogger(logger)
}

// Response helpers
func (h *Handler) writeJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(data); err != nil {
		slog.Error("Unable to encode JSON response", "err", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

func (h *Handler) writeError(w http.ResponseWriter, message string, code int) {
	slog.Error("request failed", "status", code)
	http.Error(w, message, code)
}

// Session helpers
func (h *Handler) getSessionOrError(w http.ResponseWriter, sessionID string) (*models.CorrectionSession, bool) {
	session, exists := h.sessionStore.Get(sessionID)
	if !exists {
		h.writeError(w, "Session not found", http.StatusNotFound)
		return nil, false
	}
	return session, true
}

// File operation helpers
func (h *Handler) ensureUploadsDir() error {
	uploadsDir := "uploads"
	return os.MkdirAll(uploadsDir, 0o750)
}

func (h *Handler) saveUploadedImageBytes(ctx context.Context, imageFilename string, imageData []byte, contentType string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	imageFilePath := filepath.Join("uploads", imageFilename)
	if err := os.WriteFile(imageFilePath, imageData, 0o600); err != nil {
		return "", fmt.Errorf("save image: %w", err)
	}
	if err := uploadblob.Put(ctx, imageFilename, imageData, contentType); err != nil {
		return "", fmt.Errorf("save image to shared upload store: %w", err)
	}
	return imageFilePath, nil
}

func (h *Handler) wasCacheUsed(contentHash string) bool {
	hocrFilename := contentHash + ".xml"
	hocrFilePath := filepath.Join("uploads", hocrFilename)
	_, err := os.Stat(hocrFilePath)
	return err == nil
}

func (h *Handler) createImageSession(sessionID string, result *ImageProcessResult, config SessionConfig) *models.CorrectionSession {
	session := &models.CorrectionSession{
		ID:        sessionID,
		Images:    []models.ImageItem{},
		Current:   0,
		CreatedAt: time.Now(),
		Config: models.EvalConfig{
			Model:       config.Model,
			Prompt:      config.Prompt,
			Temperature: config.Temperature,
			Timestamp:   time.Now().Format("2006-01-02_15-04-05"),
		},
	}

	imageItem := models.ImageItem{
		ID:            "img_1",
		ImagePath:     result.ImageFilename,
		ImageURL:      "/static/uploads/" + result.ImageFilename,
		OriginalHOCR:  result.HOCRXML,
		CorrectedHOCR: "",
		Completed:     false,
		ImageWidth:    result.Width,
		ImageHeight:   result.Height,
	}

	session.Images = []models.ImageItem{imageItem}
	return session
}

func (h *Handler) getDetectedHOCRForImage(imagePath string) (string, error) {
	return h.hocrService.DetectLinesToHOCR(imagePath)
}

func (h *Handler) TranscribeImageRegion(ctx context.Context, imagePath string, minX, minY, maxX, maxY int, provider, model string) (string, error) {
	return h.hocrService.TranscribeRegionWithContext(ctx, imagePath, minX, minY, maxX, maxY, provider, model)
}

func (h *Handler) TranscribeImageToHOCR(imagePath, provider, model string) (string, error) {
	return h.hocrService.ProcessImageToHOCRWithProviderAndModel(imagePath, provider, model)
}

func (h *Handler) TranscribeImageFile(imagePath, provider, model string) (string, error) {
	return h.hocrService.TranscribeImage(imagePath, provider, model)
}

func (h *Handler) TranscribeImageFileWithContext(ctx context.Context, imagePath, provider, model string) (string, error) {
	return h.hocrService.TranscribeImageWithContext(ctx, imagePath, provider, model)
}

// ProcessImageURLWithContext downloads an image, runs the full segmentation+transcription
// pipeline defined by pctx, and returns a ProcessResult with complete hOCR.
// Unlike ProcessImageURLWithProviderAndModel this does not use the detection-only cache
// and does not require a separate async transcription step.
func (h *Handler) ProcessImageURLWithContext(ctx context.Context, imageURL string, pctx hocr.ProcessingContext) (*ProcessResult, error) {
	if err := h.ensureUploadsDir(); err != nil {
		return nil, fmt.Errorf("create uploads dir: %w", err)
	}
	imageData, contentType, err := h.downloadImageFromURL(ctx, imageURL)
	if err != nil {
		return nil, err
	}
	return h.processDataWithContext(ctx, imageData, contentType, imageURL, imageURL, pctx)
}

// ProcessImageUploadWithContext saves uploaded image bytes, runs the full
// segmentation+transcription pipeline defined by pctx, and returns a ProcessResult
// with complete hOCR.
func (h *Handler) ProcessImageUploadWithContext(ctx context.Context, filename string, fileData []byte, pctx hocr.ProcessingContext) (*ProcessResult, error) {
	if err := h.ensureUploadsDir(); err != nil {
		return nil, fmt.Errorf("create uploads dir: %w", err)
	}
	return h.processDataWithContext(ctx, fileData, "", filename, "", pctx)
}

// processDataWithContext is the shared implementation for ProcessImageURLWithContext
// and ProcessImageUploadWithContext.
func (h *Handler) processDataWithContext(ctx context.Context, imageData []byte, contentType, filename, sourceURL string, pctx hocr.ProcessingContext) (*ProcessResult, error) {
	if err := validateUploadedImageData(imageData); err != nil {
		return nil, err
	}

	if needsHoudiniConversion(contentType, sourceURL) {
		converted, err := h.convertImageViaHoudini(ctx, imageData, contentType)
		if err != nil {
			return nil, fmt.Errorf("convert image: %w", err)
		}
		imageData = converted
		contentType = "image/jpeg"
		if err := validateUploadedImageData(imageData); err != nil {
			return nil, err
		}
	}

	contentHash := utils.CalculateDataHash(imageData)
	ext := h.getFileExtension(contentType, filename)
	imageFilename := contentHash + ext
	imageFilePath, err := h.saveUploadedImageBytes(ctx, imageFilename, imageData, contentType)
	if err != nil {
		return nil, err
	}

	imageLocalURL := "/static/uploads/" + imageFilename
	width, height := utils.GetImageDimensions(imageFilePath)

	baseFilename := strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename))
	sessionID := fmt.Sprintf("%s_%d", baseFilename, time.Now().Unix())
	ctx = hocr.WithProviderCallMetadata(ctx, sessionID, nil, nil)

	hocrXML, provider, model, err := h.hocrService.ProcessImageWithContext(ctx, imageFilePath, pctx)
	if err != nil {
		return nil, fmt.Errorf("process image with context: %w", err)
	}

	plainText, err := HOCRToPlainText(hocrXML)
	if err != nil {
		return nil, fmt.Errorf("hocr to plain text: %w", err)
	}

	session := h.createImageSession(sessionID, &ImageProcessResult{
		ImageFilename: imageFilename,
		ImageFilePath: imageFilePath,
		HOCRXML:       hocrXML,
		Width:         width,
		Height:        height,
		ContentHash:   contentHash,
	}, SessionConfig{})
	h.sessionStore.Set(sessionID, session)

	return &ProcessResult{
		SessionID: sessionID,
		HOCR:      hocrXML,
		PlainText: plainText,
		ImageURL:  imageLocalURL,
		Provider:  provider,
		Model:     model,
	}, nil
}
