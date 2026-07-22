package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/lehigh-university-libraries/scribe/internal/config"
	dbstore "github.com/lehigh-university-libraries/scribe/internal/db"
	ocrhandlers "github.com/lehigh-university-libraries/scribe/internal/handlers"
	"github.com/lehigh-university-libraries/scribe/internal/hocr"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/models"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	"github.com/lehigh-university-libraries/scribe/internal/worklimit"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

const segmentCapTestHOCR = `<!DOCTYPE html><html><body><div class="ocr_page" title="bbox 0 0 100 100">
<span class="ocr_line" id="line_1" title="bbox 0 0 100 20"><span class="ocrx_word" title="bbox 0 0 30 20">one</span></span>
<span class="ocr_line" id="line_2" title="bbox 0 30 100 50"><span class="ocrx_word" title="bbox 0 30 30 50">two</span></span>
</div></body></html>`

type segmentCapOCR struct {
	segmentationCalls  int
	transcriptionCalls int
}

func TestTranscriptionLeaseHeartbeatStopCancelsAndJoinsRenewal(t *testing.T) {
	t.Parallel()

	ticks := make(chan time.Time)
	renewStarted := make(chan struct{})
	renewReturned := make(chan struct{})
	leaseCtx, stop := newTranscriptionJobLeaseHeartbeat(context.Background(), ticks, func(ctx context.Context) error {
		close(renewStarted)
		<-ctx.Done()
		close(renewReturned)
		return ctx.Err()
	})

	ticks <- time.Now()
	<-renewStarted
	stop()
	select {
	case <-renewReturned:
	default:
		t.Fatal("heartbeat stop returned before the active renewal")
	}
	if !errors.Is(context.Cause(leaseCtx), context.Canceled) {
		t.Fatalf("heartbeat stop cause = %v; want context canceled", context.Cause(leaseCtx))
	}
	// Stop is idempotent and every caller observes the joined goroutine.
	stop()
}

func TestTranscriptionLeaseHeartbeatReportsFenceFailure(t *testing.T) {
	t.Parallel()

	ticks := make(chan time.Time, 1)
	fenceErr := errors.New("attempt fence rejected")
	leaseCtx, stop := newTranscriptionJobLeaseHeartbeat(context.Background(), ticks, func(context.Context) error {
		return fenceErr
	})
	defer stop()
	ticks <- time.Now()

	select {
	case <-leaseCtx.Done():
		if cause := context.Cause(leaseCtx); !errors.Is(cause, errTranscriptionJobLeaseLost) || !strings.Contains(cause.Error(), fenceErr.Error()) {
			t.Fatalf("heartbeat failure cause = %v; want fenced lease loss", cause)
		}
	case <-time.After(time.Second):
		t.Fatal("heartbeat did not cancel the attempt after renewal failure")
	}
}

func (*segmentCapOCR) SetProviderCallAuditLogger(hocr.ProviderCallAuditLogger) {}

func (*segmentCapOCR) ProcessImageURLWithContext(context.Context, string, hocr.ProcessingContext) (*ocrhandlers.ProcessResult, error) {
	return nil, fmt.Errorf("unexpected durable image processing call")
}

func (f *segmentCapOCR) ProcessImageURLTransientWithContext(context.Context, string, hocr.ProcessingContext) (*ocrhandlers.ProcessResult, error) {
	f.segmentationCalls++
	return &ocrhandlers.ProcessResult{HOCR: segmentCapTestHOCR}, nil
}

func (*segmentCapOCR) ProcessImageUploadWithContext(context.Context, string, []byte, hocr.ProcessingContext) (*ocrhandlers.ProcessResult, error) {
	return nil, fmt.Errorf("unexpected upload processing call")
}

func (*segmentCapOCR) StoreUploadedImage(context.Context, string, []byte) (string, error) {
	return "", fmt.Errorf("unexpected upload storage call")
}

func (f *segmentCapOCR) TranscribeImageFileWithContext(context.Context, string, string, string) (string, error) {
	f.transcriptionCalls++
	return "unexpected", nil
}

func TestPoisonedTranscriptionEventRedactsMessageBody(t *testing.T) {
	const secretBody = "untrusted-payload-secret-token"
	data := poisonedTranscriptionEventData(" message-1 ", []byte(secretBody))
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal poisoned event data: %v", err)
	}
	if strings.Contains(string(encoded), secretBody) {
		t.Fatalf("poisoned event leaked raw body: %s", encoded)
	}
	if data["messageId"] != "message-1" || data["bodyBytes"] != len(secretBody) || data["error"] != "invalid transcription queue message" {
		t.Fatalf("poisoned event data = %+v", data)
	}
	digest, ok := data["bodySha256"].(string)
	if !ok || len(digest) != 64 {
		t.Fatalf("body digest = %#v", data["bodySha256"])
	}
}

func TestTranscriptionJobReadErrorsDistinguishMissingFromInfrastructure(t *testing.T) {
	t.Parallel()
	if err := transcriptionJobReadConnectError(sql.ErrNoRows); connect.CodeOf(err) != connect.CodeNotFound || strings.Contains(err.Error(), "sql") {
		t.Fatalf("missing job error = %v", err)
	}
	infrastructure := errors.New("driver-secret-topology")
	if err := transcriptionJobReadConnectError(infrastructure); connect.CodeOf(err) != connect.CodeInternal || !errors.Is(err, infrastructure) {
		t.Fatalf("infrastructure job error = %v", err)
	}
	sanitized := sanitizeConnectError(transcriptionJobReadConnectError(infrastructure))
	if connect.CodeOf(sanitized) != connect.CodeInternal || strings.Contains(sanitized.Error(), "driver-secret-topology") {
		t.Fatalf("sanitized infrastructure error = %v", sanitized)
	}
}

func TestTranscriptionStreamRejectsEleventhWorkspaceConnectionBeforePolling(t *testing.T) {
	limiter := newConnectionLimiter()
	releases := make([]func(), 0, maxSSEPerWorkspace)
	for range maxSSEPerWorkspace {
		release, ok := limiter.Acquire(store.AnonymousWorkspaceID)
		if !ok {
			t.Fatal("failed to reserve stream fixture connection")
		}
		releases = append(releases, release)
	}
	defer func() {
		for _, release := range releases {
			release()
		}
	}()

	handler := &Handler{sseLimiter: limiter}
	err := handler.StreamTranscriptionJob(
		context.Background(),
		connect.NewRequest(&scribev1.StreamTranscriptionJobRequest{JobId: 99}),
		nil,
	)
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("eleventh stream error = %v, want resource_exhausted", err)
	}
}

func TestBackgroundTranscriptionUsesWorkspaceAndProviderLimiter(t *testing.T) {
	database := openTestDB(t)
	ctx := context.Background()
	owner := createTestUser(t, database, "worker-limit")
	workspaceID := createTestWorkspace(t, database, owner, "worker-limit")
	items := store.NewItemStore(database)
	item, err := items.Create(ctx, dbstore.CreateItemParams{
		ID: "item_worker_limit", UserID: owner, WorkspaceID: workspaceID,
		Name: "worker limit", SourceType: "manifest", SourceURL: "https://source.example/manifest",
	})
	if err != nil {
		t.Fatalf("create item: %v", err)
	}
	image, err := items.AddImage(ctx, dbstore.CreateItemImageParams{
		ItemID: item.ID, ImageURL: "https://images.example/worker-limit.jpg", CanvasURI: "https://source.example/canvas/worker-limit",
		Width: 100, Height: 100,
	})
	if err != nil {
		t.Fatalf("create image: %v", err)
	}
	contexts := store.NewContextStore(database)
	processingContext, err := contexts.Create(ctx, store.Context{
		UserID: &owner, WorkspaceID: &workspaceID, Name: "worker-limit-context",
		SegmentationModel: "layout", TranscriptionProvider: "tesseract", TranscriptionModel: "eng",
	})
	if err != nil {
		t.Fatalf("create context: %v", err)
	}
	annotationStore := store.NewAnnotationStore(database)
	pageJSON, err := iiif.NewAnnotationPage(iiif.PageIdentity{
		PublicBaseURL: "https://scribe.example",
		ItemImageID:   image.ID,
		CanvasURI:     image.CanvasURI,
	}, []any{transcriptionAnnotation("worker-limit-line", "line", "text", image.CanvasURI, models.BBox{X1: 0, Y1: 0, X2: 10, Y2: 10})})
	if err != nil {
		t.Fatalf("build worker limiter page: %v", err)
	}
	pageID, err := iiif.CanonicalPageID("https://scribe.example", image.ID)
	if err != nil {
		t.Fatalf("build worker limiter page id: %v", err)
	}
	if _, err := annotationStore.SavePage(ctx, store.AnnotationPage{
		WorkspaceID: workspaceID,
		ItemImageID: image.ID,
		PageID:      pageID,
		CanvasURI:   image.CanvasURI,
		Payload:     string(pageJSON),
	}, 0); err != nil {
		t.Fatalf("save worker limiter page: %v", err)
	}
	snapshot, err := json.Marshal(processingContext)
	if err != nil {
		t.Fatalf("marshal context: %v", err)
	}
	limiter, err := worklimit.NewHierarchical(1, 1, 1)
	if err != nil {
		t.Fatalf("create limiter: %v", err)
	}
	release, err := limiter.Acquire(ctx, workspaceID, processingLimitProvider(processingContext))
	if err != nil {
		t.Fatalf("occupy provider slot: %v", err)
	}
	defer release()
	handler := &Handler{items: items, contexts: contexts, annotations: annotationStore, processingLimiter: limiter}
	deadlineCtx, cancel := context.WithTimeout(ctx, 25*time.Millisecond)
	defer cancel()
	err = handler.processTranscriptionJob(deadlineCtx, &store.TranscriptionJob{
		ID: 99, ItemImageID: image.ID, ContextID: &processingContext.ID, ContextSnapshot: snapshot,
		InputRevision: 1, AttemptCount: 1, LeaseToken: "worker-limit-fence",
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("processTranscriptionJob error = %v, want limiter deadline", err)
	}
}

func TestTranscriptionJobSegmentLimitStopsBeforeCredentialsProgressAndProvider(t *testing.T) {
	previous := config.Get()
	configured := previous
	configured.Config.Transcription.MaxSegmentsPerJob = 1
	config.Init(configured)
	t.Cleanup(func() { config.Init(previous) })

	database := openTestDB(t)
	ctx := context.Background()
	owner := createTestUser(t, database, uniqueName("segment-cap-owner"))
	workspaceID := createTestWorkspace(t, database, owner, uniqueName("segment-cap-workspace"))
	items := store.NewItemStore(database)
	item, err := items.Create(ctx, dbstore.CreateItemParams{
		ID: uniqueName("segment-cap-item"), UserID: owner, WorkspaceID: workspaceID,
		Name: "segment cap", SourceType: "manifest", SourceURL: "https://source.example/manifest",
	})
	if err != nil {
		t.Fatalf("create item: %v", err)
	}
	t.Cleanup(func() { _ = items.DeleteForWorkspace(context.Background(), item.ID, workspaceID) })
	image, err := items.AddImage(ctx, dbstore.CreateItemImageParams{
		ItemID: item.ID, ImageURL: "https://images.example/segment-cap.jpg", CanvasURI: "https://source.example/canvas/segment-cap",
	})
	if err != nil {
		t.Fatalf("create image: %v", err)
	}

	annotationStore := store.NewAnnotationStore(database)
	pageJSON, err := iiif.NewAnnotationPage(iiif.PageIdentity{
		PublicBaseURL: "https://scribe.example", ItemImageID: image.ID, CanvasURI: image.CanvasURI,
	}, nil)
	if err != nil {
		t.Fatalf("build empty page: %v", err)
	}
	pageID, err := iiif.CanonicalPageID("https://scribe.example", image.ID)
	if err != nil {
		t.Fatalf("build page id: %v", err)
	}
	if _, err := annotationStore.SavePage(ctx, store.AnnotationPage{
		WorkspaceID: workspaceID, ItemImageID: image.ID, PageID: pageID,
		CanvasURI: image.CanvasURI, Payload: string(pageJSON),
	}, 0); err != nil {
		t.Fatalf("save empty page: %v", err)
	}

	processingContext := store.Context{
		ID: 77, WorkspaceID: &workspaceID, UserID: &owner, Name: "segment cap",
		SegmentationModel: "scribe", TranscriptionProvider: "openai", TranscriptionModel: "test-model",
	}
	snapshot, err := json.Marshal(processingContext)
	if err != nil {
		t.Fatalf("marshal context: %v", err)
	}
	limiter, err := worklimit.NewHierarchical(1, 1, 1)
	if err != nil {
		t.Fatalf("create limiter: %v", err)
	}
	providerSecrets := &recordingProviderSecretResolver{}
	vault := &countingProviderVault{}
	ocr := &segmentCapOCR{}
	handler := &Handler{
		items: items, annotations: annotationStore, ocr: ocr,
		processingLimiter: limiter, providerSecrets: providerSecrets, vault: vault,
	}
	err = handler.processTranscriptionJob(ctx, &store.TranscriptionJob{
		ID: 99, ItemImageID: image.ID, ContextID: &processingContext.ID, ContextSnapshot: snapshot,
		InputRevision: 1, AttemptCount: 1, LeaseToken: "segment-cap-fence",
	})
	var permanent permanentTranscriptionError
	if !errors.As(err, &permanent) || !strings.Contains(err.Error(), "configured maximum is 1") {
		t.Fatalf("processTranscriptionJob error = %v, want permanent segment-limit failure", err)
	}
	if ocr.segmentationCalls != 1 || ocr.transcriptionCalls != 0 {
		t.Fatalf("OCR calls = segmentation:%d transcription:%d, want 1/0", ocr.segmentationCalls, ocr.transcriptionCalls)
	}
	if providerSecrets.calls != 0 || vault.reads != 0 {
		t.Fatalf("credential work = resolver:%d Vault:%d, want 0/0", providerSecrets.calls, vault.reads)
	}
	if safe := store.SafeTranscriptionFailureMessage(err); safe != "transcription job exceeds configured segment limit" {
		t.Fatalf("safe failure = %q", safe)
	}
}

func TestStoreJobToProtoExposesAttemptAuditWithoutLeaseToken(t *testing.T) {
	const secretLeaseToken = "worker-secret-lease-token"
	startedAt := time.Date(2026, 7, 20, 11, 12, 13, 123456000, time.UTC)
	finishedAt := startedAt.Add(3 * time.Second)
	resultRevision := uint64(8)
	converted := storeJobToProto(store.TranscriptionJob{
		ID:            42,
		ItemImageID:   9,
		InputRevision: 7,
		Status:        store.TranscriptionJobStatusSuperseded,
		AttemptCount:  1,
		MaxAttempts:   3,
		LeaseToken:    secretLeaseToken,
		Attempts: []store.TranscriptionJobAttempt{{
			JobID:            42,
			AttemptNumber:    1,
			ContextSnapshot:  json.RawMessage(`{"id":3,"transcription_provider":"registered"}`),
			InputRevision:    7,
			LeaseOwner:       "scribe-worker@worker-1",
			Outcome:          store.TranscriptionAttemptCompleted,
			ResultRevision:   &resultRevision,
			StartedAt:        startedAt,
			FinishedAt:       &finishedAt,
			SafeErrorMessage: "",
		}},
	})
	if converted.GetStatus() != scribev1.TranscriptionJobStatus_TRANSCRIPTION_JOB_STATUS_SUPERSEDED || len(converted.GetAttempts()) != 1 {
		t.Fatalf("converted job audit = %+v", converted)
	}
	attempt := converted.GetAttempts()[0]
	if attempt.GetOutcome() != scribev1.TranscriptionJobAttemptOutcome_TRANSCRIPTION_JOB_ATTEMPT_OUTCOME_COMPLETED ||
		attempt.GetAttemptNumber() != 1 || attempt.GetInputRevision() != 7 || attempt.GetResultRevision() != 8 ||
		attempt.GetStartedAt() != startedAt.Format(time.RFC3339Nano) || attempt.GetFinishedAt() != finishedAt.Format(time.RFC3339Nano) {
		t.Fatalf("converted attempt audit = %+v", attempt)
	}
	encoded, err := protojson.Marshal(converted)
	if err != nil {
		t.Fatalf("marshal converted job: %v", err)
	}
	if strings.Contains(string(encoded), secretLeaseToken) || strings.Contains(string(encoded), "leaseToken") {
		t.Fatalf("public job response exposed lease token: %s", encoded)
	}
}

func TestStoreJobSummaryToProtoOmitsPointReadPayloads(t *testing.T) {
	converted := storeJobSummaryToProto(store.TranscriptionJobSummary{
		ID: 42, ItemImageID: 9, Status: store.TranscriptionJobStatusRunning,
	})
	encoded, err := protojson.Marshal(converted)
	if err != nil {
		t.Fatalf("marshal transcription job summary: %v", err)
	}
	for _, forbidden := range []string{"attempts", "contextSnapshot", "currentAnnotationJson", "lastResultAnnotationJson"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("list summary leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestReconcileTranscribedWordsScalesAcrossMaximumMixedPage(t *testing.T) {
	const lineCount = 5_000
	const canvasURI = "https://source.example/canvas/spatial-reconciliation"
	items := make([]any, 0, lineCount*2)
	for index := 0; index < lineCount; index++ {
		y := index * 3
		items = append(items,
			transcriptionAnnotation(fmt.Sprintf("line-%d", index), "line", fmt.Sprintf("updated-%d", index), canvasURI, models.BBox{X1: 0, Y1: y, X2: 100, Y2: y + 2}),
			transcriptionAnnotation(fmt.Sprintf("word-%d", index), "word", "stale", canvasURI, models.BBox{X1: 1, Y1: y, X2: 20, Y2: y + 2}),
		)
	}
	started := time.Now()
	reconciled := reconcileTranscribedWords(items)
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("10,000-item spatial reconciliation took %s", elapsed)
	}
	if len(reconciled) != len(items) {
		t.Fatalf("reconciled item count = %d, want %d", len(reconciled), len(items))
	}
	for _, index := range []int{0, lineCount / 2, lineCount - 1} {
		word := reconciled[index*2+1].(map[string]any)
		if got, want := extractAnnotationText(word), fmt.Sprintf("updated-%d", index); got != want {
			t.Fatalf("word %d text = %q, want %q", index, got, want)
		}
	}

	// With overlapping boxes, the first eligible line in canonical item order
	// owns the word deterministically.
	overlapping := []any{
		transcriptionAnnotation("line-first", "line", "first", canvasURI, models.BBox{X1: 0, Y1: 0, X2: 100, Y2: 20}),
		transcriptionAnnotation("line-second", "line", "second", canvasURI, models.BBox{X1: 0, Y1: 0, X2: 100, Y2: 20}),
		transcriptionAnnotation("word-overlap", "word", "stale", canvasURI, models.BBox{X1: 10, Y1: 5, X2: 20, Y2: 15}),
	}
	if got := extractAnnotationText(reconcileTranscribedWords(overlapping)[2].(map[string]any)); got != "first" {
		t.Fatalf("overlapping word owner text = %q, want first", got)
	}
}

func createTestUser(t *testing.T, db *sql.DB, name string) uint64 {
	t.Helper()

	result, err := db.Exec(
		`INSERT INTO users (name, email, google_subject) VALUES (?, ?, ?)`,
		name,
		fmt.Sprintf("%s@example.test", name),
		fmt.Sprintf("sub-%s-%d", name, time.Now().UnixNano()),
	)
	if err != nil {
		t.Fatalf("insert user %q: %v", name, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id for user %q: %v", name, err)
	}
	userID := uint64(id)
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM auth_sessions WHERE user_id = ?`, userID)
		_, _ = db.Exec(`DELETE FROM users WHERE id = ?`, userID)
	})
	return userID
}

func TestReconcileTranscribedWordsUpdatesMatchingTokens(t *testing.T) {
	items := []any{
		transcriptionAnnotation("line-1", "line", "new words", "https://example.test/canvas", models.BBox{X1: 0, Y1: 0, X2: 100, Y2: 20}),
		transcriptionAnnotation("word-1", "word", "old", "https://example.test/canvas", models.BBox{X1: 0, Y1: 0, X2: 40, Y2: 20}),
		transcriptionAnnotation("word-2", "word", "text", "https://example.test/canvas", models.BBox{X1: 50, Y1: 0, X2: 100, Y2: 20}),
	}

	got := reconcileTranscribedWords(items)
	if len(got) != 3 {
		t.Fatalf("len(reconciled) = %d, want 3", len(got))
	}
	if text := extractAnnotationText(got[1].(map[string]any)); text != "new" {
		t.Fatalf("first word text = %q, want new", text)
	}
	if text := extractAnnotationText(got[2].(map[string]any)); text != "words" {
		t.Fatalf("second word text = %q, want words", text)
	}
}

func TestReconcileTranscribedWordsDropsStaleGranularityOnTokenMismatch(t *testing.T) {
	items := []any{
		transcriptionAnnotation("line-1", "line", "one token", "https://example.test/canvas", models.BBox{X1: 0, Y1: 0, X2: 100, Y2: 20}),
		transcriptionAnnotation("word-1", "word", "stale", "https://example.test/canvas", models.BBox{X1: 0, Y1: 0, X2: 100, Y2: 20}),
	}

	got := reconcileTranscribedWords(items)
	if len(got) != 1 {
		t.Fatalf("len(reconciled) = %d, want only the canonical line", len(got))
	}
}

func TestReconcileEditedLineWordsKeepsBothCanonicalViewsConsistent(t *testing.T) {
	canvas := "https://example.test/canvas/edit-reconcile"
	identity := iiif.PageIdentity{PublicBaseURL: "https://scribe.example", ItemImageID: 1, CanvasURI: canvas}
	bbox := models.BBox{X1: 0, Y1: 0, X2: 100, Y2: 20}
	base := []any{
		transcriptionAnnotation("line-1", "line", "Course Catalog", canvas, bbox),
		transcriptionAnnotation("word-1", "word", "Course", canvas, models.BBox{X1: 0, Y1: 0, X2: 45, Y2: 20}),
		transcriptionAnnotation("word-2", "word", "Catalog", canvas, models.BBox{X1: 50, Y1: 0, X2: 100, Y2: 20}),
	}
	lineEdit := []any{
		transcriptionAnnotation("line-1", "line", "Revised Course Catalog", canvas, bbox),
		// This intentionally mirrors the current browser redistribution: the
		// inserted token is put in the first existing word and the final word
		// receives the remaining tokens. Reconciliation keys its LCS from the
		// committed word text, not these transient bodies.
		transcriptionAnnotation("word-1", "word", "Revised", canvas, models.BBox{X1: 0, Y1: 0, X2: 45, Y2: 20}),
		transcriptionAnnotation("word-2", "word", "Course Catalog", canvas, models.BBox{X1: 50, Y1: 0, X2: 100, Y2: 20}),
	}
	lineEdit[1].(map[string]any)["scribe:confidence"] = 0.99
	reconciled, err := reconcileEditedLineWords(base, lineEdit, identity, 1)
	if err != nil {
		t.Fatalf("reconcile inserted line token: %v", err)
	}
	words := make(map[string]map[string]any)
	orderedText := make([]string, 0, 3)
	for _, value := range reconciled {
		annotation := value.(map[string]any)
		if annStringValue(annotation, "textGranularity") != "word" {
			continue
		}
		words[annStringValue(annotation, "id")] = annotation
		orderedText = append(orderedText, extractAnnotationText(annotation))
	}
	if got := strings.Join(orderedText, " "); got != "Revised Course Catalog" {
		t.Fatalf("reconciled word text = %q", got)
	}
	if extractAnnotationText(words["word-1"]) != "Course" || extractAnnotationText(words["word-2"]) != "Catalog" {
		t.Fatalf("LCS-matched word IDs were not retained: %#v", words)
	}
	if fmt.Sprint(words["word-1"]["scribe:confidence"]) != "0.99" {
		t.Fatal("line reconciliation discarded unknown word property")
	}

	wordEdit := []any{
		transcriptionAnnotation("line-1", "line", "Course Catalog", canvas, bbox),
		transcriptionAnnotation("word-1", "word", "Course", canvas, models.BBox{X1: 0, Y1: 0, X2: 45, Y2: 20}),
		transcriptionAnnotation("word-2", "word", "Syllabus", canvas, models.BBox{X1: 50, Y1: 0, X2: 100, Y2: 20}),
	}
	reconciled, err = reconcileEditedLineWords(base, wordEdit, identity, 1)
	if err != nil {
		t.Fatalf("reconcile word edit: %v", err)
	}
	if got := extractAnnotationText(reconciled[0].(map[string]any)); got != "Course Syllabus" {
		t.Fatalf("word edit line = %q, want Course Syllabus", got)
	}
}

func createTestWorkspace(t *testing.T, db *sql.DB, userID uint64, name string) uint64 {
	t.Helper()

	slug := fmt.Sprintf("%s-%d", store.Slugify(name), time.Now().UnixNano())
	result, err := db.Exec(
		`INSERT INTO workspaces (owner_user_id, name, slug, is_personal, created_by_user_id) VALUES (?, ?, ?, FALSE, ?)`,
		userID,
		name,
		slug,
		userID,
	)
	if err != nil {
		t.Fatalf("insert workspace %q: %v", name, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id for workspace %q: %v", name, err)
	}
	workspaceID := uint64(id)
	if _, err := db.Exec(`INSERT INTO storage_quota_usage (workspace_id) VALUES (?)`, workspaceID); err != nil {
		t.Fatalf("insert workspace quota row for %q: %v", name, err)
	}
	if _, err := db.Exec(`INSERT INTO workspace_members (workspace_id, user_id, role) VALUES (?, ?, 'admin')`, workspaceID, userID); err != nil {
		t.Fatalf("insert workspace member for %q: %v", name, err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM workspace_members WHERE workspace_id = ?`, workspaceID)
		_, _ = db.Exec(`DELETE FROM workspaces WHERE id = ?`, workspaceID)
		_, _ = db.Exec(`DELETE FROM storage_quota_usage WHERE workspace_id = ?`, workspaceID)
	})
	return workspaceID
}

func TestProviderCallAuditRetentionDeletesOldRows(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	audits := store.NewProviderCallAuditStore(db)
	workspaceID := createTestWorkspace(t, db, store.AnonymousUserID, uniqueName("audit-retention"))

	oldSession := uniqueName("old-audit")
	newSession := uniqueName("new-audit")
	if err := audits.Create(ctx, store.ProviderCallAudit{
		WorkspaceID: workspaceID,
		SessionID:   oldSession,
		Provider:    "gemini",
		Model:       "gemini-test",
		Operation:   "transcribe",
	}); err != nil {
		t.Fatalf("create old audit: %v", err)
	}
	if err := audits.Create(ctx, store.ProviderCallAudit{
		WorkspaceID: workspaceID,
		SessionID:   newSession,
		Provider:    "gemini",
		Model:       "gemini-test",
		Operation:   "transcribe",
	}); err != nil {
		t.Fatalf("create new audit: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`UPDATE provider_call_audits SET created_at = '2000-01-01 00:00:00' WHERE workspace_id = ? AND session_id IN (?, ?)`, workspaceID, oldSession, newSession)
		_ = audits.Retain(context.Background(), time.Nanosecond)
	})
	if _, err := db.Exec(`UPDATE provider_call_audits SET created_at = ? WHERE session_id = ?`, time.Now().UTC().Add(-48*time.Hour), oldSession); err != nil {
		t.Fatalf("age old audit: %v", err)
	}

	if err := audits.Retain(ctx, 24*time.Hour); err != nil {
		t.Fatalf("Retain() error = %v", err)
	}

	var oldCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM provider_call_audits WHERE session_id = ?`, oldSession).Scan(&oldCount); err != nil {
		t.Fatalf("count old audit: %v", err)
	}
	if oldCount != 0 {
		t.Fatalf("old audit count = %d, want 0", oldCount)
	}
	var newCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM provider_call_audits WHERE session_id = ?`, newSession).Scan(&newCount); err != nil {
		t.Fatalf("count new audit: %v", err)
	}
	if newCount != 1 {
		t.Fatalf("new audit count = %d, want 1", newCount)
	}
}

func TestResolveTranscriptionJobContextUsesSnapshotAndItemWorkspace(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	itemStore := store.NewItemStore(db)
	contextStore := store.NewContextStore(db)
	handler := &Handler{
		items:    itemStore,
		contexts: contextStore,
	}

	ownerUserID := createTestUser(t, db, fmt.Sprintf("owner-%d", time.Now().UnixNano()))
	otherUserID := createTestUser(t, db, fmt.Sprintf("other-%d", time.Now().UnixNano()))
	ownerWorkspaceID := createTestWorkspace(t, db, ownerUserID, uniqueName("owner-workspace"))
	otherWorkspaceID := createTestWorkspace(t, db, otherUserID, uniqueName("other-workspace"))

	defaultCtx, err := contextStore.Create(ctx, store.Context{
		Name:                  uniqueName("job-default"),
		IsDefault:             true,
		SegmentationModel:     "scribe",
		TranscriptionProvider: "ollama",
		TranscriptionModel:    "default-model",
	})
	if err != nil {
		t.Fatalf("create default context: %v", err)
	}
	t.Cleanup(func() {
		_ = contextStore.Delete(context.Background(), defaultCtx.ID)
	})

	otherCtxUserID := otherUserID
	otherCtx, err := contextStore.Create(ctx, store.Context{
		UserID:                &otherCtxUserID,
		WorkspaceID:           &otherWorkspaceID,
		Name:                  uniqueName("other-user-context"),
		IsDefault:             false,
		SegmentationModel:     "scribe",
		TranscriptionProvider: "ollama",
		TranscriptionModel:    "other-user-model",
	})
	if err != nil {
		t.Fatalf("create other user context: %v", err)
	}
	t.Cleanup(func() {
		_ = contextStore.Delete(context.Background(), otherCtx.ID)
	})

	rule, err := contextStore.CreateRuleForWorkspace(ctx, otherWorkspaceID, store.ContextSelectionRule{
		ContextID: otherCtx.ID,
		Priority:  100,
	})
	if err != nil {
		t.Fatalf("create other workspace selection rule: %v", err)
	}
	t.Cleanup(func() {
		_ = contextStore.DeleteRule(context.Background(), rule.ID)
	})

	itemID := uniqueName("job-item")
	item, err := itemStore.Create(ctx, dbstore.CreateItemParams{
		ID:          itemID,
		UserID:      ownerUserID,
		WorkspaceID: ownerWorkspaceID,
		Name:        "Job Item",
		SourceType:  "upload",
	})
	if err != nil {
		t.Fatalf("create item: %v", err)
	}
	t.Cleanup(func() {
		_ = itemStore.DeleteForWorkspace(context.Background(), item.ID, item.WorkspaceID)
	})

	image, err := itemStore.AddImage(ctx, dbstore.CreateItemImageParams{
		ItemID:    item.ID,
		Sequence:  0,
		ImageURL:  "https://example.test/image.jpg",
		CanvasURI: "https://example.test/canvas/job-item",
	})
	if err != nil {
		t.Fatalf("add item image: %v", err)
	}

	contextSnapshot, err := json.Marshal(defaultCtx)
	if err != nil {
		t.Fatalf("marshal context snapshot: %v", err)
	}
	defaultContextID := defaultCtx.ID
	resolved, workspaceID, err := handler.resolveTranscriptionJobContext(ctx, &store.TranscriptionJob{
		ItemImageID:     image.ID,
		ContextID:       &defaultContextID,
		ContextSnapshot: contextSnapshot,
	})
	if err != nil {
		t.Fatalf("resolveTranscriptionJobContext: %v", err)
	}
	if workspaceID != ownerWorkspaceID {
		t.Fatalf("workspaceID = %d, want %d", workspaceID, ownerWorkspaceID)
	}
	if !resolved.IsDefault {
		t.Fatalf("resolved.IsDefault = false, want true; worker should fall back to a default context for the owning user")
	}
	if resolved.ID == otherCtx.ID {
		t.Fatalf("resolved context = %d; worker leaked another user's selection rule", resolved.ID)
	}
}

func TestTranscriptionJobUsesImmutableContextSnapshot(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	itemStore := store.NewItemStore(db)
	contextStore := store.NewContextStore(db)
	jobStore := store.NewTranscriptionJobStore(db)
	handler := &Handler{items: itemStore, contexts: contextStore, transcriptionJobs: jobStore}

	userID := createTestUser(t, db, uniqueName("snapshot-owner"))
	workspaceID := createTestWorkspace(t, db, userID, uniqueName("snapshot-workspace"))
	processingContext, err := contextStore.Create(ctx, store.Context{
		UserID:                &userID,
		WorkspaceID:           &workspaceID,
		Name:                  uniqueName("snapshot-context"),
		SegmentationModel:     "tesseract",
		TranscriptionProvider: "ollama",
		TranscriptionModel:    "model-before-update",
	})
	if err != nil {
		t.Fatalf("create context: %v", err)
	}
	t.Cleanup(func() { _ = contextStore.Delete(context.Background(), processingContext.ID) })

	item, err := itemStore.Create(ctx, dbstore.CreateItemParams{
		ID:          uniqueName("snapshot-item"),
		UserID:      userID,
		WorkspaceID: workspaceID,
		Name:        "Snapshot item",
		SourceType:  "upload",
	})
	if err != nil {
		t.Fatalf("create item: %v", err)
	}
	t.Cleanup(func() { _ = itemStore.DeleteForWorkspace(context.Background(), item.ID, item.WorkspaceID) })
	canvasURI := fmt.Sprintf("https://example.test/items/%s/canvas/1", item.ID)
	image, err := itemStore.AddImage(ctx, dbstore.CreateItemImageParams{
		ItemID: item.ID, Sequence: 0, ImageURL: "https://example.test/snapshot.jpg", CanvasURI: canvasURI,
	})
	if err != nil {
		t.Fatalf("add item image: %v", err)
	}
	pageJSON, err := iiif.NewAnnotationPage(iiif.PageIdentity{
		PublicBaseURL: "https://example.test",
		ItemImageID:   image.ID,
		CanvasURI:     canvasURI,
	}, []any{})
	if err != nil {
		t.Fatalf("build snapshot annotation page: %v", err)
	}
	pageID, err := iiif.CanonicalPageID("https://example.test", image.ID)
	if err != nil {
		t.Fatalf("snapshot annotation page id: %v", err)
	}
	if _, err := store.NewAnnotationStore(db).SavePage(ctx, store.AnnotationPage{
		WorkspaceID: workspaceID,
		ItemImageID: image.ID,
		PageID:      pageID,
		CanvasURI:   canvasURI,
		Payload:     string(pageJSON),
	}, 0); err != nil {
		t.Fatalf("save snapshot annotation page: %v", err)
	}

	contextID := processingContext.ID
	jobID, err := handler.createTranscriptionJob(ctx, image.ID, &contextID)
	if err != nil {
		t.Fatalf("create transcription job: %v", err)
	}
	processingContext.TranscriptionModel = "model-after-update"
	if _, err := contextStore.UpdateForWorkspace(ctx, processingContext, workspaceID, userID); err != nil {
		t.Fatalf("update context: %v", err)
	}
	job, err := jobStore.Get(ctx, jobID)
	if err != nil {
		t.Fatalf("get transcription job: %v", err)
	}
	resolved, gotWorkspaceID, err := handler.resolveTranscriptionJobContext(ctx, &job)
	if err != nil {
		t.Fatalf("resolve job context: %v", err)
	}
	if gotWorkspaceID != workspaceID || resolved.TranscriptionModel != "model-before-update" {
		t.Fatalf("resolved snapshot workspace/model = %d/%q, want %d/%q", gotWorkspaceID, resolved.TranscriptionModel, workspaceID, "model-before-update")
	}
}

func TestSeedTranscriptionJobOCRRunUsesActualCompletedCount(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	itemStore := store.NewItemStore(db)
	contextStore := store.NewContextStore(db)
	annotationStore := store.NewAnnotationStore(db)
	ocrRunStore := store.NewOCRRunStore(db)
	handler := &Handler{
		annotations: annotationStore,
		ocrRuns:     ocrRunStore,
	}

	userID := createTestUser(t, db, uniqueName("seed-user"))
	workspaceID := createTestWorkspace(t, db, userID, uniqueName("seed-workspace"))
	contextRow, err := contextStore.Create(ctx, store.Context{
		Name:                  uniqueName("seed-context"),
		SegmentationModel:     "scribe",
		TranscriptionProvider: "ollama",
		TranscriptionModel:    "seed-model",
	})
	if err != nil {
		t.Fatalf("create context: %v", err)
	}
	t.Cleanup(func() {
		_ = contextStore.Delete(context.Background(), contextRow.ID)
	})

	item, err := itemStore.Create(ctx, dbstore.CreateItemParams{
		ID:          uniqueName("seed-item"),
		UserID:      userID,
		WorkspaceID: workspaceID,
		Name:        "Seed Item",
		SourceType:  "upload",
	})
	if err != nil {
		t.Fatalf("create item: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM item_images WHERE item_id = ?`, item.ID)
		_ = itemStore.DeleteForWorkspace(context.Background(), item.ID, item.WorkspaceID)
	})

	createImageWithAnnotation := func(t *testing.T, sequence int) (store.ItemImage, store.AnnotationPage) {
		t.Helper()
		canvasURI := fmt.Sprintf("https://example.test/items/%s/canvas/%d", item.ID, sequence)
		image, err := itemStore.AddImage(ctx, dbstore.CreateItemImageParams{
			ItemID: item.ID, Sequence: uint32(sequence), ImageURL: fmt.Sprintf("https://example.test/seed-%d.jpg", sequence), CanvasURI: canvasURI,
			Width: 1000, Height: 1000,
		})
		if err != nil {
			t.Fatalf("add image %d: %v", sequence, err)
		}
		pageID, err := iiif.CanonicalPageID("https://example.test", image.ID)
		if err != nil {
			t.Fatalf("build annotation page id %d: %v", sequence, err)
		}
		annotationID, err := iiif.AnnotationID(pageID, fmt.Sprintf("line-%d", sequence))
		if err != nil {
			t.Fatalf("build annotation id %d: %v", sequence, err)
		}
		items := []any{map[string]any{
			"id":              annotationID,
			"type":            "Annotation",
			"motivation":      "supplementing",
			"textGranularity": "line",
			"body": []any{map[string]any{
				"type": "TextualBody", "purpose": "supplementing", "value": fmt.Sprintf("Seeded text %d", sequence), "format": "text/plain",
			}},
			"target": map[string]any{
				"source":   map[string]any{"id": canvasURI, "type": "Canvas"},
				"selector": map[string]any{"type": "FragmentSelector", "conformsTo": "http://www.w3.org/TR/media-frags/", "value": "xywh=10,20,100,30"},
			},
		}}
		raw, err := iiif.NewAnnotationPage(iiif.PageIdentity{PublicBaseURL: "https://example.test", ItemImageID: image.ID, CanvasURI: canvasURI}, items)
		if err != nil {
			t.Fatalf("build annotation page %d: %v", sequence, err)
		}
		page, err := annotationStore.SavePage(ctx, store.AnnotationPage{
			WorkspaceID: workspaceID,
			ItemImageID: image.ID,
			PageID:      pageID,
			CanvasURI:   canvasURI,
			Payload:     string(raw),
		}, 0)
		if err != nil {
			t.Fatalf("save annotation page %d: %v", sequence, err)
		}
		return image, page
	}

	completedImage, completedPage := createImageWithAnnotation(t, 1)
	job := &store.TranscriptionJob{
		ID:                uint64(time.Now().UnixNano()),
		ItemImageID:       completedImage.ID,
		CompletedSegments: 0,
	}
	provenance, err := handler.transcriptionJobOCRRun(ctx, job, contextRow, completedImage, completedPage, 1)
	if err != nil {
		t.Fatalf("build completed OCR run: %v", err)
	}
	if provenance == nil {
		t.Fatal("completed OCR provenance is nil")
	}
	if err := ocrRunStore.Create(ctx, *provenance); err != nil {
		t.Fatalf("persist completed OCR run fixture: %v", err)
	}
	run, err := ocrRunStore.GetByItemImageID(ctx, completedImage.ID)
	if err != nil {
		t.Fatalf("get seeded OCR run: %v", err)
	}
	if run.OriginalText != "Seeded text 1" {
		t.Fatalf("OriginalText = %q, want %q", run.OriginalText, "Seeded text 1")
	}

	emptyImage, emptyPage := createImageWithAnnotation(t, 2)
	emptyJob := &store.TranscriptionJob{
		ID:                uint64(time.Now().UnixNano() + 1),
		ItemImageID:       emptyImage.ID,
		CompletedSegments: 1,
	}
	emptyProvenance, err := handler.transcriptionJobOCRRun(ctx, emptyJob, contextRow, emptyImage, emptyPage, 0)
	if err != nil {
		t.Fatalf("build empty OCR run: %v", err)
	}
	if emptyProvenance != nil {
		t.Fatalf("empty job OCR provenance = %#v, want nil", emptyProvenance)
	}
	if _, err := ocrRunStore.GetByItemImageID(ctx, emptyImage.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("empty job OCR run error = %v, want sql.ErrNoRows", err)
	}
}

func TestResolveTranscriptionJobContextRejectsCrossWorkspaceSnapshot(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	itemStore := store.NewItemStore(db)
	contextStore := store.NewContextStore(db)
	handler := &Handler{
		items:    itemStore,
		contexts: contextStore,
	}

	ownerUserID := createTestUser(t, db, fmt.Sprintf("owner-explicit-%d", time.Now().UnixNano()))
	otherUserID := createTestUser(t, db, fmt.Sprintf("other-explicit-%d", time.Now().UnixNano()))
	ownerWorkspaceID := createTestWorkspace(t, db, ownerUserID, uniqueName("owner-explicit-workspace"))
	otherWorkspaceID := createTestWorkspace(t, db, otherUserID, uniqueName("other-explicit-workspace"))

	otherCtxUserID := otherUserID
	otherCtx, err := contextStore.Create(ctx, store.Context{
		UserID:                &otherCtxUserID,
		WorkspaceID:           &otherWorkspaceID,
		Name:                  uniqueName("other-explicit-context"),
		SegmentationModel:     "kraken",
		TranscriptionProvider: "ollama",
		TranscriptionModel:    "other-explicit-model",
	})
	if err != nil {
		t.Fatalf("create other user context: %v", err)
	}
	t.Cleanup(func() {
		_ = contextStore.Delete(context.Background(), otherCtx.ID)
	})

	itemID := uniqueName("job-item-explicit")
	item, err := itemStore.Create(ctx, dbstore.CreateItemParams{
		ID:          itemID,
		UserID:      ownerUserID,
		WorkspaceID: ownerWorkspaceID,
		Name:        "Job Item Explicit",
		SourceType:  "upload",
	})
	if err != nil {
		t.Fatalf("create item: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM item_images WHERE item_id = ?`, item.ID)
		_ = itemStore.DeleteForWorkspace(context.Background(), item.ID, item.WorkspaceID)
	})

	image, err := itemStore.AddImage(ctx, dbstore.CreateItemImageParams{
		ItemID: item.ID, Sequence: 0, ImageURL: "https://example.test/image-explicit.jpg", CanvasURI: "https://example.test/canvas/job-item-explicit",
	})
	if err != nil {
		t.Fatalf("add item image: %v", err)
	}

	otherContextID := otherCtx.ID
	contextSnapshot, err := json.Marshal(otherCtx)
	if err != nil {
		t.Fatalf("marshal context snapshot: %v", err)
	}
	if _, _, err := handler.resolveTranscriptionJobContext(ctx, &store.TranscriptionJob{
		ItemImageID:     image.ID,
		ContextID:       &otherContextID,
		ContextSnapshot: contextSnapshot,
	}); err == nil {
		t.Fatal("resolveTranscriptionJobContext succeeded with another user's explicit context")
	}
}
