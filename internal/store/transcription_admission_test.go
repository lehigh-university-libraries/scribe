package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"
	db "github.com/lehigh-university-libraries/scribe/internal/db"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

func TestTranscriptionJobAdmissionIsRaceFreePerWorkspace(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	const (
		quota      = 3
		imageCount = 16
	)
	policy, err := store.NewTranscriptionJobAdmissionPolicy(quota)
	if err != nil {
		t.Fatalf("create admission policy: %v", err)
	}
	suffix := uuid.NewString()
	processingContext := createAnnotationTestContext(t, database, suffix+"-quota-race")
	workspaceID, firstImageID := createAnnotationTestResource(
		t,
		database,
		suffix+"-quota-race",
		"https://source.example/canvas/quota/0/"+suffix,
	)
	imageIDs := createCanonicalImagesInWorkspace(t, database, workspaceID, firstImageID, imageCount)
	jobStore := store.NewTranscriptionJobStoreWithAdmission(database, policy)

	type result struct {
		index int
		id    uint64
		err   error
	}
	start := make(chan struct{})
	results := make(chan result, len(imageIDs))
	var workers sync.WaitGroup
	for index, imageID := range imageIDs {
		workers.Add(1)
		go func(index int, imageID uint64) {
			defer workers.Done()
			<-start
			id, createErr := jobStore.Create(context.Background(), imageID, processingContext)
			results <- result{index: index, id: id, err: createErr}
		}(index, imageID)
	}
	close(start)
	workers.Wait()
	close(results)

	created := make([]result, 0, quota)
	rejected := make([]result, 0, imageCount-quota)
	for admission := range results {
		if admission.err == nil {
			created = append(created, admission)
			continue
		}
		var quotaErr *store.TranscriptionJobQuotaExceededError
		if !errors.As(admission.err, &quotaErr) {
			t.Fatalf("Create image %d error = %v, want typed quota error", admission.index, admission.err)
		}
		if quotaErr.WorkspaceID != workspaceID || quotaErr.Limit != quota {
			t.Fatalf("quota error = %+v, want workspace %d limit %d", quotaErr, workspaceID, quota)
		}
		rejected = append(rejected, admission)
	}
	if len(created) != quota || len(rejected) != imageCount-quota {
		t.Fatalf("parallel admissions created/rejected = %d/%d, want %d/%d", len(created), len(rejected), quota, imageCount-quota)
	}
	assertActiveTranscriptionJobCount(t, database, workspaceID, quota)

	// Capacity is derived from pending/running state. A terminal transition
	// releases one slot without a separate counter update.
	if err := jobStore.Cancel(ctx, created[0].id); err != nil {
		t.Fatalf("cancel admitted job: %v", err)
	}
	replacementID, err := jobStore.Create(ctx, imageIDs[rejected[0].index], processingContext)
	if err != nil || replacementID == 0 {
		t.Fatalf("Create after terminal release = %d, %v", replacementID, err)
	}
	assertActiveTranscriptionJobCount(t, database, workspaceID, quota)
}

func TestReprocessQuotaRejectionRollsBackAllCanonicalSideEffects(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	policy, err := store.NewTranscriptionJobAdmissionPolicy(1)
	if err != nil {
		t.Fatalf("create admission policy: %v", err)
	}
	suffix := uuid.NewString()
	processingContext := createAnnotationTestContext(t, database, suffix+"-reprocess-quota")
	workspaceID, firstImageID := createAnnotationTestResource(
		t,
		database,
		suffix+"-reprocess-quota",
		"https://source.example/canvas/reprocess-quota/0/"+suffix,
	)
	imageIDs := createCanonicalImagesInWorkspace(t, database, workspaceID, firstImageID, 2)
	capacityImageID, reprocessImageID := imageIDs[0], imageIDs[1]
	jobStore := store.NewTranscriptionJobStoreWithAdmission(database, policy)
	annotationStore := store.NewAnnotationStoreWithTranscriptionAdmission(database, policy)
	ocrStore := store.NewOCRRunStore(database)

	if _, err := jobStore.Create(ctx, capacityImageID, processingContext); err != nil {
		t.Fatalf("fill workspace job capacity: %v", err)
	}
	base, err := annotationStore.LoadPage(ctx, workspaceID, reprocessImageID)
	if err != nil {
		t.Fatalf("load reprocess page: %v", err)
	}
	contextID := processingContext.ID
	originalSessionID := "quota-original-" + suffix
	if err := ocrStore.Create(ctx, store.OCRRun{
		SessionID:    originalSessionID,
		ItemImageID:  &reprocessImageID,
		ContextID:    &contextID,
		ImageURL:     "https://source.example/reprocess-quota.jpg",
		Provider:     "tesseract",
		Model:        "original-model",
		OriginalHOCR: "<html>original</html>",
		OriginalText: "original",
	}); err != nil {
		t.Fatalf("create original OCR baseline: %v", err)
	}
	operationKey := "reprocess-quota:" + suffix
	requestHash := fmt.Sprintf("%064x", 42)
	reservation, created, err := jobStore.ReserveExternalRequestForItemImage(
		ctx,
		workspaceID,
		reprocessImageID,
		"image-reprocess",
		operationKey,
		requestHash,
		"",
	)
	if err != nil || !created {
		t.Fatalf("reserve reprocess request = %+v, created %t, error %v", reservation, created, err)
	}

	base.Payload = replacePageText(t, base.Payload, "must not commit")
	newSessionID := "quota-rejected-" + suffix
	eventID := "quota-event-" + suffix
	_, err = annotationStore.SavePageAndStartReprocessing(ctx, base, base.Revision, store.AnnotationReprocessCommit{
		OCRRun: store.OCRRun{
			SessionID:    newSessionID,
			ItemImageID:  &reprocessImageID,
			ContextID:    &contextID,
			ImageURL:     "https://source.example/reprocess-quota.jpg",
			Provider:     "tesseract",
			Model:        "rejected-model",
			OriginalHOCR: "<html>rejected</html>",
			OriginalText: "must not commit",
		},
		Context:   processingContext,
		EventID:   eventID,
		EventType: "dev.scribe.annotations.reprocessed",
		Subject:   fmt.Sprintf("item-images/%d", reprocessImageID),
		BodyJSON:  fmt.Sprintf(`{"id":%q}`, eventID),
		ExternalRequest: &store.AnnotationReprocessExternalRequest{
			Source:         "image-reprocess",
			IdempotencyKey: operationKey,
			LeaseOwner:     reservation.LeaseOwner,
		},
	})
	var quotaErr *store.TranscriptionJobQuotaExceededError
	if !errors.As(err, &quotaErr) || quotaErr.WorkspaceID != workspaceID || quotaErr.Limit != 1 {
		t.Fatalf("reprocess error = %v (%+v), want workspace quota rejection", err, quotaErr)
	}

	unchanged, err := annotationStore.LoadPage(ctx, workspaceID, reprocessImageID)
	if err != nil {
		t.Fatalf("reload canonical page: %v", err)
	}
	if unchanged.Revision != base.Revision {
		t.Fatalf("canonical revision = %d, want unchanged %d", unchanged.Revision, base.Revision)
	}
	assertPageText(t, unchanged.Payload, fmt.Sprintf("segmented-%d", 1))
	if _, err := ocrStore.Get(ctx, newSessionID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("rejected OCR baseline lookup error = %v, want sql.ErrNoRows", err)
	}
	current, err := ocrStore.GetByItemImageID(ctx, reprocessImageID)
	if err != nil || current.SessionID != originalSessionID {
		t.Fatalf("current OCR baseline = %+v, %v; want %q", current, err, originalSessionID)
	}
	var jobCount, eventCount int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcription_jobs WHERE item_image_id = ?`, reprocessImageID).Scan(&jobCount); err != nil {
		t.Fatalf("count rejected jobs: %v", err)
	}
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_outbox WHERE event_id = ?`, eventID).Scan(&eventCount); err != nil {
		t.Fatalf("count rejected events: %v", err)
	}
	if jobCount != 0 || eventCount != 0 {
		t.Fatalf("rejected reprocess left jobs/events = %d/%d", jobCount, eventCount)
	}
	request, err := jobStore.GetExternalRequest(ctx, workspaceID, "image-reprocess", operationKey)
	if err != nil {
		t.Fatalf("reload operation reservation: %v", err)
	}
	if request.Status != store.ExternalRequestStatusInProgress || request.TranscriptionJobID != 0 || request.SessionID != "" {
		t.Fatalf("rejected reprocess mutated operation reservation: %+v", request)
	}
}

func createCanonicalImagesInWorkspace(
	t *testing.T,
	database *sql.DB,
	workspaceID, firstImageID uint64,
	count int,
) []uint64 {
	t.Helper()
	if count < 1 {
		t.Fatal("canonical image count must be positive")
	}
	var itemID string
	if err := database.QueryRow(`SELECT item_id FROM item_images WHERE id = ?`, firstImageID).Scan(&itemID); err != nil {
		t.Fatalf("load canonical test item: %v", err)
	}
	imageIDs := make([]uint64, 0, count)
	imageIDs = append(imageIDs, firstImageID)
	itemStore := store.NewItemStore(database)
	for sequence := 1; sequence < count; sequence++ {
		canvasURI := fmt.Sprintf("https://source.example/canvas/quota/%d/%s", sequence, uuid.NewString())
		image, err := itemStore.AddImage(context.Background(), db.CreateItemImageParams{
			ItemID: itemID, Sequence: uint32(sequence), // #nosec G115 -- bounded by the small test count.
			ImageURL:  fmt.Sprintf("https://source.example/image/%d.jpg", sequence),
			CanvasURI: canvasURI, Width: 10000, Height: 10000,
		})
		if err != nil {
			t.Fatalf("insert quota test image %d: %v", sequence, err)
		}
		imageIDs = append(imageIDs, image.ID)
	}
	annotationStore := store.NewAnnotationStore(database)
	for index, imageID := range imageIDs {
		var canvasURI string
		if err := database.QueryRow(`SELECT canvas_uri FROM item_images WHERE id = ?`, imageID).Scan(&canvasURI); err != nil {
			t.Fatalf("load quota test canvas %d: %v", index, err)
		}
		if _, err := annotationStore.SavePage(
			context.Background(),
			canonicalTestPage(t, workspaceID, imageID, canvasURI, fmt.Sprintf("segmented-%d", index)),
			0,
		); err != nil {
			t.Fatalf("initialize quota test page %d: %v", index, err)
		}
	}
	return imageIDs
}

func assertActiveTranscriptionJobCount(t *testing.T, database *sql.DB, workspaceID uint64, want int) {
	t.Helper()
	var count int
	if err := database.QueryRow(`
		SELECT COUNT(*)
		FROM transcription_jobs tj
		JOIN item_images ii ON ii.id = tj.item_image_id
		JOIN items i ON i.id = ii.item_id
		WHERE i.workspace_id = ? AND tj.status IN ('pending', 'running')
	`, workspaceID).Scan(&count); err != nil {
		t.Fatalf("count active transcription jobs: %v", err)
	}
	if count != want {
		t.Fatalf("active transcription job count = %d, want %d", count, want)
	}
}
