package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/lehigh-university-libraries/scribe/internal/auth"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	"github.com/lehigh-university-libraries/scribe/internal/worklimit"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
)

func TestEnforceManifestCanvasLimitCountsDeclaredV2AndV3Canvases(t *testing.T) {
	tests := []struct {
		name     string
		manifest map[string]any
	}{
		{
			name: "Presentation 3 items",
			manifest: map[string]any{
				"type":  "Manifest",
				"items": []any{map[string]any{}, map[string]any{}, map[string]any{}, map[string]any{}},
			},
		},
		{
			name: "Presentation 2 sequences",
			manifest: map[string]any{
				"@type": "sc:Manifest",
				"sequences": []any{
					map[string]any{"canvases": []any{map[string]any{}, map[string]any{}}},
					map[string]any{"canvases": []any{map[string]any{}, map[string]any{}}},
				},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := enforceManifestCanvasLimit(test.manifest, 4); err != nil {
				t.Fatalf("exact canvas limit rejected: %v", err)
			}
			err := enforceManifestCanvasLimit(test.manifest, 3)
			if err == nil || !strings.Contains(err.Error(), "4 canvases") || !strings.Contains(err.Error(), "maximum is 3") {
				t.Fatalf("oversized manifest error = %v", err)
			}
		})
	}
	if err := enforceManifestCanvasLimit(map[string]any{"type": "Manifest"}, 0); err == nil {
		t.Fatal("non-positive manifest canvas limit was accepted")
	}
}

func TestFetchManifestRejectsSourceLargerThanPersistedProjectionLimit(t *testing.T) {
	source := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(strings.Repeat(" ", iiif.MaxSourceManifestBytes+1)))
	}))
	t.Cleanup(source.Close)

	if _, raw, err := fetchIIIFManifest(context.Background(), source.URL, 1); err == nil || raw != nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized source result = raw %d/error %v", len(raw), err)
	}
}

func TestSourceManifestBudgetRejectsBeforeTenantContentWrite(t *testing.T) {
	database := openTestDB(t)
	source, payload := newChoiceManifestSourceWithoutHOCR(t)
	handler := NewHandler(
		store.NewOCRRunStore(database), store.NewItemStore(database), store.NewContextStore(database),
		store.NewAnnotationStore(database), store.NewTranscriptionJobStore(database), nil, nil, nil,
	)
	handler.maxManifestImportBytes = uint64(len(payload) - 1)
	idempotencyKey := "source-manifest-byte-budget-" + uuid.NewString()
	digest := sha256.Sum256([]byte(idempotencyKey))
	storedKey := fmt.Sprintf("%x", digest[:])
	registerManifestImportFixtureCleanup(t, database, source.URL+"/manifest", storedKey)

	_, err := handler.ImportManifest(context.Background(), connect.NewRequest(&scribev1.ImportManifestRequest{
		Name: "Source budget", ManifestUrl: source.URL + "/manifest", IdempotencyKey: idempotencyKey,
	}))
	if connect.CodeOf(err) != connect.CodeResourceExhausted || !strings.Contains(err.Error(), "source manifest") {
		t.Fatalf("source manifest budget error = %v", err)
	}
	assertNoManifestTenantStateWithFailedReservation(t, database, source.URL+"/manifest", storedKey)
}

func TestOversizedManifestIsRejectedBeforeTenantContentWrite(t *testing.T) {
	database := openTestDB(t)
	ctx := context.Background()

	var manifestSource *httptest.Server
	manifestSource = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/manifest":
			response.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(response).Encode(testPresentation2Manifest(manifestSource.URL, 3))
		case "/hocr.xml":
			response.Header().Set("Content-Type", "text/vnd.hocr+html; charset=utf-8")
			_, _ = response.Write([]byte(minimalHOCR))
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(manifestSource.Close)

	items := store.NewItemStore(database)
	contexts := store.NewContextStore(database)
	jobs := store.NewTranscriptionJobStore(database)
	handler := NewHandler(
		store.NewOCRRunStore(database),
		items,
		contexts,
		store.NewAnnotationStore(database),
		jobs,
		nil,
		nil,
		nil,
	)
	handler.maxManifestCanvases = 2

	idempotencyKey := "manifest-limit-atomic-rejection-" + uuid.NewString()
	idempotencyDigest := sha256.Sum256([]byte(idempotencyKey))
	storedKey := fmt.Sprintf("%x", idempotencyDigest[:])
	registerManifestImportFixtureCleanup(t, database, manifestSource.URL+"/manifest", storedKey)

	_, err := handler.ImportManifest(ctx, connect.NewRequest(&scribev1.ImportManifestRequest{
		Name:           "Oversized manifest",
		ManifestUrl:    manifestSource.URL + "/manifest",
		IdempotencyKey: idempotencyKey,
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "3 canvases") || !strings.Contains(err.Error(), "maximum is 2") {
		t.Fatalf("CreateItem oversized manifest error = %v", err)
	}

	assertNoManifestTenantStateWithFailedReservation(t, database, manifestSource.URL+"/manifest", storedKey)
}

func TestManifestHOCRAggregateBudgetRejectsBeforeTenantContentWrite(t *testing.T) {
	database := openTestDB(t)
	ctx := context.Background()
	var hocrRequests atomic.Int32

	var manifestSource *httptest.Server
	manifestSource = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/manifest":
			response.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(response).Encode(testPresentation2Manifest(manifestSource.URL, 3))
		case "/hocr.xml":
			hocrRequests.Add(1)
			response.Header().Set("Content-Type", "text/vnd.hocr+html; charset=utf-8")
			_, _ = response.Write([]byte(minimalHOCR))
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(manifestSource.Close)

	items := store.NewItemStore(database)
	handler := NewHandler(
		store.NewOCRRunStore(database),
		items,
		store.NewContextStore(database),
		store.NewAnnotationStore(database),
		store.NewTranscriptionJobStore(database),
		nil,
		nil,
		nil,
	)
	handler.maxManifestImportBytes = 12

	idempotencyKey := "manifest-byte-limit-atomic-rejection-" + uuid.NewString()
	idempotencyDigest := sha256.Sum256([]byte(idempotencyKey))
	storedKey := fmt.Sprintf("%x", idempotencyDigest[:])
	registerManifestImportFixtureCleanup(t, database, manifestSource.URL+"/manifest", storedKey)

	_, err := handler.ImportManifest(ctx, connect.NewRequest(&scribev1.ImportManifestRequest{
		Name:           "Oversized hOCR aggregate",
		ManifestUrl:    manifestSource.URL + "/manifest",
		IdempotencyKey: idempotencyKey,
	}))
	if connect.CodeOf(err) != connect.CodeResourceExhausted || !strings.Contains(err.Error(), "manifest import byte budget") {
		t.Fatalf("CreateItem aggregate hOCR error = %v", err)
	}
	if got := hocrRequests.Load(); got < 1 || got > maxConcurrentManifestHOCRFetches {
		t.Fatalf("hOCR requests = %d, want a bounded concurrent wave of 1..%d", got, maxConcurrentManifestHOCRFetches)
	}
	assertNoManifestTenantStateWithFailedReservation(t, database, manifestSource.URL+"/manifest", storedKey)
}

func TestManifestImportDeadlineBoundsWholeHOCRFanout(t *testing.T) {
	database := openTestDB(t)
	var active atomic.Int32
	var peak atomic.Int32
	var manifestSource *httptest.Server
	manifestSource = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/manifest":
			response.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(response).Encode(testPresentation2Manifest(manifestSource.URL, 12))
		case "/hocr.xml":
			current := active.Add(1)
			defer active.Add(-1)
			for observed := peak.Load(); current > observed && !peak.CompareAndSwap(observed, current); observed = peak.Load() {
			}
			<-request.Context().Done()
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(manifestSource.Close)
	handler := NewHandler(
		store.NewOCRRunStore(database), store.NewItemStore(database), store.NewContextStore(database),
		store.NewAnnotationStore(database), store.NewTranscriptionJobStore(database), nil, nil, nil,
	)
	handler.manifestImportTimeout = 500 * time.Millisecond
	idempotencyKey := "manifest-aggregate-deadline-" + uuid.NewString()
	digest := sha256.Sum256([]byte(idempotencyKey))
	storedKey := fmt.Sprintf("%x", digest[:])
	registerManifestImportFixtureCleanup(t, database, manifestSource.URL+"/manifest", storedKey)
	_, err := handler.ImportManifest(context.Background(), connect.NewRequest(&scribev1.ImportManifestRequest{
		Name: "Timed manifest", ManifestUrl: manifestSource.URL + "/manifest", IdempotencyKey: idempotencyKey,
	}))
	if connect.CodeOf(err) != connect.CodeDeadlineExceeded {
		t.Fatalf("manifest aggregate deadline error = %v, want deadline_exceeded", err)
	}
	if got := peak.Load(); got < 1 || got > maxConcurrentManifestHOCRFetches {
		t.Fatalf("peak hOCR requests = %d, want 1..%d", got, maxConcurrentManifestHOCRFetches)
	}
	assertNoManifestTenantStateWithFailedReservation(t, database, manifestSource.URL+"/manifest", storedKey)
}

func TestOversizedManifestLabelIsRejectedBeforeHOCRFetchOrTenantContentWrite(t *testing.T) {
	database := openTestDB(t)
	ctx := context.Background()
	var hocrRequests atomic.Int32

	var manifestSource *httptest.Server
	manifestSource = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/manifest":
			manifest := testPresentation2Manifest(manifestSource.URL, 1)
			manifest["label"] = strings.Repeat("x", maxItemNameRunes+1)
			response.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(response).Encode(manifest)
		case "/hocr.xml":
			hocrRequests.Add(1)
			_, _ = response.Write([]byte(minimalHOCR))
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(manifestSource.Close)

	handler := NewHandler(
		store.NewOCRRunStore(database),
		store.NewItemStore(database),
		store.NewContextStore(database),
		store.NewAnnotationStore(database),
		store.NewTranscriptionJobStore(database),
		nil,
		nil,
		nil,
	)
	idempotencyKey := "manifest-label-atomic-rejection-" + uuid.NewString()
	digest := sha256.Sum256([]byte(idempotencyKey))
	storedKey := fmt.Sprintf("%x", digest[:])
	registerManifestImportFixtureCleanup(t, database, manifestSource.URL+"/manifest", storedKey)
	_, err := handler.ImportManifest(ctx, connect.NewRequest(&scribev1.ImportManifestRequest{
		ManifestUrl:    manifestSource.URL + "/manifest",
		IdempotencyKey: idempotencyKey,
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "at most 255 characters") {
		t.Fatalf("CreateItem oversized manifest label error = %v", err)
	}
	if got := hocrRequests.Load(); got != 0 {
		t.Fatalf("oversized manifest label triggered %d hOCR requests", got)
	}
	assertNoManifestTenantStateWithFailedReservation(t, database, manifestSource.URL+"/manifest", storedKey)
}

func TestManifestHOCRPrefetchUsesBoundedConcurrency(t *testing.T) {
	var active atomic.Int32
	var peak atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		current := active.Add(1)
		defer active.Add(-1)
		for observed := peak.Load(); current > observed && !peak.CompareAndSwap(observed, current); observed = peak.Load() {
		}
		time.Sleep(75 * time.Millisecond)
		response.Header().Set("Content-Type", "text/vnd.hocr+html; charset=utf-8")
		_, _ = response.Write([]byte(minimalHOCR))
	}))
	t.Cleanup(source.Close)

	canvases := make([]canvasInfo, 8)
	for index := range canvases {
		canvases[index] = canvasInfo{hocrURL: fmt.Sprintf("%s/hocr/%d", source.URL, index)}
	}
	prefetched, _, err := prefetchManifestHOCR(context.Background(), canvases, 1<<20)
	if err != nil {
		t.Fatalf("prefetchManifestHOCR: %v", err)
	}
	if got := peak.Load(); got < 2 || got > maxConcurrentManifestHOCRFetches {
		t.Fatalf("peak hOCR requests = %d, want 2..%d", got, maxConcurrentManifestHOCRFetches)
	}
	for index, canvas := range prefetched {
		if canvas.hocrXML == "" || canvas.plainText == "" {
			t.Fatalf("canvas %d was not prefetched", index+1)
		}
	}
}

func TestManifestHOCRPrefetchHonorsAggregateDeadline(t *testing.T) {
	var active atomic.Int32
	var peak atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		current := active.Add(1)
		defer active.Add(-1)
		for observed := peak.Load(); current > observed && !peak.CompareAndSwap(observed, current); observed = peak.Load() {
		}
		<-request.Context().Done()
	}))
	t.Cleanup(source.Close)

	canvases := make([]canvasInfo, 20)
	for index := range canvases {
		canvases[index].hocrURL = fmt.Sprintf("%s/hocr/%d", source.URL, index)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	if _, _, err := prefetchManifestHOCR(ctx, canvases, 1<<20); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("aggregate deadline error = %v, want deadline exceeded", err)
	}
	if got := peak.Load(); got < 1 || got > maxConcurrentManifestHOCRFetches {
		t.Fatalf("peak hOCR requests = %d, want 1..%d", got, maxConcurrentManifestHOCRFetches)
	}
}

func TestParallelManifestFetchesShareBoundedWorkspaceImportSlot(t *testing.T) {
	database := openTestDB(t)
	firstFetch := make(chan struct{})
	releaseFirst := make(chan struct{})
	var fetches atomic.Int32

	manifestSource := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		requestNumber := fetches.Add(1)
		if requestNumber == 1 {
			close(firstFetch)
			<-releaseFirst
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"@context":"http://iiif.io/api/presentation/2/context.json","@id":"https://example.test/manifest","@type":"sc:Manifest","sequences":[]}`))
	}))
	t.Cleanup(manifestSource.Close)

	handler := NewHandler(
		store.NewOCRRunStore(database),
		store.NewItemStore(database),
		store.NewContextStore(database),
		store.NewAnnotationStore(database),
		store.NewTranscriptionJobStore(database),
		nil,
		nil,
		nil,
	)
	limiter, err := worklimit.NewHierarchical(1, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	handler.processingLimiter = limiter

	results := make(chan error, 2)
	firstKey := "manifest-import-slot-a-" + uuid.NewString()
	secondKey := "manifest-import-slot-b-" + uuid.NewString()
	for _, key := range []string{firstKey, secondKey} {
		digest := sha256.Sum256([]byte(key))
		registerManifestImportFixtureCleanup(t, database, manifestSource.URL, fmt.Sprintf("%x", digest[:]))
	}
	request := func(key string) {
		_, requestErr := handler.ImportManifest(context.Background(), connect.NewRequest(&scribev1.ImportManifestRequest{
			Name:           "Import slot",
			ManifestUrl:    manifestSource.URL,
			IdempotencyKey: key,
		}))
		results <- requestErr
	}
	go request(firstKey)
	select {
	case <-firstFetch:
	case <-time.After(2 * time.Second):
		t.Fatal("first manifest fetch did not start")
	}
	go request(secondKey)
	select {
	case <-time.After(100 * time.Millisecond):
		if got := fetches.Load(); got != 1 {
			t.Fatalf("parallel manifest fetches = %d while slot held, want 1", got)
		}
	case requestErr := <-results:
		t.Fatalf("second manifest request completed while first slot held: %v", requestErr)
	}
	close(releaseFirst)
	for range 2 {
		if requestErr := <-results; connect.CodeOf(requestErr) != connect.CodeInvalidArgument {
			t.Fatalf("manifest request error = %v, want invalid argument for empty manifest", requestErr)
		}
	}
	if got := fetches.Load(); got != 2 {
		t.Fatalf("manifest fetches after release = %d, want 2", got)
	}
}

func TestImportManifestRejectsForeignContextBeforeReservationOrFetch(t *testing.T) {
	database := openTestDB(t)
	workspaceID, userID := createServerTestWorkspace(t, database)
	foreignWorkspaceID, foreignUserID := createServerTestWorkspace(t, database)
	contexts := store.NewContextStore(database)
	foreignContext, err := contexts.Create(context.Background(), store.Context{
		UserID: &foreignUserID, WorkspaceID: &foreignWorkspaceID, Name: "foreign-manifest-context",
		SegmentationModel: "layout", TranscriptionProvider: "tesseract", TranscriptionModel: "eng",
	})
	if err != nil {
		t.Fatalf("create foreign context: %v", err)
	}
	var requests atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	t.Cleanup(source.Close)
	jobs := store.NewTranscriptionJobStore(database)
	handler := NewHandler(
		store.NewOCRRunStore(database), store.NewItemStore(database), contexts,
		store.NewAnnotationStore(database), jobs, &auth.Manager{}, nil, nil,
	)
	key := "foreign-context-before-manifest-fetch-" + uuid.NewString()
	requestCtx := auth.WithPrincipal(context.Background(), auth.Principal{
		UserID: userID, Authenticated: true, WorkspaceID: workspaceID, WorkspaceRole: "editor",
	})
	_, err = handler.ImportManifest(requestCtx, connect.NewRequest(&scribev1.ImportManifestRequest{
		Name: "Foreign context", ManifestUrl: source.URL, ContextId: foreignContext.ID, IdempotencyKey: key,
	}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("foreign context error = %v, want not_found", err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("foreign context triggered %d external requests", got)
	}
	digest := sha256.Sum256([]byte(key))
	var reservations int
	if err := database.QueryRowContext(context.Background(), `
SELECT COUNT(*) FROM external_requests
WHERE workspace_id = ? AND source = 'item-create' AND idempotency_key = ?`, workspaceID, fmt.Sprintf("%x", digest[:])).Scan(&reservations); err != nil {
		t.Fatalf("count foreign-context reservations: %v", err)
	}
	if reservations != 0 {
		t.Fatalf("foreign context created %d idempotency reservations", reservations)
	}
}

func TestImportManifestCompletedReplayDoesNotRefetchSource(t *testing.T) {
	database := openTestDB(t)
	var requests atomic.Int32
	var source *httptest.Server
	source = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		switch request.URL.Path {
		case "/manifest":
			response.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(response).Encode(testPresentation2Manifest(source.URL, 1))
		case "/hocr.xml":
			response.Header().Set("Content-Type", "text/vnd.hocr+html; charset=utf-8")
			_, _ = response.Write([]byte(minimalHOCR))
		default:
			http.NotFound(response, request)
		}
	}))
	jobs := store.NewTranscriptionJobStore(database)
	handler := NewHandler(
		store.NewOCRRunStore(database), store.NewItemStore(database), store.NewContextStore(database),
		store.NewAnnotationStore(database), jobs, nil, nil, nil,
	)
	key := "manifest-replay-with-unavailable-source-" + uuid.NewString()
	manifestURL := source.URL + "/manifest"
	digest := sha256.Sum256([]byte(key))
	storedKey := fmt.Sprintf("%x", digest[:])
	registerManifestImportFixtureCleanup(t, database, manifestURL, storedKey)
	request := &scribev1.ImportManifestRequest{
		Name: "Replay without source", ManifestUrl: manifestURL, IdempotencyKey: key,
	}
	created, err := handler.ImportManifest(context.Background(), connect.NewRequest(request))
	if err != nil {
		source.Close()
		t.Fatalf("initial manifest import: %v", err)
	}
	requestsAfterCommit := requests.Load()
	source.Close()

	replayCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	replayed, err := handler.ImportManifest(replayCtx, connect.NewRequest(request))
	if err != nil {
		t.Fatalf("completed replay with unavailable source: %v", err)
	}
	if replayed.Msg.GetItem().GetId() != created.Msg.GetItem().GetId() {
		t.Fatalf("replayed item = %q, want %q", replayed.Msg.GetItem().GetId(), created.Msg.GetItem().GetId())
	}
	if got := requests.Load(); got != requestsAfterCommit {
		t.Fatalf("completed replay issued %d additional source requests", got-requestsAfterCommit)
	}
}

func testPresentation2Manifest(baseURL string, canvasCount int) map[string]any {
	canvases := make([]any, 0, canvasCount)
	for index := 0; index < canvasCount; index++ {
		canvasID := fmt.Sprintf("%s/canvas/%d", baseURL, index+1)
		canvases = append(canvases, map[string]any{
			"@id":    canvasID,
			"@type":  "sc:Canvas",
			"label":  fmt.Sprintf("Page %d", index+1),
			"height": 1000,
			"width":  800,
			"images": []any{map[string]any{
				"@id":        canvasID + "/painting",
				"@type":      "oa:Annotation",
				"motivation": "sc:painting",
				"resource": map[string]any{
					"@id":    fmt.Sprintf("%s/image/%d.jpg", baseURL, index+1),
					"@type":  "dctypes:Image",
					"format": "image/jpeg",
					"height": 1000,
					"width":  800,
				},
				"on": canvasID,
			}},
			"seeAlso": map[string]any{
				"@id":    baseURL + "/hocr.xml",
				"format": "text/vnd.hocr+html",
			},
		})
	}
	return map[string]any{
		"@context": "http://iiif.io/api/presentation/2/context.json",
		"@id":      baseURL + "/manifest",
		"@type":    "sc:Manifest",
		"label":    "Oversized manifest",
		"sequences": []any{map[string]any{
			"@id":      baseURL + "/sequence/normal",
			"@type":    "sc:Sequence",
			"canvases": canvases,
		}},
	}
}

func registerManifestImportFixtureCleanup(t *testing.T, database *sql.DB, manifestURL, idempotencyKey string) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		rows, err := database.QueryContext(ctx, `SELECT id, workspace_id FROM items WHERE source_url = ?`, manifestURL)
		if err != nil {
			t.Errorf("list manifest fixture items: %v", err)
		} else {
			type itemIdentity struct {
				id          string
				workspaceID uint64
			}
			var identities []itemIdentity
			for rows.Next() {
				var identity itemIdentity
				if err := rows.Scan(&identity.id, &identity.workspaceID); err != nil {
					t.Errorf("scan manifest fixture item: %v", err)
					break
				}
				identities = append(identities, identity)
			}
			if err := rows.Err(); err != nil {
				t.Errorf("iterate manifest fixture items: %v", err)
			}
			if err := rows.Close(); err != nil {
				t.Errorf("close manifest fixture item rows: %v", err)
			}
			items := store.NewItemStore(database)
			for _, identity := range identities {
				imageRows, err := database.QueryContext(ctx, `SELECT id FROM item_images WHERE workspace_id = ? AND item_id = ?`, identity.workspaceID, identity.id)
				if err != nil {
					t.Errorf("list manifest fixture item images for %q: %v", identity.id, err)
					continue
				}
				var imageIDs []uint64
				for imageRows.Next() {
					var imageID uint64
					if err := imageRows.Scan(&imageID); err != nil {
						t.Errorf("scan manifest fixture item image for %q: %v", identity.id, err)
						break
					}
					imageIDs = append(imageIDs, imageID)
				}
				if err := imageRows.Err(); err != nil {
					t.Errorf("iterate manifest fixture item images for %q: %v", identity.id, err)
				}
				if err := imageRows.Close(); err != nil {
					t.Errorf("close manifest fixture item image rows for %q: %v", identity.id, err)
				}
				if err := items.DeleteForWorkspace(ctx, identity.id, identity.workspaceID); err != nil {
					t.Errorf("delete manifest fixture item %q: %v", identity.id, err)
					continue
				}
				if _, err := database.ExecContext(ctx, `
DELETE FROM resource_cleanup_outbox
WHERE workspace_id = ? AND kind = 'triplet_presentation_item' AND resource_key = ?`, identity.workspaceID, identity.id); err != nil {
					t.Errorf("delete manifest fixture item cleanup %q: %v", identity.id, err)
				}
				for _, imageID := range imageIDs {
					if _, err := database.ExecContext(ctx, `
DELETE FROM resource_cleanup_outbox
WHERE workspace_id = ? AND kind = 'triplet_presentation_image' AND resource_key = ?`, identity.workspaceID, fmt.Sprint(imageID)); err != nil {
						t.Errorf("delete manifest fixture image cleanup %d: %v", imageID, err)
					}
				}
			}
		}
		if _, err := database.ExecContext(ctx, `
DELETE FROM external_requests
WHERE workspace_id = ? AND source = 'item-create' AND idempotency_key = ?`, store.AnonymousWorkspaceID, idempotencyKey); err != nil {
			t.Errorf("delete manifest fixture request: %v", err)
		}
	})
}

func assertNoManifestTenantStateWithFailedReservation(t *testing.T, database *sql.DB, manifestURL, idempotencyKey string) {
	t.Helper()
	ctx := context.Background()
	queries := []struct {
		name  string
		query string
		args  []any
	}{
		{name: "items", query: `SELECT COUNT(*) FROM items WHERE source_url = ?`, args: []any{manifestURL}},
		{name: "images", query: `SELECT COUNT(*) FROM item_images ii JOIN items i ON i.id = ii.item_id WHERE i.source_url = ?`, args: []any{manifestURL}},
		{name: "OCR runs", query: `SELECT COUNT(*) FROM ocr_runs o JOIN item_images ii ON ii.id = o.item_image_id JOIN items i ON i.id = ii.item_id WHERE i.source_url = ?`, args: []any{manifestURL}},
		{name: "annotation pages", query: `SELECT COUNT(*) FROM annotation_pages ap JOIN item_images ii ON ii.id = ap.item_image_id JOIN items i ON i.id = ii.item_id WHERE i.source_url = ?`, args: []any{manifestURL}},
		{name: "jobs", query: `SELECT COUNT(*) FROM transcription_jobs tj JOIN item_images ii ON ii.id = tj.item_image_id JOIN items i ON i.id = ii.item_id WHERE i.source_url = ?`, args: []any{manifestURL}},
	}
	for _, check := range queries {
		var count int
		if err := database.QueryRowContext(ctx, check.query, check.args...).Scan(&count); err != nil {
			t.Fatalf("count %s after manifest rejection: %v", check.name, err)
		}
		if count != 0 {
			t.Fatalf("manifest rejection left %d %s rows", count, check.name)
		}
	}
	var status string
	if err := database.QueryRowContext(ctx, `
SELECT status FROM external_requests
WHERE workspace_id = ? AND source = 'item-create' AND idempotency_key = ?`, store.AnonymousWorkspaceID, idempotencyKey).Scan(&status); err != nil {
		t.Fatalf("load failed manifest reservation: %v", err)
	}
	if status != string(store.ExternalRequestStatusFailed) {
		t.Fatalf("manifest reservation status = %q, want failed", status)
	}
}
