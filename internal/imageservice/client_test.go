package imageservice

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/uploadref"
)

const (
	testSourceBase  = "http://api:8080/static/uploads"
	testSourceToken = "triplet-source-read-token-at-least-32-bytes"
	testImageName   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-123e4567-e89b-42d3-a456-426614174000.jpg"
)

func TestCropUsesAbsoluteSourceURLAndConstrainedBearer(t *testing.T) {
	var gotPath, gotAuthorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		gotPath = request.URL.EscapedPath()
		gotAuthorization = request.Header.Get("Authorization")
		writeTestJPEG(t, w, 2, 1)
	}))
	t.Cleanup(server.Close)
	initializeTripletClientConfig(server.URL)

	data, err := New().Crop(context.Background(), filepath.Join("uploads", testImageName), Box{X: 10, Y: 20, Width: 30, Height: 40})
	if err != nil {
		t.Fatalf("Crop: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("Crop returned empty body")
	}
	wantPath := "/iiif/3/" + url.PathEscape(testSourceBase+"/"+testImageName) + "/10,20,30,40/max/0/default.jpg"
	if gotPath != wantPath {
		t.Fatalf("path = %q, want %q", gotPath, wantPath)
	}
	if gotAuthorization != "Bearer "+testSourceToken {
		t.Fatalf("authorization = %q", gotAuthorization)
	}
}

func TestStitchHorizontalReusesOneAbsoluteSource(t *testing.T) {
	var gotPaths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		gotPaths = append(gotPaths, request.URL.EscapedPath())
		writeTestJPEG(t, w, 3, 2)
	}))
	t.Cleanup(server.Close)
	initializeTripletClientConfig(server.URL)

	data, err := New().StitchHorizontal(context.Background(), filepath.Join("uploads", testImageName), []Box{
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
	identifier := url.PathEscape(testSourceBase + "/" + testImageName)
	joined := strings.Join(gotPaths, "\n")
	for _, region := range []string{"5,15,40,50", "45,55,80,90"} {
		if !strings.Contains(joined, "/iiif/3/"+identifier+"/"+region+"/max/0/default.jpg") {
			t.Fatalf("missing crop %s; got:\n%s", region, joined)
		}
	}
}

func TestNormalizeStagesAbsoluteSourceAndCleansIt(t *testing.T) {
	t.Chdir(t.TempDir())
	var stagedName string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		prefix := "/iiif/3/"
		suffix := "/full/max/0/default.jpg"
		escapedIdentifier := strings.TrimSuffix(strings.TrimPrefix(request.URL.EscapedPath(), prefix), suffix)
		sourceURL, err := url.PathUnescape(escapedIdentifier)
		if err != nil {
			t.Errorf("unescape source identifier: %v", err)
			http.Error(w, "bad identifier", http.StatusBadRequest)
			return
		}
		if !strings.HasPrefix(sourceURL, testSourceBase+"/") {
			t.Errorf("source URL = %q", sourceURL)
		}
		stagedName = strings.TrimPrefix(sourceURL, testSourceBase+"/")
		if !uploadref.IsImmutableName(stagedName) {
			t.Errorf("staged source name = %q", stagedName)
		}
		if _, err := os.Stat(filepath.Join("uploads", stagedName)); err != nil {
			t.Errorf("staged source unavailable during request: %v", err)
		}
		writeTestJPEG(t, w, 4, 3)
	}))
	t.Cleanup(server.Close)
	initializeTripletClientConfig(server.URL)

	data, err := New().Normalize(context.Background(), []byte("fake jp2 bytes"), "image/jp2")
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if len(data) == 0 || stagedName == "" {
		t.Fatal("Normalize did not make a staged Triplet request")
	}
	if _, err := os.Stat(filepath.Join("uploads", stagedName)); !os.IsNotExist(err) {
		t.Fatalf("temporary Triplet source survived request: %v", err)
	}
}

func TestNormalizeRejectsNoncanonicalUnknownImages(t *testing.T) {
	initializeTripletClientConfig("http://triplet.invalid")
	if _, err := New().Normalize(context.Background(), []byte("not an image"), "application/octet-stream"); err == nil {
		t.Fatal("Normalize accepted an unsupported staged image type")
	}
}

func TestStagedUploadRootRejectsNoncanonicalNames(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, name := range []string{"../escape.jpg", "/absolute.jpg", "not-an-immutable-name.jpg"} {
		if file, err := createStagedUpload(name); err == nil {
			_ = file.Close()
			t.Fatalf("createStagedUpload(%q) succeeded", name)
		}
	}
	if _, err := os.Stat("escape.jpg"); !os.IsNotExist(err) {
		t.Fatalf("noncanonical staged name escaped uploads root: %v", err)
	}

	file, err := createStagedUpload(testImageName)
	if err != nil {
		t.Fatalf("create canonical staged upload: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close canonical staged upload: %v", err)
	}
	info, err := os.Stat(filepath.Join(stagedUploadsDirectory, testImageName))
	if err != nil {
		t.Fatalf("stat canonical staged upload: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("canonical staged upload mode = %#o, want 0600", got)
	}
	if err := removeStagedUpload(testImageName); err != nil {
		t.Fatalf("remove canonical staged upload: %v", err)
	}
}

func initializeTripletClientConfig(internalServer string) {
	config.Init(config.Runtime{Config: config.Config{IIIF: config.IIIFConfig{
		InternalBase:    strings.TrimRight(internalServer, "/") + "/iiif/3",
		SourceBase:      testSourceBase,
		SourceReadToken: testSourceToken,
	}}})
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
		t.Errorf("encode jpeg: %v", err)
	}
}
