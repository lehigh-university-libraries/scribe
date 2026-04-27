package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lehigh-university-libraries/scribe/internal/config"
)

func TestTripletAnnotationPageIDUsesConfiguredPublicBase(t *testing.T) {
	config.Init(config.Runtime{Config: config.Config{Annotation: config.AnnotationConfig{
		TripletPresentationBase:         "https://triplet.example.edu/presentation/v3",
		TripletPresentationInternalBase: "http://triplet:8080/presentation/v3",
	}}})

	h := &Handler{}
	got := h.tripletAnnotationPageID("https://scribe.example.edu/v1/item-images/42/manifest/canvas/page-1")
	want := "https://triplet.example.edu/presentation/v3/item-image-42/canvas/page-1/annotations"
	if got != want {
		t.Fatalf("tripletAnnotationPageID = %q, want %q", got, want)
	}
}

func TestCurrentAnnotationItemsReadsTripletFirst(t *testing.T) {
	canvasURI := "https://scribe.example.edu/v1/item-images/42/manifest/canvas/page-1"
	page := annotationPageJSON(t, "https://triplet.test/presentation/v3/item-image-42/canvas/page-1/annotations", canvasURI, "line-1", "from triplet")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", r.Method)
		}
		if r.URL.EscapedPath() != "/presentation/v3/item-image-42/canvas/page-1/annotations" {
			t.Fatalf("path = %s", r.URL.EscapedPath())
		}
		w.Header().Set("Content-Type", "application/ld+json")
		w.Header().Set("ETag", `"triplet-etag"`)
		_, _ = w.Write([]byte(page))
	}))
	t.Cleanup(srv.Close)
	config.Init(config.Runtime{Config: config.Config{Annotation: config.AnnotationConfig{
		TripletPresentationInternalBase: srv.URL + "/presentation/v3",
	}}})

	items, err := (&Handler{}).currentAnnotationItems(context.Background(), canvasURI, "http://scribe.test")
	if err != nil {
		t.Fatalf("currentAnnotationItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	anno := items[0].(map[string]any)
	if got := extractAnnotationText(anno); got != "from triplet" {
		t.Fatalf("annotation text = %q, want %q", got, "from triplet")
	}
}

func TestSaveAnnotationPageUsesTripletIfMatch(t *testing.T) {
	canvasURI := "https://scribe.example.edu/v1/item-images/42/manifest/canvas/page-1"
	current := annotationPageJSON(t, "https://triplet.test/presentation/v3/item-image-42/canvas/page-1/annotations", canvasURI, "line-1", "old")
	var sawPut bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/ld+json")
			w.Header().Set("ETag", `"current-etag"`)
			_, _ = w.Write([]byte(current))
		case http.MethodPut:
			sawPut = true
			if got := r.Header.Get("Authorization"); got != "Bearer write-token" {
				t.Fatalf("Authorization = %q", got)
			}
			if got := r.Header.Get("If-Match"); got != `"current-etag"` {
				t.Fatalf("If-Match = %q, want current ETag", got)
			}
			var page map[string]any
			if err := json.NewDecoder(r.Body).Decode(&page); err != nil {
				t.Fatalf("decode PUT body: %v", err)
			}
			if page["id"] == "" || !strings.Contains(page["id"].(string), "/presentation/v3/item-image-42/canvas/page-1/annotations") {
				t.Fatalf("unexpected page id: %v", page["id"])
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	}))
	t.Cleanup(srv.Close)
	config.Init(config.Runtime{Config: config.Config{Annotation: config.AnnotationConfig{
		TripletPresentationBase:         "https://triplet.test/presentation/v3",
		TripletPresentationInternalBase: srv.URL + "/presentation/v3",
		TripletPresentationWriteToken:   "write-token",
	}}})

	updated := map[string]any{
		"id":              "line-1",
		"type":            "Annotation",
		"textGranularity": "line",
		"motivation":      "supplementing",
		"body": map[string]any{
			"type":  "TextualBody",
			"value": "updated",
		},
		"target": map[string]any{
			"type":   "SpecificResource",
			"source": canvasURI,
			"selector": map[string]any{
				"type":  "FragmentSelector",
				"value": "xywh=1,2,3,4",
			},
		},
	}
	if _, err := (&Handler{}).saveAnnotationPage(context.Background(), canvasURI, []any{updated}); err != nil {
		t.Fatalf("saveAnnotationPage: %v", err)
	}
	if !sawPut {
		t.Fatal("Triplet PUT was not called")
	}
}

func TestCanvasURIFromAnnotationID(t *testing.T) {
	got := canvasURIFromAnnotationID("urn:scribe:annotation:item-image-42:line:line_1")
	want := "/v1/item-images/42/manifest/canvas/page-1"
	if got != want {
		t.Fatalf("canvasURIFromAnnotationID = %q, want %q", got, want)
	}
}

func annotationPageJSON(t *testing.T, pageID, canvasURI, annotationID, text string) string {
	t.Helper()
	page := map[string]any{
		"@context": annotationPageContexts(),
		"id":       pageID,
		"type":     "AnnotationPage",
		"items": []any{
			map[string]any{
				"id":              annotationID,
				"type":            "Annotation",
				"textGranularity": "line",
				"motivation":      "supplementing",
				"body": map[string]any{
					"type":  "TextualBody",
					"value": text,
				},
				"target": map[string]any{
					"type":   "SpecificResource",
					"source": canvasURI,
					"selector": map[string]any{
						"type":  "FragmentSelector",
						"value": "xywh=1,2,3,4",
					},
				},
			},
		},
	}
	b, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
