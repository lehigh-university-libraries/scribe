package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lehigh-university-libraries/htr/pkg/ocrpage"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

func TestGroundTruthBundleCanBeScoredWithoutImageDownload(t *testing.T) {
	page := ocrpage.FromLines(100, 100, []ocrpage.Line{{ID: "l", Box: ocrpage.Rect{X1: 10, Y1: 10, X2: 90, Y2: 30}, Text: "checked"}})
	original, err := ocrpage.WriteJSON(page)
	if err != nil {
		t.Fatal(err)
	}
	items, err := pageAnnotations("scope", "https://example.org/canvas", page)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"type": "AnnotationPage", "items": items})
	if err != nil {
		t.Fatal(err)
	}
	metric := segmentationCorrection(context.Background(), store.OCRRun{OriginalPage: string(original)}, raw, 100, 100, "manuscript")
	if !bytes.Contains([]byte(metric), []byte(`"Score":1`)) || !bytes.Contains([]byte(metric), []byte(`"FallbackReason":`)) {
		t.Fatalf("missing scored fallback: %s", metric)
	}
	data, err := writeGroundTruthBundle([]store.GroundTruth{{ItemImageID: 1, Revision: 2, DocumentType: "manuscript", Payload: string(raw), OriginalPage: string(original), Transcript: "checked", ImageUrl: "https://example.org/image.jpg"}})
	if err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, file := range archive.File {
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		files[file.Name], err = io.ReadAll(reader)
		_ = reader.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	rows, err := csv.NewReader(bytes.NewReader(files["pages.csv"])).ReadAll()
	if err != nil || len(rows) != 2 || rows[1][2] != "manuscript" {
		t.Fatalf("manifest %v %v", rows, err)
	}
	gt, err := ocrpage.Parse(files[rows[1][0]], ocrpage.ReadOptions{})
	if err != nil || gt.Width != 100 || gt.Height != 100 || gt.PlainText() != "checked" {
		t.Fatalf("GT geometry/text %+v %v", gt, err)
	}
	if len(files["1-r2.iiif.json"]) == 0 {
		t.Fatal("canonical source missing")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("Go toolchain is required for the cross-repository CLI acceptance check")
	}
	module := exec.CommandContext(t.Context(), "go", "list", "-m", "-f", "{{.Dir}}", "github.com/lehigh-university-libraries/htr")
	moduleDir, err := module.Output()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "htr")
	build := exec.CommandContext(t.Context(), "go", "build", "-mod=readonly", "-o", binary, ".")
	build.Dir = strings.TrimSpace(string(moduleDir))
	build.Env = append(os.Environ(), "GOWORK=off")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build released HTR CLI: %v\n%s", err, output)
	}
	for name, data := range files {
		if filepath.Base(name) != name {
			t.Fatalf("unsafe bundle entry %q", name)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	score := exec.CommandContext(t.Context(), binary, "segeval-external", "--csv", filepath.Join(dir, "pages.csv"), "--dir", dir, "-o", filepath.Join(dir, "scores.yaml"))
	score.Dir = dir
	if output, err := score.CombinedOutput(); err != nil || !bytes.Contains(output, []byte("score=1.000000")) {
		t.Fatalf("score Scribe GT bundle: %v\n%s", err, output)
	}
}

func TestInteractiveBaselineScoringRequiresOfflineAbove32Lines(t *testing.T) {
	for _, withBaselines := range []bool{false, true} {
		lines := make([]ocrpage.Line, 33)
		for i := range lines {
			y := i * 20
			lines[i] = ocrpage.Line{ID: fmt.Sprintf("line-%d", i), Box: ocrpage.Rect{X1: 10, Y1: y, X2: 90, Y2: y + 10}}
			if withBaselines {
				lines[i].Baseline = []ocrpage.Point{{X: 10, Y: y + 8}, {X: 90, Y: y + 8}}
			}
		}
		page := ocrpage.FromLines(100, 1000, lines)
		original, err := ocrpage.WriteJSON(page)
		if err != nil {
			t.Fatal(err)
		}
		items, err := pageAnnotations("scope", "https://example.org/canvas", page)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(map[string]any{"type": "AnnotationPage", "items": items})
		if err != nil {
			t.Fatal(err)
		}
		metric := segmentationCorrection(t.Context(), store.OCRRun{OriginalPage: string(original)}, raw, 100, 1000, "manuscript")
		if withBaselines && !strings.Contains(metric, "offline scoring required above 32 elements") {
			t.Fatal(metric)
		}
		if !withBaselines && !strings.Contains(metric, `"Score":1`) {
			t.Fatal(metric)
		}
	}
}

func TestInteractiveBaselineBudgetIncludesResampling(t *testing.T) {
	page := ocrpage.FromLines(1000000, 100, []ocrpage.Line{{ID: "long", Box: ocrpage.Rect{X1: 10, Y1: 10, X2: 999990, Y2: 30}, Baseline: []ocrpage.Point{{X: 10, Y: 28}, {X: 999990, Y: 28}}}})
	original, err := ocrpage.WriteJSON(page)
	if err != nil {
		t.Fatal(err)
	}
	items, err := pageAnnotations("scope", "https://example.org/canvas", page)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"type": "AnnotationPage", "items": items})
	if err != nil {
		t.Fatal(err)
	}
	metric := segmentationCorrection(t.Context(), store.OCRRun{OriginalPage: string(original)}, raw, 1000000, 100, "manuscript")
	if !strings.Contains(metric, "offline scoring required above 4096 sampled baseline points") {
		t.Fatal(metric)
	}
}
