//go:build !remoteocr

package hocr

import (
	"context"
	"fmt"
	"io/fs"
	"os"

	"github.com/lehigh-university-libraries/scribe/internal/imagemagick"
	"github.com/lehigh-university-libraries/scribe/internal/imageservice"
)

// extractLineImage extracts a line region from the image.
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

	if client := imageservice.New(); client.Enabled() {
		data, err := client.Crop(ctx, imagePath, imageservice.Box{
			X:      minX,
			Y:      minY,
			Width:  width,
			Height: height,
		})
		if err == nil {
			if outputPath, writeErr := persistTemporaryImage(fmt.Sprintf("line-%d-*.png", lineIndex), data, os.WriteFile); writeErr == nil {
				return outputPath, nil
			}
		}
	}

	padding := 10
	cropX := max(0, minX-padding)
	cropY := max(0, minY-padding)
	cropWidth := width + 2*padding
	cropHeight := height + 2*padding

	outputPath, err := tempImagePath(fmt.Sprintf("line-%d-*.png", lineIndex))
	if err != nil {
		return "", err
	}
	cmd, err := imagemagick.ConvertCommandContext(ctx, imagePath,
		"-crop", fmt.Sprintf("%dx%d+%d+%d", cropWidth, cropHeight, cropX, cropY),
		"+repage",
		outputPath)
	if err != nil {
		return "", err
	}
	if err := cmd.Run(); err != nil {
		_ = os.Remove(outputPath)
		return "", fmt.Errorf("failed to extract line image: %w", err)
	}
	return outputPath, nil
}

func tempImagePath(pattern string) (string, error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

func persistTemporaryImage(pattern string, data []byte, writeFile func(string, []byte, fs.FileMode) error) (string, error) {
	outputPath, err := tempImagePath(pattern)
	if err != nil {
		return "", err
	}
	if err := writeFile(outputPath, data, 0o600); err != nil {
		_ = os.Remove(outputPath)
		return "", err
	}
	return outputPath, nil
}
