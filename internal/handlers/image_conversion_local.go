//go:build !remoteocr

package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/lehigh-university-libraries/scribe/internal/imagemagick"
	"github.com/lehigh-university-libraries/scribe/internal/imageservice"
	"github.com/lehigh-university-libraries/scribe/internal/safefile"
	"github.com/lehigh-university-libraries/scribe/internal/uploadlimits"
)

// convertImageViaHoudini converts JP2/TIFF images to JPG using ImageMagick locally.
func (h *Handler) convertImageViaHoudini(ctx context.Context, imageData []byte, contentType string) ([]byte, error) {
	hash := sha256.Sum256(imageData)
	cacheKey := hex.EncodeToString(hash[:])
	cacheFilename := cacheKey + "_converted.jpg"
	cacheDir := "cache/houdini"
	cachePath := filepath.Join(cacheDir, cacheFilename)

	if cachedData, err := safefile.ReadFileLimit(cachePath, uploadlimits.MaxImageBytes); err == nil {
		touchNormalizationCache(cachePath)
		slog.Info("Using cached Houdini conversion", "cache_key", cacheKey)
		return cachedData, nil
	}

	if client := imageservice.New(); client.Enabled() {
		convertedData, err := client.Normalize(ctx, imageData, contentType)
		if err == nil {
			if writeErr := writeNormalizationCache(cacheDir, cachePath, convertedData); writeErr != nil {
				logImageConversionFailure("Failed to write normalized image cache", writeErr, "cache_key", cacheKey)
			}
			return convertedData, nil
		}
		logImageConversionFailure("Remote image normalization failed; falling back to local ImageMagick", err)
	}

	if err := os.MkdirAll(cacheDir, 0o750); err != nil {
		return nil, fmt.Errorf("create Houdini cache directory: %w", err)
	}
	temporary, err := os.CreateTemp(cacheDir, ".scribe-normalization-*.jpg")
	if err != nil {
		return nil, fmt.Errorf("create Houdini output: %w", err)
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return nil, fmt.Errorf("close Houdini output: %w", err)
	}
	defer func() { _ = os.Remove(temporaryPath) }()

	cmd, err := imagemagick.ConvertCommandContext(ctx, "-", temporaryPath)
	if err != nil {
		return nil, err
	}
	cmd.Stdin = bytes.NewReader(imageData)
	slog.Info("Converting image", "content_type", contentType)
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("imagemagick preprocessing failed: %w", err)
	}

	convertedData, err := safefile.ReadFileLimit(temporaryPath, uploadlimits.MaxImageBytes)
	if err != nil {
		return nil, err
	}
	if writeErr := writeNormalizationCache(cacheDir, cachePath, convertedData); writeErr != nil {
		logImageConversionFailure("Failed to write local normalized image cache", writeErr, "cache_key", cacheKey)
	}
	return convertedData, nil
}
