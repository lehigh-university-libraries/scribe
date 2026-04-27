//go:build !localocr && !remoteocr

package worddetection

import (
	"context"
	"fmt"
)

type TesseractProvider struct{}

func NewTesseract() *TesseractProvider {
	return &TesseractProvider{}
}

func (p *TesseractProvider) Name() string {
	return "tesseract"
}

func (p *TesseractProvider) DetectWords(ctx context.Context, imagePath string) ([]WordBox, error) {
	return nil, fmt.Errorf("tesseract word detection requires building with -tags localocr or remoteocr")
}
