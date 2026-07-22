package hocr

import (
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGetImageDimensionsRejectsOversizedDecodedImage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "too-wide.png")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create image: %v", err)
	}
	if err := png.Encode(file, image.NewNRGBA(image.Rect(0, 0, 30_001, 1))); err != nil {
		_ = file.Close()
		t.Fatalf("encode image: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close image: %v", err)
	}

	_, _, err = NewService().getImageDimensions(context.Background(), path)
	if err == nil || !strings.Contains(err.Error(), "exceed processing limits") {
		t.Fatalf("getImageDimensions error = %v; want dimension limit", err)
	}
}
