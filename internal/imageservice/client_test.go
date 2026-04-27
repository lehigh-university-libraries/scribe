package imageservice

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lehigh-university-libraries/scribe/internal/config"
)

func TestCropUsesIIIFV3MaxRequest(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		writeTestJPEG(t, w, 2, 1)
	}))
	t.Cleanup(srv.Close)

	config.Init(config.Runtime{Config: config.Config{IIIF: config.IIIFConfig{InternalBase: srv.URL + "/iiif/3"}}})
	client := New()

	data, err := client.Crop(context.Background(), "/tmp/uploads/page one.jpg", Box{X: 10, Y: 20, Width: 30, Height: 40})
	if err != nil {
		t.Fatalf("Crop: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("Crop returned empty body")
	}
	want := "/iiif/3/page%20one.jpg/10,20,30,40/max/0/default.jpg"
	if gotPath != want {
		t.Fatalf("path = %q, want %q", gotPath, want)
	}
}

func TestStitchHorizontalUsesIIIFCrops(t *testing.T) {
	var gotPaths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.EscapedPath())
		writeTestJPEG(t, w, 3, 2)
	}))
	t.Cleanup(srv.Close)

	config.Init(config.Runtime{Config: config.Config{IIIF: config.IIIFConfig{InternalBase: srv.URL + "/iiif/3"}}})
	client := New()

	data, err := client.StitchHorizontal(context.Background(), "uploads/page.jpg", []Box{
		{X: 10, Y: 20, Width: 30, Height: 40},
		{X: 50, Y: 60, Width: 70, Height: 80},
	}, 5)
	if err != nil {
		t.Fatalf("StitchHorizontal: %v", err)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode stitched image: %v", err)
	}
	if got := img.Bounds().Dx(); got != 6 {
		t.Fatalf("stitched width = %d, want 6", got)
	}
	if got := img.Bounds().Dy(); got != 2 {
		t.Fatalf("stitched height = %d, want 2", got)
	}
	joined := strings.Join(gotPaths, "\n")
	if !strings.Contains(joined, "/iiif/3/page.jpg/5,15,40,50/max/0/default.jpg") {
		t.Fatalf("missing first crop path; got:\n%s", joined)
	}
	if !strings.Contains(joined, "/iiif/3/page.jpg/45,55,80,90/max/0/default.jpg") {
		t.Fatalf("missing second crop path; got:\n%s", joined)
	}
}

func TestNormalizeUsesIIIFV3FullImageRequest(t *testing.T) {
	t.Chdir(t.TempDir())
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		writeTestJPEG(t, w, 4, 3)
	}))
	t.Cleanup(srv.Close)

	config.Init(config.Runtime{Config: config.Config{IIIF: config.IIIFConfig{InternalBase: srv.URL + "/iiif/3"}}})
	client := New()
	input := []byte("fake jp2 bytes")

	data, err := client.Normalize(context.Background(), input, "image/jp2")
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("Normalize returned empty body")
	}
	sum := sha256.Sum256(input)
	name := hex.EncodeToString(sum[:]) + ".jp2"
	wantPath := "/iiif/3/" + name + "/full/max/0/default.jpg"
	if gotPath != wantPath {
		t.Fatalf("path = %q, want %q", gotPath, wantPath)
	}
	if _, err := os.Stat(filepath.Join("uploads", name)); err != nil {
		t.Fatalf("normalized source was not staged for IIIF: %v", err)
	}
}

func writeTestJPEG(t *testing.T, w http.ResponseWriter, width, height int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: 200, G: 200, B: 200, A: 255})
		}
	}
	w.Header().Set("Content-Type", "image/jpeg")
	if err := jpeg.Encode(w, img, nil); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
}
