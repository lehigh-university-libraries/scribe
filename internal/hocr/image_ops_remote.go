//go:build remoteocr

package hocr

import (
	"context"
	"fmt"
	"os"

	"github.com/lehigh-university-libraries/scribe/internal/imageservice"
)

func (s *Service) extractLineImage(ctx context.Context, imagePath string, minX, minY, maxX, maxY, lineIndex int) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	width := maxX - minX
	height := maxY - minY
	if width <= 0 || height <= 0 {
		return "", fmt.Errorf("invalid dimensions: width=%d, height=%d", width, height)
	}

	padding := 10
	client := imageservice.New()
	if !client.Enabled() {
		return "", fmt.Errorf("iiif.internal_base, iiif.source_base, and the Triplet source token are required when built with remoteocr")
	}

	data, err := client.Crop(ctx, imagePath, imageservice.Box{
		X:      max(0, minX-padding),
		Y:      max(0, minY-padding),
		Width:  width + 2*padding,
		Height: height + 2*padding,
	})
	if err != nil {
		return "", err
	}
	return writeTempImage(data, "line-*.jpg")
}

func writeTempImage(data []byte, pattern string) (string, error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}
