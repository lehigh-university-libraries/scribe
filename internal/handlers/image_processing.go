package handlers

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/lehigh-university-libraries/hOCRedit/internal/utils"
)

func (h *Handler) processImageFile(fileData []byte, filename string) (*ImageProcessResult, error) {
	return h.processImageFileWithProviderAndModel(fileData, filename, "", "")
}

func (h *Handler) processImageFileWithModel(fileData []byte, filename, model string) (*ImageProcessResult, error) {
	return h.processImageFileWithProviderAndModel(fileData, filename, "", model)
}

func (h *Handler) processImageFileWithProviderAndModel(fileData []byte, filename, provider, model string) (*ImageProcessResult, error) {
	md5Hash := utils.CalculateDataMD5(fileData)
	ext := filepath.Ext(filename)
	imageFilename := md5Hash + ext
	imageFilePath := filepath.Join("uploads", imageFilename)

	if err := os.WriteFile(imageFilePath, fileData, 0644); err != nil {
		return nil, fmt.Errorf("failed to save image: %w", err)
	}

	slog.Info("Image saved", "filename", imageFilename, "md5", md5Hash)

	width, height := utils.GetImageDimensions(imageFilePath)
	hocrXML, err := h.processHOCRWithProviderAndModel(imageFilePath, md5Hash, provider, model)
	if err != nil {
		return nil, fmt.Errorf("failed to process hOCR: %w", err)
	}

	return &ImageProcessResult{
		ImageFilename: imageFilename,
		ImageFilePath: imageFilePath,
		HOCRXML:       hocrXML,
		Width:         width,
		Height:        height,
		MD5Hash:       md5Hash,
	}, nil
}

func (h *Handler) downloadImageFromURL(imageURL string) ([]byte, string, error) {
	resp, err := http.Get(imageURL)
	if err != nil {
		return nil, "", fmt.Errorf("failed to download image: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("failed to download image: HTTP %d", resp.StatusCode)
	}

	imageData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read image data: %w", err)
	}

	contentType := resp.Header.Get("Content-Type")
	return imageData, contentType, nil
}

func (h *Handler) processImageFromURL(imageURL string) (*ImageProcessResult, error) {
	return h.processImageFromURLWithProviderAndModel(imageURL, "", "")
}

func (h *Handler) processImageFromURLWithModel(imageURL, model string) (*ImageProcessResult, error) {
	return h.processImageFromURLWithProviderAndModel(imageURL, "", model)
}

func (h *Handler) processImageFromURLWithProviderAndModel(imageURL, provider, model string) (*ImageProcessResult, error) {
	// Download image from URL
	imageData, contentType, err := h.downloadImageFromURL(imageURL)
	if err != nil {
		return nil, err
	}

	return h.processImageFromDataWithProviderAndModel(imageData, contentType, imageURL, provider, model)
}

func (h *Handler) processImageFromData(imageData []byte, contentType, sourceURL string) (*ImageProcessResult, error) {
	return h.processImageFromDataWithProviderAndModel(imageData, contentType, sourceURL, "", "")
}

func (h *Handler) processImageFromDataWithModel(imageData []byte, contentType, sourceURL, model string) (*ImageProcessResult, error) {
	return h.processImageFromDataWithProviderAndModel(imageData, contentType, sourceURL, "", model)
}

func (h *Handler) processImageFromDataWithProviderAndModel(imageData []byte, contentType, sourceURL, provider, model string) (*ImageProcessResult, error) {
	// Convert JP2/TIFF images using Houdini if needed
	originalImageData := imageData
	if needsHoudiniConversion(contentType, sourceURL) {
		slog.Info("Image requires Houdini conversion", "content_type", contentType, "url", sourceURL)
		convertedData, err := h.convertImageViaHoudini(imageData, contentType)
		if err != nil {
			return nil, fmt.Errorf("failed to convert image via Houdini: %w", err)
		}
		imageData = convertedData
		contentType = "image/jpeg"
	}

	// Calculate MD5 hash of the original image data for consistent caching
	md5Hash := utils.CalculateDataMD5(originalImageData)

	// Determine file extension from content type
	ext := h.getFileExtension(contentType, sourceURL)

	if err := h.ensureUploadsDir(); err != nil {
		return nil, fmt.Errorf("failed to create uploads directory: %w", err)
	}

	imageFilename := md5Hash + ext
	imageFilePath := filepath.Join("uploads", imageFilename)

	// Save image file
	if err := os.WriteFile(imageFilePath, imageData, 0644); err != nil {
		return nil, fmt.Errorf("failed to save image: %w", err)
	}

	slog.Info("Image processed and saved", "filename", imageFilename, "md5", md5Hash, "source", sourceURL)

	// Get image dimensions
	width, height := utils.GetImageDimensions(imageFilePath)

	// Process hOCR
	hocrXML, err := h.processHOCRWithProviderAndModel(imageFilePath, md5Hash, provider, model)
	if err != nil {
		return nil, fmt.Errorf("failed to process hOCR: %w", err)
	}

	return &ImageProcessResult{
		ImageFilename: imageFilename,
		ImageFilePath: imageFilePath,
		HOCRXML:       hocrXML,
		Width:         width,
		Height:        height,
		MD5Hash:       md5Hash,
	}, nil
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
			ext = urlExt
		}
	}
	return ext
}

func (h *Handler) processHOCR(imageFilePath, md5Hash string) (string, error) {
	return h.processHOCRWithProviderAndModel(imageFilePath, md5Hash, "", "")
}

func (h *Handler) processHOCRWithModel(imageFilePath, md5Hash, model string) (string, error) {
	return h.processHOCRWithProviderAndModel(imageFilePath, md5Hash, "", model)
}

func (h *Handler) processHOCRWithProviderAndModel(imageFilePath, md5Hash, provider, model string) (string, error) {
	hocrFilename := buildHOCRCacheFilename(md5Hash, model)
	hocrFilePath := filepath.Join("uploads", hocrFilename)

	// Check cache first
	if _, err := os.Stat(hocrFilePath); err == nil {
		hocrData, err := os.ReadFile(hocrFilePath)
		if err != nil {
			slog.Warn("Failed to read existing hOCR file", "error", err, "path", hocrFilePath)
		} else {
			slog.Info("Using cached hOCR", "filename", hocrFilename)
			return string(hocrData), nil
		}
	}

	// Generate new hOCR
	var hocrXML string
	var err error
	if strings.TrimSpace(provider) != "" || strings.TrimSpace(model) != "" {
		hocrXML, err = h.getOCRForImageWithProviderAndModel(imageFilePath, provider, model)
	} else {
		hocrXML, err = h.getOCRForImage(imageFilePath)
	}
	if err != nil {
		return "", fmt.Errorf("failed to process image with OCR: %w", err)
	}

	// Cache the result
	if err := os.WriteFile(hocrFilePath, []byte(hocrXML), 0644); err != nil {
		slog.Warn("Failed to save hOCR file", "error", err)
	} else {
		slog.Info("hOCR cached", "filename", hocrFilename)
	}

	return hocrXML, nil
}

func buildHOCRCacheFilename(imageHash, model string) string {
	normalizedModel := strings.TrimSpace(strings.ToLower(model))
	if normalizedModel == "" {
		return imageHash + ".xml"
	}

	modelHash := md5.Sum([]byte(normalizedModel))
	return imageHash + "_" + hex.EncodeToString(modelHash[:8]) + ".xml"
}

func (h *Handler) extractFilenameFromURL(imageURL, md5Hash string) string {
	if urlParts := strings.Split(imageURL, "/"); len(urlParts) > 0 {
		lastPart := urlParts[len(urlParts)-1]
		if lastPart != "" && strings.Contains(lastPart, ".") {
			return strings.TrimSuffix(lastPart, filepath.Ext(lastPart))
		}
	}
	return md5Hash
}

func (h *Handler) createSessionFromURL(imageURL string) (string, error) {
	result, err := h.processImageFromURL(imageURL)
	if err != nil {
		return "", err
	}

	// Extract filename from URL or use md5 hash
	filename := h.extractFilenameFromURL(imageURL, result.MD5Hash)
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

// convertImageViaHoudini converts JP2/TIFF images to JPG using Houdini service
func (h *Handler) convertImageViaHoudini(imageData []byte, contentType string) ([]byte, error) {

	hash := md5.Sum(imageData)
	cacheKey := hex.EncodeToString(hash[:])
	cacheFilename := cacheKey + "_converted.jpg"
	cacheDir := "cache/houdini"
	cachePath := filepath.Join(cacheDir, cacheFilename)

	// Check cache first
	if cachedData, err := os.ReadFile(cachePath); err == nil {
		slog.Info("Using cached Houdini conversion", "cache_key", cacheKey)
		return cachedData, nil
	}
	// Create cache directory
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		slog.Warn("Failed to create Houdini cache directory", "error", err)
	}

	// Convert to grayscale, enhance contrast, and apply morphological operations
	cmd := exec.Command("magick", "-", cachePath)
	cmd.Stdin = bytes.NewReader(imageData)
	slog.Info("Converting image", "cmd", cmd.String())
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("imagemagick preprocessing failed: %w", err)
	}

	convertedData, err := os.ReadFile(cachePath)
	if err != nil {
		return nil, err
	}

	return convertedData, nil
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
