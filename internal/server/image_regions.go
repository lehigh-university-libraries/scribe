package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/lehigh-university-libraries/scribe/internal/imageservice"
	"github.com/lehigh-university-libraries/scribe/internal/safehttp"
	"github.com/lehigh-university-libraries/scribe/internal/uploadlimits"
)

const (
	staticUploadsPrefix = "/static/uploads/"
	maxRemoteImageBytes = uploadlimits.MaxImageBytes
)

func fetchImageRegionToTemp(ctx context.Context, imageURL string, x1, y1, x2, y2 int) (string, func(), error) {
	if width, height := x2-x1, y2-y1; width <= 0 || height <= 0 {
		return "", func() {}, fmt.Errorf("invalid bbox")
	}

	if imagePath, ok := localUploadPathFromImageURL(imageURL); ok {
		return fetchLocalImageRegionToTemp(ctx, imagePath, x1, y1, x2, y2)
	}

	if serviceID := strings.TrimRight(iiifServiceFromImageURL(imageURL), "/"); serviceID != "" {
		return fetchIIIFServiceRegionToTemp(ctx, serviceID, x1, y1, x2, y2)
	}

	// Presentation manifests may reference a direct public image without an
	// Image API service. Fetch it through the SSRF-safe client, then use the
	// bounded Triplet client for the same crop path as uploaded files.
	sourcePath, cleanupSource, err := fetchImageURLToTemp(ctx, imageURL, "scribe-region-source-*")
	if err != nil {
		return "", func() {}, fmt.Errorf("fetch direct image source: %w", err)
	}
	defer cleanupSource()
	return fetchLocalImageRegionToTemp(ctx, sourcePath, x1, y1, x2, y2)
}

func localUploadPathFromImageURL(imageURL string) (string, bool) {
	name := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(imageURL), staticUploadsPrefix))
	if name == "" || strings.Contains(name, "/") || strings.Contains(name, "\\") {
		return "", false
	}
	return filepath.Join("uploads", name), true
}

func fetchLocalImageRegionToTemp(ctx context.Context, imagePath string, x1, y1, x2, y2 int) (string, func(), error) {
	client := imageservice.New()
	if !client.Enabled() {
		return "", func() {}, fmt.Errorf("triplet image client is not configured for local upload crops")
	}
	data, err := client.Crop(ctx, imagePath, imageservice.Box{
		X:      x1,
		Y:      y1,
		Width:  x2 - x1,
		Height: y2 - y1,
	})
	if err != nil {
		return "", func() {}, fmt.Errorf("remote crop local image: %w", err)
	}
	return writeImageBytesToTemp(data, "scribe-region-*.jpg")
}

func writeImageBytesToTemp(data []byte, pattern string) (string, func(), error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", func() {}, fmt.Errorf("create temp file: %w", err)
	}
	cleanup := func() { _ = os.Remove(f.Name()) }
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		cleanup()
		return "", func() {}, fmt.Errorf("write temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("close temp file: %w", err)
	}
	return f.Name(), cleanup, nil
}

func fetchIIIFServiceRegionToTemp(ctx context.Context, serviceID string, x1, y1, x2, y2 int) (string, func(), error) {
	width := x2 - x1
	height := y2 - y1
	if width <= 0 || height <= 0 {
		return "", func() {}, fmt.Errorf("invalid bbox")
	}
	cropURL := fmt.Sprintf("%s/%d,%d,%d,%d/max/0/default.jpg", strings.TrimRight(serviceID, "/"), x1, y1, width, height)
	path, cleanup, err := fetchImageURLToTemp(ctx, cropURL, "scribe-region-*.jpg")
	if err != nil {
		return "", func() {}, fmt.Errorf("fetch iiif crop: %w", err)
	}
	return path, cleanup, nil
}

func fetchImageURLToTemp(ctx context.Context, imageURL, pattern string) (string, func(), error) {
	resp, err := safehttp.Get(ctx, imageURL)
	if err != nil {
		return "", func() {}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", func() {}, fmt.Errorf("status %d", resp.StatusCode)
	}

	data, err := safehttp.ReadAllLimit(resp.Body, maxRemoteImageBytes)
	if err != nil {
		return "", func() {}, fmt.Errorf("read image: %w", err)
	}
	return writeImageBytesToTemp(data, pattern)
}
