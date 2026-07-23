package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lehigh-university-libraries/scribe/internal/config"
	dbstore "github.com/lehigh-university-libraries/scribe/internal/db"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

type tripletTestStore struct {
	mu         sync.Mutex
	resources  map[string][]byte
	versions   map[string]uint64
	failDelete map[string]int
}

func newTripletTestStore(t *testing.T) (*tripletTestStore, *httptest.Server) {
	t.Helper()
	store := &tripletTestStore{
		resources:  make(map[string][]byte),
		versions:   make(map[string]uint64),
		failDelete: make(map[string]int),
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		path := request.URL.EscapedPath()
		store.mu.Lock()
		defer store.mu.Unlock()
		payload, exists := store.resources[path]
		etag := `"` + strconv.FormatUint(store.versions[path], 10) + `"`
		switch request.Method {
		case http.MethodGet, http.MethodHead:
			if !exists {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("ETag", etag)
			if request.Method == http.MethodGet {
				_, _ = w.Write(payload)
			}
		case http.MethodPut:
			if exists {
				if request.Header.Get("If-Match") != etag {
					w.WriteHeader(http.StatusPreconditionFailed)
					return
				}
			} else if request.Header.Get("If-None-Match") != "*" {
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			body, err := io.ReadAll(request.Body)
			if err != nil {
				http.Error(w, "read test request", http.StatusInternalServerError)
				return
			}
			store.versions[path]++
			store.resources[path] = bytes.Clone(body)
			if exists {
				w.WriteHeader(http.StatusNoContent)
			} else {
				w.WriteHeader(http.StatusCreated)
			}
		case http.MethodDelete:
			if store.failDelete[path] > 0 {
				store.failDelete[path]--
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			if !exists || request.Header.Get("If-Match") != etag {
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			delete(store.resources, path)
			delete(store.versions, path)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)
	return store, server
}

func (s *tripletTestStore) failNextDelete(resourceID string) {
	parsed, err := url.Parse(resourceID)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failDelete[parsed.EscapedPath()]++
}

func (s *tripletTestStore) contains(resourceID string) bool {
	parsed, err := url.Parse(resourceID)
	if err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, exists := s.resources[parsed.EscapedPath()]
	return exists
}

func TestPublishedPresentationGraphUsesCanonicalSnapshotsAndTripletIDs(t *testing.T) {
	database := openTestDB(t)
	ctx := context.Background()
	previous := config.Get()
	configured := previous
	configured.Config.PublicBaseURL = "https://scribe.example"
	configured.Config.IIIF.Base = "https://scribe.example/iiif/3"
	configured.Config.Annotation.APIInternalBase = "http://api:8080"
	configured.Config.Annotation.TripletPresentationBase = "https://scribe.example/presentation/v3"
	config.Init(configured)
	t.Cleanup(func() { config.Init(previous) })

	workspaceID, userID := createServerTestWorkspace(t, database)
	image := createServerTestUploadItemImage(t, database, workspaceID, userID, "https://source.invalid/replaced-canvas")
	itemStore := store.NewItemStore(database)
	annotationStore := store.NewAnnotationStore(database)
	presentationBase := (&Handler{}).publicAnnotationBaseURL()
	canvasID, err := iiif.ItemImageCanvasID(presentationBase, image.ID)
	if err != nil {
		t.Fatalf("ItemImageCanvasID: %v", err)
	}
	if err := itemStore.UpdateImageCanvasURI(ctx, image.ID, canvasID); err != nil {
		t.Fatalf("UpdateImageCanvasURI: %v", err)
	}
	uploadName := strings.Repeat("a", 64) + "-" + uuid.NewString() + ".jpg"
	if _, err := database.ExecContext(ctx, `UPDATE item_images SET image_url = ? WHERE id = ?`, "/static/uploads/"+uploadName, image.ID); err != nil {
		t.Fatalf("set immutable upload URL: %v", err)
	}
	image, err = itemStore.GetImageForWorkspace(ctx, image.ID, workspaceID)
	if err != nil {
		t.Fatalf("reload item image: %v", err)
	}

	pageID, err := iiif.CanonicalPageID(presentationBase, image.ID)
	if err != nil {
		t.Fatalf("CanonicalPageID: %v", err)
	}
	annotationID, err := iiif.AnnotationID(pageID, "publication-graph-line")
	if err != nil {
		t.Fatalf("AnnotationID: %v", err)
	}
	annotation := mustDecodeAnnotation(canonicalMutationTestAnnotation(annotationID, canvasID, "published words"))
	annotation["scribe:testExtension"] = map[string]any{"preserved": true}
	pagePayload, err := iiif.NewAnnotationPage(iiif.PageIdentity{
		PublicBaseURL: presentationBase,
		ItemImageID:   image.ID,
		CanvasURI:     canvasID,
	}, []any{annotation})
	if err != nil {
		t.Fatalf("NewAnnotationPage: %v", err)
	}
	draft, err := annotationStore.SavePage(ctx, store.AnnotationPage{
		WorkspaceID: workspaceID,
		ItemImageID: image.ID,
		PageID:      pageID,
		CanvasURI:   canvasID,
		Payload:     string(pagePayload),
	}, 0)
	if err != nil {
		t.Fatalf("SavePage: %v", err)
	}
	if _, err := annotationStore.PublishPage(ctx, workspaceID, image.ID, store.AnnotationPublicationOptions{
		ExpectedRevision: draft.Revision,
		PublishedByUserID: func() *uint64 {
			value := userID
			return &value
		}(),
	}); err != nil {
		t.Fatalf("PublishPage: %v", err)
	}

	h := &Handler{items: itemStore, annotations: annotationStore}
	resources, err := h.buildPublishedPresentationResources(ctx, image.ID)
	if err != nil {
		t.Fatalf("buildPublishedPresentationResources: %v", err)
	}
	if got, want := len(resources), 6; got != want {
		t.Fatalf("resource count = %d, want %d", got, want)
	}
	paintingAnnotationID, err := iiif.PaintingAnnotationID(presentationBase, image.ID)
	if err != nil {
		t.Fatal(err)
	}
	paintingPageID, err := iiif.PaintingPageID(presentationBase, image.ID)
	if err != nil {
		t.Fatal(err)
	}
	itemImageManifestID, err := iiif.ItemImageManifestID(presentationBase, image.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantIDs := []string{
		annotationID,
		paintingAnnotationID,
		paintingPageID,
		pageID,
		canvasID,
		itemImageManifestID,
	}
	for index, resource := range resources {
		if resource.ID != wantIDs[index] {
			t.Fatalf("resource %d id = %q, want %q", index, resource.ID, wantIDs[index])
		}
		if resource.Parallel != (index == 0) {
			t.Fatalf("resource %d parallel = %v", index, resource.Parallel)
		}
		assertValidPresentationResource(t, resource.Payload)
	}
	if !strings.Contains(string(resources[0].Payload), `"scribe:testExtension":{"preserved":true}`) {
		t.Fatalf("standalone Annotation lost extension properties: %s", resources[0].Payload)
	}
	if !strings.Contains(string(resources[3].Payload), `"scribe:testExtension":{"preserved":true}`) {
		t.Fatalf("canonical AnnotationPage lost extension properties: %s", resources[3].Payload)
	}

	var paintingAnnotation map[string]any
	if err := iiif.DecodeJSON(resources[1].Payload, &paintingAnnotation); err != nil {
		t.Fatalf("decode painting Annotation: %v", err)
	}
	body, _ := paintingAnnotation["body"].(map[string]any)
	encodedSource := url.PathEscape("http://api:8080/static/uploads/" + uploadName)
	wantImageID := "https://scribe.example/iiif/3/" + encodedSource + "/full/max/0/default.jpg"
	if body["id"] != wantImageID {
		t.Fatalf("painting body id = %v, want %q", body["id"], wantImageID)
	}

	aggregateResource, err := h.buildPublishedItemManifest(ctx, image.ItemID)
	if err != nil || aggregateResource == nil {
		t.Fatalf("buildPublishedItemManifest = %+v/%v", aggregateResource, err)
	}
	itemManifestID, err := iiif.ItemManifestID(presentationBase, image.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	if aggregateResource.ID != itemManifestID {
		t.Fatalf("aggregate Manifest id = %q, want %q", aggregateResource.ID, itemManifestID)
	}
	assertValidPresentationResource(t, aggregateResource.Payload)
	var aggregate struct {
		Type  string `json:"type"`
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(aggregateResource.Payload, &aggregate); err != nil {
		t.Fatalf("decode aggregate Manifest: %v", err)
	}
	if aggregate.Type != "Manifest" || len(aggregate.Items) != 1 || aggregate.Items[0].ID != canvasID {
		t.Fatalf("aggregate Manifest = %+v", aggregate)
	}
}

func TestRepublishingAnnotationPageDeletesRemovedStandaloneChild(t *testing.T) {
	database := openTestDB(t)
	ctx := context.Background()
	remoteStore, triplet := newTripletTestStore(t)
	previousRuntime := config.Get()
	t.Cleanup(func() { config.Init(previousRuntime) })
	presentationBase := triplet.URL + "/presentation/v3"
	config.Init(config.Runtime{Config: config.Config{
		PublicBaseURL: triplet.URL,
		IIIF:          config.IIIFConfig{Base: triplet.URL + "/iiif/3"},
		Annotation: config.AnnotationConfig{
			APIInternalBase:                 "http://api:8080",
			TripletPresentationBase:         presentationBase,
			TripletPresentationInternalBase: presentationBase,
			TripletPresentationWriteToken:   "test-triplet-presentation-write-token-32-bytes-minimum",
		},
	}})

	workspaceID, userID := createServerTestWorkspace(t, database)
	image := createServerTestUploadItemImage(t, database, workspaceID, userID, "https://source.example/canvas/removed-child")
	itemStore := store.NewItemStore(database)
	annotationStore := store.NewAnnotationStore(database)
	pageID, err := iiif.CanonicalPageID(presentationBase, image.ID)
	if err != nil {
		t.Fatal(err)
	}
	annotationID, err := iiif.AnnotationID(pageID, "removed-line")
	if err != nil {
		t.Fatal(err)
	}
	annotation := mustDecodeAnnotation(canonicalMutationTestAnnotation(annotationID, image.CanvasURI, "remove me"))
	firstPayload, err := iiif.NewAnnotationPage(iiif.PageIdentity{
		PublicBaseURL: presentationBase,
		ItemImageID:   image.ID,
		CanvasURI:     image.CanvasURI,
	}, []any{annotation})
	if err != nil {
		t.Fatal(err)
	}
	first, err := annotationStore.SavePage(ctx, store.AnnotationPage{
		WorkspaceID: workspaceID, ItemImageID: image.ID, PageID: pageID,
		CanvasURI: image.CanvasURI, Payload: string(firstPayload),
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := annotationStore.PublishPage(ctx, workspaceID, image.ID, store.AnnotationPublicationOptions{ExpectedRevision: first.Revision}); err != nil {
		t.Fatal(err)
	}
	handler := &Handler{items: itemStore, annotations: annotationStore}
	firstDelivery, err := annotationStore.ClaimAnnotationMirror(ctx, annotationMirrorLeaseDuration)
	if err != nil || firstDelivery == nil {
		t.Fatalf("claim first Annotation mirror = %+v/%v", firstDelivery, err)
	}
	if err := handler.deliverAnnotationMirror(ctx, *firstDelivery); err != nil {
		t.Fatalf("deliver first Annotation mirror: %v", err)
	}
	if err := annotationStore.CompleteAnnotationMirror(ctx, *firstDelivery); err != nil {
		t.Fatal(err)
	}
	if !remoteStore.contains(annotationID) {
		t.Fatal("first publication did not materialize standalone Annotation")
	}

	secondPayload, err := iiif.NewAnnotationPage(iiif.PageIdentity{
		PublicBaseURL: presentationBase,
		ItemImageID:   image.ID,
		CanvasURI:     image.CanvasURI,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := annotationStore.SavePage(ctx, store.AnnotationPage{
		WorkspaceID: workspaceID, ItemImageID: image.ID, PageID: pageID,
		CanvasURI: image.CanvasURI, Payload: string(secondPayload),
	}, first.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := annotationStore.PublishPage(ctx, workspaceID, image.ID, store.AnnotationPublicationOptions{ExpectedRevision: second.Revision}); err != nil {
		t.Fatal(err)
	}
	secondDelivery, err := annotationStore.ClaimAnnotationMirror(ctx, annotationMirrorLeaseDuration)
	if err != nil || secondDelivery == nil {
		t.Fatalf("claim replacement Annotation mirror = %+v/%v", secondDelivery, err)
	}
	remoteStore.failNextDelete(annotationID)
	firstDeleteErr := handler.deliverAnnotationMirror(ctx, *secondDelivery)
	if firstDeleteErr == nil {
		t.Fatal("replacement delivery unexpectedly succeeded when Triplet rejected the stale-child DELETE")
	}
	pending, err := annotationStore.LoadAnnotationMirrorTombstones(ctx, image.ID)
	if err != nil || len(pending.AnnotationIDs) != 1 || pending.AnnotationIDs[0] != annotationID {
		t.Fatalf("tombstones after failed DELETE = %+v/%v", pending, err)
	}
	if err := annotationStore.RetryAnnotationMirror(ctx, *secondDelivery, firstDeleteErr, time.Now().UTC().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	retryDelivery, err := annotationStore.ClaimAnnotationMirror(ctx, annotationMirrorLeaseDuration)
	if err != nil || retryDelivery == nil {
		t.Fatalf("claim replacement Annotation mirror retry = %+v/%v", retryDelivery, err)
	}
	if err := handler.deliverAnnotationMirror(ctx, *retryDelivery); err != nil {
		t.Fatalf("retry replacement Annotation mirror: %v", err)
	}
	if err := annotationStore.CompleteAnnotationMirror(ctx, *retryDelivery); err != nil {
		t.Fatal(err)
	}
	if remoteStore.contains(annotationID) {
		t.Fatal("removed standalone Annotation remained dereferenceable after replacement delivery")
	}
	tombstones, err := annotationStore.LoadAnnotationMirrorTombstones(ctx, image.ID)
	if err != nil || len(tombstones.AnnotationIDs) != 0 {
		t.Fatalf("drained Annotation tombstones = %+v/%v", tombstones, err)
	}
}

func TestItemManifestProjectionGenerationFencesConcurrentImagePublications(t *testing.T) {
	database := openTestDB(t)
	ctx := context.Background()
	previousRuntime := config.Get()
	t.Cleanup(func() { config.Init(previousRuntime) })
	config.Init(config.Runtime{Config: config.Config{
		PublicBaseURL: "https://scribe.example",
		IIIF:          config.IIIFConfig{Base: "https://scribe.example/iiif/3"},
		Annotation: config.AnnotationConfig{
			APIInternalBase:                 "http://api:8080",
			TripletPresentationBase:         "https://scribe.example/presentation/v3",
			TripletPresentationInternalBase: "http://triplet:8080/presentation/v3",
			TripletPresentationWriteToken:   "test-triplet-presentation-write-token-32-bytes-minimum",
		},
	}})

	workspaceID, userID := createServerTestWorkspace(t, database)
	firstImage := createServerTestUploadItemImage(t, database, workspaceID, userID, "https://source.example/canvas/projection-1")
	itemStore := store.NewItemStore(database)
	secondImage, err := itemStore.AddImage(ctx, dbstore.CreateItemImageParams{
		ItemID: firstImage.ItemID, Sequence: 2,
		ImageURL:  "https://source.example/image/projection-2.jpg",
		CanvasURI: "https://source.example/canvas/projection-2",
		Width:     10000, Height: 10000,
	})
	if err != nil {
		t.Fatal(err)
	}
	annotationStore := store.NewAnnotationStore(database)
	publishImage := func(t *testing.T, image store.ItemImage) {
		t.Helper()
		pageID, err := iiif.CanonicalPageID("https://scribe.example/presentation/v3", image.ID)
		if err != nil {
			t.Fatal(err)
		}
		payload, err := iiif.NewAnnotationPage(iiif.PageIdentity{
			PublicBaseURL: "https://scribe.example/presentation/v3", ItemImageID: image.ID, CanvasURI: image.CanvasURI,
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		page, err := annotationStore.SavePage(ctx, store.AnnotationPage{
			WorkspaceID: workspaceID, ItemImageID: image.ID, PageID: pageID, CanvasURI: image.CanvasURI, Payload: string(payload),
		}, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := annotationStore.PublishPage(ctx, workspaceID, image.ID, store.AnnotationPublicationOptions{ExpectedRevision: page.Revision}); err != nil {
			t.Fatal(err)
		}
	}

	publishImage(t, firstImage)
	var oldID, oldGeneration uint64
	if err := database.QueryRowContext(ctx, `
SELECT id, generation
FROM resource_cleanup_outbox
WHERE kind = 'triplet_presentation_item' AND resource_key = ?`, firstImage.ItemID).Scan(&oldID, &oldGeneration); err != nil {
		t.Fatal(err)
	}
	oldLease := time.Now().UTC().Add(90 * time.Second).Truncate(time.Second)
	if _, err := database.ExecContext(ctx, `
UPDATE resource_cleanup_outbox
SET status = 'processing', attempt_count = 1, lease_until = ?, locked_by = 'old-item-projection'
WHERE id = ? AND generation = ?`, oldLease, oldID, oldGeneration); err != nil {
		t.Fatal(err)
	}

	publishImage(t, secondImage)
	var newGeneration uint64
	var status string
	var nextAttempt time.Time
	if err := database.QueryRowContext(ctx, `
SELECT generation, status, next_attempt_at
FROM resource_cleanup_outbox
WHERE kind = 'triplet_presentation_item' AND resource_key = ?`, firstImage.ItemID).Scan(&newGeneration, &status, &nextAttempt); err != nil {
		t.Fatal(err)
	}
	if newGeneration != oldGeneration+1 || status != "pending" || nextAttempt.Before(oldLease) {
		t.Fatalf("replacement item projection = generation %d status %s next %s; want generation %d pending at/after %s", newGeneration, status, nextAttempt, oldGeneration+1, oldLease)
	}
	oldDelivery := store.ResourceCleanupDelivery{
		ID: oldID, Kind: store.ResourceCleanupTripletPresentationItem, ResourceKey: firstImage.ItemID,
		WorkspaceID: workspaceID, Generation: oldGeneration, LeaseOwner: "old-item-projection",
	}
	if err := itemStore.CompleteResourceCleanup(ctx, oldDelivery); !errors.Is(err, store.ErrResourceCleanupLease) {
		t.Fatalf("stale item projection completion error = %v, want ErrResourceCleanupLease", err)
	}

	handler := &Handler{items: itemStore, annotations: annotationStore}
	aggregate, err := handler.buildPublishedItemManifest(ctx, firstImage.ItemID)
	if err != nil || aggregate == nil {
		t.Fatalf("build latest aggregate Manifest = %+v/%v", aggregate, err)
	}
	var manifest struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(aggregate.Payload, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Items) != 2 {
		t.Fatalf("latest aggregate Manifest has %d Canvases, want 2", len(manifest.Items))
	}
	for _, image := range []store.ItemImage{firstImage, secondImage} {
		resources, err := handler.buildPublishedPresentationResources(ctx, image.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, resource := range resources {
			if resource.ID == aggregate.ID {
				t.Fatalf("image-scoped delivery for %d still contains shared aggregate Manifest", image.ID)
			}
		}
	}
}

func assertValidPresentationResource(t *testing.T, raw []byte) {
	t.Helper()
	var identity struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &identity); err != nil {
		t.Fatalf("decode Presentation resource: %v", err)
	}
	var err error
	switch identity.Type {
	case "Manifest":
		err = iiif.ValidateManifest(raw)
	case "Canvas":
		err = iiif.ValidateCanvas(raw)
	case "AnnotationPage":
		err = iiif.ValidateAnnotationPage(raw)
	case "Annotation":
		err = iiif.ValidateAnnotation(raw)
	default:
		t.Fatalf("unexpected Presentation resource type %q", identity.Type)
	}
	if err != nil {
		t.Fatalf("invalid %s: %v\n%s", identity.Type, err, raw)
	}
}
