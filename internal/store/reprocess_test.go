package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

func TestReprocessCommitAtomicallyAdvancesPageCurrentBaselineJobAndEvent(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	canvasURI := "https://source.example/canvas/reprocess-" + suffix
	processingContext := createAnnotationTestContext(t, database, suffix+"-reprocess")
	workspaceID, imageID := createAnnotationTestResource(t, database, suffix+"-reprocess", canvasURI)
	annotationStore := store.NewAnnotationStore(database)
	jobStore := store.NewTranscriptionJobStore(database)
	ocrStore := store.NewOCRRunStore(database)

	base, err := annotationStore.SavePage(ctx, canonicalTestPage(t, workspaceID, imageID, canvasURI, "old text"), 0)
	if err != nil {
		t.Fatalf("save base page: %v", err)
	}
	contextID := processingContext.ID
	oldSessionID := "reprocess-old-" + suffix
	newSessionID := "reprocess-new-" + suffix
	if err := ocrStore.Create(ctx, store.OCRRun{
		SessionID:    oldSessionID,
		ItemImageID:  &imageID,
		ContextID:    &contextID,
		ImageURL:     "https://source.example/old.jpg",
		Provider:     "tesseract",
		Model:        "old-model",
		OriginalHOCR: "<html>old</html>",
		OriginalText: "old text",
	}); err != nil {
		t.Fatalf("save old OCR baseline: %v", err)
	}
	oldJobID, err := jobStore.Create(ctx, imageID, processingContext)
	if err != nil {
		t.Fatalf("create old transcription job: %v", err)
	}

	base.Payload = replacePageText(t, base.Payload, "new segmented text")
	forgedContext := processingContext
	forgedContext.Name = "caller-forged-reprocess"
	forgedContext.TranscriptionModel = "forged-model"
	eventID := "reprocess-event-" + suffix
	result, err := annotationStore.SavePageAndStartReprocessing(ctx, base, base.Revision, store.AnnotationReprocessCommit{
		OCRRun: store.OCRRun{
			SessionID:    newSessionID,
			ItemImageID:  &imageID,
			ContextID:    &contextID,
			ImageURL:     "https://source.example/new.jpg",
			Provider:     "tesseract",
			Model:        "new-model",
			OriginalHOCR: "<html>new</html>",
			OriginalText: "new segmented text",
		},
		Context:   forgedContext,
		EventID:   eventID,
		EventType: "dev.scribe.annotations.reprocessed",
		Subject:   fmt.Sprintf("item-images/%d", imageID),
		BodyJSON:  fmt.Sprintf(`{"id":%q}`, eventID),
	})
	if err != nil {
		t.Fatalf("SavePageAndStartReprocessing: %v", err)
	}
	if result.Page.Revision != base.Revision+1 {
		t.Fatalf("reprocessed revision = %d, want %d", result.Page.Revision, base.Revision+1)
	}
	if result.TranscriptionJobID == 0 || result.TranscriptionJobID == oldJobID {
		t.Fatalf("replacement job id = %d, old job id = %d", result.TranscriptionJobID, oldJobID)
	}
	assertPageText(t, result.Page.Payload, "new segmented text")

	oldJob, err := jobStore.Get(ctx, oldJobID)
	if err != nil || oldJob.Status != store.TranscriptionJobStatusSuperseded {
		t.Fatalf("superseded job = %+v, %v", oldJob, err)
	}
	newJob, err := jobStore.Get(ctx, result.TranscriptionJobID)
	if err != nil || newJob.Status != store.TranscriptionJobStatusPending {
		t.Fatalf("replacement job = %+v, %v", newJob, err)
	}
	if newJob.InputRevision != result.Page.Revision {
		t.Fatalf("replacement input revision = %d, want %d", newJob.InputRevision, result.Page.Revision)
	}
	var snapshottedContext store.Context
	if err := json.Unmarshal(newJob.ContextSnapshot, &snapshottedContext); err != nil {
		t.Fatalf("decode replacement context snapshot: %v", err)
	}
	if snapshottedContext.Name != processingContext.Name || snapshottedContext.TranscriptionModel != processingContext.TranscriptionModel {
		t.Fatalf("replacement context snapshot = %+v, want authoritative %+v", snapshottedContext, processingContext)
	}
	baseline, err := ocrStore.Get(ctx, newSessionID)
	if err != nil {
		t.Fatalf("load replacement OCR baseline: %v", err)
	}
	if baseline.OriginalText != "new segmented text" || baseline.Model != "new-model" || baseline.CanonicalRevision != nil {
		t.Fatalf("replacement OCR baseline = %+v", baseline)
	}
	current, err := ocrStore.GetByItemImageID(ctx, imageID)
	if err != nil {
		t.Fatalf("load current OCR baseline: %v", err)
	}
	if current.SessionID != newSessionID {
		t.Fatalf("current OCR session = %q, want %q", current.SessionID, newSessionID)
	}
	original, err := ocrStore.Get(ctx, oldSessionID)
	if err != nil {
		t.Fatalf("reload original OCR baseline: %v", err)
	}
	if original.OriginalText != "old text" || original.Model != "old-model" || original.ImageURL != "https://source.example/old.jpg" {
		t.Fatalf("original OCR baseline was mutated: %+v", original)
	}
	var eventCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM event_outbox WHERE event_id = ?`, eventID).Scan(&eventCount); err != nil {
		t.Fatalf("count reprocess event: %v", err)
	}
	if eventCount != 1 {
		t.Fatalf("reprocess event count = %d, want 1", eventCount)
	}
}

func TestReprocessLateFailureRollsBackEveryCanonicalSideEffect(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	canvasURI := "https://source.example/canvas/reprocess-rollback-" + suffix
	processingContext := createAnnotationTestContext(t, database, suffix+"-reprocess-rollback")
	workspaceID, imageID := createAnnotationTestResource(t, database, suffix+"-reprocess-rollback", canvasURI)
	annotationStore := store.NewAnnotationStore(database)
	jobStore := store.NewTranscriptionJobStore(database)
	ocrStore := store.NewOCRRunStore(database)

	base, err := annotationStore.SavePage(ctx, canonicalTestPage(t, workspaceID, imageID, canvasURI, "committed text"), 0)
	if err != nil {
		t.Fatalf("save base page: %v", err)
	}
	contextID := processingContext.ID
	oldSessionID := "reprocess-rollback-old-" + suffix
	newSessionID := "reprocess-rollback-new-" + suffix
	if err := ocrStore.Create(ctx, store.OCRRun{
		SessionID:    oldSessionID,
		ItemImageID:  &imageID,
		ContextID:    &contextID,
		ImageURL:     "https://source.example/committed.jpg",
		Provider:     "tesseract",
		Model:        "committed-model",
		OriginalHOCR: "<html>committed</html>",
		OriginalText: "committed text",
	}); err != nil {
		t.Fatalf("save committed OCR baseline: %v", err)
	}
	oldJobID, err := jobStore.Create(ctx, imageID, processingContext)
	if err != nil {
		t.Fatalf("create active job: %v", err)
	}
	operationKey := "reprocess-rollback:" + suffix
	requestHash := fmt.Sprintf("%064x", 1)
	reservation, created, err := jobStore.ReserveExternalRequest(ctx, workspaceID, "image-reprocess", operationKey, requestHash, "")
	if err != nil || !created {
		t.Fatalf("reserve reprocess operation = %+v, created %t, error %v", reservation, created, err)
	}
	t.Cleanup(func() {
		_ = jobStore.FailExternalRequest(context.Background(), workspaceID, "image-reprocess", operationKey, reservation.LeaseOwner, "test cleanup")
	})

	base.Payload = replacePageText(t, base.Payload, "must roll back")
	_, err = annotationStore.SavePageAndStartReprocessing(ctx, base, base.Revision, store.AnnotationReprocessCommit{
		OCRRun: store.OCRRun{
			SessionID:    newSessionID,
			ItemImageID:  &imageID,
			ContextID:    &contextID,
			ImageURL:     "https://source.example/must-roll-back.jpg",
			Provider:     "tesseract",
			Model:        "must-roll-back-model",
			OriginalHOCR: "<html>must roll back</html>",
			OriginalText: "must roll back",
		},
		Context: processingContext,
		ExternalRequest: &store.AnnotationReprocessExternalRequest{
			Source:         "image-reprocess",
			IdempotencyKey: operationKey,
			LeaseOwner:     reservation.LeaseOwner + "-mismatch",
		},
	})
	if err == nil {
		t.Fatal("SavePageAndStartReprocessing succeeded with a mismatched operation fence")
	}

	page, err := annotationStore.LoadPage(ctx, workspaceID, imageID)
	if err != nil {
		t.Fatalf("reload canonical page: %v", err)
	}
	if page.Revision != base.Revision {
		t.Fatalf("rolled-back page revision = %d, want %d", page.Revision, base.Revision)
	}
	assertPageText(t, page.Payload, "committed text")
	baseline, err := ocrStore.Get(ctx, oldSessionID)
	if err != nil {
		t.Fatalf("reload OCR baseline: %v", err)
	}
	if baseline.OriginalText != "committed text" || baseline.Model != "committed-model" {
		t.Fatalf("OCR baseline changed despite rollback: %+v", baseline)
	}
	if _, err := ocrStore.Get(ctx, newSessionID); err == nil {
		t.Fatalf("rolled-back OCR session %q was persisted", newSessionID)
	}
	current, err := ocrStore.GetByItemImageID(ctx, imageID)
	if err != nil {
		t.Fatalf("load current OCR baseline after rollback: %v", err)
	}
	if current.SessionID != oldSessionID {
		t.Fatalf("current OCR session after rollback = %q, want %q", current.SessionID, oldSessionID)
	}
	activeJob, err := jobStore.GetActiveByItemImage(ctx, imageID)
	if err != nil || activeJob.ID != oldJobID {
		t.Fatalf("active job after rollback = %+v, %v; want %d", activeJob, err, oldJobID)
	}
	var jobCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM transcription_jobs WHERE item_image_id = ?`, imageID).Scan(&jobCount); err != nil {
		t.Fatalf("count jobs after rollback: %v", err)
	}
	if jobCount != 1 {
		t.Fatalf("job count after rollback = %d, want 1", jobCount)
	}
}
