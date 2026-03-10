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
	"strings"
	"testing"

	"github.com/lehigh-university-libraries/scribe/internal/database"
	"github.com/lehigh-university-libraries/scribe/internal/store"
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
						"@id":   "https://example.org/canvas/1",
						"label": "Page 1",
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
}

// TestManifestURLCandidates verifies that the /manifest suffix is tried first.
func TestManifestURLCandidates(t *testing.T) {
	canvasURI := "https://preserve.example.org/node/70000/canvas/237948"
	candidates := manifestURLCandidatesFromCanvasURI(canvasURI)
	if len(candidates) < 2 {
		t.Fatalf("got %d candidates; want at least 2", len(candidates))
	}
	if candidates[0] != "https://preserve.example.org/node/70000/manifest" {
		t.Errorf("candidates[0] = %q; want .../manifest suffix first", candidates[0])
	}
	if candidates[1] != "https://preserve.example.org/node/70000" {
		t.Errorf("candidates[1] = %q; want bare base as fallback", candidates[1])
	}
}

// TestManifestURLCandidatesNoCanvas verifies that an unrecognised URI returns nothing.
func TestManifestURLCandidatesNoCanvas(t *testing.T) {
	got := manifestURLCandidatesFromCanvasURI("https://example.org/no-canvas-segment")
	if len(got) != 0 {
		t.Errorf("expected no candidates, got %v", got)
	}
}

// --- integration test (requires TEST_DSN) ---

// openTestDB opens a MariaDB connection from TEST_DSN, runs migrations, and
// returns the pool. The test is skipped if TEST_DSN is not set.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
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
//  2. The manifest is ingested via the HTTP API (Connect RPC CreateItem).
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

	h := NewHandler(ocrRunStore, itemStore, contextStore, annotationStore)
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

	// — step 2: ingest the manifest via Connect RPC CreateItem —
	manifestURL := iiifServer.URL + "/manifest"
	reqBody := fmt.Sprintf(`{"name":"Test Manifest","sourceType":"manifest","sourceUrl":%q}`, manifestURL)
	createReq, _ := http.NewRequest(http.MethodPost,
		appServer.URL+"/scribe.v1.ItemService/CreateItem",
		strings.NewReader(reqBody))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("Connect-Protocol-Version", "1")

	createResp, err := http.DefaultClient.Do(createReq)
	if err != nil {
		t.Fatalf("CreateItem request: %v", err)
	}
	defer createResp.Body.Close()
	if createResp.StatusCode != http.StatusOK {
		t.Fatalf("CreateItem status %d", createResp.StatusCode)
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
		t.Fatalf("decode CreateItem response: %v", err)
	}
	if len(createBody.Item.Images) == 0 {
		t.Fatal("CreateItem returned no images")
	}
	itemImageID := createBody.Item.Images[0].ID
	if itemImageID == "" || itemImageID == "0" {
		t.Fatalf("CreateItem returned bad image id: %q", itemImageID)
	}
	t.Logf("item_image_id = %s", itemImageID)

	// Clean up the created item after the test.
	t.Cleanup(func() {
		delReq, _ := http.NewRequest(http.MethodPost,
			appServer.URL+"/scribe.v1.ItemService/DeleteItem",
			strings.NewReader(fmt.Sprintf(`{"itemId":%q}`, createBody.Item.ID)))
		delReq.Header.Set("Content-Type", "application/json")
		delReq.Header.Set("Connect-Protocol-Version", "1")
		_, _ = http.DefaultClient.Do(delReq)
	})

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

	// — step 4: call the IIIF annotations endpoint (what Mirador's adapter calls) —
	annURL := fmt.Sprintf("%s/v1/item-images/%s/annotations", appServer.URL, itemImageID)
	annResp, err := http.Get(annURL)
	if err != nil {
		t.Fatalf("GET annotations: %v", err)
	}
	defer annResp.Body.Close()
	if annResp.StatusCode != http.StatusOK {
		t.Fatalf("annotations status %d", annResp.StatusCode)
	}

	var annPage struct {
		Type  string           `json:"type"`
		Items []map[string]any `json:"items"`
	}
	if err := json.NewDecoder(annResp.Body).Decode(&annPage); err != nil {
		t.Fatalf("decode annotation page: %v", err)
	}
	if annPage.Type != "AnnotationPage" {
		t.Errorf("type = %q; want AnnotationPage", annPage.Type)
	}
	// The hOCR has 2 lines → expect 2 line annotations.
	if len(annPage.Items) != 2 {
		t.Errorf("got %d annotation items; want 2 (one per hOCR line)", len(annPage.Items))
	}
	// Verify each item has the expected IIIF structure.
	for i, item := range annPage.Items {
		if item["type"] != "Annotation" {
			t.Errorf("item[%d].type = %v; want Annotation", i, item["type"])
		}
		if item["textGranularity"] != "line" {
			t.Errorf("item[%d].textGranularity = %v; want line", i, item["textGranularity"])
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
		strings.NewReader(fmt.Sprintf(`{"canvasUri":%q}`, createBody.Item.Images[0].CanvasUri)),
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
		t.Errorf("search returned %d items; want 5 (2 lines + 3 words)", len(searchPage.Items))
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

// TestAnnotationPageRevisionSaveSemantics verifies the two-version save contract:
//
//  1. Original generated annotations are preserved in original_hocr (never mutated).
//  2. Edits via UpdateAnnotation overwrite the previous edit, not accumulate history.
//  3. SearchAnnotations returns the latest edited state after a save.
//  4. A second save overwrites the first (no revision history accumulates).
//  5. The hOCR endpoint still returns the original after edits.
func TestAnnotationPageRevisionSaveSemantics(t *testing.T) {
	db := openTestDB(t)

	ocrRunStore := store.NewOCRRunStore(db)
	itemStore := store.NewItemStore(db)
	contextStore := store.NewContextStore(db)
	annotationStore := store.NewAnnotationStore(db)

	h := NewHandler(ocrRunStore, itemStore, contextStore, annotationStore)
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
		appServer.URL+"/scribe.v1.ItemService/CreateItem",
		strings.NewReader(fmt.Sprintf(`{"name":"Rev Test","sourceType":"manifest","sourceUrl":%q}`, manifestURL)))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("Connect-Protocol-Version", "1")
	createResp, err := http.DefaultClient.Do(createReq)
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	defer createResp.Body.Close()
	if createResp.StatusCode != http.StatusOK {
		t.Fatalf("CreateItem status %d", createResp.StatusCode)
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
		t.Fatalf("decode CreateItem: %v", err)
	}
	if len(createBody.Item.Images) == 0 {
		t.Fatal("no images in CreateItem response")
	}
	itemImageID := createBody.Item.Images[0].ID
	canvasURI := createBody.Item.Images[0].CanvasUri
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
			strings.NewReader(fmt.Sprintf(`{"canvasUri":%q}`, canvasURI)))
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

	// --- Step 2: edit the annotation via UpdateAnnotation ---
	edited := make(map[string]any)
	for k, v := range firstLine {
		edited[k] = v
	}
	edited["body"] = []any{map[string]any{
		"type": "TextualBody", "purpose": "supplementing",
		"format": "text/plain", "value": "First Edit",
	}}
	editedJSON, _ := json.Marshal(edited)
	updateBody, _ := json.Marshal(map[string]string{"annotationJson": string(editedJSON)})

	updateReq, _ := http.NewRequest(http.MethodPost,
		appServer.URL+"/scribe.v1.AnnotationService/UpdateAnnotation",
		strings.NewReader(string(updateBody)))
	updateReq.Header.Set("Content-Type", "application/json")
	updateReq.Header.Set("Connect-Protocol-Version", "1")
	updateResp, err := http.DefaultClient.Do(updateReq)
	if err != nil {
		t.Fatalf("step 2: UpdateAnnotation: %v", err)
	}
	defer updateResp.Body.Close()
	if updateResp.StatusCode != http.StatusOK {
		t.Fatalf("step 2: UpdateAnnotation status %d", updateResp.StatusCode)
	}

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

	// --- Step 4: second edit overwrites first (no history accumulation) ---
	edited2 := make(map[string]any)
	for k, v := range firstLine {
		edited2[k] = v
	}
	edited2["body"] = []any{map[string]any{
		"type": "TextualBody", "purpose": "supplementing",
		"format": "text/plain", "value": "Second Edit",
	}}
	edited2JSON, _ := json.Marshal(edited2)
	updateBody2, _ := json.Marshal(map[string]string{"annotationJson": string(edited2JSON)})

	updateReq2, _ := http.NewRequest(http.MethodPost,
		appServer.URL+"/scribe.v1.AnnotationService/UpdateAnnotation",
		strings.NewReader(string(updateBody2)))
	updateReq2.Header.Set("Content-Type", "application/json")
	updateReq2.Header.Set("Connect-Protocol-Version", "1")
	updateResp2, err := http.DefaultClient.Do(updateReq2)
	if err != nil {
		t.Fatalf("step 4: UpdateAnnotation: %v", err)
	}
	defer updateResp2.Body.Close()
	if updateResp2.StatusCode != http.StatusOK {
		t.Fatalf("step 4: UpdateAnnotation status %d", updateResp2.StatusCode)
	}

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
	// The annotation table should have exactly one row for this id (no history).
	var rowCount int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM annotations WHERE id = ?`, firstLineID,
	).Scan(&rowCount); err != nil {
		t.Fatalf("step 4: count annotations: %v", err)
	}
	if rowCount != 1 {
		t.Errorf("step 4: %d rows for annotation id; want exactly 1 (no history)", rowCount)
	}

	// --- Step 5: original hOCR endpoint is unchanged ---
	hocrURL := fmt.Sprintf("%s/v1/item-images/%s/hocr", appServer.URL, itemImageID)
	hocrResp, err := http.Get(hocrURL)
	if err != nil {
		t.Fatalf("step 5: GET hocr: %v", err)
	}
	defer hocrResp.Body.Close()
	if hocrResp.StatusCode != http.StatusOK {
		t.Fatalf("step 5: hOCR status %d", hocrResp.StatusCode)
	}
	var hocrBody strings.Builder
	if _, err := io.Copy(&hocrBody, hocrResp.Body); err != nil {
		t.Fatalf("step 5: read hocr body: %v", err)
	}
	if !strings.Contains(hocrBody.String(), "Course") || !strings.Contains(hocrBody.String(), "Catalog") {
		t.Errorf("step 5: original hOCR missing expected text: %s", hocrBody.String())
	}
	// The original text should not contain our edits.
	if strings.Contains(hocrBody.String(), "First Edit") || strings.Contains(hocrBody.String(), "Second Edit") {
		t.Error("step 5: original hOCR was mutated by edits (should be immutable)")
	}
}
