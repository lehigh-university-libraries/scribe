package handlers

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/hocr"
	"github.com/lehigh-university-libraries/scribe/internal/models"
	"github.com/lehigh-university-libraries/scribe/internal/utils"
)

type ProcessResult struct {
	SessionID string `json:"session_id"`
	HOCR      string `json:"hocr"`
	PlainText string `json:"plain_text"`
	ImageURL  string `json:"image_url"`
	Provider  string `json:"provider,omitempty"`
	Model     string `json:"model,omitempty"`
}

func (h *Handler) ProcessImageURL(imageURL string) (*ProcessResult, error) {
	return h.ProcessImageURLWithProviderAndModel(imageURL, "", "")
}

func (h *Handler) ProcessImageURLWithModel(imageURL, model string) (*ProcessResult, error) {
	return h.ProcessImageURLWithProviderAndModel(imageURL, "", model)
}

func (h *Handler) ProcessImageURLWithProviderAndModel(imageURL, provider, model string) (*ProcessResult, error) {
	result, err := h.processImageFromURLWithProviderAndModel(imageURL, provider, model)
	if err != nil {
		return nil, err
	}

	filename := h.extractFilenameFromURL(imageURL, result.MD5Hash)
	sessionID := fmt.Sprintf("%s_%d", filename, time.Now().Unix())
	session := h.createImageSession(sessionID, result, SessionConfig{Model: model})
	h.sessionStore.Set(sessionID, session)

	return h.buildProcessResult(sessionID)
}

func (h *Handler) ProcessImageUpload(filename string, fileData []byte) (*ProcessResult, error) {
	return h.ProcessImageUploadWithProviderAndModel(filename, fileData, "", "")
}

func (h *Handler) ProcessImageUploadWithModel(filename string, fileData []byte, model string) (*ProcessResult, error) {
	return h.ProcessImageUploadWithProviderAndModel(filename, fileData, "", model)
}

func (h *Handler) ProcessImageUploadWithProviderAndModel(filename string, fileData []byte, provider, model string) (*ProcessResult, error) {
	if err := h.ensureUploadsDir(); err != nil {
		return nil, fmt.Errorf("create uploads dir: %w", err)
	}

	result, err := h.processImageFileWithProviderAndModel(fileData, filename, provider, model)
	if err != nil {
		return nil, err
	}

	baseFilename := strings.TrimSuffix(filename, filepath.Ext(filename))
	sessionID := fmt.Sprintf("%s_%d", baseFilename, time.Now().Unix())

	session := h.createImageSession(sessionID, result, SessionConfig{Model: model})
	h.sessionStore.Set(sessionID, session)

	return h.buildProcessResult(sessionID)
}

func (h *Handler) StoreUploadedImage(filename string, fileData []byte) (string, error) {
	if err := h.ensureUploadsDir(); err != nil {
		return "", fmt.Errorf("create uploads dir: %w", err)
	}

	md5Hash := utils.CalculateDataMD5(fileData)
	ext := filepath.Ext(filename)
	if ext == "" {
		ext = ".jpg"
	}

	imageFilename := md5Hash + ext
	imageFilePath := filepath.Join("uploads", imageFilename)
	if err := os.WriteFile(imageFilePath, fileData, 0644); err != nil {
		return "", fmt.Errorf("save uploaded image: %w", err)
	}

	return "/static/uploads/" + imageFilename, nil
}

func (h *Handler) buildProcessResult(sessionID string) (*ProcessResult, error) {
	session, exists := h.sessionStore.Get(sessionID)
	if !exists || len(session.Images) == 0 {
		return nil, fmt.Errorf("session %q not found", sessionID)
	}

	hocrXML := session.Images[0].OriginalHOCR
	plainText, err := HOCRToPlainText(hocrXML)
	if err != nil {
		return nil, err
	}

	return &ProcessResult{
		SessionID: sessionID,
		HOCR:      hocrXML,
		PlainText: plainText,
		ImageURL:  session.Images[0].ImageURL,
	}, nil
}

func HOCRToPlainText(hocrXML string) (string, error) {
	lines, err := HOCRToLines(hocrXML)
	if err != nil {
		return "", fmt.Errorf("parse hocr lines: %w", err)
	}

	out := make([]string, 0, len(lines))
	for _, line := range lines {
		words := make([]string, 0, len(line.Words))
		for _, word := range line.Words {
			text := strings.TrimSpace(word.Text)
			if text != "" {
				words = append(words, text)
			}
		}
		if len(words) > 0 {
			out = append(out, strings.Join(words, " "))
		}
	}

	return strings.Join(out, "\n"), nil
}

func HOCRToLines(hocrXML string) ([]models.HOCRLine, error) {
	return hocr.ParseHOCRLines(hocrXML)
}
