package worddetection

import (
	"context"
)

// WordBox represents a detected word with its bounding box
type WordBox struct {
	X, Y, Width, Height int
	Text                string // Detected text (if available)
	Confidence          float64
}

// DetectionResult contains the results from a word detection provider
type DetectionResult struct {
	Words    []WordBox
	Provider string
}

// Provider interface that all word detection providers must implement
type Provider interface {
	// DetectWords detects word bounding boxes in an image
	// Returns the list of detected words
	DetectWords(ctx context.Context, imagePath string) ([]WordBox, error)
	// Name returns the provider's name
	Name() string
}
