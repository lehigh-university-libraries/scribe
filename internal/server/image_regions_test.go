package server

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/safehttp"
)

func TestFetchImageRegionSupportsDirectManifestImage(t *testing.T) {
	previousRuntime := config.Get()
	t.Cleanup(func() { config.Init(previousRuntime) })
	t.Setenv(safehttp.AllowPrivateFetchesEnv, "true")

	source := image.NewRGBA(image.Rect(0, 0, 20, 10))
	for y := 0; y < 10; y++ {
		for x := 0; x < 20; x++ {
			source.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, source); err != nil {
		t.Fatalf("encode source image: %v", err)
	}
	sourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(encoded.Bytes())
	}))
	defer sourceServer.Close()

	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Authorization"); got != "Bearer test-triplet-source-token-32-bytes-minimum" {
			t.Errorf("Triplet authorization = %q", got)
		}
		crop := image.NewRGBA(image.Rect(0, 0, 6, 4))
		if err := jpeg.Encode(w, crop, nil); err != nil {
			t.Errorf("encode Triplet crop: %v", err)
		}
	}))
	defer imageServer.Close()
	config.Init(config.Runtime{Config: config.Config{IIIF: config.IIIFConfig{
		InternalBase:    imageServer.URL + "/iiif/3",
		SourceBase:      "http://api:8080/static/uploads",
		SourceReadToken: "test-triplet-source-token-32-bytes-minimum",
	}}})

	path, cleanup, err := fetchImageRegionToTemp(context.Background(), sourceServer.URL+"/page.png", 2, 3, 8, 7)
	if err != nil {
		t.Fatalf("fetchImageRegionToTemp() error = %v", err)
	}
	defer cleanup()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open crop: %v", err)
	}
	defer file.Close()
	cfg, _, err := image.DecodeConfig(file)
	if err != nil {
		t.Fatalf("decode crop: %v", err)
	}
	if cfg.Width != 6 || cfg.Height != 4 {
		t.Fatalf("crop dimensions = %dx%d; want 6x4", cfg.Width, cfg.Height)
	}
}
