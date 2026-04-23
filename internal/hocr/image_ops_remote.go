//go:build remoteocr

package hocr

import (
	"context"
	"fmt"
	"os"

	"github.com/lehigh-university-libraries/scribe/internal/imageservice"
	"github.com/lehigh-university-libraries/scribe/internal/worddetection"
)

func (s *Service) extractLineImage(imagePath string, minX, minY, maxX, maxY, lineIndex int) (string, error) {
	width := maxX - minX
	height := maxY - minY
	if width <= 0 || height <= 0 {
		return "", fmt.Errorf("invalid dimensions: width=%d, height=%d", width, height)
	}

	padding := 10
	client := imageservice.New()
	if !client.Enabled() {
		return "", fmt.Errorf("image_service.url is required when built with remoteocr")
	}

	data, err := client.Crop(context.Background(), imagePath, imageservice.Box{
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

func (s *Service) stitchWordImages(imagePath string, words []worddetection.WordBox) (string, error) {
	if len(words) == 0 {
		return "", fmt.Errorf("no words to stitch")
	}
	client := imageservice.New()
	if !client.Enabled() {
		return "", fmt.Errorf("image_service.url is required when built with remoteocr")
	}

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
	if err != nil {
		return "", err
	}
	return writeTempImage(data, "stitched-*.jpg")
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
