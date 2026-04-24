//go:build !remoteocr

package handlers

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/lehigh-university-libraries/scribe/internal/imagemagick"
	"github.com/lehigh-university-libraries/scribe/internal/imageservice"
)

// convertImageViaHoudini converts JP2/TIFF images to JPG using ImageMagick locally.
func (h *Handler) convertImageViaHoudini(imageData []byte, contentType string) ([]byte, error) {
	hash := md5.Sum(imageData)
	cacheKey := hex.EncodeToString(hash[:])
	cacheFilename := cacheKey + "_converted.jpg"
	cacheDir := "cache/houdini"
	cachePath := filepath.Join(cacheDir, cacheFilename)

	if cachedData, err := os.ReadFile(cachePath); err == nil {
		slog.Info("Using cached Houdini conversion", "cache_key", cacheKey)
		return cachedData, nil
	}
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		slog.Warn("Failed to create Houdini cache directory", "error", err)
	}

	if client := imageservice.New(); client.Enabled() {
		convertedData, err := client.Normalize(context.Background(), imageData, contentType)
		if err == nil {
			if writeErr := os.WriteFile(cachePath, convertedData, 0o644); writeErr != nil {
				slog.Warn("Failed to write normalized image cache", "cache_path", cachePath, "error", writeErr)
			}
			return convertedData, nil
		}
		slog.Warn("Remote image normalization failed; falling back to local ImageMagick", "error", err)
	}

	cmd, err := imagemagick.ConvertCommand("-", cachePath)
	if err != nil {
		return nil, err
	}
	cmd.Stdin = bytes.NewReader(imageData)
	slog.Info("Converting image", "cmd", cmd.String(), "content_type", contentType)
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("imagemagick preprocessing failed: %w", err)
	}

	convertedData, err := os.ReadFile(cachePath)
	if err != nil {
		return nil, err
	}
	return convertedData, nil
}
