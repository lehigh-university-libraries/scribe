package worddetection

import (
	"context"
	"fmt"
)

type autoProvider struct{}

func NewAuto() Provider {
	return &autoProvider{}
}

func (p *autoProvider) Name() string {
	return "auto"
}

func (p *autoProvider) DetectWords(ctx context.Context, imagePath string) ([]WordBox, error) {
	var tesseractProvider Provider = NewTesseract()
	customProvider := NewCustom()

	tesseractWords, tesseractErr := tesseractProvider.DetectWords(ctx, imagePath)
	customWords, customErr := customProvider.DetectWords(ctx, imagePath)

	if tesseractErr != nil && customErr != nil {
		return nil, fmt.Errorf("both detection methods failed - tesseract: %v, custom: %v", tesseractErr, customErr)
	}
	if tesseractErr != nil {
		return customWords, nil
	}
	if customErr != nil {
		return tesseractWords, nil
	}
	if len(tesseractWords) >= len(customWords) {
		return tesseractWords, nil
	}
	return customWords, nil
}
