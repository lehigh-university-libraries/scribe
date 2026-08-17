package handlers

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/lehigh-university-libraries/scribe/internal/hocr"
	"github.com/lehigh-university-libraries/scribe/internal/utils"
)

type ProcessResult struct {
	SessionID string `json:"session_id"`
	HOCR      string `json:"hocr"`
	PlainText string `json:"plain_text"`
	ImageURL  string `json:"image_url"`
	// StoredBytes is the exact physical size when ImageURL identifies an
	// immutable Scribe-owned upload. It is zero when the result deliberately
	// retains an externally owned image reference; it is never an estimate of
	// bytes fetched from that remote URL.
	StoredBytes uint64         `json:"stored_bytes"`
	Provider    string         `json:"provider,omitempty"`
	Model       string         `json:"model,omitempty"`
	ParsedHOCR  *hocr.Document `json:"-"`
}

func (h *Handler) StoreUploadedImage(ctx context.Context, _ string, fileData []byte) (string, error) {
	if err := h.ensureUploadsDir(); err != nil {
		return "", markUploadProcessingFailure(ErrUploadStorageFailure, fmt.Errorf("create uploads dir: %w", err))
	}
	if err := validateUploadedImageData(fileData); err != nil {
		return "", err
	}

	contentType, ext, err := canonicalImageMediaType(fileData)
	if err != nil {
		return "", err
	}

	imageFilename := immutableUploadName(fileData, ext)
	imagePath, err := h.saveUploadedImageBytes(ctx, imageFilename, fileData, contentType)
	if err != nil {
		return "", markUploadProcessingFailure(ErrUploadStorageFailure, fmt.Errorf("save uploaded image: %w", err))
	}
	if err := removeLocalUploadAfterSharedCommit(imagePath); err != nil {
		cleanupCtx, cancel := uploadCleanupContext(ctx)
		cleanupErr := h.deleteUploadedImage(cleanupCtx, imageFilename)
		cancel()
		return "", markUploadProcessingFailure(ErrUploadStorageFailure, &StoredUploadError{
			ImageURL:    "/static/uploads/" + imageFilename,
			StoredBytes: uint64(len(fileData)),
			Err:         errors.Join(err, cleanupErr),
		})
	}

	return "/static/uploads/" + imageFilename, nil
}

func immutableUploadName(data []byte, extension string) string {
	return utils.CalculateDataHash(data) + "-" + uuid.NewString() + extension
}

func HOCRToPlainText(hocrXML string) (string, error) {
	document, err := hocr.ParseDocument(hocrXML)
	if err != nil {
		return "", fmt.Errorf("parse hocr: %w", err)
	}
	return hocr.PlainText(document.Lines), nil
}
