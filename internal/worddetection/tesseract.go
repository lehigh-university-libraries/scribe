//go:build localocr && !remoteocr

package worddetection

import (
	"context"
	"fmt"
	"strings"

	"github.com/lehigh-university-libraries/scribe/internal/safefile"
	"github.com/otiai10/gosseract/v2"
)

// TesseractProvider implements word detection using Tesseract OCR
type TesseractProvider struct{}

// NewTesseract creates a new Tesseract word detection provider
func NewTesseract() *TesseractProvider {
	return &TesseractProvider{}
}

// Name returns the provider name
func (p *TesseractProvider) Name() string {
	return "tesseract"
}

// DetectWords detects word bounding boxes using Tesseract
func (p *TesseractProvider) DetectWords(ctx context.Context, imagePath string) ([]WordBox, error) {
	client := gosseract.NewClient()
	defer client.Close()

	imageData, err := safefile.ReadFile(imagePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read image: %w", err)
	}
	if err := client.SetImageFromBytes(imageData); err != nil {
		return nil, fmt.Errorf("failed to set image bytes: %w", err)
	}

	// Get word-level bounding boxes
	boxes, err := client.GetBoundingBoxes(gosseract.RIL_WORD)
	if err != nil {
		return nil, fmt.Errorf("failed to get bounding boxes: %w", err)
	}

	// Convert to our WordBox format
	words := make([]WordBox, 0, len(boxes))
	for _, box := range boxes {
		// Skip empty words
		if strings.TrimSpace(box.Word) == "" {
			continue
		}

		words = append(words, WordBox{
			X:          box.Box.Min.X,
			Y:          box.Box.Min.Y,
			Width:      box.Box.Max.X - box.Box.Min.X,
			Height:     box.Box.Max.Y - box.Box.Min.Y,
			Text:       box.Word,
			Confidence: float64(box.Confidence),
		})
	}

	return words, nil
}
