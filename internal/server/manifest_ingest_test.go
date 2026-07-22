package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/database"
	dbstore "github.com/lehigh-university-libraries/scribe/internal/db"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/models"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
)

// minimalHOCR is a valid hOCR document with two lines and three words.
const minimalHOCR = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html>
<html>
  <head><title>OCR Output</title></head>
  <body>
    <div class="ocr_page" id="page_1" title="bbox 0 0 2160 3632">
      <span class="ocr_line" id="line_1" title="bbox 10 20 500 45">
        <span class="ocrx_word" id="word_1" title="bbox 10 20 100 45; x_wconf 95">Course</span>
        <span class="ocrx_word" id="word_2" title="bbox 110 20 200 45; x_wconf 92">Catalog</span>
      </span>
      <span class="ocr_line" id="line_2" title="bbox 10 60 400 85">
        <span class="ocrx_word" id="word_3" title="bbox 10 60 150 85; x_wconf 88">1908-1909</span>
      </span>
    </div>
  </body>
</html>`

// --- unit tests (no DB required) ---

// TestExtractSeeAlsoV2 verifies that a IIIF v2 canvas seeAlso object whose
// format is text/vnd.hocr+html has its @id returned correctly.
func TestExtractSeeAlsoV2(t *testing.T) {
	canvas := map[string]any{
		"@id":   "https://example.org/canvas/1",
		"label": "Page 1",
		"seeAlso": map[string]any{
			"@id":     "https://example.org/hocr/1.xml",
			"format":  "text/vnd.hocr+html",
			"profile": "http://kba.cloud/hocr-spec",
			"label":   "hOCR embedded text",
		},
	}
	got := extractHOCRSeeAlso(canvas, "@id")
	if got != "https://example.org/hocr/1.xml" {
		t.Errorf("extractHOCRSeeAlso = %q; want %q", got, "https://example.org/hocr/1.xml")
	}
}

// TestExtractSeeAlsoV2Array verifies the array variant of seeAlso.
func TestExtractSeeAlsoV2Array(t *testing.T) {
	canvas := map[string]any{
		"@id": "https://example.org/canvas/1",
		"seeAlso": []any{
			map[string]any{
				"@id":    "https://example.org/metadata.json",
				"format": "application/json",
			},
			map[string]any{
				"@id":    "https://example.org/hocr/1.xml",
				"format": "text/vnd.hocr+html",
			},
		},
	}
	got := extractHOCRSeeAlso(canvas, "@id")
	if got != "https://example.org/hocr/1.xml" {
		t.Errorf("extractHOCRSeeAlso = %q; want %q", got, "https://example.org/hocr/1.xml")
	}
}

// TestExtractCanvasesV2HocrURL verifies that extractCanvasesV2 picks up the
// hOCR seeAlso URL and stores it in canvasInfo.
func TestExtractCanvasesV2HocrURL(t *testing.T) {
	manifest := map[string]any{
		"@context": "http://iiif.io/api/presentation/2/context.json",
		"@type":    "sc:Manifest",
		"sequences": []any{
			map[string]any{
				"@type": "sc:Sequence",
				"canvases": []any{
					map[string]any{
						"@id":    "https://example.org/canvas/1",
						"label":  "Page 1",
						"width":  2160,
						"height": 3632,
						"images": []any{
							map[string]any{
								"resource": map[string]any{
									"@id": "https://example.org/image.jpg",
								},
							},
						},
						"seeAlso": map[string]any{
							"@id":    "https://example.org/hocr.xml",
							"format": "text/vnd.hocr+html",
						},
					},
				},
			},
		},
	}
	canvases, err := extractCanvasesFromManifest(manifest)
	if err != nil {
		t.Fatalf("extractCanvasesFromManifest: %v", err)
	}
	if len(canvases) != 1 {
		t.Fatalf("got %d canvases; want 1", len(canvases))
	}
	if canvases[0].hocrURL != "https://example.org/hocr.xml" {
		t.Errorf("hocrURL = %q; want %q", canvases[0].hocrURL, "https://example.org/hocr.xml")
	}
	if canvases[0].width != 2160 || canvases[0].height != 3632 {
		t.Errorf("dimensions = %dx%d; want 2160x3632", canvases[0].width, canvases[0].height)
	}
}

func TestExtractCanvasesRejectsNonPublicPaintingBody(t *testing.T) {
	for name, imageURL := range map[string]string{
		"application upload alias": "/static/uploads/existing.jpg",
		"embedded credentials":     "https://user:password@example.org/image.jpg",
	} {
		t.Run(name, func(t *testing.T) {
			manifest := map[string]any{
				"@context": "http://iiif.io/api/presentation/2/context.json",
				"@type":    "sc:Manifest",
				"sequences": []any{map[string]any{"canvases": []any{map[string]any{
					"@id": "https://example.org/canvas/1",
					"images": []any{map[string]any{"resource": map[string]any{
						"@id": imageURL,
					}}},
				}}}},
			}
			if _, err := extractCanvasesFromManifest(manifest); err == nil {
				t.Fatalf("extractCanvasesFromManifest accepted %q", imageURL)
			}
		})
	}
}

func TestExtractCanvasesV3PreservesCanvasDimensionsWithImageChoice(t *testing.T) {
	manifest := map[string]any{
		"@context": "http://iiif.io/api/presentation/3/context.json",
		"id":       "https://example.org/manifest/1",
		"type":     "Manifest",
		"label":    map[string]any{"none": []any{"Test"}},
		"items": []any{map[string]any{
			"id":     "https://example.org/canvas/1",
			"type":   "Canvas",
			"label":  map[string]any{"none": []any{"Page 1"}},
			"width":  float64(4096),
			"height": float64(3072),
			"items": []any{map[string]any{
				"id":   "https://example.org/canvas/1/painting",
				"type": "AnnotationPage",
				"items": []any{map[string]any{
					"id":         "https://example.org/canvas/1/painting/image",
					"type":       "Annotation",
					"motivation": "painting",
					"target":     "https://example.org/canvas/1",
					"body": map[string]any{
						"type": "Choice",
						"items": []any{
							map[string]any{"id": "https://example.org/image/full/max/0/default.jpg", "type": "Image", "format": "image/jpeg"},
						},
					},
				}},
			}},
		}},
	}

	canvases, err := extractCanvasesFromManifest(manifest)
	if err != nil {
		t.Fatalf("extractCanvasesFromManifest: %v", err)
	}
	if len(canvases) != 1 {
		t.Fatalf("got %d canvases; want 1", len(canvases))
	}
	if canvases[0].width != 4096 || canvases[0].height != 3072 {
		t.Fatalf("dimensions = %dx%d; want 4096x3072", canvases[0].width, canvases[0].height)
	}
}

func TestExtractCanvasesV3ChoiceSelectsFirstSupportedPublicImage(t *testing.T) {
	painting := map[string]any{
		"id": "https://example.org/annotation/choice", "type": "Annotation", "motivation": "painting", "target": "https://example.org/canvas/choice",
		"body": map[string]any{"type": "Choice", "items": []any{
			map[string]any{"id": "https://example.org/transcript.txt", "type": "Text", "format": "text/plain"},
			map[string]any{"id": "https://example.org/vector.svg", "type": "Image", "format": "image/svg+xml"},
			map[string]any{"id": "https://user:password@example.org/private.jpg", "type": "Image", "format": "image/jpeg"},
			map[string]any{"type": "SpecificResource", "source": map[string]any{"id": "https://example.org/supported.png", "type": "Image", "format": "image/png"}},
		}},
	}
	paintingPage := map[string]any{
		"id": "https://example.org/page/choice", "type": "AnnotationPage", "items": []any{painting},
	}
	canvas := map[string]any{
		"id": "https://example.org/canvas/choice", "type": "Canvas", "width": 1000, "height": 800,
		"items": []any{paintingPage},
	}
	manifest := map[string]any{
		"@context": "http://iiif.io/api/presentation/3/context.json",
		"id":       "https://example.org/manifest/choice",
		"type":     "Manifest",
		"label":    map[string]any{"none": []any{"Choice"}},
		"items":    []any{canvas},
	}

	canvases, err := extractCanvasesFromManifest(manifest)
	if err != nil {
		t.Fatalf("extractCanvasesFromManifest: %v", err)
	}
	if got := canvases[0].imageURL; got != "https://example.org/supported.png" {
		t.Fatalf("selected image = %q, want supported public Image", got)
	}

	painting["body"] = map[string]any{
		"type": "Choice", "items": []any{
			map[string]any{"id": "https://example.org/vector.svg", "type": "Image", "format": "image/svg+xml"},
			map[string]any{"id": "https://user:password@example.org/private.jpg", "type": "Image", "format": "image/jpeg"},
		},
	}
	if _, err := extractCanvasesFromManifest(manifest); err == nil {
		t.Fatal("Choice without a supported public Image was accepted")
	}
}

func TestExtractCanvasesV3RequiresPaintingMotivationAndExactTarget(t *testing.T) {
	const canvasID = "https://example.org/canvas/strict-painting"
	painting := map[string]any{
		"id":         "https://example.org/annotation/strict-painting",
		"type":       "Annotation",
		"motivation": "painting",
		"target":     canvasID,
		"body":       map[string]any{"id": "https://example.org/image.jpg", "type": "Image", "format": "image/jpeg"},
	}
	manifest := map[string]any{
		"@context": "http://iiif.io/api/presentation/3/context.json",
		"id":       "https://example.org/manifest/strict-painting",
		"type":     "Manifest",
		"items": []any{map[string]any{
			"id": canvasID, "type": "Canvas",
			"items": []any{map[string]any{
				"id": "https://example.org/page/strict-painting", "type": "AnnotationPage", "items": []any{painting},
			}},
		}},
	}

	if _, err := extractCanvasesFromManifest(manifest); err != nil {
		t.Fatalf("valid painting annotation rejected: %v", err)
	}
	delete(painting, "motivation")
	if _, err := extractCanvasesFromManifest(manifest); err == nil || !strings.Contains(err.Error(), "painting motivation") {
		t.Fatalf("missing painting motivation error = %v", err)
	}
	painting["motivation"] = "painting"
	painting["target"] = "https://example.org/canvas/other"
	if _, err := extractCanvasesFromManifest(manifest); err == nil || !strings.Contains(err.Error(), "exactly match") {
		t.Fatalf("mismatched painting target error = %v", err)
	}
	painting["target"] = canvasID + "#xywh=0,0,10,10"
	if _, err := extractCanvasesFromManifest(manifest); err == nil || !strings.Contains(err.Error(), "exactly match") {
		t.Fatalf("fragment painting target error = %v", err)
	}
}

func TestExtractCanvasesRejectsIncompleteAndDuplicateCanvasSets(t *testing.T) {
	validV3Canvas := func(id string) map[string]any {
		return map[string]any{
			"id": id, "type": "Canvas",
			"items": []any{map[string]any{
				"id": id + "/page", "type": "AnnotationPage",
				"items": []any{map[string]any{
					"id": id + "/painting", "type": "Annotation", "motivation": "painting", "target": id,
					"body": map[string]any{"id": "https://example.org/image.jpg", "type": "Image", "format": "image/jpeg"},
				}},
			}},
		}
	}
	const duplicateID = "https://example.org/canvas/duplicate"
	duplicateManifest := map[string]any{
		"@context": "http://iiif.io/api/presentation/3/context.json",
		"id":       "https://example.org/manifest/duplicate",
		"type":     "Manifest",
		"items":    []any{validV3Canvas(duplicateID), validV3Canvas(duplicateID)},
	}
	if _, err := extractCanvasesFromManifest(duplicateManifest); err == nil || !strings.Contains(err.Error(), "duplicates") {
		t.Fatalf("duplicate Canvas ID error = %v", err)
	}

	incompleteManifest := map[string]any{
		"@context": "http://iiif.io/api/presentation/3/context.json",
		"id":       "https://example.org/manifest/incomplete",
		"type":     "Manifest",
		"items": []any{
			validV3Canvas("https://example.org/canvas/complete"),
			map[string]any{"id": "https://example.org/canvas/missing-image", "type": "Canvas", "items": []any{}},
		},
	}
	if _, err := extractCanvasesFromManifest(incompleteManifest); err == nil || !strings.Contains(err.Error(), "supported public painting Image") {
		t.Fatalf("incomplete Canvas error = %v", err)
	}

	validV2Canvas := func(id string) map[string]any {
		return map[string]any{
			"@id": id, "@type": "sc:Canvas",
			"images": []any{map[string]any{
				"@id": id + "/painting", "@type": "oa:Annotation", "motivation": "sc:painting", "on": id,
				"resource": map[string]any{"@id": "https://example.org/image.jpg", "@type": "dctypes:Image", "format": "image/jpeg"},
			}},
		}
	}
	duplicateV2Manifest := map[string]any{
		"@context": "http://iiif.io/api/presentation/2/context.json", "@type": "sc:Manifest",
		"sequences": []any{map[string]any{"canvases": []any{validV2Canvas(duplicateID), validV2Canvas(duplicateID)}}},
	}
	if _, err := extractCanvasesFromManifest(duplicateV2Manifest); err == nil || !strings.Contains(err.Error(), "duplicates") {
		t.Fatalf("duplicate Presentation 2 Canvas ID error = %v", err)
	}
	incompleteV2Manifest := map[string]any{
		"@context": "http://iiif.io/api/presentation/2/context.json", "@type": "sc:Manifest",
		"sequences": []any{map[string]any{"canvases": []any{
			validV2Canvas("https://example.org/canvas/v2-complete"),
			map[string]any{"@id": "https://example.org/canvas/v2-missing-image", "@type": "sc:Canvas", "images": []any{}},
		}}},
	}
	if _, err := extractCanvasesFromManifest(incompleteV2Manifest); err == nil || !strings.Contains(err.Error(), "supported painting Image") {
		t.Fatalf("incomplete Presentation 2 Canvas error = %v", err)
	}
}

func TestExtractCanvasesRejectsOverlongSourceCanvasURI(t *testing.T) {
	canvasID := "https://example.org/canvas/" + strings.Repeat("a", iiif.MaxCanvasURIBytes)
	painting := map[string]any{
		"type": "Annotation", "motivation": "painting", "target": canvasID,
		"body": map[string]any{"id": "https://example.org/image.jpg", "type": "Image"},
	}
	paintingPage := map[string]any{"type": "AnnotationPage", "items": []any{painting}}
	canvas := map[string]any{
		"id": canvasID, "type": "Canvas",
		"items": []any{paintingPage},
	}
	manifest := map[string]any{
		"@context": "http://iiif.io/api/presentation/3/context.json", "id": "https://example.org/manifest", "type": "Manifest",
		"items": []any{canvas},
	}
	if _, err := extractCanvasesFromManifest(manifest); err == nil || !strings.Contains(err.Error(), "source URI exceeds") {
		t.Fatalf("overlong source Canvas URI error = %v", err)
	}
}

// --- integration test (requires TEST_DSN) ---

// openTestDB opens a MariaDB connection from TEST_DSN, runs migrations, and
// returns the pool. The test is skipped if TEST_DSN is not set.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	runtime := config.Get()
	if strings.TrimSpace(runtime.Config.Annotation.TripletPresentationBase) == "" {
		runtime.Config.Annotation.TripletPresentationBase = "https://scribe.test/presentation/v3"
		runtime.Config.Annotation.TripletPresentationInternalBase = "http://triplet:8080/presentation/v3"
		runtime.Config.Annotation.TripletPresentationWriteToken = "test-triplet-presentation-write-token-32-bytes-minimum"
		config.Init(runtime)
	}
	dsn := os.Getenv("TEST_DSN")
	if dsn == "" {
		t.Skip("TEST_DSN not set; skipping integration test (set to e.g. 'user:pass@tcp(127.0.0.1:3306)/testdb')")
	}
	db, err := database.NewPool(dsn, database.DefaultConfig())
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	if err := database.Migrate(db); err != nil {
		_ = db.Close()
		t.Fatalf("migrate test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// buildIIIFv2Manifest returns a IIIF Presentation v2 manifest JSON string where
// the canvas seeAlso points to hocrURL served by the same test server.
func buildIIIFv2Manifest(baseURL string) string {
	m := map[string]any{
		"@context": "http://iiif.io/api/presentation/2/context.json",
		"@type":    "sc:Manifest",
		"@id":      baseURL + "/manifest",
		"label":    "Test Manifest",
		"sequences": []any{
			map[string]any{
				"@context": "http://iiif.io/api/presentation/2/context.json",
				"@id":      baseURL + "/sequence/normal",
				"@type":    "sc:Sequence",
				"canvases": []any{
					map[string]any{
						"@id":    baseURL + "/canvas/1",
						"@type":  "sc:Canvas",
						"label":  "Page 1",
						"height": 3632,
						"width":  2160,
						"images": []any{
							map[string]any{
								"@id":        baseURL + "/annotation/1",
								"@type":      "oa:Annotation",
								"motivation": "sc:painting",
								"resource": map[string]any{
									"@id":    "https://example.org/image/full/full/0/default.jpg",
									"@type":  "dctypes:Image",
									"format": "image/jpeg",
									"height": 3632,
									"width":  2160,
								},
								"on": baseURL + "/canvas/1",
							},
						},
						"seeAlso": map[string]any{
							"@id":     baseURL + "/hocr.xml",
							"format":  "text/vnd.hocr+html",
							"profile": "http://kba.cloud/hocr-spec",
							"label":   "hOCR embedded text",
						},
					},
				},
			},
		},
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// TestManifestIngestLoadsHOCRAnnotations is a full end-to-end integration test:
//
//  1. A mock IIIF server serves a v2 manifest whose canvas seeAlso points to a
//     mock hOCR document.
//  2. The manifest is ingested via the HTTP API (Connect RPC ImportManifest).
//  3. The IIIF annotations endpoint for the resulting item-image is called.
//  4. The response must be a valid IIIF AnnotationPage with line annotations
//     whose body text is derived from the mock hOCR.
func TestManifestIngestLoadsHOCRAnnotations(t *testing.T) {
	db := openTestDB(t)

	// — mock IIIF / hOCR server —
	var iiifServer *httptest.Server
	iiifServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, buildIIIFv2Manifest(iiifServer.URL))
		case "/hocr.xml":
			w.Header().Set("Content-Type", "text/vnd.hocr+html; charset=utf-8")
			fmt.Fprint(w, minimalHOCR)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(iiifServer.Close)

	// — application handler —
	ocrRunStore := store.NewOCRRunStore(db)
	itemStore := store.NewItemStore(db)
	contextStore := store.NewContextStore(db)
	annotationStore := store.NewAnnotationStore(db)
	transcriptionJobStore := store.NewTranscriptionJobStore(db)

	h := NewHandler(ocrRunStore, itemStore, contextStore, annotationStore, transcriptionJobStore, nil, nil, nil)
	appServer := httptest.NewServer(h)
	t.Cleanup(appServer.Close)
	t.Setenv("ANNOTATION_API_BASE", appServer.URL)

	// — step 1: seed a default context so the handler initialises cleanly —
	if err := contextStore.EnsureDefault(context.Background(), store.Context{
		Name:                  "test-default",
		IsDefault:             true,
		SegmentationModel:     "tesseract",
		TranscriptionProvider: "ollama",
		TranscriptionModel:    "test-model",
	}); err != nil {
		t.Fatalf("seed context: %v", err)
	}

	// — step 2: ingest the manifest via Connect RPC ImportManifest —
	manifestURL := iiifServer.URL + "/manifest"
	reqBody := fmt.Sprintf(`{"name":"Test Manifest","manifestUrl":%q,"idempotencyKey":"manifest-ingest-e2e"}`, manifestURL)
	createReq, _ := http.NewRequest(http.MethodPost,
		appServer.URL+"/scribe.v1.ItemService/ImportManifest",
		strings.NewReader(reqBody))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("Connect-Protocol-Version", "1")

	createResp, err := http.DefaultClient.Do(createReq)
	if err != nil {
		t.Fatalf("ImportManifest request: %v", err)
	}
	defer createResp.Body.Close()
	if createResp.StatusCode != http.StatusOK {
		t.Fatalf("ImportManifest status %d", createResp.StatusCode)
	}

	var createBody struct {
		Item struct {
			ID     string `json:"id"`
			Images []struct {
				ID        string `json:"id"`
				CanvasUri string `json:"canvasUri"`
				Width     uint32 `json:"width"`
				Height    uint32 `json:"height"`
			} `json:"images"`
		} `json:"item"`
	}
	if err := json.NewDecoder(createResp.Body).Decode(&createBody); err != nil {
		t.Fatalf("decode ImportManifest response: %v", err)
	}
	if len(createBody.Item.Images) == 0 {
		t.Fatal("ImportManifest returned no images")
	}
	itemImageID := createBody.Item.Images[0].ID
	if itemImageID == "" || itemImageID == "0" {
		t.Fatalf("ImportManifest returned bad image id: %q", itemImageID)
	}
	if got := createBody.Item.Images[0]; got.Width != 2160 || got.Height != 3632 {
		t.Fatalf("ImportManifest dimensions = %dx%d; want 2160x3632", got.Width, got.Height)
	}
	t.Logf("item_image_id = %s", itemImageID)
	numericItemImageID, err := strconv.ParseUint(itemImageID, 10, 64)
	if err != nil {
		t.Fatalf("parse item image id: %v", err)
	}

	// Clean up the created item after the test.
	t.Cleanup(func() {
		delReq, _ := http.NewRequest(http.MethodPost,
			appServer.URL+"/scribe.v1.ItemService/DeleteItem",
			strings.NewReader(fmt.Sprintf(`{"itemId":%q}`, createBody.Item.ID)))
		delReq.Header.Set("Content-Type", "application/json")
		delReq.Header.Set("Connect-Protocol-Version", "1")
		_, _ = http.DefaultClient.Do(delReq)
	})

	// The editor loads its draft Manifest through the typed application API;
	// Triplet is the only HTTP server for published Presentation resources.
	editorManifest, err := h.GetEditorManifest(context.Background(), connect.NewRequest(&scribev1.GetEditorManifestRequest{
		ItemImageId: numericItemImageID,
	}))
	if err != nil {
		t.Fatalf("GetEditorManifest: %v", err)
	}
	itemManifestPayload := []byte(editorManifest.Msg.GetManifestJson())
	if err := iiif.ValidateManifest(itemManifestPayload); err != nil {
		t.Fatalf("item manifest failed libops IIIF validation: %v", err)
	}
	var emittedManifest struct {
		Items []struct {
			Width  uint32 `json:"width"`
			Height uint32 `json:"height"`
		} `json:"items"`
	}
	if err := json.Unmarshal(itemManifestPayload, &emittedManifest); err != nil {
		t.Fatalf("decode emitted item manifest: %v", err)
	}
	if len(emittedManifest.Items) != 1 || emittedManifest.Items[0].Width != 2160 || emittedManifest.Items[0].Height != 3632 {
		t.Fatalf("emitted Canvas dimensions = %#v; want 2160x3632", emittedManifest.Items)
	}

	// — step 3: call GetOCRRun (mirrors what the editor does before loading Mirador) —
	getRunReq, _ := http.NewRequest(http.MethodPost,
		appServer.URL+"/scribe.v1.ImageProcessingService/GetOCRRun",
		strings.NewReader(fmt.Sprintf(`{"itemImageId":%s}`, itemImageID)))
	getRunReq.Header.Set("Content-Type", "application/json")
	getRunReq.Header.Set("Connect-Protocol-Version", "1")

	getRunResp, err := http.DefaultClient.Do(getRunReq)
	if err != nil {
		t.Fatalf("GetOCRRun request: %v", err)
	}
	defer getRunResp.Body.Close()
	if getRunResp.StatusCode != http.StatusOK {
		t.Fatalf("GetOCRRun status %d (editor would bail out here)", getRunResp.StatusCode)
	}
	var runBody struct {
		ImageURL string `json:"imageUrl"`
		Model    string `json:"model"`
	}
	if err := json.NewDecoder(getRunResp.Body).Decode(&runBody); err != nil {
		t.Fatalf("decode GetOCRRun response: %v", err)
	}
	if runBody.ImageURL == "" {
		t.Error("GetOCRRun returned empty imageUrl")
	}
	t.Logf("run.imageUrl = %s, run.model = %s", runBody.ImageURL, runBody.Model)

	// — step 4: explicitly publish the current canonical revision, then build
	// the immutable Presentation graph dispatched to Triplet. —
	canonical, err := annotationStore.LoadPage(context.Background(), store.AnonymousWorkspaceID, numericItemImageID)
	if err != nil {
		t.Fatalf("load canonical page before publication: %v", err)
	}
	if _, err := annotationStore.PublishPage(context.Background(), store.AnonymousWorkspaceID, numericItemImageID, store.AnnotationPublicationOptions{
		ExpectedRevision: canonical.Revision,
	}); err != nil {
		t.Fatalf("publish canonical page: %v", err)
	}
	resources, err := h.buildPublishedPresentationResources(context.Background(), numericItemImageID)
	if err != nil {
		t.Fatalf("build published Triplet graph: %v", err)
	}
	var annotationPagePayload []byte
	for _, resource := range resources {
		if resource.ID == canonical.PageID {
			annotationPagePayload = resource.Payload
			break
		}
	}
	if len(annotationPagePayload) == 0 {
		t.Fatalf("published Triplet graph omitted canonical page %q", canonical.PageID)
	}

	var annPage struct {
		Type  string           `json:"type"`
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(annotationPagePayload, &annPage); err != nil {
		t.Fatalf("decode annotation page: %v", err)
	}
	if annPage.Type != "AnnotationPage" {
		t.Errorf("type = %q; want AnnotationPage", annPage.Type)
	}
	// Preserve the finest available hOCR geometry: two lines and three words.
	if len(annPage.Items) != 5 {
		t.Errorf("got %d annotation items; want 5 line/word annotations", len(annPage.Items))
	}
	// Verify each item has the expected IIIF structure.
	for i, item := range annPage.Items {
		if item["type"] != "Annotation" {
			t.Errorf("item[%d].type = %v; want Annotation", i, item["type"])
		}
		if item["textGranularity"] != "line" && item["textGranularity"] != "word" {
			t.Errorf("item[%d].textGranularity = %v; want line or word", i, item["textGranularity"])
		}
		body, _ := item["body"].([]any)
		if len(body) == 0 {
			t.Errorf("item[%d].body is empty", i)
			continue
		}
		bodyItem, _ := body[0].(map[string]any)
		if bodyItem["value"] == "" || bodyItem["value"] == nil {
			t.Errorf("item[%d].body[0].value is empty", i)
		}
		t.Logf("annotation[%d] text = %v", i, bodyItem["value"])
	}

	// Verify line 1 text contains the expected words from the hOCR.
	if len(annPage.Items) >= 1 {
		body, _ := annPage.Items[0]["body"].([]any)
		if len(body) > 0 {
			bodyItem, _ := body[0].(map[string]any)
			text := fmt.Sprintf("%v", bodyItem["value"])
			if !strings.Contains(text, "Course") || !strings.Contains(text, "Catalog") {
				t.Errorf("line 1 text = %q; want to contain 'Course Catalog'", text)
			}
		}
	}

	// — step 5: call SearchAnnotations over Connect with the external canvas URI
	// used by the viewer and verify the returned annotations are bound to that
	// requested canvas rather than the internal item-image canvas URI.
	searchReq, _ := http.NewRequest(
		http.MethodPost,
		appServer.URL+"/scribe.v1.AnnotationService/SearchAnnotations",
		strings.NewReader(fmt.Sprintf(`{"itemImageId":%q,"canvasUri":%q}`, itemImageID, createBody.Item.Images[0].CanvasUri)),
	)
	searchReq.Header.Set("Content-Type", "application/json")
	searchReq.Header.Set("Connect-Protocol-Version", "1")
	searchResp, err := http.DefaultClient.Do(searchReq)
	if err != nil {
		t.Fatalf("SearchAnnotations request: %v", err)
	}
	defer searchResp.Body.Close()
	if searchResp.StatusCode != http.StatusOK {
		t.Fatalf("annotation search status %d", searchResp.StatusCode)
	}

	// SearchAnnotations is a Connect RPC; the page JSON is nested inside the
	// annotationPageJson string field of the response envelope.
	var searchEnvelope struct {
		AnnotationPageJson string `json:"annotationPageJson"`
	}
	if err := json.NewDecoder(searchResp.Body).Decode(&searchEnvelope); err != nil {
		t.Fatalf("decode annotation search envelope: %v", err)
	}
	var searchPage struct {
		Type  string           `json:"type"`
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal([]byte(searchEnvelope.AnnotationPageJson), &searchPage); err != nil {
		t.Fatalf("decode annotation search page json: %v", err)
	}
	if searchPage.Type != "AnnotationPage" {
		t.Errorf("search type = %q; want AnnotationPage", searchPage.Type)
	}
	if len(searchPage.Items) != 5 {
		t.Errorf("search returned %d items; want all 5 canonical annotations", len(searchPage.Items))
	}
	lineCount := 0
	wordCount := 0
	for i, item := range searchPage.Items {
		target, _ := item["target"].(map[string]any)
		source, _ := target["source"].(map[string]any)
		gotCanvas := fmt.Sprintf("%v", source["id"])
		if gotCanvas != createBody.Item.Images[0].CanvasUri {
			t.Errorf("search item[%d] target.source.id = %q; want %q", i, gotCanvas, createBody.Item.Images[0].CanvasUri)
		}
		switch item["textGranularity"] {
		case "line":
			lineCount++
		case "word":
			wordCount++
		}
	}
	if lineCount != 2 {
		t.Errorf("search returned %d line annotations; want 2", lineCount)
	}
	if wordCount != 3 {
		t.Errorf("search returned %d word annotations; want 3", wordCount)
	}
}

func TestAddImageRejectsMissingCanvasURI(t *testing.T) {
	db := openTestDB(t)
	itemStore := store.NewItemStore(db)
	item, err := itemStore.Create(context.Background(), dbstore.CreateItemParams{
		ID:          t.Name(),
		UserID:      store.AnonymousUserID,
		WorkspaceID: 1,
		Name:        "Test Item",
		SourceType:  "upload",
	})
	if err != nil {
		t.Fatalf("create item: %v", err)
	}
	t.Cleanup(func() { _ = itemStore.DeleteForWorkspace(context.Background(), item.ID, item.WorkspaceID) })
	_, err = itemStore.AddImage(context.Background(), dbstore.CreateItemImageParams{
		ItemID:   item.ID,
		Sequence: 1,
		ImageURL: "https://example.org/image.jpg",
	})
	if err == nil || !strings.Contains(err.Error(), "canvas uri") {
		t.Fatalf("AddImage missing Canvas error = %v", err)
	}
	var imageCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM item_images WHERE item_id = ?`, item.ID).Scan(&imageCount); err != nil || imageCount != 0 {
		t.Fatalf("missing-Canvas image count = %d/%v, want 0", imageCount, err)
	}
}

func TestGetAnnotationPageDoesNotInitializeMissingCanonicalPage(t *testing.T) {
	db := openTestDB(t)

	ocrRunStore := store.NewOCRRunStore(db)
	itemStore := store.NewItemStore(db)
	contextStore := store.NewContextStore(db)
	annotationStore := store.NewAnnotationStore(db)
	transcriptionJobStore := store.NewTranscriptionJobStore(db)

	h := NewHandler(ocrRunStore, itemStore, contextStore, annotationStore, transcriptionJobStore, nil, nil, nil)

	item, err := itemStore.Create(context.Background(), dbstore.CreateItemParams{
		ID:          t.Name(),
		UserID:      store.AnonymousUserID,
		WorkspaceID: 1,
		Name:        "Test Item",
		SourceType:  "upload",
	})
	if err != nil {
		t.Fatalf("create item: %v", err)
	}
	t.Cleanup(func() { _ = itemStore.DeleteForWorkspace(context.Background(), item.ID, item.WorkspaceID) })
	img, err := itemStore.AddImage(context.Background(), dbstore.CreateItemImageParams{
		ItemID: item.ID, Sequence: 1, ImageURL: "https://example.org/image.jpg", CanvasURI: "https://example.org/canvas/read-only-missing-page",
		Width: 2160, Height: 3632,
	})
	if err != nil {
		t.Fatalf("add item image: %v", err)
	}
	if err := ocrRunStore.Create(context.Background(), store.OCRRun{
		SessionID:    t.Name() + "-session",
		ItemImageID:  &img.ID,
		ImageURL:     img.ImageURL,
		Provider:     "test",
		Model:        "test",
		OriginalHOCR: minimalHOCR,
		OriginalText: "Course Catalog\n1908-1909",
	}); err != nil {
		t.Fatalf("create ocr run: %v", err)
	}

	if _, err := h.GetAnnotationPage(context.Background(), connect.NewRequest(&scribev1.GetAnnotationPageRequest{
		ItemImageId: img.ID,
	})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("GetAnnotationPage error = %v; want not_found", err)
	}

	updated, err := itemStore.GetImage(context.Background(), img.ID)
	if err != nil {
		t.Fatalf("get updated item image: %v", err)
	}
	if updated.CanvasURI != img.CanvasURI {
		t.Fatalf("canvas_uri = %q; read-only annotation GET changed %q", updated.CanvasURI, img.CanvasURI)
	}
}

func TestGetEditorManifestDoesNotOverwriteExistingCanvasURI(t *testing.T) {
	db := openTestDB(t)

	ocrRunStore := store.NewOCRRunStore(db)
	itemStore := store.NewItemStore(db)
	contextStore := store.NewContextStore(db)
	annotationStore := store.NewAnnotationStore(db)
	transcriptionJobStore := store.NewTranscriptionJobStore(db)

	h := NewHandler(ocrRunStore, itemStore, contextStore, annotationStore, transcriptionJobStore, nil, nil, nil)

	item, err := itemStore.Create(context.Background(), dbstore.CreateItemParams{
		ID:          t.Name(),
		UserID:      store.AnonymousUserID,
		WorkspaceID: 1,
		Name:        "Test Item",
		SourceType:  "manifest",
	})
	if err != nil {
		t.Fatalf("create item: %v", err)
	}
	t.Cleanup(func() { _ = itemStore.DeleteForWorkspace(context.Background(), item.ID, item.WorkspaceID) })
	const existingCanvasURI = "https://example.org/external/canvas/1"
	img, err := itemStore.AddImage(context.Background(), dbstore.CreateItemImageParams{
		ItemID:    item.ID,
		Sequence:  1,
		ImageURL:  "https://example.org/image.jpg",
		CanvasURI: existingCanvasURI,
		Width:     2160,
		Height:    3632,
	})
	if err != nil {
		t.Fatalf("add item image: %v", err)
	}
	if err := ocrRunStore.Create(context.Background(), store.OCRRun{
		SessionID:    t.Name() + "-session",
		ItemImageID:  &img.ID,
		ImageURL:     img.ImageURL,
		Provider:     "test",
		Model:        "test",
		OriginalHOCR: minimalHOCR,
		OriginalText: "Course Catalog\n1908-1909",
	}); err != nil {
		t.Fatalf("create ocr run: %v", err)
	}
	run, err := ocrRunStore.GetByItemImageID(context.Background(), img.ID)
	if err != nil {
		t.Fatalf("load OCR run: %v", err)
	}
	if err := h.ensureItemImageCanvasAndAnnotations(context.Background(), run, img.ID, nil); err != nil {
		t.Fatalf("initialize canonical page: %v", err)
	}

	editorManifest, err := h.GetEditorManifest(context.Background(), connect.NewRequest(&scribev1.GetEditorManifestRequest{
		ItemImageId: img.ID,
	}))
	if err != nil {
		t.Fatalf("GetEditorManifest: %v", err)
	}
	if err := iiif.ValidateManifest([]byte(editorManifest.Msg.GetManifestJson())); err != nil {
		t.Fatalf("editor Manifest failed IIIF validation: %v", err)
	}

	updated, err := itemStore.GetImage(context.Background(), img.ID)
	if err != nil {
		t.Fatalf("get updated item image: %v", err)
	}
	if updated.CanvasURI != existingCanvasURI {
		t.Fatalf("canvas_uri = %q; want existing %q", updated.CanvasURI, existingCanvasURI)
	}
}

func TestSearchAnnotationsPersistsBootstrappedInternalAnnotations(t *testing.T) {
	db := openTestDB(t)

	ocrRunStore := store.NewOCRRunStore(db)
	itemStore := store.NewItemStore(db)
	contextStore := store.NewContextStore(db)
	annotationStore := store.NewAnnotationStore(db)
	transcriptionJobStore := store.NewTranscriptionJobStore(db)

	h := NewHandler(ocrRunStore, itemStore, contextStore, annotationStore, transcriptionJobStore, nil, nil, nil)
	appServer := httptest.NewServer(h)
	t.Cleanup(appServer.Close)
	t.Setenv("ANNOTATION_API_BASE", appServer.URL)

	item, err := itemStore.Create(context.Background(), dbstore.CreateItemParams{
		ID:          t.Name(),
		UserID:      store.AnonymousUserID,
		WorkspaceID: 1,
		Name:        "Test Item",
		SourceType:  "upload",
	})
	if err != nil {
		t.Fatalf("create item: %v", err)
	}
	t.Cleanup(func() { _ = itemStore.DeleteForWorkspace(context.Background(), item.ID, item.WorkspaceID) })
	canvasURI := "https://example.org/canvas/search-bootstrap"
	img, err := itemStore.AddImage(context.Background(), dbstore.CreateItemImageParams{
		ItemID: item.ID, Sequence: 1, ImageURL: "https://example.org/image.jpg", CanvasURI: canvasURI,
		Width: 2160, Height: 3632,
	})
	if err != nil {
		t.Fatalf("add item image: %v", err)
	}
	if err := ocrRunStore.Create(context.Background(), store.OCRRun{
		SessionID:    t.Name() + "-session",
		ItemImageID:  &img.ID,
		ImageURL:     img.ImageURL,
		Provider:     "test",
		Model:        "test",
		OriginalHOCR: minimalHOCR,
		OriginalText: "Course Catalog\n1908-1909",
	}); err != nil {
		t.Fatalf("create ocr run: %v", err)
	}

	run, err := ocrRunStore.GetByItemImageID(context.Background(), img.ID)
	if err != nil {
		t.Fatalf("load OCR run: %v", err)
	}
	if err := h.ensureItemImageCanvasAndAnnotations(context.Background(), run, img.ID, nil); err != nil {
		t.Fatalf("explicitly initialize canonical annotation page: %v", err)
	}
	searchReq, _ := http.NewRequest(
		http.MethodPost,
		appServer.URL+"/scribe.v1.AnnotationService/SearchAnnotations",
		strings.NewReader(fmt.Sprintf(`{"itemImageId":%q,"canvasUri":%q}`, strconv.FormatUint(img.ID, 10), canvasURI)),
	)
	searchReq.Header.Set("Content-Type", "application/json")
	searchReq.Header.Set("Connect-Protocol-Version", "1")
	searchResp, err := http.DefaultClient.Do(searchReq)
	if err != nil {
		t.Fatalf("SearchAnnotations request: %v", err)
	}
	searchResp.Body.Close()
	if searchResp.StatusCode != http.StatusOK {
		t.Fatalf("SearchAnnotations status %d", searchResp.StatusCode)
	}

	payloads, err := annotationStore.SearchIndex(context.Background(), store.AnonymousWorkspaceID, img.ID)
	if err != nil {
		t.Fatalf("search persisted annotations: %v", err)
	}
	if len(payloads) != 5 {
		t.Fatalf("persisted %d annotations; want 5", len(payloads))
	}
}

func TestInitialAnnotationCreateCannotOverwriteConcurrentCorrection(t *testing.T) {
	database := openTestDB(t)
	ctx := context.Background()
	itemStore := store.NewItemStore(database)
	annotationStore := store.NewAnnotationStore(database)
	handler := NewHandler(nil, itemStore, nil, annotationStore, nil, nil, nil, nil)

	item, err := itemStore.Create(ctx, dbstore.CreateItemParams{
		ID: t.Name(), UserID: store.AnonymousUserID, WorkspaceID: store.AnonymousWorkspaceID,
		Name: "initializer race", SourceType: "upload",
	})
	if err != nil {
		t.Fatalf("create item: %v", err)
	}
	t.Cleanup(func() { _ = itemStore.DeleteForWorkspace(context.Background(), item.ID, item.WorkspaceID) })
	image, err := itemStore.AddImage(ctx, dbstore.CreateItemImageParams{
		ItemID: item.ID, Sequence: 1, ImageURL: "https://example.org/race.jpg", CanvasURI: "https://example.org/canvas/initializer-race",
		Width: 2160, Height: 3632,
	})
	if err != nil {
		t.Fatalf("add item image: %v", err)
	}
	identity := iiif.PageIdentity{PublicBaseURL: handler.publicAnnotationBaseURL(), ItemImageID: image.ID, CanvasURI: image.CanvasURI}
	pageID, err := iiif.CanonicalPageID(identity.PublicBaseURL, image.ID)
	if err != nil {
		t.Fatal(err)
	}
	lineID, err := iiif.AnnotationID(pageID, "corrected-line")
	if err != nil {
		t.Fatal(err)
	}
	corrected := transcriptionAnnotation(lineID, "line", "human correction", image.CanvasURI, models.BBox{X1: 0, Y1: 0, X2: 100, Y2: 20})
	payload, err := iiif.NewAnnotationPage(identity, []any{corrected})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := annotationStore.SavePage(ctx, store.AnnotationPage{
		WorkspaceID: store.AnonymousWorkspaceID, ItemImageID: image.ID, PageID: pageID, CanvasURI: image.CanvasURI, Payload: string(payload),
	}, 0)
	if err != nil {
		t.Fatalf("save concurrent correction: %v", err)
	}
	created, err := handler.createInitialAnnotationPage(ctx, image, []any{
		transcriptionAnnotation(testAnnotationID("stale-initializer"), "line", "stale model output", image.CanvasURI, models.BBox{X1: 0, Y1: 0, X2: 100, Y2: 20}),
	})
	if err != nil {
		t.Fatalf("atomic initializer: %v", err)
	}
	if created {
		t.Fatal("initializer reported creation over an existing canonical revision")
	}
	current, err := annotationStore.LoadPage(ctx, store.AnonymousWorkspaceID, image.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Revision != saved.Revision || !strings.Contains(current.Payload, "human correction") || strings.Contains(current.Payload, "stale model output") {
		t.Fatalf("initializer changed concurrent correction at revision %d: %s", current.Revision, current.Payload)
	}
}

func TestSearchAnnotationsUsesTypedGranularityAndPreservesCanvasQuery(t *testing.T) {
	db := openTestDB(t)

	ocrRunStore := store.NewOCRRunStore(db)
	itemStore := store.NewItemStore(db)
	contextStore := store.NewContextStore(db)
	annotationStore := store.NewAnnotationStore(db)
	transcriptionJobStore := store.NewTranscriptionJobStore(db)

	h := NewHandler(ocrRunStore, itemStore, contextStore, annotationStore, transcriptionJobStore, nil, nil, nil)
	appServer := httptest.NewServer(h)
	t.Cleanup(appServer.Close)
	t.Setenv("ANNOTATION_API_BASE", appServer.URL)

	item, err := itemStore.Create(context.Background(), dbstore.CreateItemParams{
		ID:          t.Name(),
		UserID:      store.AnonymousUserID,
		WorkspaceID: 1,
		Name:        "Test Item",
		SourceType:  "upload",
	})
	if err != nil {
		t.Fatalf("create item: %v", err)
	}
	t.Cleanup(func() { _ = itemStore.DeleteForWorkspace(context.Background(), item.ID, item.WorkspaceID) })
	canonicalCanvasURI := "https://example.org/canvas/all-granularities?collection=archives&view=transcript"
	img, err := itemStore.AddImage(context.Background(), dbstore.CreateItemImageParams{
		ItemID: item.ID, Sequence: 1, ImageURL: "https://example.org/image.jpg", CanvasURI: canonicalCanvasURI,
		Width: 2160, Height: 3632,
	})
	if err != nil {
		t.Fatalf("add item image: %v", err)
	}
	if err := ocrRunStore.Create(context.Background(), store.OCRRun{
		SessionID:    t.Name() + "-session",
		ItemImageID:  &img.ID,
		ImageURL:     img.ImageURL,
		Provider:     "test",
		Model:        "test",
		OriginalHOCR: minimalHOCR,
		OriginalText: "Course Catalog\n1908-1909",
	}); err != nil {
		t.Fatalf("create ocr run: %v", err)
	}
	run, err := ocrRunStore.GetByItemImageID(context.Background(), img.ID)
	if err != nil {
		t.Fatalf("load OCR run: %v", err)
	}
	if err := h.ensureItemImageCanvasAndAnnotations(context.Background(), run, img.ID, nil); err != nil {
		t.Fatalf("explicitly initialize canonical annotation page: %v", err)
	}

	canvasURI := "  " + canonicalCanvasURI + "  "
	searchReq, _ := http.NewRequest(
		http.MethodPost,
		appServer.URL+"/scribe.v1.AnnotationService/SearchAnnotations",
		strings.NewReader(fmt.Sprintf(`{"itemImageId":%q,"canvasUri":%q,"granularity":"ANNOTATION_GRANULARITY_ALL"}`, strconv.FormatUint(img.ID, 10), canvasURI)),
	)
	searchReq.Header.Set("Content-Type", "application/json")
	searchReq.Header.Set("Connect-Protocol-Version", "1")
	searchResp, err := http.DefaultClient.Do(searchReq)
	if err != nil {
		t.Fatalf("SearchAnnotations request: %v", err)
	}
	defer searchResp.Body.Close()
	if searchResp.StatusCode != http.StatusOK {
		t.Fatalf("SearchAnnotations status %d", searchResp.StatusCode)
	}

	var envelope struct {
		AnnotationPageJson string `json:"annotationPageJson"`
	}
	if err := json.NewDecoder(searchResp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode search envelope: %v", err)
	}
	var page struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal([]byte(envelope.AnnotationPageJson), &page); err != nil {
		t.Fatalf("decode search page: %v", err)
	}
	if len(page.Items) != 5 {
		t.Fatalf("search returned %d items; want 5 when requesting all granularities", len(page.Items))
	}
	wordResponse, err := h.SearchAnnotations(context.Background(), connect.NewRequest(&scribev1.SearchAnnotationsRequest{
		ItemImageId: img.ID,
		CanvasUri:   canvasURI,
		Granularity: scribev1.AnnotationGranularity_ANNOTATION_GRANULARITY_WORD,
	}))
	if err != nil {
		t.Fatalf("typed word SearchAnnotations: %v", err)
	}
	if err := json.Unmarshal([]byte(wordResponse.Msg.GetAnnotationPageJson()), &page); err != nil {
		t.Fatalf("decode typed word search page: %v", err)
	}
	if len(page.Items) != 3 {
		t.Fatalf("typed word search returned %d items; want 3", len(page.Items))
	}
	for index, annotation := range page.Items {
		if annotation["textGranularity"] != "word" {
			t.Fatalf("typed word search item %d granularity = %v; want word", index, annotation["textGranularity"])
		}
	}
}

// TestAnnotationPageRevisionSaveSemantics verifies the two-version save contract:
//
//  1. Original generated annotations are preserved in original_hocr provenance.
//  2. Full-page CAS saves overwrite the previous edit, not accumulate history.
//  3. SearchAnnotations returns the latest edited state after a save.
//  4. A second save overwrites the first (no revision history accumulates).
//  5. The hOCR endpoint derives the latest canonical correction while the OCR
//     provenance row remains immutable.
func TestAnnotationPageRevisionSaveSemantics(t *testing.T) {
	db := openTestDB(t)

	ocrRunStore := store.NewOCRRunStore(db)
	itemStore := store.NewItemStore(db)
	contextStore := store.NewContextStore(db)
	annotationStore := store.NewAnnotationStore(db)
	transcriptionJobStore := store.NewTranscriptionJobStore(db)

	h := NewHandler(ocrRunStore, itemStore, contextStore, annotationStore, transcriptionJobStore, nil, nil, nil)
	appServer := httptest.NewServer(h)
	t.Cleanup(appServer.Close)
	t.Setenv("ANNOTATION_API_BASE", appServer.URL)

	// Seed a default context.
	if err := contextStore.EnsureDefault(context.Background(), store.Context{
		Name:                  "test-default",
		IsDefault:             true,
		SegmentationModel:     "tesseract",
		TranscriptionProvider: "ollama",
		TranscriptionModel:    "test-model",
	}); err != nil {
		t.Fatalf("seed context: %v", err)
	}

	// Stand up a minimal IIIF / hOCR server.
	var iiifServer *httptest.Server
	iiifServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, buildIIIFv2Manifest(iiifServer.URL))
		case "/hocr.xml":
			w.Header().Set("Content-Type", "text/vnd.hocr+html; charset=utf-8")
			fmt.Fprint(w, minimalHOCR)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(iiifServer.Close)

	// Ingest a manifest.
	manifestURL := iiifServer.URL + "/manifest"
	createReq, _ := http.NewRequest(http.MethodPost,
		appServer.URL+"/scribe.v1.ItemService/ImportManifest",
		strings.NewReader(fmt.Sprintf(`{"name":"Rev Test","manifestUrl":%q,"idempotencyKey":"manifest-revision-e2e"}`, manifestURL)))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("Connect-Protocol-Version", "1")
	createResp, err := http.DefaultClient.Do(createReq)
	if err != nil {
		t.Fatalf("ImportManifest: %v", err)
	}
	defer createResp.Body.Close()
	if createResp.StatusCode != http.StatusOK {
		t.Fatalf("ImportManifest status %d", createResp.StatusCode)
	}
	var createBody struct {
		Item struct {
			ID     string `json:"id"`
			Images []struct {
				ID        string `json:"id"`
				CanvasUri string `json:"canvasUri"`
			} `json:"images"`
		} `json:"item"`
	}
	if err := json.NewDecoder(createResp.Body).Decode(&createBody); err != nil {
		t.Fatalf("decode ImportManifest: %v", err)
	}
	if len(createBody.Item.Images) == 0 {
		t.Fatal("no images in ImportManifest response")
	}
	itemImageID := createBody.Item.Images[0].ID
	canvasURI := createBody.Item.Images[0].CanvasUri
	parsedItemImageID, err := strconv.ParseUint(itemImageID, 10, 64)
	if err != nil {
		t.Fatalf("parse item image id: %v", err)
	}
	t.Cleanup(func() {
		delReq, _ := http.NewRequest(http.MethodPost,
			appServer.URL+"/scribe.v1.ItemService/DeleteItem",
			strings.NewReader(fmt.Sprintf(`{"itemId":%q}`, createBody.Item.ID)))
		delReq.Header.Set("Content-Type", "application/json")
		delReq.Header.Set("Connect-Protocol-Version", "1")
		_, _ = http.DefaultClient.Do(delReq)
	})

	// Helper: call SearchAnnotations and return the decoded page items.
	searchAnnotations := func(t *testing.T) []map[string]any {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost,
			appServer.URL+"/scribe.v1.AnnotationService/SearchAnnotations",
			strings.NewReader(fmt.Sprintf(`{"itemImageId":%q,"canvasUri":%q}`, itemImageID, canvasURI)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Connect-Protocol-Version", "1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("SearchAnnotations: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("SearchAnnotations status %d", resp.StatusCode)
		}
		var envelope struct {
			AnnotationPageJson string `json:"annotationPageJson"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode search envelope: %v", err)
		}
		var page struct {
			Items []map[string]any `json:"items"`
		}
		if err := json.Unmarshal([]byte(envelope.AnnotationPageJson), &page); err != nil {
			t.Fatalf("decode search page: %v", err)
		}
		return page.Items
	}

	// Helper: get text value from an annotation body.
	annotationText := func(ann map[string]any) string {
		body, _ := ann["body"].([]any)
		if len(body) == 0 {
			return ""
		}
		item, _ := body[0].(map[string]any)
		return fmt.Sprintf("%v", item["value"])
	}
	saveAnnotationText := func(t *testing.T, annotationID, text string) {
		t.Helper()
		current, err := h.GetAnnotationPage(context.Background(), connect.NewRequest(&scribev1.GetAnnotationPageRequest{
			ItemImageId: parsedItemImageID,
		}))
		if err != nil {
			t.Fatalf("GetAnnotationPage: %v", err)
		}
		var page map[string]any
		if err := json.Unmarshal([]byte(current.Msg.GetAnnotationPageJson()), &page); err != nil {
			t.Fatalf("decode canonical annotation page: %v", err)
		}
		items, _ := page["items"].([]any)
		found := false
		for _, raw := range items {
			annotation, _ := raw.(map[string]any)
			if fmt.Sprintf("%v", annotation["id"]) != annotationID {
				continue
			}
			annotation["body"] = []any{map[string]any{
				"type": "TextualBody", "purpose": "supplementing",
				"format": "text/plain", "value": text,
			}}
			found = true
			break
		}
		if !found {
			t.Fatalf("annotation %q not found in canonical page", annotationID)
		}
		payload, err := json.Marshal(page)
		if err != nil {
			t.Fatalf("encode canonical annotation page: %v", err)
		}
		if _, err := h.SaveAnnotationPage(context.Background(), connect.NewRequest(&scribev1.SaveAnnotationPageRequest{
			ItemImageId:        parsedItemImageID,
			AnnotationPageJson: string(payload),
			ExpectedRevision:   current.Msg.GetRevision(),
		})); err != nil {
			t.Fatalf("SaveAnnotationPage: %v", err)
		}
	}

	// --- Step 1: bootstrap from hOCR (no edits yet) ---
	items := searchAnnotations(t)
	if len(items) == 0 {
		t.Fatal("step 1: expected bootstrapped annotations from hOCR, got none")
	}
	// Find a line annotation to edit.
	var firstLine map[string]any
	var firstLineID string
	for _, item := range items {
		if item["textGranularity"] == "line" {
			firstLine = item
			firstLineID = fmt.Sprintf("%v", item["id"])
			break
		}
	}
	if firstLine == nil {
		t.Fatal("step 1: no line annotation found in bootstrap")
	}
	originalText := annotationText(firstLine)
	t.Logf("step 1: original text = %q, id = %s", originalText, firstLineID)

	// --- Step 2: edit the annotation in the local page and save one CAS ---
	saveAnnotationText(t, firstLineID, "First Edit")

	// --- Step 3: verify SearchAnnotations returns the edited text ---
	itemsAfterEdit1 := searchAnnotations(t)
	var found1 bool
	for _, item := range itemsAfterEdit1 {
		if fmt.Sprintf("%v", item["id"]) == firstLineID {
			if got := annotationText(item); got != "First Edit" {
				t.Errorf("step 3: text = %q; want %q", got, "First Edit")
			}
			found1 = true
		}
	}
	if !found1 {
		t.Error("step 3: edited annotation not found in search results")
	}

	// --- Step 4: a second full-page save overwrites the first ---
	saveAnnotationText(t, firstLineID, "Second Edit")

	itemsAfterEdit2 := searchAnnotations(t)
	var found2 bool
	for _, item := range itemsAfterEdit2 {
		if fmt.Sprintf("%v", item["id"]) == firstLineID {
			got := annotationText(item)
			if got != "Second Edit" {
				t.Errorf("step 4: text = %q; want %q (second edit should overwrite first)", got, "Second Edit")
			}
			found2 = true
		}
	}
	if !found2 {
		t.Error("step 4: annotation not found after second edit")
	}
	// The tenant-scoped derived index should have exactly one current row for
	// this id; revision history is adjacent to the canonical page, not duplicated
	// annotation rows.
	indexed, err := annotationStore.SearchIndex(context.Background(), store.AnonymousWorkspaceID, parsedItemImageID)
	if err != nil {
		t.Fatalf("step 4: search annotation index: %v", err)
	}
	rowCount := 0
	for _, entry := range indexed {
		if entry.ID == firstLineID {
			rowCount++
		}
	}
	if rowCount != 1 {
		t.Errorf("step 4: %d rows for annotation id; want exactly 1 (no history)", rowCount)
	}

	// --- Step 5: hOCR is derived from canonical state; provenance is unchanged ---
	currentForExport, err := h.GetAnnotationPage(context.Background(), connect.NewRequest(&scribev1.GetAnnotationPageRequest{
		ItemImageId: parsedItemImageID,
	}))
	if err != nil {
		t.Fatalf("step 5: load canonical revision: %v", err)
	}
	hocrURL := fmt.Sprintf(
		"%s/v1/item-images/%s/annotations/revisions/%d/hocr",
		appServer.URL, itemImageID, currentForExport.Msg.GetRevision(),
	)
	hocrResp, err := http.Get(hocrURL)
	if err != nil {
		t.Fatalf("step 5: GET hocr: %v", err)
	}
	defer hocrResp.Body.Close()
	if hocrResp.StatusCode != http.StatusOK {
		t.Fatalf("step 5: hOCR status %d", hocrResp.StatusCode)
	}
	wantDisposition := fmt.Sprintf(`attachment; filename="item-%s.hocr"`, itemImageID)
	if got := hocrResp.Header.Get("Content-Disposition"); got != wantDisposition {
		t.Fatalf("step 5: hOCR Content-Disposition = %q; want %q", got, wantDisposition)
	}
	if got := hocrResp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("step 5: hOCR X-Content-Type-Options = %q; want nosniff", got)
	}
	var hocrBody strings.Builder
	if _, err := io.Copy(&hocrBody, hocrResp.Body); err != nil {
		t.Fatalf("step 5: read hocr body: %v", err)
	}
	if !strings.Contains(hocrBody.String(), "Second") || !strings.Contains(hocrBody.String(), "Edit") {
		t.Errorf("step 5: canonical hOCR missing latest correction: %s", hocrBody.String())
	}
	if strings.Contains(hocrBody.String(), "Course") || strings.Contains(hocrBody.String(), "Catalog") || strings.Contains(hocrBody.String(), "First Edit") {
		t.Error("step 5: derived hOCR exposed stale canonical text")
	}
	baseline, err := ocrRunStore.GetByItemImageID(context.Background(), parsedItemImageID)
	if err != nil {
		t.Fatalf("step 5: load immutable OCR provenance: %v", err)
	}
	if !strings.Contains(baseline.OriginalHOCR, "Course") || !strings.Contains(baseline.OriginalHOCR, "Catalog") ||
		strings.Contains(baseline.OriginalHOCR, "First Edit") || strings.Contains(baseline.OriginalHOCR, "Second Edit") {
		t.Error("step 5: original OCR provenance was mutated")
	}
}
