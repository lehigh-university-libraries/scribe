//go:build remoteocr

package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/lehigh-university-libraries/scribe/internal/imageservice"
	"github.com/lehigh-university-libraries/scribe/internal/safefile"
)

// convertImageViaHoudini converts JP2/TIFF images to JPG using the external image service.
func (h *Handler) convertImageViaHoudini(ctx context.Context, imageData []byte, contentType string) ([]byte, error) {
	hash := sha256.Sum256(imageData)
	cacheKey := hex.EncodeToString(hash[:])
	cacheFilename := cacheKey + "_converted.jpg"
	cacheDir := "cache/houdini"
	cachePath := filepath.Join(cacheDir, cacheFilename)

	if cachedData, err := safefile.ReadFile(cachePath); err == nil {
		slog.Info("Using cached remote image normalization", "cache_key", cacheKey)
		return cachedData, nil
	}
	if err := os.MkdirAll(cacheDir, 0o750); err != nil {
		slog.Warn("Failed to create image normalization cache directory", "error", err)
	}

	client := imageservice.New()
	if !client.Enabled() {
		return nil, fmt.Errorf("image_service.url is required when built with remoteocr")
	}
	normalized, err := client.Normalize(ctx, imageData, contentType)
	if err != nil {
		return nil, err
	}
	if writeErr := os.WriteFile(cachePath, normalized, 0o600); writeErr != nil {
		slog.Warn("Failed to write normalized image cache", "path", cachePath, "error", writeErr)
	}
	return normalized, nil
}
