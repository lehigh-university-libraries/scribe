//go:build remoteocr

package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/lehigh-university-libraries/scribe/internal/imageservice"
	"github.com/lehigh-university-libraries/scribe/internal/safefile"
	"github.com/lehigh-university-libraries/scribe/internal/uploadlimits"
)

// convertImageViaHoudini converts JP2/TIFF images to JPEG through Triplet.
func (h *Handler) convertImageViaHoudini(ctx context.Context, imageData []byte, contentType string) ([]byte, error) {
	hash := sha256.Sum256(imageData)
	cacheKey := hex.EncodeToString(hash[:])
	cacheFilename := cacheKey + "_converted.jpg"
	cacheDir := "cache/houdini"
	cachePath := filepath.Join(cacheDir, cacheFilename)

	if cachedData, err := safefile.ReadFileLimit(cachePath, uploadlimits.MaxImageBytes); err == nil {
		touchNormalizationCache(cachePath)
		slog.Info("Using cached remote image normalization", "cache_key", cacheKey)
		return cachedData, nil
	}

	client := imageservice.New()
	if !client.Enabled() {
		return nil, fmt.Errorf("iiif.internal_base, iiif.source_base, and the Triplet source token are required when built with remoteocr")
	}
	normalized, err := client.Normalize(ctx, imageData, contentType)
	if err != nil {
		return nil, err
	}
	if writeErr := writeNormalizationCache(cacheDir, cachePath, normalized); writeErr != nil {
		logImageConversionFailure("Failed to write normalized image cache", writeErr, "cache_key", cacheKey)
	}
	return normalized, nil
}
