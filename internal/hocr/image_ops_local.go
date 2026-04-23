//go:build !remoteocr

package hocr

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/imageservice"
	"github.com/lehigh-university-libraries/scribe/internal/worddetection"
)

// extractLineImage extracts a line region from the image.
func (s *Service) extractLineImage(imagePath string, minX, minY, maxX, maxY, lineIndex int) (string, error) {
	width := maxX - minX
	height := maxY - minY
	if width <= 0 || height <= 0 {
		return "", fmt.Errorf("invalid dimensions: width=%d, height=%d", width, height)
	}

	if client := imageservice.New(); client.Enabled() {
		data, err := client.Crop(context.Background(), imagePath, imageservice.Box{
			X:      minX,
			Y:      minY,
			Width:  width,
			Height: height,
		})
		if err == nil {
			outputPath := filepath.Join("/tmp", fmt.Sprintf("line_%d_%d.png", lineIndex, time.Now().UnixNano()))
			if writeErr := os.WriteFile(outputPath, data, 0o644); writeErr == nil {
				return outputPath, nil
			}
		}
	}

	padding := 10
	cropX := max(0, minX-padding)
	cropY := max(0, minY-padding)
	cropWidth := width + 2*padding
	cropHeight := height + 2*padding

	outputPath := filepath.Join("/tmp", fmt.Sprintf("line_%d_%d.png", lineIndex, time.Now().UnixNano()))
	cmd := exec.Command("magick", imagePath,
		"-crop", fmt.Sprintf("%dx%d+%d+%d", cropWidth, cropHeight, cropX, cropY),
		"+repage",
		outputPath)
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to extract line image: %w", err)
	}
	return outputPath, nil
}

// stitchWordImages combines multiple word images horizontally into a single image.
func (s *Service) stitchWordImages(imagePath string, words []worddetection.WordBox) (string, error) {
	if len(words) == 0 {
		return "", fmt.Errorf("no words to stitch")
	}

	if client := imageservice.New(); client.Enabled() {
		boxes := make([]imageservice.Box, 0, len(words))
		for _, word := range words {
			boxes = append(boxes, imageservice.Box{
				X:      word.X,
				Y:      word.Y,
				Width:  word.Width,
				Height: word.Height,
			})
		}
		data, err := client.StitchHorizontal(context.Background(), imagePath, boxes, 5)
		if err == nil {
			outputPath := filepath.Join("/tmp", fmt.Sprintf("stitched_%d.png", time.Now().UnixNano()))
			if writeErr := os.WriteFile(outputPath, data, 0o644); writeErr == nil {
				return outputPath, nil
			}
		}
	}

	outputPath := filepath.Join("/tmp", fmt.Sprintf("stitched_%d.png", time.Now().UnixNano()))
	args := []string{imagePath}
	for _, word := range words {
		padding := 5
		cropX := max(0, word.X-padding)
		cropY := max(0, word.Y-padding)
		cropWidth := word.Width + 2*padding
		cropHeight := word.Height + 2*padding

		args = append(args, "(", "-clone", "0",
			"-crop", fmt.Sprintf("%dx%d+%d+%d", cropWidth, cropHeight, cropX, cropY),
			"+repage", ")")
	}
	args = append(args, "-delete", "0", "+append", outputPath)

	cmd := exec.Command("magick", args...)
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to stitch word images: %w", err)
	}
	return outputPath, nil
}
