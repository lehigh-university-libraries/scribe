package utils

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/lehigh-university-libraries/scribe/internal/imageservice"
	"github.com/lehigh-university-libraries/scribe/internal/safefile"
)

func CalculateFileHash(filePath string) (string, error) {
	file, err := safefile.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

func CalculateDataHash(data []byte) string {
	hash := sha256.New()
	hash.Write(data)
	return hex.EncodeToString(hash.Sum(nil))
}

func RespondWithError(w http.ResponseWriter, message string, statusCode int) {
	w.WriteHeader(statusCode)
	response := map[string]string{
		"error": message,
	}
	if err := json.NewEncoder(w).Encode(response); err != nil {
		slog.Error("Failed to encode error response", "error", err)
	}
}

func GetImageDimensions(imagePath string) (int, int) {
	file, err := safefile.Open(imagePath)
	if err == nil {
		cfg, _, decodeErr := image.DecodeConfig(file)
		_ = file.Close()
		if decodeErr == nil {
			return cfg.Width, cfg.Height
		}
		slog.Warn("Failed to decode image dimensions directly", "path", imagePath, "error", decodeErr)
	} else {
		slog.Warn("Failed to open image for dimensions", "path", imagePath, "error", err)
	}

	data, err := safefile.ReadFile(imagePath)
	if err != nil {
		slog.Warn("Failed to read image for dimension fallback", "path", imagePath, "error", err)
		return 1000, 1400
	}

	client := imageservice.New()
	if client.Enabled() {
		normalized, normalizeErr := client.Normalize(context.Background(), data, detectImageContentType(imagePath, data))
		if normalizeErr != nil {
			slog.Warn("Failed to normalize image for dimensions", "path", imagePath, "error", normalizeErr)
		} else {
			cfg, _, decodeErr := image.DecodeConfig(bytes.NewReader(normalized))
			if decodeErr == nil {
				return cfg.Width, cfg.Height
			}
			slog.Warn("Failed to decode normalized image dimensions", "path", imagePath, "error", decodeErr)
		}
	}

	return 1000, 1400
}

func detectImageContentType(imagePath string, data []byte) string {
	contentType := http.DetectContentType(data)
	if contentType != "application/octet-stream" {
		return contentType
	}
	switch strings.ToLower(filepath.Ext(imagePath)) {
	case ".jp2", ".j2k", ".jpx":
		return "image/jp2"
	case ".tif", ".tiff":
		return "image/tiff"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		return "image/jpeg"
	}
}
