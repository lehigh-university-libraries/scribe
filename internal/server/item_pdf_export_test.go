package server

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lehigh-university-libraries/scribe/internal/hocr"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/models"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
)

func TestPDFItemExportUsesOrderedCanonicalTextAndImages(t *testing.T) {
	plan, handler, imageBytes := testPDFExportPlan(t)
	responsePDF := "%PDF-1.7\nconverter output\n%%EOF\n"
	converter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/x-tar" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("converter received an unexpected method, content type, or credential")
		}
		archive := tar.NewReader(r.Body)
		for index, text := range []string{"First corrected café 東京", "Second corrected page"} {
			for _, extension := range []string{"hocr", "jpg"} {
				header, err := archive.Next()
				if err != nil {
					t.Error(err)
					return
				}
				if header.Name != fmt.Sprintf("page-%06d.%s", index+1, extension) || header.Typeflag != tar.TypeReg {
					t.Error("PDF pages arrived out of order or with an unsafe name/type")
				}
				content, err := io.ReadAll(archive)
				if err != nil {
					t.Error(err)
				}
				if extension == "hocr" {
					lines, err := hocr.ParseHOCRLines(string(content))
					if err != nil || len(lines) != 1 || strings.TrimSpace(joinLineWords(lines[0])) != text {
						t.Error("PDF input did not contain the corrected canonical text")
					}
				}
				if extension == "jpg" && !bytes.Equal(content, imageBytes) {
					t.Error("PDF input image changed")
				}
			}
		}
		if _, err := archive.Next(); err != io.EOF {
			t.Error("unexpected additional PDF input")
		}
		_, _ = io.WriteString(w, responsePDF)
	}))
	defer converter.Close()
	handler.pdfExportURL = converter.URL
	tempRoot := t.TempDir()
	t.Setenv("TMPDIR", tempRoot)
	staged, cleanup, err := handler.stagePDFItemExport(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	assertEmptyExportTempRoot(t, tempRoot)
	result, err := io.ReadAll(staged.File)
	if err != nil || string(result) != responsePDF || staged.Size != int64(len(responsePDF)) {
		t.Fatalf("staged PDF differs from converter output: %v", err)
	}
	if format, err := itemExportFormatName(scribev1.AnnotationExportFormat_ANNOTATION_EXPORT_FORMAT_PDF); err != nil || format != "pdf" {
		t.Fatalf("PDF format = %q/%v", format, err)
	}
	if filename, mediaType := itemExportMetadata(plan.Item, "pdf"); filename != "Corrected.pdf" || mediaType != "application/pdf" {
		t.Fatalf("PDF metadata = %q/%q", filename, mediaType)
	}
}

func TestPDFItemExportKeepsPagesWithoutText(t *testing.T) {
	plan, handler, _ := testPDFExportPlan(t)
	canvas := "https://source.example/canvas/1"
	payload, err := iiif.NewAnnotationPage(iiif.PageIdentity{
		PublicBaseURL: "https://scribe.example", ItemImageID: 1, CanvasURI: canvas,
	}, []any{transcriptionAnnotation("https://scribe.example/line/1", "line", "", canvas, models.BBox{X1: 5, Y1: 5, X2: 75, Y2: 20})})
	if err != nil {
		t.Fatal(err)
	}
	plan.Pages[0].Page.Payload = string(payload)
	converter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		archive := tar.NewReader(r.Body)
		pages := 0
		for {
			header, err := archive.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Error(err)
				return
			}
			if strings.HasSuffix(header.Name, ".hocr") {
				pages++
			}
		}
		if pages != 2 {
			t.Errorf("converter received %d hOCR pages, want 2", pages)
		}
		_, _ = io.WriteString(w, "%PDF-1.7\nconverter output\n%%EOF\n")
	}))
	defer converter.Close()
	handler.pdfExportURL = converter.URL
	t.Setenv("TMPDIR", t.TempDir())
	_, cleanup, err := handler.stagePDFItemExport(context.Background(), plan)
	if err != nil {
		t.Fatalf("a page without text failed the PDF export: %v", err)
	}
	cleanup()
}

func TestPDFItemExportRejectsConverterFailuresAndTruncatedOutput(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
	}{
		{"converter error", http.StatusInternalServerError, "private converter diagnostic"},
		{"truncated PDF", http.StatusOK, "%PDF-1.7\ntruncated output"},
		{"wrong content", http.StatusOK, "not a PDF response\n%%EOF\n"},
		{"redirect", http.StatusTemporaryRedirect, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, handler, _ := testPDFExportPlan(t)
			converter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Location", "/redirect-target")
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer converter.Close()
			handler.pdfExportURL = converter.URL
			tempRoot := t.TempDir()
			t.Setenv("TMPDIR", tempRoot)
			staged, cleanup, err := handler.stagePDFItemExport(context.Background(), plan)
			if err == nil || staged != nil || cleanup != nil || strings.Contains(err.Error(), "private converter diagnostic") {
				t.Fatalf("invalid converter result accepted or exposed: %v", err)
			}
			assertEmptyExportTempRoot(t, tempRoot)
		})
	}
}

func testPDFExportPlan(t *testing.T) (canonicalItemExportPlan, *Handler, []byte) {
	t.Helper()
	var imageBytes bytes.Buffer
	if err := jpeg.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 80, 60)), nil); err != nil {
		t.Fatal(err)
	}
	imagePath := filepath.Join(t.TempDir(), "source.jpg")
	if err := os.WriteFile(imagePath, imageBytes.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	plan := canonicalItemExportPlan{Item: store.Item{ID: "pdf-item", Name: "Corrected"}, Format: "pdf"}
	for index, text := range []string{"First corrected café 東京", "Second corrected page"} {
		id := uint64(index + 1)
		canvas := fmt.Sprintf("https://source.example/canvas/%d", id)
		payload, err := iiif.NewAnnotationPage(iiif.PageIdentity{
			PublicBaseURL: "https://scribe.example", ItemImageID: id, CanvasURI: canvas,
		}, []any{transcriptionAnnotation(fmt.Sprintf("https://scribe.example/line/%d", id), "line", text, canvas, models.BBox{X1: 5, Y1: 5, X2: 75, Y2: 20})})
		if err != nil {
			t.Fatal(err)
		}
		plan.Pages = append(plan.Pages, canonicalExportPage{
			Image: store.ItemImage{ID: id, Width: 80, Height: 60, ImageURL: "https://source.example/page.jpg"},
			Page:  store.AnnotationPage{Revision: 7, Payload: string(payload)},
		})
	}
	handler := &Handler{imageRegionFetcher: func(_ context.Context, url string, x1, y1, x2, y2 int) (string, func(), error) {
		if url != "https://source.example/page.jpg" || x1 != 0 || y1 != 0 || x2 != 80 || y2 != 60 {
			t.Fatal("PDF export did not fetch the complete canonical image")
		}
		return imagePath, func() {}, nil
	}}
	return plan, handler, imageBytes.Bytes()
}
