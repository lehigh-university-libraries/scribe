package handlers

import (
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
	MD5Hash       string
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

// Response helpers
func (h *Handler) writeJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(data); err != nil {
		slog.Error("Unable to encode JSON response", "err", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

func (h *Handler) writeError(w http.ResponseWriter, message string, code int) {
	slog.Error(message)
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
	return os.MkdirAll(uploadsDir, 0755)
}

func (h *Handler) wasCacheUsed(md5Hash string) bool {
	hocrFilename := md5Hash + ".xml"
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

func (h *Handler) getOCRForImage(imagePath string) (string, error) {
	// Use the simplified OCR service that bundles word detection + ChatGPT transcription
	return h.hocrService.ProcessImageToHOCR(imagePath)
}

func (h *Handler) getOCRForImageWithModel(imagePath, model string) (string, error) {
	return h.hocrService.ProcessImageToHOCRWithModel(imagePath, model)
}

func (h *Handler) getOCRForImageWithProviderAndModel(imagePath, provider, model string) (string, error) {
	return h.hocrService.ProcessImageToHOCRWithProviderAndModel(imagePath, provider, model)
}

func (h *Handler) getDetectedHOCRForImage(imagePath string) (string, error) {
	return h.hocrService.DetectLinesToHOCR(imagePath)
}

func (h *Handler) TranscribeImageRegion(imagePath string, minX, minY, maxX, maxY int, provider, model string) (string, error) {
	return h.hocrService.TranscribeRegion(imagePath, minX, minY, maxX, maxY, provider, model)
}

func (h *Handler) TranscribeImageToHOCR(imagePath, provider, model string) (string, error) {
	return h.hocrService.ProcessImageToHOCRWithProviderAndModel(imagePath, provider, model)
}

func (h *Handler) TranscribeImageFile(imagePath, provider, model string) (string, error) {
	return h.hocrService.TranscribeImage(imagePath, provider, model)
}

// ProcessImageURLWithContext downloads an image, runs the full segmentation+transcription
// pipeline defined by pctx, and returns a ProcessResult with complete hOCR.
// Unlike ProcessImageURLWithProviderAndModel this does not use the detection-only cache
// and does not require a separate async transcription step.
func (h *Handler) ProcessImageURLWithContext(imageURL string, pctx hocr.ProcessingContext) (*ProcessResult, error) {
	if err := h.ensureUploadsDir(); err != nil {
		return nil, fmt.Errorf("create uploads dir: %w", err)
	}
	imageData, contentType, err := h.downloadImageFromURL(imageURL)
	if err != nil {
		return nil, err
	}
	return h.processDataWithContext(imageData, contentType, imageURL, imageURL, pctx)
}

// ProcessImageUploadWithContext saves uploaded image bytes, runs the full
// segmentation+transcription pipeline defined by pctx, and returns a ProcessResult
// with complete hOCR.
func (h *Handler) ProcessImageUploadWithContext(filename string, fileData []byte, pctx hocr.ProcessingContext) (*ProcessResult, error) {
	if err := h.ensureUploadsDir(); err != nil {
		return nil, fmt.Errorf("create uploads dir: %w", err)
	}
	return h.processDataWithContext(fileData, "", filename, "", pctx)
}

// processDataWithContext is the shared implementation for ProcessImageURLWithContext
// and ProcessImageUploadWithContext.
func (h *Handler) processDataWithContext(imageData []byte, contentType, filename, sourceURL string, pctx hocr.ProcessingContext) (*ProcessResult, error) {
	if needsHoudiniConversion(contentType, sourceURL) {
		converted, err := h.convertImageViaHoudini(imageData, contentType)
		if err != nil {
			return nil, fmt.Errorf("convert image: %w", err)
		}
		imageData = converted
		contentType = "image/jpeg"
	}

	md5Hash := utils.CalculateDataMD5(imageData)
	ext := h.getFileExtension(contentType, filename)
	imageFilename := md5Hash + ext
	imageFilePath := filepath.Join("uploads", imageFilename)

	if err := os.WriteFile(imageFilePath, imageData, 0644); err != nil {
		return nil, fmt.Errorf("save image: %w", err)
	}

	imageLocalURL := "/static/uploads/" + imageFilename
	width, height := utils.GetImageDimensions(imageFilePath)

	hocrXML, provider, model, err := h.hocrService.ProcessImageWithContext(imageFilePath, pctx)
	if err != nil {
		return nil, fmt.Errorf("process image with context: %w", err)
	}

	plainText, err := HOCRToPlainText(hocrXML)
	if err != nil {
		return nil, fmt.Errorf("hocr to plain text: %w", err)
	}

	baseFilename := strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename))
	sessionID := fmt.Sprintf("%s_%d", baseFilename, time.Now().Unix())
	session := h.createImageSession(sessionID, &ImageProcessResult{
		ImageFilename: imageFilename,
		ImageFilePath: imageFilePath,
		HOCRXML:       hocrXML,
		Width:         width,
		Height:        height,
		MD5Hash:       md5Hash,
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
