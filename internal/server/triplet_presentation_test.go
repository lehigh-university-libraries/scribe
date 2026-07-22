package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
)

func TestCanonicalAnnotationPageIDUsesTripletPublicBase(t *testing.T) {
	previous := config.Get()
	configured := previous
	configured.Config.PublicBaseURL = "https://scribe.example.edu"
	configured.Config.Annotation.TripletPresentationBase = "https://triplet.example.edu/presentation/v3"
	config.Init(configured)
	t.Cleanup(func() { config.Init(previous) })

	got, err := (&Handler{}).annotationPageIDForItemImage(42)
	if err != nil {
		t.Fatalf("annotationPageIDForItemImage: %v", err)
	}
	want := "https://triplet.example.edu/presentation/v3/item-image-42/canvas/page-1/annotations"
	if got != want {
		t.Fatalf("annotation page id = %q, want %q", got, want)
	}
}

func TestTripletResourceURLMappingIsExactAndRejectsSSRF(t *testing.T) {
	previous := config.Get()
	configuredRuntime := previous
	configuredRuntime.Config.Annotation.TripletPresentationBase = "https://scribe.example/presentation/v3"
	configuredRuntime.Config.Annotation.TripletPresentationInternalBase = "http://triplet:8080/presentation/v3"
	config.Init(configuredRuntime)
	t.Cleanup(func() { config.Init(previous) })
	h := &Handler{}
	got, configured, err := h.tripletURLForResourceID("https://scribe.example/presentation/v3/item-image-42/canvas/page-1/annotations/items/word-1")
	if err != nil || !configured {
		t.Fatalf("tripletURLForResourceID: configured=%v err=%v", configured, err)
	}
	if want := "http://triplet:8080/presentation/v3/item-image-42/canvas/page-1/annotations/items/word-1"; got != want {
		t.Fatalf("mapped URL = %q, want %q", got, want)
	}

	for _, resourceID := range []string{
		"https://attacker.example/presentation/v3/item-image-42/manifest",
		"https://scribe.example/presentation/v30/item-image-42/manifest",
		"https://scribe.example/presentation/v3/item-image-42/manifest?token=secret",
		"https://scribe.example/presentation/v3/item-image-42/manifest#fragment",
	} {
		if _, _, err := h.tripletURLForResourceID(resourceID); err == nil {
			t.Fatalf("tripletURLForResourceID(%q) accepted an out-of-scope identifier", resourceID)
		}
	}
}

func TestTripletConditionalCreateUsesAuthenticatedExactRoute(t *testing.T) {
	var sawPut atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/presentation/v3/item-image-42/manifest" {
			t.Fatalf("path = %s", r.URL.EscapedPath())
		}
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusNotFound)
		case http.MethodPut:
			sawPut.Store(true)
			if got := r.Header.Get("If-None-Match"); got != "*" {
				t.Fatalf("If-None-Match = %q, want *", got)
			}
			if got := r.Header.Get("If-Match"); got != "" {
				t.Fatalf("If-Match = %q", got)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer write-token" {
				t.Fatalf("Authorization = %q", got)
			}
			w.WriteHeader(http.StatusCreated)
		default:
			t.Fatalf("method = %s", r.Method)
		}
	}))
	t.Cleanup(srv.Close)
	configureTripletTest(t, srv.URL, "write-token")

	resource := tripletPresentationResource{
		ID:      srv.URL + "/presentation/v3/item-image-42/manifest",
		Payload: []byte(`{"id":"` + srv.URL + `/presentation/v3/item-image-42/manifest","type":"Manifest"}`),
	}
	if err := (&Handler{}).putTripletResource(context.Background(), resource); err != nil {
		t.Fatalf("putTripletResource: %v", err)
	}
	if !sawPut.Load() {
		t.Fatal("Triplet PUT was not called")
	}
}

func TestTripletConditionalUpdateRetriesFreshStrongETag(t *testing.T) {
	var getCount, putCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			attempt := getCount.Add(1)
			if attempt == 1 {
				w.Header().Set("ETag", `"first"`)
				_, _ = w.Write([]byte(`{"value":"old"}`))
				return
			}
			w.Header().Set("ETag", `"second"`)
			_, _ = w.Write([]byte(`{"value":"concurrent"}`))
		case http.MethodPut:
			attempt := putCount.Add(1)
			want := `"first"`
			if attempt == 2 {
				want = `"second"`
			}
			if got := r.Header.Get("If-Match"); got != want {
				t.Fatalf("PUT %d If-Match = %q, want %q", attempt, got, want)
			}
			if attempt == 1 {
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("method = %s", r.Method)
		}
	}))
	t.Cleanup(srv.Close)
	configureTripletTest(t, srv.URL, "write-token")

	resource := tripletPresentationResource{ID: srv.URL + "/presentation/v3/resource", Payload: []byte(`{"value":"new"}`)}
	if err := (&Handler{}).putTripletResource(context.Background(), resource); err != nil {
		t.Fatalf("putTripletResource: %v", err)
	}
	if getCount.Load() != 2 || putCount.Load() != 2 {
		t.Fatalf("requests = GET %d PUT %d, want 2 each", getCount.Load(), putCount.Load())
	}
}

func TestTripletByteIdenticalResourceDoesNotRewrite(t *testing.T) {
	payload := []byte(`{"value":"same"}`)
	var puts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			puts.Add(1)
			t.Fatal("byte-identical resource was rewritten")
		}
		w.Header().Set("ETag", `"same"`)
		_, _ = w.Write(payload)
	}))
	t.Cleanup(srv.Close)
	configureTripletTest(t, srv.URL, "write-token")

	resource := tripletPresentationResource{ID: srv.URL + "/presentation/v3/resource", Payload: payload}
	if err := (&Handler{}).putTripletResource(context.Background(), resource); err != nil {
		t.Fatalf("putTripletResource: %v", err)
	}
	if puts.Load() != 0 {
		t.Fatalf("PUT count = %d", puts.Load())
	}
}

func TestTripletClientRejectsEveryRedirect(t *testing.T) {
	var redirectedRequests atomic.Int32
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirectedRequests.Add(1)
	}))
	t.Cleanup(redirectTarget.Close)
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirectTarget.URL+"/stolen", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(internal.Close)
	configureTripletTest(t, internal.URL, "write-token")

	resource := tripletPresentationResource{ID: internal.URL + "/presentation/v3/resource", Payload: []byte(`{}`)}
	if err := (&Handler{}).putTripletResource(context.Background(), resource); err == nil {
		t.Fatal("putTripletResource accepted a redirect")
	}
	if redirectedRequests.Load() != 0 {
		t.Fatalf("redirect target received %d requests", redirectedRequests.Load())
	}
}

func TestTripletConditionalDeleteAndMissingResource(t *testing.T) {
	var deletes atomic.Int32
	var exists atomic.Bool
	exists.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			if !exists.Load() {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("ETag", `"current"`)
		case http.MethodDelete:
			deletes.Add(1)
			if got := r.Header.Get("If-Match"); got != `"current"` {
				t.Fatalf("If-Match = %q", got)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer write-token" {
				t.Fatalf("Authorization = %q", got)
			}
			exists.Store(false)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("method = %s", r.Method)
		}
	}))
	t.Cleanup(srv.Close)
	configureTripletTest(t, srv.URL, "write-token")
	h := &Handler{}
	resourceID := srv.URL + "/presentation/v3/resource"
	if err := h.deleteTripletResource(context.Background(), resourceID); err != nil {
		t.Fatalf("deleteTripletResource: %v", err)
	}
	if err := h.deleteTripletResource(context.Background(), resourceID); err != nil {
		t.Fatalf("delete missing Triplet resource: %v", err)
	}
	if deletes.Load() != 1 {
		t.Fatalf("DELETE count = %d, want 1", deletes.Load())
	}
}

func TestTripletImageGraphDeletionRetainsPageUntilDynamicChildrenAreGone(t *testing.T) {
	var srv *httptest.Server
	var mu sync.Mutex
	stored := make(map[string][]byte)
	deleted := make([]string, 0, 6)
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		payload, exists := stored[r.URL.EscapedPath()]
		mu.Unlock()
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			if !exists {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("ETag", `"stored"`)
			if r.Method == http.MethodGet {
				_, _ = w.Write(payload)
			}
		case http.MethodDelete:
			if !exists {
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			mu.Lock()
			delete(stored, r.URL.EscapedPath())
			deleted = append(deleted, r.URL.EscapedPath())
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("method = %s", r.Method)
		}
	}))
	t.Cleanup(srv.Close)
	configureTripletTest(t, srv.URL, "write-token")

	base := srv.URL + "/presentation/v3"
	pageID, err := iiif.CanonicalPageID(base, 42)
	if err != nil {
		t.Fatal(err)
	}
	canvasID, err := iiif.ItemImageCanvasID(base, 42)
	if err != nil {
		t.Fatal(err)
	}
	annotationID, err := iiif.AnnotationID(pageID, "cleanup-child")
	if err != nil {
		t.Fatal(err)
	}
	page, err := iiif.NewAnnotationPage(iiif.PageIdentity{PublicBaseURL: base, ItemImageID: 42, CanvasURI: canvasID}, []any{
		map[string]any{
			"id": annotationID, "type": "Annotation", "motivation": "supplementing", "textGranularity": "line",
			"body":   map[string]any{"type": "TextualBody", "value": "delete me"},
			"target": canvasID + "#xywh=1,2,3,4",
		},
	})
	if err != nil {
		t.Fatalf("NewAnnotationPage: %v", err)
	}
	itemImageManifestID, err := iiif.ItemImageManifestID(base, 42)
	if err != nil {
		t.Fatal(err)
	}
	paintingPageID, err := iiif.PaintingPageID(base, 42)
	if err != nil {
		t.Fatal(err)
	}
	paintingAnnotationID, err := iiif.PaintingAnnotationID(base, 42)
	if err != nil {
		t.Fatal(err)
	}
	resourceIDs := []string{
		pageID,
		annotationID,
		itemImageManifestID,
		canvasID,
		paintingPageID,
		paintingAnnotationID,
	}
	for _, resourceID := range resourceIDs {
		parsed, parseErr := url.Parse(resourceID)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		stored[parsed.EscapedPath()] = []byte(`{}`)
	}
	parsedPage, _ := url.Parse(pageID)
	stored[parsedPage.EscapedPath()] = page

	if err := (&Handler{}).deleteTripletImageGraph(context.Background(), 42); err != nil {
		t.Fatalf("deleteTripletImageGraph: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(stored) != 0 {
		t.Fatalf("Triplet image graph retained %d resources: %#v", len(stored), stored)
	}
	childPath, _ := url.Parse(annotationID)
	pagePath, _ := url.Parse(pageID)
	childIndex := indexOfString(deleted, childPath.EscapedPath())
	pageIndex := indexOfString(deleted, pagePath.EscapedPath())
	if childIndex == len(deleted) || pageIndex == len(deleted) || childIndex > pageIndex {
		t.Fatalf("page was deleted before dynamic child: %v", deleted)
	}
}

func indexOfString(values []string, want string) int {
	for index, value := range values {
		if value == want {
			return index
		}
	}
	return len(values)
}

func TestTripletFailureDoesNotExposeResponseContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("secret upstream response body"))
	}))
	t.Cleanup(srv.Close)
	configureTripletTest(t, srv.URL, "")

	_, err := (&Handler{}).readTripletResource(context.Background(), srv.URL+"/presentation/v3/resource", false)
	if err == nil {
		t.Fatal("readTripletResource succeeded on HTTP 502")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("sanitized error leaked response content: %q", err)
	}
	if got := err.Error(); got != "triplet presentation HEAD status 502" {
		t.Fatalf("sanitized error = %q", got)
	}
}

func TestTripletChildResourcesCompleteBeforeParents(t *testing.T) {
	const childCount = 24
	var completedChildren atomic.Int32
	var mu sync.Mutex
	created := make(map[string]bool)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.EscapedPath()
		switch r.Method {
		case http.MethodGet:
			mu.Lock()
			exists := created[path]
			mu.Unlock()
			if !exists {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("ETag", `"existing"`)
			_, _ = w.Write([]byte(`{}`))
		case http.MethodPut:
			if strings.HasSuffix(path, "/manifest") && completedChildren.Load() != childCount {
				t.Errorf("parent became visible after only %d/%d children", completedChildren.Load(), childCount)
			}
			mu.Lock()
			created[path] = true
			mu.Unlock()
			if strings.Contains(path, "/child-") {
				completedChildren.Add(1)
			}
			w.WriteHeader(http.StatusCreated)
		default:
			t.Fatalf("method = %s", r.Method)
		}
	}))
	t.Cleanup(srv.Close)
	configureTripletTest(t, srv.URL, "write-token")

	resources := make([]tripletPresentationResource, 0, childCount+1)
	for index := range childCount {
		resources = append(resources, tripletPresentationResource{
			ID: srv.URL + fmt.Sprintf("/presentation/v3/child-%d", index), Payload: []byte(`{}`), Parallel: true,
		})
	}
	resources = append(resources, tripletPresentationResource{ID: srv.URL + "/presentation/v3/manifest", Payload: []byte(`{}`)})
	if err := (&Handler{}).publishTripletResources(context.Background(), resources); err != nil {
		t.Fatalf("publishTripletResources: %v", err)
	}
}

func TestAnnotationMirrorRetryDelayIsBounded(t *testing.T) {
	if got := annotationMirrorRetryDelay(1); got != time.Second {
		t.Fatalf("first retry delay = %s, want 1s", got)
	}
	if got := annotationMirrorRetryDelay(99); got != 128*time.Second {
		t.Fatalf("bounded retry delay = %s, want 128s", got)
	}
}

func TestAnnotationMirrorOperationEndsBeforeLease(t *testing.T) {
	if annotationMirrorOperationLimit >= annotationMirrorLeaseDuration {
		t.Fatalf("annotation mirror operation %s must end before lease %s", annotationMirrorOperationLimit, annotationMirrorLeaseDuration)
	}
}

func TestResourceCleanupRetryDelayIsBounded(t *testing.T) {
	if got := resourceCleanupRetryDelay(1); got != time.Second {
		t.Fatalf("first retry delay = %s, want 1s", got)
	}
	if got := resourceCleanupRetryDelay(99); got != time.Hour {
		t.Fatalf("bounded retry delay = %s, want 1h", got)
	}
}

func configureTripletTest(t *testing.T, serverURL, token string) {
	t.Helper()
	previous := config.Get()
	configured := previous
	configured.Config.Annotation.TripletPresentationBase = serverURL + "/presentation/v3"
	configured.Config.Annotation.TripletPresentationInternalBase = serverURL + "/presentation/v3"
	configured.Config.Annotation.TripletPresentationWriteToken = token
	config.Init(configured)
	t.Cleanup(func() { config.Init(previous) })
}
