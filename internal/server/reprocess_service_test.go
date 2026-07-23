package server

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	ocrhandlers "github.com/lehigh-university-libraries/scribe/internal/handlers"
	"github.com/lehigh-university-libraries/scribe/internal/hocr"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
	"google.golang.org/protobuf/proto"
)

const reprocessServiceTestHOCR = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html>
<html>
  <body>
    <div class="ocr_page" id="page_1" title="bbox 0 0 1200 1600">
      <span class="ocr_line" id="line_1" title="bbox 20 30 500 70">
        <span class="ocrx_word" id="word_1" title="bbox 20 30 100 70; x_wconf 98">new</span>
        <span class="ocrx_word" id="word_2" title="bbox 110 30 300 70; x_wconf 97">segmented</span>
        <span class="ocrx_word" id="word_3" title="bbox 310 30 500 70; x_wconf 96">text</span>
      </span>
    </div>
  </body>
</html>`

type reprocessOCRProcessor struct {
	result         ocrhandlers.ProcessResult
	transientCalls int
	lastImageURL   string
	lastContext    hocr.ProcessingContext
}

func (f *reprocessOCRProcessor) SetProviderCallAuditLogger(hocr.ProviderCallAuditLogger) {}

func (f *reprocessOCRProcessor) ProcessImageURLWithContext(context.Context, string, hocr.ProcessingContext) (*ocrhandlers.ProcessResult, error) {
	return nil, fmt.Errorf("unexpected durable URL processing call")
}

func (f *reprocessOCRProcessor) ProcessImageURLTransientWithContext(_ context.Context, imageURL string, processingContext hocr.ProcessingContext) (*ocrhandlers.ProcessResult, error) {
	f.transientCalls++
	f.lastImageURL = imageURL
	f.lastContext = processingContext
	result := f.result
	return &result, nil
}

func (f *reprocessOCRProcessor) ProcessImageUploadWithContext(context.Context, string, []byte, hocr.ProcessingContext) (*ocrhandlers.ProcessResult, error) {
	return nil, fmt.Errorf("unexpected upload processing call")
}

func (f *reprocessOCRProcessor) StoreUploadedImage(context.Context, string, []byte) (string, error) {
	return "", fmt.Errorf("unexpected upload persistence call")
}

func (f *reprocessOCRProcessor) TranscribeImageFileWithContext(context.Context, string, string, string) (string, error) {
	return "", fmt.Errorf("unexpected transcription call")
}

func TestReprocessItemImageRevisionFenceIdempotencyAndOCRHistory(t *testing.T) {
	database := openTestDB(t)
	ctx := context.Background()
	contextStore := store.NewContextStore(database)
	itemStore := store.NewItemStore(database)
	annotationStore := store.NewAnnotationStore(database)
	ocrRunStore := store.NewOCRRunStore(database)
	jobStore := store.NewTranscriptionJobStore(database)

	primaryContext := createReprocessServiceContext(t, contextStore, "primary")
	otherContext := createReprocessServiceContext(t, contextStore, "other")
	foreignWorkspaceID, foreignUserID := createServerTestWorkspace(t, database)
	foreignContext := createWorkspaceReprocessServiceContext(t, contextStore, foreignWorkspaceID, foreignUserID)
	canvasURI := "https://source.example/canvas/reprocess-service-" + uuid.NewString()
	image := createServerTestItemImage(t, database, store.AnonymousWorkspaceID, store.AnonymousUserID, canvasURI)

	fakeOCR := &reprocessOCRProcessor{result: ocrhandlers.ProcessResult{
		SessionID: "reprocess-service-" + uuid.NewString(),
		HOCR:      reprocessServiceTestHOCR,
		PlainText: "new segmented text",
		Provider:  "test-segmentor",
		Model:     "test-layout-model",
	}}
	handler := &Handler{
		ocrRuns:           ocrRunStore,
		items:             itemStore,
		contexts:          contextStore,
		annotations:       annotationStore,
		transcriptionJobs: jobStore,
		appCtx:            context.Background(),
		ocr:               fakeOCR,
	}

	pageID, err := handler.annotationPageIDForItemImage(image.ID)
	if err != nil {
		t.Fatalf("annotation page ID: %v", err)
	}
	annotationID, err := iiif.AnnotationID(pageID, "original-line")
	if err != nil {
		t.Fatalf("annotation ID: %v", err)
	}
	pageJSON, err := iiif.NewAnnotationPage(iiif.PageIdentity{
		PublicBaseURL: handler.publicAnnotationBaseURL(),
		ItemImageID:   image.ID,
		CanvasURI:     canvasURI,
	}, []any{mustDecodeAnnotation(canonicalMutationTestAnnotation(annotationID, canvasURI, "original corrected text"))})
	if err != nil {
		t.Fatalf("build initial annotation page: %v", err)
	}
	basePage, err := annotationStore.SavePage(ctx, store.AnnotationPage{
		WorkspaceID: store.AnonymousWorkspaceID,
		ItemImageID: image.ID,
		PageID:      pageID,
		CanvasURI:   canvasURI,
		Payload:     string(pageJSON),
	}, 0)
	if err != nil {
		t.Fatalf("save initial annotation page: %v", err)
	}

	primaryContextID := primaryContext.ID
	oldSessionID := "reprocess-service-old-" + uuid.NewString()
	if err := ocrRunStore.Create(ctx, store.OCRRun{
		SessionID:    oldSessionID,
		ItemImageID:  &image.ID,
		ContextID:    &primaryContextID,
		ImageURL:     image.ImageURL,
		Provider:     "old-provider",
		Model:        "old-model",
		OriginalHOCR: "<html><body>old baseline</body></html>",
		OriginalText: "old baseline",
	}); err != nil {
		t.Fatalf("seed original OCR run: %v", err)
	}
	originalBefore, err := ocrRunStore.Get(ctx, oldSessionID)
	if err != nil {
		t.Fatalf("load original OCR run: %v", err)
	}

	_, err = handler.ReprocessItemImage(ctx, connect.NewRequest(&scribev1.ReprocessItemImageRequest{
		ItemImageId: image.ID,
		ContextId:   primaryContext.ID,
	}))
	assertReprocessConnectCode(t, err, connect.CodeInvalidArgument)
	if fakeOCR.transientCalls != 0 {
		t.Fatalf("processor calls after missing revision = %d, want 0", fakeOCR.transientCalls)
	}

	_, err = handler.ReprocessItemImage(ctx, connect.NewRequest(&scribev1.ReprocessItemImageRequest{
		ItemImageId:      image.ID,
		ContextId:        foreignContext.ID,
		ExpectedRevision: basePage.Revision,
	}))
	assertReprocessConnectCode(t, err, connect.CodeNotFound)
	if fakeOCR.transientCalls != 0 {
		t.Fatalf("processor calls after foreign context = %d, want 0", fakeOCR.transientCalls)
	}
	assertNoReprocessReservation(t, database, image.ID, basePage.Revision)

	staleRequest := &scribev1.ReprocessItemImageRequest{
		ItemImageId:      image.ID,
		ContextId:        primaryContext.ID,
		ExpectedRevision: basePage.Revision + 100,
	}
	_, err = handler.ReprocessItemImage(ctx, connect.NewRequest(staleRequest))
	assertReprocessConnectCode(t, err, connect.CodeAborted)
	if fakeOCR.transientCalls != 0 {
		t.Fatalf("processor calls after stale revision = %d, want 0", fakeOCR.transientCalls)
	}
	assertNoReprocessReservation(t, database, image.ID, staleRequest.GetExpectedRevision())
	_, err = handler.ReprocessItemImage(ctx, connect.NewRequest(staleRequest))
	assertReprocessConnectCode(t, err, connect.CodeAborted)
	if fakeOCR.transientCalls != 0 {
		t.Fatalf("processor calls after repeated stale revision = %d, want 0", fakeOCR.transientCalls)
	}
	assertNoReprocessReservation(t, database, image.ID, staleRequest.GetExpectedRevision())
	assertReprocessStateUnchanged(t, annotationStore, ocrRunStore, image.ID, basePage.Revision, oldSessionID)

	abandonedRequest := &scribev1.ReprocessItemImageRequest{
		ItemImageId:      image.ID,
		ContextId:        primaryContext.ID,
		ExpectedRevision: basePage.Revision + 101,
	}
	abandonedKey := fmt.Sprintf("%d:%d", image.ID, abandonedRequest.GetExpectedRevision())
	abandonedHash := stableRequestHash(
		fmt.Sprintf("%d", image.ID),
		fmt.Sprintf("%d", abandonedRequest.GetExpectedRevision()),
		fmt.Sprintf("%d", primaryContext.ID),
	)
	if _, created, err := jobStore.ReserveExternalRequest(
		ctx,
		store.AnonymousWorkspaceID,
		"image-reprocess",
		abandonedKey,
		abandonedHash,
		"",
	); err != nil || !created {
		t.Fatalf("seed abandoned reprocess reservation = created %t, %v", created, err)
	}
	if _, err := database.ExecContext(ctx, `
UPDATE external_requests
SET lease_until = DATE_SUB(UTC_TIMESTAMP(6), INTERVAL 1 MINUTE)
WHERE workspace_id = ? AND source = 'image-reprocess' AND idempotency_key = ?`,
		store.AnonymousWorkspaceID,
		abandonedKey,
	); err != nil {
		t.Fatalf("expire abandoned reprocess reservation: %v", err)
	}
	abandonedBefore := loadReprocessReservation(t, database, abandonedKey)
	if abandonedBefore.status != string(store.ExternalRequestStatusInProgress) ||
		!abandonedBefore.leaseUntil.Valid ||
		!abandonedBefore.leaseUntil.Time.Before(time.Now().UTC()) {
		t.Fatalf("abandoned reservation was not expired in progress: %+v", abandonedBefore)
	}
	for attempt := 0; attempt < 2; attempt++ {
		_, err = handler.ReprocessItemImage(ctx, connect.NewRequest(abandonedRequest))
		assertReprocessConnectCode(t, err, connect.CodeAborted)
		if fakeOCR.transientCalls != 0 {
			t.Fatalf("processor calls after abandoned stale revision = %d, want 0", fakeOCR.transientCalls)
		}
		abandonedAfter := loadReprocessReservation(t, database, abandonedKey)
		if !sameReprocessReservation(abandonedBefore, abandonedAfter) {
			t.Fatalf("stale request reclaimed abandoned reservation\nbefore: %+v\nafter:  %+v", abandonedBefore, abandonedAfter)
		}
	}
	assertReprocessStateUnchanged(t, annotationStore, ocrRunStore, image.ID, basePage.Revision, oldSessionID)

	request := &scribev1.ReprocessItemImageRequest{
		ItemImageId:      image.ID,
		ContextId:        primaryContext.ID,
		ExpectedRevision: basePage.Revision,
	}
	response, err := handler.ReprocessItemImage(ctx, connect.NewRequest(request))
	if err != nil {
		t.Fatalf("ReprocessItemImage: %v", err)
	}
	if fakeOCR.transientCalls != 1 {
		t.Fatalf("processor calls after successful reprocess = %d, want 1", fakeOCR.transientCalls)
	}
	if fakeOCR.lastImageURL != image.ImageURL {
		t.Fatalf("processed image URL = %q, want %q", fakeOCR.lastImageURL, image.ImageURL)
	}
	if !fakeOCR.lastContext.SegmentOnly {
		t.Fatal("reprocess did not force segment-only processing")
	}
	if response.Msg.GetSessionId() != fakeOCR.result.SessionID ||
		response.Msg.GetContextId() != primaryContext.ID ||
		response.Msg.GetCanonicalRevision() != basePage.Revision+1 ||
		response.Msg.GetTranscriptionJobId() == 0 {
		t.Fatalf("reprocess response = %+v", response.Msg)
	}

	replayed, err := handler.ReprocessItemImage(ctx, connect.NewRequest(request))
	if err != nil {
		t.Fatalf("replay ReprocessItemImage: %v", err)
	}
	if fakeOCR.transientCalls != 1 {
		t.Fatalf("processor calls after exact replay = %d, want 1", fakeOCR.transientCalls)
	}
	if !proto.Equal(response.Msg, replayed.Msg) {
		t.Fatalf("replayed response differs\nfirst:  %+v\nreplay: %+v", response.Msg, replayed.Msg)
	}

	_, err = handler.ReprocessItemImage(ctx, connect.NewRequest(&scribev1.ReprocessItemImageRequest{
		ItemImageId:      image.ID,
		ContextId:        otherContext.ID,
		ExpectedRevision: basePage.Revision,
	}))
	assertReprocessConnectCode(t, err, connect.CodeAlreadyExists)
	if fakeOCR.transientCalls != 1 {
		t.Fatalf("processor calls after mismatched replay = %d, want 1", fakeOCR.transientCalls)
	}

	committedPage, err := annotationStore.LoadPage(ctx, store.AnonymousWorkspaceID, image.ID)
	if err != nil {
		t.Fatalf("load reprocessed annotation page: %v", err)
	}
	if committedPage.Revision != basePage.Revision+1 {
		t.Fatalf("reprocessed page revision = %d, want %d", committedPage.Revision, basePage.Revision+1)
	}
	if got := annotationPagePlainText(t, committedPage.Payload); got != fakeOCR.result.PlainText {
		t.Fatalf("reprocessed page text = %q, want %q", got, fakeOCR.result.PlainText)
	}

	currentRun, err := ocrRunStore.GetByItemImageID(ctx, image.ID)
	if err != nil {
		t.Fatalf("load current OCR run: %v", err)
	}
	if currentRun.SessionID != fakeOCR.result.SessionID || currentRun.Model != fakeOCR.result.Model || currentRun.OriginalText != fakeOCR.result.PlainText {
		t.Fatalf("current OCR run = %+v", currentRun)
	}
	originalAfter, err := ocrRunStore.Get(ctx, oldSessionID)
	if err != nil {
		t.Fatalf("reload original OCR run: %v", err)
	}
	if !sameOCRRun(originalBefore, originalAfter) {
		t.Fatalf("original OCR history was mutated\nbefore: %+v\nafter:  %+v", originalBefore, originalAfter)
	}
}

func createReprocessServiceContext(t *testing.T, contexts *store.ContextStore, label string) store.Context {
	t.Helper()
	created, err := contexts.Create(context.Background(), store.Context{
		Name:                  "reprocess-service-" + label + "-" + uuid.NewString(),
		SegmentationModel:     "tesseract/auto",
		TranscriptionProvider: "ollama",
		TranscriptionModel:    "test-model-" + label,
	})
	if err != nil {
		t.Fatalf("create %s processing context: %v", label, err)
	}
	t.Cleanup(func() {
		if err := contexts.Delete(context.Background(), created.ID); err != nil {
			t.Logf("delete processing context %d: %v", created.ID, err)
		}
	})
	return created
}

func createWorkspaceReprocessServiceContext(
	t *testing.T,
	contexts *store.ContextStore,
	workspaceID, userID uint64,
) store.Context {
	t.Helper()
	created, err := contexts.Create(context.Background(), store.Context{
		UserID:                &userID,
		WorkspaceID:           &workspaceID,
		Name:                  "reprocess-service-foreign-" + uuid.NewString(),
		SegmentationModel:     "tesseract/auto",
		TranscriptionProvider: "ollama",
		TranscriptionModel:    "test-model-foreign",
	})
	if err != nil {
		t.Fatalf("create foreign processing context: %v", err)
	}
	t.Cleanup(func() {
		if err := contexts.Delete(context.Background(), created.ID); err != nil {
			t.Logf("delete foreign processing context %d: %v", created.ID, err)
		}
	})
	return created
}

func assertReprocessConnectCode(t *testing.T, err error, want connect.Code) {
	t.Helper()
	if err == nil || connect.CodeOf(err) != want {
		t.Fatalf("Connect error = %v/%v, want %v", connect.CodeOf(err), err, want)
	}
}

func assertNoReprocessReservation(t *testing.T, database *sql.DB, itemImageID, expectedRevision uint64) {
	t.Helper()
	operationKey := fmt.Sprintf("%d:%d", itemImageID, expectedRevision)
	var count int
	if err := database.QueryRow(`
SELECT COUNT(*)
FROM external_requests
WHERE workspace_id = ? AND source = 'image-reprocess' AND idempotency_key = ?`,
		store.AnonymousWorkspaceID,
		operationKey,
	).Scan(&count); err != nil {
		t.Fatalf("count stale reprocess reservations: %v", err)
	}
	if count != 0 {
		t.Fatalf("stale reprocess reservation count = %d, want 0", count)
	}
}

type reprocessReservationSnapshot struct {
	status       string
	attemptCount int
	leaseUntil   sql.NullTime
	lockedBy     sql.NullString
}

func loadReprocessReservation(t *testing.T, database *sql.DB, operationKey string) reprocessReservationSnapshot {
	t.Helper()
	var snapshot reprocessReservationSnapshot
	if err := database.QueryRow(`
SELECT status, attempt_count, lease_until, locked_by
FROM external_requests
WHERE workspace_id = ? AND source = 'image-reprocess' AND idempotency_key = ?`,
		store.AnonymousWorkspaceID,
		operationKey,
	).Scan(&snapshot.status, &snapshot.attemptCount, &snapshot.leaseUntil, &snapshot.lockedBy); err != nil {
		t.Fatalf("load reprocess reservation: %v", err)
	}
	return snapshot
}

func sameReprocessReservation(left, right reprocessReservationSnapshot) bool {
	return left.status == right.status &&
		left.attemptCount == right.attemptCount &&
		left.lockedBy == right.lockedBy &&
		left.leaseUntil.Valid == right.leaseUntil.Valid &&
		(!left.leaseUntil.Valid || left.leaseUntil.Time.Equal(right.leaseUntil.Time))
}

func assertReprocessStateUnchanged(
	t *testing.T,
	annotations *store.AnnotationStore,
	ocrRuns *store.OCRRunStore,
	itemImageID, wantRevision uint64,
	wantSessionID string,
) {
	t.Helper()
	page, err := annotations.LoadPage(context.Background(), store.AnonymousWorkspaceID, itemImageID)
	if err != nil {
		t.Fatalf("load annotation page after rejected request: %v", err)
	}
	if page.Revision != wantRevision {
		t.Fatalf("page revision after rejected request = %d, want %d", page.Revision, wantRevision)
	}
	current, err := ocrRuns.GetByItemImageID(context.Background(), itemImageID)
	if err != nil {
		t.Fatalf("load OCR run after rejected request: %v", err)
	}
	if current.SessionID != wantSessionID {
		t.Fatalf("current OCR session after rejected request = %q, want %q", current.SessionID, wantSessionID)
	}
}

func annotationPagePlainText(t *testing.T, raw string) string {
	t.Helper()
	items, err := annotationItemsFromPage(raw)
	if err != nil {
		t.Fatalf("read reprocessed annotation items: %v", err)
	}
	for _, item := range items {
		annotation, ok := item.(map[string]any)
		granularity, _ := annotation["textGranularity"].(string)
		if !ok || granularity != "line" {
			continue
		}
		return extractAnnotationText(annotation)
	}
	return ""
}

func sameOCRRun(left, right store.OCRRun) bool {
	return left.SessionID == right.SessionID &&
		left.ItemImageID != nil && right.ItemImageID != nil && *left.ItemImageID == *right.ItemImageID &&
		left.ContextID != nil && right.ContextID != nil && *left.ContextID == *right.ContextID &&
		left.ImageURL == right.ImageURL &&
		left.Provider == right.Provider &&
		left.Model == right.Model &&
		left.OriginalHOCR == right.OriginalHOCR &&
		left.OriginalText == right.OriginalText &&
		left.CanonicalRevision == nil && right.CanonicalRevision == nil &&
		left.LevenshteinDistance == right.LevenshteinDistance &&
		left.CreatedAt.Equal(right.CreatedAt) &&
		left.UpdatedAt.Equal(right.UpdatedAt)
}
