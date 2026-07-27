package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/lehigh-university-libraries/scribe/internal/database"
	dbgen "github.com/lehigh-university-libraries/scribe/internal/db"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

func TestAnnotationPagesAreWorkspaceIsolatedAndRevisioned(t *testing.T) {
	db := annotationTestDB(t)
	ctx := context.Background()
	storeUnderTest := store.NewAnnotationStore(db)
	suffix := uuid.NewString()
	canvasURI := "https://source.example/canvas/shared"
	processingContext := createAnnotationTestContext(t, db, suffix+"-revision")

	workspaceA, imageA := createAnnotationTestResource(t, db, suffix+"-a", canvasURI)
	workspaceB, imageB := createAnnotationTestResource(t, db, suffix+"-b", canvasURI)

	pageA := canonicalTestPage(t, workspaceA, imageA, canvasURI, "alpha")
	pageB := canonicalTestPage(t, workspaceB, imageB, canvasURI, "beta")
	savedA, err := storeUnderTest.SavePage(ctx, pageA, 0)
	if err != nil {
		t.Fatalf("SavePage(workspace A): %v", err)
	}
	savedB, err := storeUnderTest.SavePage(ctx, pageB, 0)
	if err != nil {
		t.Fatalf("SavePage(workspace B): %v", err)
	}
	if savedA.Revision != 1 || savedB.Revision != 1 {
		t.Fatalf("initial revisions = %d/%d, want 1/1", savedA.Revision, savedB.Revision)
	}

	loadedA, err := storeUnderTest.LoadPage(ctx, workspaceA, imageA)
	if err != nil {
		t.Fatalf("LoadPage(workspace A): %v", err)
	}
	loadedB, err := storeUnderTest.LoadPage(ctx, workspaceB, imageB)
	if err != nil {
		t.Fatalf("LoadPage(workspace B): %v", err)
	}
	assertPageText(t, loadedA.Payload, "alpha")
	assertPageText(t, loadedB.Payload, "beta")

	pageA.Payload = replacePageText(t, pageA.Payload, "corrected alpha")
	savedA, err = storeUnderTest.SavePage(ctx, pageA, savedA.Revision)
	if err != nil {
		t.Fatalf("SavePage corrected A: %v", err)
	}
	if savedA.Revision != 2 {
		t.Fatalf("corrected revision = %d, want 2", savedA.Revision)
	}
	if _, err := storeUnderTest.SavePage(ctx, pageA, 1); !errors.Is(err, store.ErrAnnotationRevisionConflict) {
		t.Fatalf("stale SavePage error = %v, want ErrAnnotationRevisionConflict", err)
	}

	loadedB, err = storeUnderTest.LoadPage(ctx, workspaceB, imageB)
	if err != nil {
		t.Fatalf("reload workspace B: %v", err)
	}
	assertPageText(t, loadedB.Payload, "beta")
	indexA, err := storeUnderTest.SearchIndex(ctx, workspaceA, imageA)
	if err != nil {
		t.Fatalf("SearchIndex(workspace A): %v", err)
	}
	if len(indexA) != 1 || indexA[0].TextGranularity != "line" {
		t.Fatalf("derived index = %+v", indexA)
	}

	jobStore := store.NewTranscriptionJobStore(db)
	jobID, err := jobStore.Create(ctx, imageA, processingContext)
	if err != nil {
		t.Fatalf("Create transcription job: %v", err)
	}
	duplicateJobID, err := jobStore.Create(ctx, imageA, processingContext)
	if err != nil {
		t.Fatalf("Create duplicate transcription job: %v", err)
	}
	if duplicateJobID != jobID {
		t.Fatalf("duplicate job id = %d, want existing %d", duplicateJobID, jobID)
	}
	activeJob, err := jobStore.GetActiveByItemImage(ctx, imageA)
	if err != nil || activeJob.ID != jobID {
		t.Fatalf("GetActiveByItemImage = %+v, %v", activeJob, err)
	}
}

func TestWorkspaceImageOwnershipIsEnforcedAtEveryDurableInsertBoundary(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	workspaceA, _ := createAnnotationTestResource(t, database, suffix+"-ownership-a", "https://source.example/canvas/ownership-a")
	workspaceB, imageB := createAnnotationTestResource(t, database, suffix+"-ownership-b", "https://source.example/canvas/ownership-b")
	queries := dbgen.New(database)

	// Canonical page creation is an INSERT ... SELECT ownership guard. The
	// application-level preflight and the write run in one transaction, while
	// this direct adapter assertion ensures a future caller cannot bypass it.
	foreignPageID := fmt.Sprintf("https://scribe.example/v1/item-images/%d/annotations", imageB)
	err := queries.CreateAnnotationPage(ctx, dbgen.AnnotationPage{
		WorkspaceID: workspaceA,
		ItemImageID: imageB,
		PageID:      foreignPageID,
		CanvasUri:   "https://source.example/canvas/foreign",
		Payload:     `{\"id\":\"` + foreignPageID + `\",\"type\":\"AnnotationPage\",\"items\":[]}`,
	})
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-workspace CreateAnnotationPage error = %v, want sql.ErrNoRows", err)
	}

	// Relationship enforcement belongs to repository boundaries. A separate
	// integrity-audit acceptance test deliberately injects raw SQL drift and
	// verifies that restore/release checks detect it.
	if _, err := queries.CreateTranscriptionJob(ctx, dbgen.CreateTranscriptionJobParams{
		ItemImageID: imageB, ContextSnapshot: json.RawMessage(`{}`),
	}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("job from mismatched canonical page error = %v, want sql.ErrNoRows", err)
	}

	// Metadata audit creation verifies the optional image in the same quota
	// transaction and the INSERT repeats that ownership predicate.
	err = store.NewProviderCallAuditStore(database).Create(ctx, store.ProviderCallAudit{
		WorkspaceID: workspaceA, ItemImageID: &imageB,
		Provider: "tesseract", Model: "tesseract", Operation: "transcribe",
	})
	if err == nil || !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-workspace provider audit error = %v", err)
	}

	var pageCount, jobCount, auditCount int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM annotation_pages WHERE workspace_id = ? AND item_image_id = ?`, workspaceA, imageB).Scan(&pageCount); err != nil {
		t.Fatalf("count seeded mismatched pages: %v", err)
	}
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcription_jobs WHERE item_image_id = ?`, imageB).Scan(&jobCount); err != nil {
		t.Fatalf("count rejected jobs: %v", err)
	}
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM provider_call_audits WHERE workspace_id = ? AND item_image_id = ?`, workspaceA, imageB).Scan(&auditCount); err != nil {
		t.Fatalf("count rejected audits: %v", err)
	}
	if pageCount != 0 || jobCount != 0 || auditCount != 0 {
		t.Fatalf("ownership guard state = pages %d, jobs %d, audits %d; want (0,0,0), image workspace=%d", pageCount, jobCount, auditCount, workspaceB)
	}
}

func TestAnnotationAdmissionIsAtomicAndBulkIndexIsChunked(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	canvasURI := "https://source.example/canvas/admission-" + suffix
	workspaceID, imageID := createAnnotationTestResource(t, database, suffix+"-admission", canvasURI)
	annotationStore := store.NewAnnotationStore(database)

	initial, err := annotationStore.SavePage(ctx, canonicalTestPage(t, workspaceID, imageID, canvasURI, "original"), 0)
	if err != nil {
		t.Fatalf("SavePage initial: %v", err)
	}
	var oversized map[string]any
	if err := json.Unmarshal([]byte(initial.Payload), &oversized); err != nil {
		t.Fatalf("decode initial page: %v", err)
	}
	oversized["admissionPadding"] = strings.Repeat("x", iiif.MaxAnnotationPageBytes)
	oversizedJSON, err := json.Marshal(oversized)
	if err != nil {
		t.Fatal(err)
	}
	invalid := initial
	invalid.Payload = string(oversizedJSON)
	if _, err := annotationStore.SavePage(ctx, invalid, initial.Revision); err == nil ||
		!strings.Contains(err.Error(), fmt.Sprintf("exceeds %d bytes", iiif.MaxAnnotationPageBytes)) {
		t.Fatalf("oversized SavePage error = %v", err)
	}
	reloaded, err := annotationStore.LoadPage(ctx, workspaceID, imageID)
	if err != nil {
		t.Fatalf("LoadPage after rejection: %v", err)
	}
	index, err := annotationStore.SearchIndex(ctx, workspaceID, imageID)
	if err != nil {
		t.Fatalf("SearchIndex after rejection: %v", err)
	}
	if reloaded.Revision != initial.Revision || reloaded.Payload != initial.Payload || len(index) != 1 {
		t.Fatalf("rejected page mutated canonical state: revision=%d index=%d", reloaded.Revision, len(index))
	}

	const annotationCount = 501
	items := make([]any, 0, annotationCount)
	for position := 0; position < annotationCount; position++ {
		annotationID, idErr := iiif.AnnotationID(initial.PageID, fmt.Sprintf("bulk-%d", position))
		if idErr != nil {
			t.Fatal(idErr)
		}
		items = append(items, map[string]any{
			"id":              annotationID,
			"type":            "Annotation",
			"motivation":      "supplementing",
			"textGranularity": "word",
			"body": map[string]any{
				"type": "TextualBody", "purpose": "supplementing", "value": fmt.Sprintf("word-%d", position),
			},
			"target": canvasURI + fmt.Sprintf("#xywh=%d,1,1,1", position+1),
		})
	}
	payload, err := iiif.NewAnnotationPage(iiif.PageIdentity{
		PublicBaseURL: "https://scribe.example",
		ItemImageID:   imageID,
		CanvasURI:     canvasURI,
	}, items)
	if err != nil {
		t.Fatalf("NewAnnotationPage bulk: %v", err)
	}
	bulk := initial
	bulk.Payload = string(payload)
	saved, err := annotationStore.SavePage(ctx, bulk, initial.Revision)
	if err != nil {
		t.Fatalf("SavePage bulk: %v", err)
	}
	index, err = annotationStore.SearchIndex(ctx, workspaceID, imageID)
	if err != nil {
		t.Fatalf("SearchIndex bulk: %v", err)
	}
	if saved.Revision != initial.Revision+1 || len(index) != annotationCount || index[500].Position != 500 {
		t.Fatalf("bulk save revision/index = %d/%d last=%+v", saved.Revision, len(index), index[len(index)-1])
	}
}

func TestAnnotationStoreRejectsNonCanonicalResourceOwnership(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	canvasURI := "https://source.example/canvas/canonical-identity-" + suffix
	workspaceID, imageID := createAnnotationTestResource(t, database, suffix+"-canonical-identity", canvasURI)
	annotationStore := store.NewAnnotationStore(database)
	initial, err := annotationStore.SavePage(ctx, canonicalTestPage(t, workspaceID, imageID, canvasURI, "canonical"), 0)
	if err != nil {
		t.Fatalf("SavePage initial: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*store.AnnotationPage, map[string]any)
	}{
		{
			name: "page id encodes another item image",
			mutate: func(page *store.AnnotationPage, document map[string]any) {
				foreignPageID, idErr := iiif.CanonicalPageID("https://scribe.example", imageID+1)
				if idErr != nil {
					t.Fatal(idErr)
				}
				page.PageID = foreignPageID
				document["id"] = foreignPageID
			},
		},
		{
			name: "foreign child id",
			mutate: func(_ *store.AnnotationPage, document map[string]any) {
				items := document["items"].([]any)
				items[0].(map[string]any)["id"] = "https://foreign.example/v1/item-images/1/annotations/items/0123456789abcdef0123456789abcdef"
			},
		},
		{
			name: "noncanonical child id",
			mutate: func(_ *store.AnnotationPage, document map[string]any) {
				items := document["items"].([]any)
				items[0].(map[string]any)["id"] = initial.PageID + "/items/not-a-32-lowerhex-resource-id"
			},
		},
		{
			name: "duplicate child ids",
			mutate: func(_ *store.AnnotationPage, document map[string]any) {
				items := document["items"].([]any)
				duplicateJSON, marshalErr := json.Marshal(items[0])
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				var duplicate map[string]any
				if unmarshalErr := json.Unmarshal(duplicateJSON, &duplicate); unmarshalErr != nil {
					t.Fatal(unmarshalErr)
				}
				document["items"] = append(items, duplicate)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invalid := initial
			var document map[string]any
			if err := json.Unmarshal([]byte(initial.Payload), &document); err != nil {
				t.Fatalf("decode initial payload: %v", err)
			}
			test.mutate(&invalid, document)
			payload, err := json.Marshal(document)
			if err != nil {
				t.Fatalf("encode invalid payload: %v", err)
			}
			invalid.Payload = string(payload)
			if _, err := annotationStore.SavePage(ctx, invalid, initial.Revision); err == nil {
				t.Fatal("SavePage unexpectedly accepted a noncanonical resource")
			}
			reloaded, err := annotationStore.LoadPage(ctx, workspaceID, imageID)
			if err != nil {
				t.Fatalf("LoadPage after rejection: %v", err)
			}
			if reloaded.Revision != initial.Revision || reloaded.Payload != initial.Payload {
				t.Fatalf("rejected page mutated canonical state: revision=%d", reloaded.Revision)
			}
		})
	}
}

func TestItemImageDeleteIsWorkspaceScopedAndCascadesCanonicalPage(t *testing.T) {
	db := annotationTestDB(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	sharedImageURL := "https://source.example/shared-image.jpg"
	canvasURI := "https://source.example/canvas/delete-test"

	workspaceA, imageA := createAnnotationTestResource(t, db, suffix+"-delete-a", canvasURI)
	workspaceB, imageB := createAnnotationTestResource(t, db, suffix+"-delete-b", canvasURI)
	if _, err := db.Exec(`UPDATE item_images SET image_url = ? WHERE id IN (?, ?)`, sharedImageURL, imageA, imageB); err != nil {
		t.Fatalf("share test image URL: %v", err)
	}

	itemStore := store.NewItemStore(db)
	annotationStore := store.NewAnnotationStore(db)
	if _, err := annotationStore.SavePage(ctx, canonicalTestPage(t, workspaceA, imageA, canvasURI, "alpha"), 0); err != nil {
		t.Fatalf("SavePage: %v", err)
	}
	if count, err := itemStore.ImageURLReferenceCount(ctx, sharedImageURL); err != nil || count != 2 {
		t.Fatalf("ImageURLReferenceCount before delete = %d, %v; want 2", count, err)
	}

	if err := itemStore.DeleteItemImageForWorkspace(ctx, imageA, workspaceB); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-workspace delete error = %v, want sql.ErrNoRows", err)
	}
	if _, err := itemStore.GetImageForWorkspace(ctx, imageA, workspaceA); err != nil {
		t.Fatalf("cross-workspace delete removed image: %v", err)
	}

	if err := itemStore.DeleteItemImageForWorkspace(ctx, imageA, workspaceA); err != nil {
		t.Fatalf("DeleteItemImageForWorkspace: %v", err)
	}
	if count, err := itemStore.ImageURLReferenceCount(ctx, sharedImageURL); err != nil || count != 1 {
		t.Fatalf("ImageURLReferenceCount after delete = %d, %v; want 1", count, err)
	}
	if _, err := annotationStore.LoadPage(ctx, workspaceA, imageA); !errors.Is(err, store.ErrAnnotationPageNotFound) {
		t.Fatalf("LoadPage after image delete error = %v, want ErrAnnotationPageNotFound", err)
	}
}

func TestTranscriptionCommitAtomicallyFencesAndCompletesJob(t *testing.T) {
	db := annotationTestDB(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	canvasURI := "https://source.example/canvas/job-commit"
	processingContext := createAnnotationTestContext(t, db, suffix+"-job-commit")
	workspaceID, imageID := createAnnotationTestResource(t, db, suffix+"-job-commit", canvasURI)
	createWebhookTestSubscription(t, db, workspaceID)
	annotationStore := store.NewAnnotationStore(db)
	jobStore := store.NewTranscriptionJobStore(db)

	page, err := annotationStore.SavePage(ctx, canonicalTestPage(t, workspaceID, imageID, canvasURI, "original"), 0)
	if err != nil {
		t.Fatalf("SavePage: %v", err)
	}
	jobID, err := jobStore.Create(ctx, imageID, processingContext)
	if err != nil {
		t.Fatalf("Create transcription job: %v", err)
	}
	claimed, err := jobStore.ClaimPendingByID(ctx, jobID)
	if err != nil || claimed == nil {
		t.Fatalf("ClaimPendingByID = %+v, %v", claimed, err)
	}
	fence, err := claimed.Fence()
	if err != nil {
		t.Fatalf("claimed fence: %v", err)
	}

	eventID := "evt-" + suffix
	page.Payload = replacePageText(t, page.Payload, "transcribed")
	saved, err := annotationStore.SavePageAndCompleteTranscriptionJob(ctx, page, page.Revision, store.AnnotationJobCompletion{
		TranscriptionAttemptFence: fence,
		EventID:                   eventID,
		EventType:                 "dev.scribe.transcription.completed",
		Subject:                   fmt.Sprintf("/item-images/%d", imageID),
		BodyJSON:                  fmt.Sprintf(`{"id":%q}`, eventID),
	})
	if err != nil {
		t.Fatalf("SavePageAndCompleteTranscriptionJob: %v", err)
	}
	if saved.Revision != 2 {
		t.Fatalf("saved revision = %d, want 2", saved.Revision)
	}
	completed, err := jobStore.Get(ctx, jobID)
	if err != nil || completed.Status != store.TranscriptionJobStatusCompleted {
		t.Fatalf("completed job = %+v, %v", completed, err)
	}
	var eventCount, deliveryCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM event_outbox WHERE event_id = ?`, eventID).Scan(&eventCount); err != nil {
		t.Fatalf("count event outbox: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM webhook_deliveries WHERE event_id = ?`, eventID).Scan(&deliveryCount); err != nil {
		t.Fatalf("count webhook deliveries: %v", err)
	}
	if eventCount != 1 || deliveryCount != 1 {
		t.Fatalf("completion records = event:%d delivery:%d, want 1/1", eventCount, deliveryCount)
	}
	var mirrorCount int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM annotation_mirror_outbox WHERE item_image_id = ?`,
		imageID,
	).Scan(&mirrorCount); err != nil {
		t.Fatalf("count annotation mirror outbox: %v", err)
	}
	if mirrorCount != 0 {
		t.Fatalf("draft transcription queued %d public mirror rows, want 0", mirrorCount)
	}

	staleJobID, err := jobStore.Create(ctx, imageID, processingContext)
	if err != nil {
		t.Fatalf("Create stale transcription job: %v", err)
	}
	staleClaim, err := jobStore.ClaimPendingByID(ctx, staleJobID)
	if err != nil || staleClaim == nil {
		t.Fatalf("claim stale job = %+v, %v", staleClaim, err)
	}
	staleFence, err := staleClaim.Fence()
	if err != nil {
		t.Fatalf("stale claimed fence: %v", err)
	}
	if err := jobStore.Cancel(ctx, staleJobID); err != nil {
		t.Fatalf("Cancel stale job: %v", err)
	}
	staleEventID := "evt-stale-" + suffix
	saved.Payload = replacePageText(t, saved.Payload, "must not commit")
	_, err = annotationStore.SavePageAndCompleteTranscriptionJob(ctx, saved, saved.Revision, store.AnnotationJobCompletion{
		TranscriptionAttemptFence: staleFence,
		EventID:                   staleEventID,
		EventType:                 "dev.scribe.transcription.completed",
		Subject:                   fmt.Sprintf("/item-images/%d", imageID),
		BodyJSON:                  fmt.Sprintf(`{"id":%q}`, staleEventID),
	})
	if !errors.Is(err, store.ErrAnnotationJobFence) {
		t.Fatalf("stale worker commit error = %v, want ErrAnnotationJobFence", err)
	}
	reloaded, err := annotationStore.LoadPage(ctx, workspaceID, imageID)
	if err != nil {
		t.Fatalf("LoadPage after stale commit: %v", err)
	}
	assertPageText(t, reloaded.Payload, "transcribed")
	if err := db.QueryRow(`SELECT COUNT(*) FROM event_outbox WHERE event_id = ?`, staleEventID).Scan(&eventCount); err != nil {
		t.Fatalf("count stale event outbox: %v", err)
	}
	if eventCount != 0 {
		t.Fatalf("stale event count = %d, want 0", eventCount)
	}
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM annotation_mirror_outbox WHERE item_image_id = ?`,
		imageID,
	).Scan(&mirrorCount); err != nil {
		t.Fatalf("recount annotation mirror outbox: %v", err)
	}
	if mirrorCount != 0 {
		t.Fatalf("stale draft commit queued %d public mirror rows, want 0", mirrorCount)
	}
}

func TestTranscriptionCompletionQuotaAtomicallyIncludesOCRProvenance(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	canvasURI := "https://source.example/canvas/quota-provenance-" + suffix
	processingContext := createAnnotationTestContext(t, database, suffix+"-quota-provenance")
	workspaceID, imageID := createAnnotationTestResource(t, database, suffix+"-quota-provenance", canvasURI)
	annotations := store.NewAnnotationStore(database)
	jobs := store.NewTranscriptionJobStore(database)
	items := store.NewItemStore(database)
	page, err := annotations.SavePage(ctx, canonicalTestPage(t, workspaceID, imageID, canvasURI, "original"), 0)
	if err != nil {
		t.Fatalf("SavePage: %v", err)
	}
	jobID, err := jobs.Create(ctx, imageID, processingContext)
	if err != nil {
		t.Fatalf("Create transcription job: %v", err)
	}
	claimed, err := jobs.ClaimPendingByID(ctx, jobID)
	if err != nil || claimed == nil {
		t.Fatalf("ClaimPendingByID = %+v/%v", claimed, err)
	}
	fence, err := claimed.Fence()
	if err != nil {
		t.Fatalf("Fence: %v", err)
	}
	workspaceUsage, err := items.GetStorageQuotaUsage(ctx, workspaceID)
	if err != nil {
		t.Fatalf("workspace usage: %v", err)
	}
	globalUsage, err := items.GetStorageQuotaUsage(ctx, 0)
	if err != nil {
		t.Fatalf("global usage: %v", err)
	}
	limits := storageQuotaTestLimits()
	limits.MaxBytesPerWorkspace = workspaceUsage.UploadBlobBytes + workspaceUsage.DatabaseBytes + 1
	limits.MaxBytesTotal = globalUsage.UploadBlobBytes + globalUsage.DatabaseBytes + 1
	if err := annotations.SetStorageQuotaLimits(limits); err != nil {
		t.Fatalf("SetStorageQuotaLimits: %v", err)
	}
	page.Payload = replacePageText(t, page.Payload, "corrected")
	contextID := processingContext.ID
	provenance := &store.OCRRun{
		SessionID: "quota-provenance-" + suffix, ItemImageID: &imageID, ContextID: &contextID,
		ImageURL: "https://source.example/image.jpg", Provider: "test", Model: "test",
		OriginalHOCR: strings.Repeat("x", 64<<10), OriginalText: "corrected",
	}
	_, err = annotations.SavePageAndCompleteTranscriptionJob(ctx, page, page.Revision, store.AnnotationJobCompletion{
		TranscriptionAttemptFence: fence, OCRRun: provenance,
	})
	if !errors.Is(err, store.ErrStorageQuotaExceeded) {
		t.Fatalf("quota-constrained completion error = %v, want ErrStorageQuotaExceeded", err)
	}
	reloaded, err := annotations.LoadPage(ctx, workspaceID, imageID)
	if err != nil || reloaded.Revision != page.Revision {
		t.Fatalf("page after rejected completion = %+v/%v", reloaded, err)
	}
	job, err := jobs.Get(ctx, jobID)
	if err != nil || job.Status != store.TranscriptionJobStatusRunning {
		t.Fatalf("job after rejected completion = %+v/%v", job, err)
	}
	if _, err := store.NewOCRRunStore(database).GetByItemImageID(ctx, imageID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("OCR provenance after rejected completion error = %v, want sql.ErrNoRows", err)
	}

	limits.MaxBytesPerWorkspace += 1 << 20
	limits.MaxBytesTotal += 1 << 20
	if err := annotations.SetStorageQuotaLimits(limits); err != nil {
		t.Fatalf("raise storage limits: %v", err)
	}
	saved, err := annotations.SavePageAndCompleteTranscriptionJob(ctx, page, page.Revision, store.AnnotationJobCompletion{
		TranscriptionAttemptFence: fence, OCRRun: provenance,
	})
	if err != nil || saved.Revision != page.Revision+1 {
		t.Fatalf("completion with quota headroom = %+v/%v", saved, err)
	}
	if run, err := store.NewOCRRunStore(database).GetByItemImageID(ctx, imageID); err != nil || run.SessionID != provenance.SessionID {
		t.Fatalf("committed OCR provenance = %+v/%v", run, err)
	}
}

func TestWebhookClaimsCountAttemptsAndExhaustCrashedLeases(t *testing.T) {
	db := annotationTestDB(t)
	ctx := context.Background()
	jobStore := store.NewTranscriptionJobStore(db)
	eventID := "webhook-lease-" + uuid.NewString()
	workspaceID, imageID := createAnnotationTestResource(t, db, uuid.NewString()+"-webhook-lease", "https://source.example/canvas/"+uuid.NewString())
	createWebhookTestSubscription(t, db, workspaceID)
	if err := jobStore.EnqueueWebhookEvent(
		ctx,
		eventID,
		"dev.scribe.annotation.updated",
		fmt.Sprintf("item-images/%d", imageID),
		fmt.Sprintf(`{"id":%q}`, eventID),
	); err != nil {
		t.Fatalf("EnqueueWebhookEvent: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM webhook_deliveries WHERE event_id = ?`, eventID)
		_, _ = db.Exec(`DELETE FROM event_outbox WHERE event_id = ?`, eventID)
	})

	deliveries, err := jobStore.ClaimWebhookDeliveries(ctx, 1)
	if err != nil || len(deliveries) != 1 {
		t.Fatalf("ClaimWebhookDeliveries = %+v, %v; want one delivery", deliveries, err)
	}
	if deliveries[0].AttemptCount != 1 {
		t.Fatalf("claimed attempt_count = %d, want 1", deliveries[0].AttemptCount)
	}
	var persistedAttempts int
	if err := db.QueryRow(
		`SELECT attempt_count FROM webhook_deliveries WHERE id = ?`,
		deliveries[0].ID,
	).Scan(&persistedAttempts); err != nil {
		t.Fatalf("load claimed attempt count: %v", err)
	}
	if persistedAttempts != 1 {
		t.Fatalf("persisted attempt_count = %d, want 1", persistedAttempts)
	}

	if _, err := db.Exec(
		`UPDATE webhook_deliveries SET attempt_count = max_attempts, lease_until = DATE_SUB(NOW(), INTERVAL 1 SECOND) WHERE id = ?`,
		deliveries[0].ID,
	); err != nil {
		t.Fatalf("expire exhausted webhook lease: %v", err)
	}
	deliveries, err = jobStore.ClaimWebhookDeliveries(ctx, 1)
	if err != nil {
		t.Fatalf("reclaim exhausted webhook delivery: %v", err)
	}
	if len(deliveries) != 0 {
		t.Fatalf("exhausted webhook delivery was reclaimed: %+v", deliveries)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM webhook_deliveries WHERE id = ?`, deliveriesID(t, db, eventID)).Scan(&status); err != nil {
		t.Fatalf("load exhausted webhook status: %v", err)
	}
	if status != "failed" {
		t.Fatalf("exhausted webhook status = %q, want failed", status)
	}
}

func TestWebhookFailurePersistsOnlyBoundedCategory(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	jobStore := store.NewTranscriptionJobStore(database)
	eventID := "webhook-redaction-" + uuid.NewString()
	workspaceID, imageID := createAnnotationTestResource(t, database, uuid.NewString()+"-webhook-redaction", "https://source.example/canvas/"+uuid.NewString())
	createWebhookTestSubscription(t, database, workspaceID)
	if err := jobStore.EnqueueWebhookEvent(
		ctx,
		eventID,
		"dev.scribe.annotation.updated",
		fmt.Sprintf("item-images/%d", imageID),
		fmt.Sprintf(`{"id":%q}`, eventID),
	); err != nil {
		t.Fatalf("EnqueueWebhookEvent: %v", err)
	}
	t.Cleanup(func() {
		_, _ = database.Exec(`DELETE FROM webhook_deliveries WHERE event_id = ?`, eventID)
		_, _ = database.Exec(`DELETE FROM event_outbox WHERE event_id = ?`, eventID)
	})
	deliveries, err := jobStore.ClaimWebhookDeliveries(ctx, 1)
	if err != nil || len(deliveries) != 1 {
		t.Fatalf("ClaimWebhookDeliveries = %+v/%v", deliveries, err)
	}

	untrustedFailure := strings.Repeat(
		"webhook returned 403 forbidden; "+
			"url=https://user:TOKEN-SENTINEL@hook.example/path?api_key=QUERY-SENTINEL; "+
			"response_body=BODY-SENTINEL; SQL=SELECT * FROM secrets; Vault=VAULT-SENTINEL; ",
		32,
	)
	if err := jobStore.MarkWebhookDeliveryFailed(ctx, deliveries[0].ID, deliveries[0].LeaseOwner, untrustedFailure); err != nil {
		t.Fatalf("MarkWebhookDeliveryFailed: %v", err)
	}
	var lastError string
	if err := database.QueryRow(`SELECT last_error FROM webhook_deliveries WHERE id = ?`, deliveries[0].ID).Scan(&lastError); err != nil {
		t.Fatalf("load webhook failure: %v", err)
	}
	assertBoundedFailureCategory(t, lastError, "webhook request authorization failed")
}

func assertBoundedFailureCategory(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("durable failure category = %q, want %q", got, want)
	}
	if len(got) > 128 {
		t.Fatalf("durable failure category length = %d, want at most 128", len(got))
	}
	lower := strings.ToLower(got)
	for _, sentinel := range []string{
		"token-sentinel",
		"api_key",
		"query-sentinel",
		"body-sentinel",
		"select * from secrets",
		"vault-sentinel",
		"triplet.example",
		"hook.example",
	} {
		if strings.Contains(lower, sentinel) {
			t.Errorf("durable failure contains untrusted sentinel %q: %q", sentinel, got)
		}
	}
}

func deliveriesID(t *testing.T, db *sql.DB, eventID string) uint64 {
	t.Helper()
	var id uint64
	if err := db.QueryRow(`SELECT id FROM webhook_deliveries WHERE event_id = ?`, eventID).Scan(&id); err != nil {
		t.Fatalf("load webhook delivery id: %v", err)
	}
	return id
}

func TestTranscriptionJobRequiresCanonicalInputRevision(t *testing.T) {
	db := annotationTestDB(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	processingContext := createAnnotationTestContext(t, db, suffix+"-initial-bind")
	canvasURI := "https://source.example/canvas/initial-bind"
	workspaceID, imageID := createAnnotationTestResource(t, db, suffix+"-initial-bind", canvasURI)
	jobStore := store.NewTranscriptionJobStore(db)

	if _, err := jobStore.Create(ctx, imageID, processingContext); !errors.Is(err, store.ErrTranscriptionCanonicalPageRequired) {
		t.Fatalf("Create without canonical page error = %v, want ErrTranscriptionCanonicalPageRequired", err)
	}
	annotationStore := store.NewAnnotationStore(db)
	saved, err := annotationStore.SavePage(ctx, canonicalTestPage(t, workspaceID, imageID, canvasURI, "segmented"), 0)
	if err != nil {
		t.Fatalf("SavePage: %v", err)
	}
	jobID, err := jobStore.Create(ctx, imageID, processingContext)
	if err != nil {
		t.Fatalf("Create transcription job: %v", err)
	}
	job, err := jobStore.Get(ctx, jobID)
	if err != nil || job.InputRevision != saved.Revision {
		t.Fatalf("job input revision = %d, %v; want %d", job.InputRevision, err, saved.Revision)
	}
}

func annotationTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DSN")
	if dsn == "" {
		t.Skip("TEST_DSN not set; skipping annotation repository integration test")
	}
	db, err := database.NewPool(dsn, database.DefaultConfig())
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := database.Migrate(db); err != nil {
		_ = db.Close()
		t.Fatalf("migrate database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func createAnnotationTestResource(t *testing.T, db *sql.DB, suffix, canvasURI string) (uint64, uint64) {
	t.Helper()
	ctx := context.Background()
	result, err := db.Exec(`INSERT INTO users (name) VALUES (?)`, "annotation-test-"+suffix)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	userID, _ := result.LastInsertId()
	result, err = db.Exec(
		`INSERT INTO workspaces (owner_user_id, name, slug, is_personal, created_by_user_id) VALUES (?, ?, ?, TRUE, ?)`,
		userID, "annotation-test-"+suffix, "annotation-test-"+suffix, userID,
	)
	if err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	workspaceID, _ := result.LastInsertId()
	if _, err := db.Exec(`INSERT INTO storage_quota_usage (workspace_id) VALUES (?)`, workspaceID); err != nil {
		t.Fatalf("insert workspace quota row: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspace_members (workspace_id, user_id, role) VALUES (?, ?, 'admin')`, workspaceID, userID); err != nil {
		t.Fatalf("insert workspace membership: %v", err)
	}
	itemID := "ann-" + suffix
	if len(itemID) > 64 {
		itemID = itemID[:64]
	}
	itemStore := store.NewItemStore(db)
	if _, err := itemStore.Create(ctx, dbgen.CreateItemParams{
		ID:          itemID,
		UserID:      uint64(userID), // #nosec G115 -- positive auto-increment test identifier.
		WorkspaceID: uint64(workspaceID),
		Name:        "annotation-test",
		SourceType:  "manifest",
		SourceURL:   "https://source.example/manifest/" + suffix,
	}); err != nil {
		t.Fatalf("create item: %v", err)
	}
	image, err := itemStore.AddImage(ctx, dbgen.CreateItemImageParams{
		ItemID:    itemID,
		Sequence:  0,
		ImageURL:  "https://source.example/image/" + suffix + ".jpg",
		CanvasURI: canvasURI,
		Width:     10000,
		Height:    10000,
	})
	if err != nil {
		t.Fatalf("create item image: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_ = itemStore.DeleteForWorkspace(cleanupCtx, itemID, uint64(workspaceID))
		_, _ = db.Exec(
			`DELETE FROM resource_cleanup_outbox WHERE kind = 'triplet_presentation_image' AND resource_key = ?`,
			fmt.Sprint(image.ID),
		)
		contextRows, _ := db.Query(`SELECT id FROM contexts WHERE workspace_id = ? ORDER BY id`, workspaceID)
		if contextRows != nil {
			var contextIDs []uint64
			for contextRows.Next() {
				var contextID uint64
				if contextRows.Scan(&contextID) == nil {
					contextIDs = append(contextIDs, contextID)
				}
			}
			_ = contextRows.Close()
			contextStore := store.NewContextStore(db)
			for _, contextID := range contextIDs {
				_ = contextStore.DeleteForWorkspace(cleanupCtx, contextID, uint64(workspaceID))
			}
		}
		_, _ = db.Exec(`DELETE wd FROM webhook_deliveries wd JOIN event_outbox eo ON eo.event_id = wd.event_id WHERE eo.workspace_id = ?`, workspaceID)
		for _, table := range []string{"event_outbox", "provider_call_audits", "provider_secrets", "api_keys", "external_requests", "workspace_storage_reservations", "resource_cleanup_outbox"} {
			_, _ = db.Exec(`DELETE FROM `+table+` WHERE workspace_id = ?`, workspaceID) // #nosec G202 -- table names are a closed test-only constant list.
		}
		if err := itemStore.RebuildStorageQuotaUsage(cleanupCtx); err != nil {
			t.Errorf("rebuild storage quota usage before annotation fixture owner cleanup: %v", err)
		}
		_, _ = db.Exec(`DELETE FROM workspace_members WHERE workspace_id = ?`, workspaceID)
		_, _ = db.Exec(`DELETE FROM workspaces WHERE id = ?`, workspaceID)
		_, _ = db.Exec(`DELETE FROM auth_sessions WHERE user_id = ?`, userID)
		_, _ = db.Exec(`DELETE FROM users WHERE id = ?`, userID)
		if err := itemStore.RebuildStorageQuotaUsage(cleanupCtx); err != nil {
			t.Errorf("rebuild storage quota usage after fixture cleanup: %v", err)
		}
	})
	return uint64(workspaceID), image.ID
}

func createAnnotationTestContext(t *testing.T, db *sql.DB, suffix string) store.Context {
	t.Helper()
	contextValue, err := store.NewContextStore(db).Create(context.Background(), store.Context{
		Name:                  "annotation-context-" + suffix,
		SegmentationModel:     "tesseract",
		TranscriptionProvider: "tesseract",
		TranscriptionModel:    "tesseract",
	})
	if err != nil {
		t.Fatalf("create processing context: %v", err)
	}
	t.Cleanup(func() {
		_ = store.NewContextStore(db).Delete(context.Background(), contextValue.ID)
	})
	return contextValue
}

func canonicalTestPage(t *testing.T, workspaceID, imageID uint64, canvasURI, text string) store.AnnotationPage {
	t.Helper()
	payload, err := iiif.NewAnnotationPage(iiif.PageIdentity{
		PublicBaseURL: "https://scribe.example",
		ItemImageID:   imageID,
		CanvasURI:     canvasURI,
	}, []any{map[string]any{
		"id":              fmt.Sprintf("https://scribe.example/annotation/%d", imageID),
		"type":            "Annotation",
		"motivation":      "supplementing",
		"textGranularity": "line",
		"body": map[string]any{
			"type": "TextualBody", "purpose": "supplementing", "format": "text/plain", "value": text,
		},
		"target": canvasURI + "#xywh=1,2,30,10",
	}})
	if err != nil {
		t.Fatalf("NewAnnotationPage: %v", err)
	}
	pageID, err := iiif.CanonicalPageID("https://scribe.example", imageID)
	if err != nil {
		t.Fatalf("CanonicalPageID: %v", err)
	}
	return store.AnnotationPage{
		WorkspaceID: workspaceID,
		ItemImageID: imageID,
		PageID:      pageID,
		CanvasURI:   canvasURI,
		Payload:     string(payload),
	}
}

func replacePageText(t *testing.T, payload, value string) string {
	t.Helper()
	var page map[string]any
	if err := json.Unmarshal([]byte(payload), &page); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	items := page["items"].([]any)
	annotation := items[0].(map[string]any)
	body := annotation["body"].(map[string]any)
	body["value"] = value
	updated, err := json.Marshal(page)
	if err != nil {
		t.Fatalf("encode page: %v", err)
	}
	return string(updated)
}

func assertPageText(t *testing.T, payload, want string) {
	t.Helper()
	if got := extractPageText(t, payload); got != want {
		t.Fatalf("page text = %q, want %q", got, want)
	}
}

func extractPageText(t *testing.T, payload string) string {
	t.Helper()
	var page map[string]any
	if err := json.Unmarshal([]byte(payload), &page); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	items := page["items"].([]any)
	annotation := items[0].(map[string]any)
	body := annotation["body"].(map[string]any)
	return body["value"].(string)
}
