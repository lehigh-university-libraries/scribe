package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/lehigh-university-libraries/scribe/internal/hocr"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
)

const (
	deployedReadinessRawBase     = "https://raw.githubusercontent.com/lehigh-university-libraries/scribe/8202da4a50fef7256b77d60685f2f0e08e14f3c9/"
	deployedReadinessImageURL    = deployedReadinessRawBase + "web/e2e/canvas-a.svg"
	deployedReadinessHOCRURL     = deployedReadinessRawBase + "internal/server/testdata/crosswalk/expected_hocr.html"
	deployedReadinessImageSHA256 = "0443cf4f28c60debf3237300d3357539b3b309f8c950af489491c686a13e0e16"
	deployedReadinessHOCRSHA256  = "28cd3a9f0dfc4dab86082bdd9d4012f1ab8ce17923bd6001e4c9cc888ce537bf"
)

func TestDeployedReadinessFixtureMatchesImporterContract(t *testing.T) {
	manifestRaw, err := os.ReadFile("testdata/deployed-readiness/manifest.json")
	if err != nil {
		t.Fatalf("read readiness manifest: %v", err)
	}
	if err := iiif.ValidateSourceManifest(manifestRaw); err != nil {
		t.Fatalf("validate readiness manifest: %v", err)
	}
	var manifest map[string]any
	if err := iiif.DecodeJSON(manifestRaw, &manifest); err != nil {
		t.Fatalf("decode readiness manifest: %v", err)
	}
	canvases, err := extractCanvasesFromManifest(manifest)
	if err != nil {
		t.Fatalf("extract readiness canvases: %v", err)
	}
	if len(canvases) != 6 {
		t.Fatalf("readiness canvas count = %d, want 6", len(canvases))
	}
	seenCanvasIDs := make(map[string]struct{}, len(canvases))
	for index, canvas := range canvases {
		if canvas.imageURL != deployedReadinessImageURL {
			t.Fatalf("canvas %d image URL = %q", index+1, canvas.imageURL)
		}
		if canvas.hocrURL != deployedReadinessHOCRURL {
			t.Fatalf("canvas %d hOCR URL = %q", index+1, canvas.hocrURL)
		}
		if canvas.width != 1000 || canvas.height != 600 {
			t.Fatalf("canvas %d dimensions = %dx%d", index+1, canvas.width, canvas.height)
		}
		if _, duplicate := seenCanvasIDs[canvas.canvasURI]; duplicate {
			t.Fatalf("canvas %d duplicated ID %q", index+1, canvas.canvasURI)
		}
		seenCanvasIDs[canvas.canvasURI] = struct{}{}
	}

	hocrRaw, err := os.ReadFile("testdata/crosswalk/expected_hocr.html")
	if err != nil {
		t.Fatalf("read readiness hOCR: %v", err)
	}
	document, err := hocr.ParseDocument(string(hocrRaw))
	if err != nil {
		t.Fatalf("parse readiness hOCR: %v", err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(hocrRaw)); got != deployedReadinessHOCRSHA256 {
		t.Fatalf("hOCR digest = %q", got)
	}
	if document.PageWidth != 210 || document.PageHeight != 90 {
		t.Fatalf("hOCR dimensions = %dx%d", document.PageWidth, document.PageHeight)
	}
	if len(document.Lines) != 2 || len(document.Words) != 5 || strings.TrimSpace(hocr.PlainText(document.Lines)) != "Hello world\nFoo bar baz" {
		t.Fatalf("hOCR content = %d lines, %d words, %q", len(document.Lines), len(document.Words), hocr.PlainText(document.Lines))
	}

	imageRaw, err := os.ReadFile("../../web/e2e/canvas-a.svg")
	if err != nil {
		t.Fatalf("read readiness image: %v", err)
	}
	var imageConfig struct {
		Width  int `xml:"width,attr"`
		Height int `xml:"height,attr"`
	}
	if err := xml.Unmarshal(imageRaw, &imageConfig); err != nil {
		t.Fatalf("decode readiness image: %v", err)
	}
	if imageConfig.Width != 1000 || imageConfig.Height != 600 {
		t.Fatalf("image dimensions = %dx%d", imageConfig.Width, imageConfig.Height)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(imageRaw)); got != deployedReadinessImageSHA256 {
		t.Fatalf("image digest = %q", got)
	}

	var source *httptest.Server
	source = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/manifest.json":
			localManifest := bytes.ReplaceAll(manifestRaw, []byte(deployedReadinessHOCRURL), []byte(source.URL+"/hocr.html"))
			localManifest = bytes.ReplaceAll(localManifest, []byte(deployedReadinessImageURL), []byte(source.URL+"/image.svg"))
			response.Header().Set("Content-Type", "application/ld+json")
			_, _ = response.Write(localManifest)
		case "/hocr.html":
			response.Header().Set("Content-Type", "text/vnd.hocr+html")
			_, _ = response.Write(hocrRaw)
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(source.Close)
	fetchedManifest, retainedSource, err := fetchIIIFManifest(context.Background(), source.URL+"/manifest.json", 6)
	if err != nil {
		t.Fatalf("fetch readiness manifest through importer: %v", err)
	}
	if len(retainedSource) == 0 {
		t.Fatal("readiness importer did not retain the Presentation 3 source")
	}
	fetchedCanvases, err := extractCanvasesFromManifest(fetchedManifest)
	if err != nil {
		t.Fatalf("extract fetched readiness canvases: %v", err)
	}
	prefetched, _, err := prefetchManifestHOCR(context.Background(), fetchedCanvases, 1<<20)
	if err != nil {
		t.Fatalf("prefetch readiness hOCR through importer: %v", err)
	}
	for index, canvas := range prefetched {
		if canvas.parsedHOCR == nil || canvas.plainText != "Hello world\nFoo bar baz" {
			t.Fatalf("canvas %d imported hOCR = %#v / %q", index+1, canvas.parsedHOCR, canvas.plainText)
		}
	}
}
