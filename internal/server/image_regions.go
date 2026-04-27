package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/lehigh-university-libraries/scribe/internal/imageservice"
)

const staticUploadsPrefix = "/static/uploads/"

func fetchImageRegionToTemp(imageURL string, x1, y1, x2, y2 int) (string, func(), error) {
	if width, height := x2-x1, y2-y1; width <= 0 || height <= 0 {
		return "", func() {}, fmt.Errorf("invalid bbox")
	}

	if imagePath, ok := localUploadPathFromImageURL(imageURL); ok {
		return fetchLocalImageRegionToTemp(imagePath, x1, y1, x2, y2)
	}

	if serviceID := strings.TrimRight(iiifServiceFromImageURL(imageURL), "/"); serviceID != "" {
		return fetchIIIFServiceRegionToTemp(serviceID, x1, y1, x2, y2)
	}

	if iiifID, err := iiifIdentifierFromImageURL(imageURL); err == nil {
		return fetchIIIFRegionToTemp(iiifID, x1, y1, x2, y2)
	}

	return "", func() {}, fmt.Errorf("image url %q does not map to a supported crop source", imageURL)
}

func localUploadPathFromImageURL(imageURL string) (string, bool) {
	name := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(imageURL), staticUploadsPrefix))
	if name == "" || strings.Contains(name, "/") || strings.Contains(name, "\\") {
		return "", false
	}
	return filepath.Join("uploads", name), true
}

func fetchLocalImageRegionToTemp(imagePath string, x1, y1, x2, y2 int) (string, func(), error) {
	client := imageservice.New()
	if !client.Enabled() {
		return "", func() {}, fmt.Errorf("image service is not configured for local upload crops")
	}
	data, err := client.Crop(context.Background(), imagePath, imageservice.Box{
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

func fetchIIIFServiceRegionToTemp(serviceID string, x1, y1, x2, y2 int) (string, func(), error) {
	width := x2 - x1
	height := y2 - y1
	if width <= 0 || height <= 0 {
		return "", func() {}, fmt.Errorf("invalid bbox")
	}
	cropURL := fmt.Sprintf("%s/%d,%d,%d,%d/max/0/default.jpg", strings.TrimRight(serviceID, "/"), x1, y1, width, height)
	path, cleanup, err := fetchImageURLToTemp(cropURL, "scribe-region-*.jpg")
	if err != nil {
		return "", func() {}, fmt.Errorf("fetch iiif crop: %w", err)
	}
	return path, cleanup, nil
}

func fetchImageURLToTemp(imageURL, pattern string) (string, func(), error) {
	resp, err := http.Get(imageURL) // #nosec G107 - caller passes trusted/configured IIIF URLs
	if err != nil {
		return "", func() {}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", func() {}, fmt.Errorf("status %d", resp.StatusCode)
	}

	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", func() {}, fmt.Errorf("create temp file: %w", err)
	}
	cleanup := func() { _ = os.Remove(f.Name()) }
	if _, err := io.Copy(f, resp.Body); err != nil {
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
