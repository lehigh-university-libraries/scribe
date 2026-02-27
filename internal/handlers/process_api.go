package handlers

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/lehigh-university-libraries/hOCRedit/internal/hocr"
)

type ProcessResult struct {
	SessionID string `json:"session_id"`
	HOCR      string `json:"hocr"`
	PlainText string `json:"plain_text"`
	ImageURL  string `json:"image_url"`
}

func (h *Handler) ProcessImageURL(imageURL string) (*ProcessResult, error) {
	return h.ProcessImageURLWithModel(imageURL, "")
}

func (h *Handler) ProcessImageURLWithModel(imageURL, model string) (*ProcessResult, error) {
	result, err := h.processImageFromURLWithModel(imageURL, model)
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
	return h.ProcessImageUploadWithModel(filename, fileData, "")
}

func (h *Handler) ProcessImageUploadWithModel(filename string, fileData []byte, model string) (*ProcessResult, error) {
	if err := h.ensureUploadsDir(); err != nil {
		return nil, fmt.Errorf("create uploads dir: %w", err)
	}

	result, err := h.processImageFileWithModel(fileData, filename, model)
	if err != nil {
		return nil, err
	}

	baseFilename := strings.TrimSuffix(filename, filepath.Ext(filename))
	sessionID := fmt.Sprintf("%s_%d", baseFilename, time.Now().Unix())

	session := h.createImageSession(sessionID, result, SessionConfig{Model: model})
	h.sessionStore.Set(sessionID, session)

	return h.buildProcessResult(sessionID)
}

func (h *Handler) buildProcessResult(sessionID string) (*ProcessResult, error) {
	session, exists := h.sessionStore.Get(sessionID)
	if !exists || len(session.Images) == 0 {
		return nil, fmt.Errorf("session %q not found", sessionID)
	}

	hocrXML := session.Images[0].OriginalHOCR
	plainText, err := hocrToPlainText(hocrXML)
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

func hocrToPlainText(hocrXML string) (string, error) {
	lines, err := hocr.ParseHOCRLines(hocrXML)
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
