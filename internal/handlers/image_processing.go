package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/safefile"
	"github.com/lehigh-university-libraries/scribe/internal/safehttp"
	"github.com/lehigh-university-libraries/scribe/internal/utils"
)

const (
	maxRemoteImageBytes   int64 = 100 << 20
	maxRemoteHOCRBytes    int64 = 10 << 20
	maxUploadedImageBytes       = maxRemoteImageBytes
)

func (h *Handler) processImageFile(fileData []byte, filename string) (*ImageProcessResult, error) {
	return h.processImageFileWithProviderAndModel(fileData, filename, "", "")
}

func (h *Handler) processImageFileWithProviderAndModel(fileData []byte, filename, provider, model string) (*ImageProcessResult, error) {
	if err := validateUploadedImageData(fileData); err != nil {
		return nil, err
	}

	contentHash := utils.CalculateDataHash(fileData)
	ext := safeImageExtension(filepath.Ext(filename))
	imageFilename := contentHash + ext
	imageFilePath, err := h.saveUploadedImageBytes(context.Background(), imageFilename, fileData, "")
	if err != nil {
		return nil, fmt.Errorf("failed to save image: %w", err)
	}

	slog.Info("Image saved", "filename", imageFilename, "content_hash", contentHash)

	width, height := utils.GetImageDimensions(imageFilePath)
	hocrXML, err := h.processHOCRWithProviderAndModel(imageFilePath, contentHash, provider, model)
	if err != nil {
		return nil, fmt.Errorf("failed to process hOCR: %w", err)
	}

	return &ImageProcessResult{
		ImageFilename: imageFilename,
		ImageFilePath: imageFilePath,
		HOCRXML:       hocrXML,
		Width:         width,
		Height:        height,
		ContentHash:   contentHash,
	}, nil
}

func (h *Handler) downloadImageFromURL(ctx context.Context, imageURL string) ([]byte, string, error) {
	resp, err := safehttp.Get(ctx, imageURL)
	if err != nil {
		return nil, "", fmt.Errorf("failed to download image: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("failed to download image: HTTP %d", resp.StatusCode)
	}

	imageData, err := safehttp.ReadAllLimit(resp.Body, maxRemoteImageBytes)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read image data: %w", err)
	}

	contentType := resp.Header.Get("Content-Type")
	return imageData, contentType, nil
}

func (h *Handler) processImageFromURL(imageURL string) (*ImageProcessResult, error) {
	return h.processImageFromURLWithProviderAndModel(imageURL, "", "")
}

func (h *Handler) processImageFromURLWithProviderAndModel(imageURL, provider, model string) (*ImageProcessResult, error) {
	return h.processImageFromURLWithContext(context.Background(), imageURL, provider, model)
}

func (h *Handler) processImageFromURLWithContext(ctx context.Context, imageURL, provider, model string) (*ImageProcessResult, error) {
	// Download image from URL
	imageData, contentType, err := h.downloadImageFromURL(ctx, imageURL)
	if err != nil {
		return nil, err
	}

	return h.processImageFromDataWithContext(ctx, imageData, contentType, imageURL, provider, model)
}

func (h *Handler) processImageFromDataWithContext(ctx context.Context, imageData []byte, contentType, sourceURL, provider, model string) (*ImageProcessResult, error) {
	if err := validateUploadedImageData(imageData); err != nil {
		return nil, err
	}

	// Convert JP2/TIFF images using Houdini if needed
	originalImageData := imageData
	if needsHoudiniConversion(contentType, sourceURL) {
		slog.Info("Image requires Houdini conversion", "content_type", contentType, "url", sourceURL)
		convertedData, err := h.convertImageViaHoudini(ctx, imageData, contentType)
		if err != nil {
			return nil, fmt.Errorf("failed to convert image via Houdini: %w", err)
		}
		imageData = convertedData
		contentType = "image/jpeg"
		if err := validateUploadedImageData(imageData); err != nil {
			return nil, err
		}
	}

	contentHash := utils.CalculateDataHash(originalImageData)

	// Determine file extension from content type
	ext := h.getFileExtension(contentType, sourceURL)

	if err := h.ensureUploadsDir(); err != nil {
		return nil, fmt.Errorf("failed to create uploads directory: %w", err)
	}

	imageFilename := contentHash + ext
	imageFilePath, err := h.saveUploadedImageBytes(ctx, imageFilename, imageData, contentType)
	if err != nil {
		return nil, fmt.Errorf("failed to save image: %w", err)
	}

	slog.Info("Image processed and saved", "filename", imageFilename, "content_hash", contentHash, "source", sourceURL)

	// Get image dimensions
	width, height := utils.GetImageDimensions(imageFilePath)

	// Process hOCR
	hocrXML, err := h.processHOCRWithProviderAndModel(imageFilePath, contentHash, provider, model)
	if err != nil {
		return nil, fmt.Errorf("failed to process hOCR: %w", err)
	}

	return &ImageProcessResult{
		ImageFilename: imageFilename,
		ImageFilePath: imageFilePath,
		HOCRXML:       hocrXML,
		Width:         width,
		Height:        height,
		ContentHash:   contentHash,
	}, nil
}

func validateUploadedImageData(fileData []byte) error {
	if len(fileData) == 0 {
		return fmt.Errorf("image data is required")
	}
	if int64(len(fileData)) > maxUploadedImageBytes {
		return fmt.Errorf("image data exceeds 100 MiB limit")
	}
	return nil
}

func (h *Handler) getFileExtension(contentType, sourceURL string) string {
	ext := ".jpg" // default
	switch contentType {
	case "image/png":
		ext = ".png"
	case "image/gif":
		ext = ".gif"
	case "image/webp":
		ext = ".webp"
	default:
		// Try to get extension from URL
		if urlExt := filepath.Ext(sourceURL); urlExt != "" {
			ext = safeImageExtension(urlExt)
		}
	}
	return ext
}

func safeImageExtension(ext string) string {
	switch strings.ToLower(strings.TrimSpace(ext)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".jp2", ".j2k", ".jpx", ".tif", ".tiff":
		return strings.ToLower(strings.TrimSpace(ext))
	default:
		return ".jpg"
	}
}

func (h *Handler) processHOCRWithProviderAndModel(imageFilePath, contentHash, provider, model string) (string, error) {
	_ = provider
	hocrFilename := buildHOCRBoxesCacheFilename(contentHash, model)
	hocrFilePath := filepath.Join("uploads", hocrFilename)

	// Check cache first
	if _, err := os.Stat(hocrFilePath); err == nil {
		hocrData, err := safefile.ReadFile(hocrFilePath)
		if err != nil {
			slog.Warn("Failed to read existing hOCR file", "error", err, "path", hocrFilePath)
		} else {
			slog.Info("Using cached hOCR", "filename", hocrFilename)
			return string(hocrData), nil
		}
	}

	// Generate detection-only hOCR (line boxes only). Transcription is done in editor.
	hocrXML, err := h.getDetectedHOCRForImage(imageFilePath)
	if err != nil {
		return "", fmt.Errorf("failed to process image with OCR: %w", err)
	}

	// Cache the result
	if err := os.WriteFile(hocrFilePath, []byte(hocrXML), 0o600); err != nil { // #nosec G703 -- cache filename is derived from a SHA-256 content hash and fixed suffixes.
		slog.Warn("Failed to save hOCR file", "error", err)
	} else {
		slog.Info("hOCR cached", "filename", hocrFilename)
	}

	return hocrXML, nil
}

func buildHOCRBoxesCacheFilename(imageHash, model string) string {
	normalizedModel := strings.TrimSpace(strings.ToLower(model))
	if normalizedModel == "" {
		return imageHash + "_boxes.xml"
	}

	modelHash := sha256.Sum256([]byte(normalizedModel))
	return imageHash + "_boxes_" + hex.EncodeToString(modelHash[:8]) + ".xml"
}

func (h *Handler) extractFilenameFromURL(imageURL, contentHash string) string {
	if urlParts := strings.Split(imageURL, "/"); len(urlParts) > 0 {
		lastPart := urlParts[len(urlParts)-1]
		if lastPart != "" && strings.Contains(lastPart, ".") {
			return strings.TrimSuffix(lastPart, filepath.Ext(lastPart))
		}
	}
	return contentHash
}

func (h *Handler) createSessionFromURL(imageURL string) (string, error) {
	result, err := h.processImageFromURL(imageURL)
	if err != nil {
		return "", err
	}

	filename := h.extractFilenameFromURL(imageURL, result.ContentHash)
	sessionID := fmt.Sprintf("%s_%d", filename, time.Now().Unix())

	config := SessionConfig{
		Model:       "",
		Prompt:      "",
		Temperature: 0.0,
	}

	session := h.createImageSession(sessionID, result, config)
	h.sessionStore.Set(sessionID, session)

	slog.Info("Session created from URL", "session_id", sessionID, "url", imageURL)
	return sessionID, nil
}

// needsHoudiniConversion checks if the image format requires Houdini conversion
func needsHoudiniConversion(contentType, url string) bool {
	// Check content type first
	switch contentType {
	case "image/jp2", "image/jpeg2000", "image/tiff", "image/tif":
		return true
	}

	// Check file extension from URL as fallback
	ext := strings.ToLower(filepath.Ext(url))
	switch ext {
	case ".jp2", ".jpx", ".j2k", ".tiff", ".tif":
		return true
	}

	return false
}
