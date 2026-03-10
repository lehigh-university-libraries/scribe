package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/hocr"
	"github.com/lehigh-university-libraries/scribe/internal/models"
	"github.com/lehigh-university-libraries/scribe/internal/storage"
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
