package segmentor

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lehigh-university-libraries/htr/pkg/ocrpage"
)

func TestV2SegmentPreservesNativeLineGeometryAndRedactsModelPath(t *testing.T) {
	models, bin := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(models, "blla.mlmodel"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
set -eu
printf '%s' '{"lines":[{"id":"native-line","baseline":[[10,25],[80,27]],"boundary":[[10,10],[80,12],[80,30],[10,28]]}]}' > "$3"
`
	if err := os.WriteFile(filepath.Join(bin, "kraken"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "convert"), []byte("#!/bin/sh\nwhile [ \"$1\" = -limit ]; do shift 3; done\nexec /bin/cp \"$1\" \"$2\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("KRAKEN_MODEL_DIR", models)
	t.Setenv("KRAKEN_SEGMENTATION_MODEL_ID", "kraken")
	t.Setenv("KRAKEN_SEGMENTATION_MODEL", "blla.mlmodel")
	var payload bytes.Buffer
	form := multipart.NewWriter(&payload)
	imagePart, err := form.CreateFormFile("image", "page.png")
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(imagePart, image.NewRGBA(image.Rect(0, 0, 100, 100))); err != nil {
		t.Fatal(err)
	}
	if err := form.WriteField("hints", `{"documentType":"manuscript"}`); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v2/segment", &payload)
	req.Header.Set("Content-Type", form.FormDataContentType())
	response := httptest.NewRecorder()
	NewHandler().ServeHTTP(response, req)
	if response.Code != 200 {
		t.Fatalf("status %d: %s", response.Code, response.Body.String())
	}
	page, err := ocrpage.ParseFormat(response.Body.Bytes(), ocrpage.FormatHTRJSON, ocrpage.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	lines := page.Lines()
	if len(lines) != 1 || len(lines[0].Baseline) != 2 || len(lines[0].Polygon) != 4 {
		t.Fatalf("lost geometry: %+v", page)
	}
	if strings.Contains(response.Body.String(), models) || page.Provenance.Model != "kraken" {
		t.Fatal("native model path exposed")
	}
	for _, hints := range []string{`{"documentType":"unknown"}`, `{"languages":["` + strings.Repeat("x", 65) + `"]}`, strings.Repeat("x", 4097)} {
		if _, err := segmentPreparedPage(context.Background(), "unused", "ppdoclayout", hints); err == nil {
			t.Fatalf("accepted invalid hints: %.80s", hints)
		}
	}
}

func TestCanonicalAnnotationLimitDoesNotCountPolygonVerticesAsAnnotations(t *testing.T) {
	lines := make([]ocrpage.Line, 10001)
	for i := range lines {
		lines[i] = ocrpage.Line{Box: ocrpage.Rect{X1: 1, Y1: 1, X2: 90, Y2: 30}}
	}
	if err := validateSegmentedPage(ocrpage.FromLines(100, 100, lines)); err == nil {
		t.Fatal("accepted too many canonical annotations")
	}
	lines = lines[:200]
	for i := range lines {
		for range 100 {
			lines[i].Polygon = append(lines[i].Polygon, ocrpage.Point{X: 1, Y: 1})
		}
	}
	if err := validateSegmentedPage(ocrpage.FromLines(100, 100, lines)); err != nil {
		t.Fatalf("valid vertex budget rejected as annotation count: %v", err)
	}
}
