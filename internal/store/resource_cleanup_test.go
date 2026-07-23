package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	db "github.com/lehigh-university-libraries/scribe/internal/db"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

func TestWorkspaceImageURLLookupIsExactAndScoped(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	workspaceA, imageA := createAnnotationTestResource(t, database, suffix+"-lookup-a", "https://source.example/canvas/lookup-a")
	workspaceB, _ := createAnnotationTestResource(t, database, suffix+"-lookup-b", "https://source.example/canvas/lookup-b")
	itemStore := store.NewItemStore(database)
	image, err := itemStore.GetImageForWorkspace(ctx, imageA, workspaceA)
	if err != nil {
		t.Fatal(err)
	}
	if owns, err := itemStore.WorkspaceOwnsImageURL(ctx, workspaceA, image.ImageURL); err != nil || !owns {
		t.Fatalf("own workspace lookup = %v/%v, want true", owns, err)
	}
	if owns, err := itemStore.WorkspaceOwnsImageURL(ctx, workspaceB, image.ImageURL); err != nil || owns {
		t.Fatalf("cross-workspace lookup = %v/%v, want false", owns, err)
	}
	if owns, err := itemStore.WorkspaceOwnsImageURL(ctx, workspaceA, image.ImageURL+"-suffix"); err != nil || owns {
		t.Fatalf("inexact URL lookup = %v/%v, want false", owns, err)
	}
}

func TestItemImageDeletionAtomicallyQueuesDurableSharedResourceCleanup(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	canvasURI := "https://source.example/canvas/cleanup-" + suffix
	workspaceA, imageA := createAnnotationTestResource(t, database, suffix+"-cleanup-a", canvasURI+"/a")
	workspaceB, imageB := createAnnotationTestResource(t, database, suffix+"-cleanup-b", canvasURI+"/b")
	uploadName := immutableUploadTestName(strings.Repeat("d", 64))
	imageURL := "/static/uploads/" + uploadName
	if _, err := database.Exec(`UPDATE item_images SET image_url = ? WHERE id IN (?, ?)`, imageURL, imageA, imageB); err != nil {
		t.Fatalf("share upload URL: %v", err)
	}
	t.Cleanup(func() {
		_, _ = database.Exec(`DELETE FROM resource_cleanup_outbox WHERE resource_key IN (?, ?, ?)`, uploadName, fmt.Sprint(imageA), fmt.Sprint(imageB))
	})

	annotationStore := store.NewAnnotationStore(database)
	savedA, err := annotationStore.SavePage(ctx, canonicalTestPage(t, workspaceA, imageA, canvasURI+"/a", "alpha"), 0)
	if err != nil {
		t.Fatalf("SavePage workspace A: %v", err)
	}
	savedB, err := annotationStore.SavePage(ctx, canonicalTestPage(t, workspaceB, imageB, canvasURI+"/b", "beta"), 0)
	if err != nil {
		t.Fatalf("SavePage workspace B: %v", err)
	}
	if _, err := annotationStore.PublishPage(ctx, workspaceA, imageA, store.AnnotationPublicationOptions{ExpectedRevision: savedA.Revision}); err != nil {
		t.Fatalf("PublishPage workspace A: %v", err)
	}
	if _, err := annotationStore.PublishPage(ctx, workspaceB, imageB, store.AnnotationPublicationOptions{ExpectedRevision: savedB.Revision}); err != nil {
		t.Fatalf("PublishPage workspace B: %v", err)
	}
	var mirrorCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM annotation_mirror_outbox WHERE item_image_id = ?`, imageA).Scan(&mirrorCount); err != nil {
		t.Fatalf("count mirror rows before deletion: %v", err)
	}
	if mirrorCount != 1 {
		t.Fatalf("publication mirror row count before deletion = %d, want 1", mirrorCount)
	}

	itemStore := store.NewItemStore(database)
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	if err := itemStore.DeleteItemImageForWorkspace(canceledCtx, imageA, workspaceA); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled delete error = %v, want context.Canceled", err)
	}
	if _, err := itemStore.GetImageForWorkspace(ctx, imageA, workspaceA); err != nil {
		t.Fatalf("canceled delete removed image: %v", err)
	}
	assertCleanupCount(t, database, uploadName, 0)
	assertCleanupCount(t, database, fmt.Sprint(imageA), 0)

	if err := itemStore.DeleteItemImageForWorkspace(ctx, imageA, workspaceB); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-workspace delete error = %v, want sql.ErrNoRows", err)
	}
	assertCleanupCount(t, database, uploadName, 0)
	assertCleanupCount(t, database, fmt.Sprint(imageA), 0)

	if err := itemStore.DeleteItemImageForWorkspace(ctx, imageA, workspaceA); err != nil {
		t.Fatalf("DeleteItemImageForWorkspace: %v", err)
	}
	if _, err := annotationStore.LoadPage(ctx, workspaceA, imageA); !errors.Is(err, store.ErrAnnotationPageNotFound) {
		t.Fatalf("canonical page survived image deletion: %v", err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM annotation_mirror_outbox WHERE item_image_id = ?`, imageA).Scan(&mirrorCount); err != nil {
		t.Fatalf("count mirror rows: %v", err)
	}
	if mirrorCount != 0 {
		t.Fatalf("publication mirror row count = %d, want cascade to remove it", mirrorCount)
	}
	assertCleanupCount(t, database, uploadName, 1)
	assertCleanupCount(t, database, fmt.Sprint(imageA), 1)
	if count, err := itemStore.ImageURLReferenceCount(ctx, imageURL); err != nil || count != 1 {
		t.Fatalf("shared upload references = %d, %v; want 1", count, err)
	}

	// Only the blob task is immediately due. The Triplet DELETE has a grace
	// period so an already-running, timeout-bounded mirror PUT cannot win last.
	makeCleanupsOldest(t, database, uploadName)
	delivery, err := itemStore.ClaimResourceCleanup(ctx, time.Minute)
	if err != nil || delivery == nil {
		t.Fatalf("ClaimResourceCleanup = %+v, %v", delivery, err)
	}
	if delivery.Kind != store.ResourceCleanupUploadBlob || delivery.ResourceKey != uploadName {
		t.Fatalf("first cleanup = %+v, want upload %q", delivery, uploadName)
	}
	if err := itemStore.CompleteResourceCleanup(ctx, *delivery, false); err != nil {
		t.Fatalf("CompleteResourceCleanup: %v", err)
	}

	if err := itemStore.DeleteItemImageForWorkspace(ctx, imageB, workspaceB); err != nil {
		t.Fatalf("delete final shared reference: %v", err)
	}
	if count, err := itemStore.ImageURLReferenceCount(ctx, imageURL); err != nil || count != 0 {
		t.Fatalf("shared upload references after final delete = %d, %v; want 0", count, err)
	}
	assertCleanupCount(t, database, uploadName, 1)

	if _, err := database.Exec(`UPDATE resource_cleanup_outbox SET next_attempt_at = '2000-01-01 00:00:00' WHERE resource_key IN (?, ?, ?)`, uploadName, fmt.Sprint(imageA), fmt.Sprint(imageB)); err != nil {
		t.Fatalf("make Triplet cleanups due: %v", err)
	}
	want := map[string]bool{uploadName: false, fmt.Sprint(imageA): false, fmt.Sprint(imageB): false}
	for range len(want) {
		delivery, err := itemStore.ClaimResourceCleanup(ctx, time.Minute)
		if err != nil || delivery == nil {
			t.Fatalf("claim remaining cleanup = %+v, %v", delivery, err)
		}
		if _, ok := want[delivery.ResourceKey]; !ok {
			t.Fatalf("unexpected cleanup: %+v", delivery)
		}
		want[delivery.ResourceKey] = true
		fenceUploadCleanupForTest(t, ctx, itemStore, *delivery)
		if err := itemStore.CompleteResourceCleanup(ctx, *delivery); err != nil {
			t.Fatalf("complete cleanup %q: %v", delivery.ResourceKey, err)
		}
	}
	for key, seen := range want {
		if !seen {
			t.Errorf("cleanup %q was not claimed", key)
		}
	}
}

func TestResourceCleanupGenerationFencesStaleWorkerAndUploadRetriesBeyondAttemptLimit(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	itemStore := store.NewItemStore(database)
	uploadName := immutableUploadTestName(strings.Repeat("a", 64))
	imageURL := "/static/uploads/" + uploadName
	t.Cleanup(func() { _, _ = database.Exec(`DELETE FROM resource_cleanup_outbox WHERE resource_key = ?`, uploadName) })

	if err := itemStore.EnqueueUploadCleanup(ctx, store.AnonymousWorkspaceID, imageURL, 123); err != nil {
		t.Fatalf("EnqueueUploadCleanup: %v", err)
	}
	var cleanupID, staleGeneration uint64
	if err := database.QueryRow(
		`SELECT id, generation FROM resource_cleanup_outbox WHERE kind = 'upload_blob' AND resource_key = ?`,
		uploadName,
	).Scan(&cleanupID, &staleGeneration); err != nil {
		t.Fatalf("load initial cleanup: %v", err)
	}
	const staleOwner = "stale-cleanup-owner"
	if _, err := database.Exec(`
UPDATE resource_cleanup_outbox
SET status = 'processing', attempt_count = 1, lease_until = DATE_ADD(NOW(), INTERVAL 1 MINUTE), locked_by = ?
WHERE id = ?`, staleOwner, cleanupID); err != nil {
		t.Fatalf("claim initial cleanup: %v", err)
	}
	stale := store.ResourceCleanupDelivery{ID: cleanupID, Generation: staleGeneration, LeaseOwner: staleOwner}
	if err := itemStore.EnqueueUploadCleanup(ctx, store.AnonymousWorkspaceID, imageURL, 123); err != nil {
		t.Fatalf("re-enqueue upload cleanup: %v", err)
	}
	if err := itemStore.CompleteResourceCleanup(ctx, stale); !errors.Is(err, store.ErrResourceCleanupLease) {
		t.Fatalf("stale completion error = %v, want ErrResourceCleanupLease", err)
	}

	var currentGeneration uint64
	if err := database.QueryRow(`SELECT generation FROM resource_cleanup_outbox WHERE id = ?`, cleanupID).Scan(&currentGeneration); err != nil {
		t.Fatalf("load replacement generation: %v", err)
	}
	if currentGeneration <= stale.Generation {
		t.Fatalf("replacement generation = %d, want > %d", currentGeneration, stale.Generation)
	}
	const currentOwner = "current-cleanup-owner"
	if _, err := database.Exec(`
UPDATE resource_cleanup_outbox
SET status = 'processing', attempt_count = 1, max_attempts = 20,
    lease_until = DATE_ADD(NOW(), INTERVAL 1 MINUTE), locked_by = ?
WHERE id = ?`, currentOwner, cleanupID); err != nil {
		t.Fatalf("claim replacement cleanup: %v", err)
	}
	current := store.ResourceCleanupDelivery{ID: cleanupID, Generation: currentGeneration, LeaseOwner: currentOwner, Attempt: 1, MaxAttempts: 20}
	for failedAttempts := 1; failedAttempts <= 21; failedAttempts++ {
		if err := itemStore.RetryResourceCleanup(ctx, current, errors.New("backend unavailable"), time.Now().Add(-time.Second)); err != nil {
			t.Fatalf("RetryResourceCleanup failure %d: %v", failedAttempts, err)
		}
		if failedAttempts == 20 {
			var status string
			var attempts int
			if err := database.QueryRow(`SELECT status, attempt_count FROM resource_cleanup_outbox WHERE id = ?`, cleanupID).Scan(&status, &attempts); err != nil {
				t.Fatalf("load renewed upload cleanup epoch: %v", err)
			}
			if status != "pending" || attempts != 20 {
				t.Fatalf("upload cleanup beyond threshold = %s/%d, want pending/20", status, attempts)
			}
		}
		result, err := database.Exec(`
UPDATE resource_cleanup_outbox
SET status = 'processing', attempt_count = attempt_count + 1,
    lease_until = DATE_ADD(NOW(), INTERVAL 1 MINUTE), locked_by = ?
WHERE id = ? AND status = 'pending'`, currentOwner, cleanupID)
		if err != nil {
			t.Fatalf("claim after upload cleanup failure %d: %v", failedAttempts, err)
		}
		if affected, rowsErr := result.RowsAffected(); rowsErr != nil || affected != 1 {
			t.Fatalf("claim after upload cleanup failure %d rows = %d/%v, want 1", failedAttempts, affected, rowsErr)
		}
		if err := database.QueryRow(`SELECT attempt_count FROM resource_cleanup_outbox WHERE id = ?`, cleanupID).Scan(&current.Attempt); err != nil {
			t.Fatalf("load attempt after failure %d: %v", failedAttempts, err)
		}
	}
	if current.Attempt != 22 {
		t.Fatalf("attempt after 21 failures = %d, want 22", current.Attempt)
	}
	current.Kind = store.ResourceCleanupUploadBlob
	current.ResourceKey = uploadName
	current.WorkspaceID = store.AnonymousWorkspaceID
	fenceUploadCleanupForTest(t, ctx, itemStore, current)
	if err := itemStore.CompleteResourceCleanup(ctx, current); err != nil {
		t.Fatalf("complete recovered upload cleanup: %v", err)
	}
}

func TestUploadBlobRetirementSerializesReferencesAndSurvivesRetry(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	workspaceID, existingImageID := createAnnotationTestResource(
		t, database, suffix+"-retirement-fence", "https://source.example/canvas/"+suffix,
	)
	var itemID string
	if err := database.QueryRow(`SELECT item_id FROM item_images WHERE id = ?`, existingImageID).Scan(&itemID); err != nil {
		t.Fatalf("load retirement test item: %v", err)
	}
	uploadName := immutableUploadTestName(strings.Repeat("f", 64))
	imageURL := "/static/uploads/" + uploadName
	t.Cleanup(func() {
		_, _ = database.Exec(`DELETE FROM item_images WHERE item_id = ? AND sequence = 1`, itemID)
		_, _ = database.Exec(`DELETE FROM resource_cleanup_outbox WHERE kind = 'upload_blob' AND resource_key = ?`, uploadName)
	})

	itemStore := store.NewItemStore(database)
	if err := itemStore.EnqueueUploadCleanup(ctx, workspaceID, imageURL, 80); err != nil {
		t.Fatalf("enqueue raced upload cleanup: %v", err)
	}
	makeCleanupsOldest(t, database, uploadName)
	delivery, err := itemStore.ClaimResourceCleanup(ctx, time.Minute)
	if err != nil || delivery == nil || delivery.ResourceKey != uploadName {
		t.Fatalf("claim raced upload cleanup = %+v, %v", delivery, err)
	}

	// Model a reference creator after it has taken the canonical quota guards
	// but before its item_image insert commits. The cleanup worker must wait and
	// then observe that committed reference rather than tombstoning the blob.
	creator, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin reference creator: %v", err)
	}
	defer func() { _ = creator.Rollback() }()
	for _, quotaWorkspaceID := range []uint64{0, workspaceID} {
		if err := creator.QueryRow(
			`SELECT workspace_id FROM storage_quota_usage WHERE workspace_id = ? FOR UPDATE`, quotaWorkspaceID,
		).Scan(&quotaWorkspaceID); err != nil {
			t.Fatalf("lock reference quota guard %d: %v", quotaWorkspaceID, err)
		}
	}
	if _, err := creator.Exec(`
INSERT INTO item_images (workspace_id, item_id, sequence, image_url, storage_bytes, canvas_uri)
VALUES (?, ?, 1, ?, 80, ?)`, workspaceID, itemID, imageURL, "https://source.example/canvas/"+suffix+"/raced"); err != nil {
		t.Fatalf("insert uncommitted upload reference: %v", err)
	}
	type retirementResult struct {
		deleteBlob bool
		err        error
	}
	retirementDone := make(chan retirementResult, 1)
	go func() {
		deleteBlob, retirementErr := itemStore.BeginUploadBlobRetirement(ctx, *delivery)
		retirementDone <- retirementResult{deleteBlob: deleteBlob, err: retirementErr}
	}()
	select {
	case result := <-retirementDone:
		t.Fatalf("retirement bypassed the uncommitted reference guard: %+v", result)
	case <-time.After(150 * time.Millisecond):
	}
	if err := creator.Commit(); err != nil {
		t.Fatalf("commit raced upload reference: %v", err)
	}
	select {
	case result := <-retirementDone:
		if result.err != nil || result.deleteBlob {
			t.Fatalf("retirement after reference commit = %+v, want deleteBlob false", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("retirement did not resume after reference commit")
	}
	if err := itemStore.CompleteResourceCleanup(ctx, *delivery, false); err != nil {
		t.Fatalf("complete referenced cleanup: %v", err)
	}
	if _, err := database.Exec(`DELETE FROM item_images WHERE item_id = ? AND sequence = 1`, itemID); err != nil {
		t.Fatalf("remove raced upload reference: %v", err)
	}

	// When retirement wins, the durable tombstone blocks every later reference
	// transaction, including while a failed physical DELETE is pending retry.
	if err := itemStore.EnqueueUploadCleanup(ctx, workspaceID, imageURL, 80); err != nil {
		t.Fatalf("enqueue fenced upload cleanup: %v", err)
	}
	makeCleanupsOldest(t, database, uploadName)
	delivery, err = itemStore.ClaimResourceCleanup(ctx, time.Minute)
	if err != nil || delivery == nil || delivery.ResourceKey != uploadName {
		t.Fatalf("claim fenced upload cleanup = %+v, %v", delivery, err)
	}
	deleteBlob, err := itemStore.BeginUploadBlobRetirement(ctx, *delivery)
	if err != nil || !deleteBlob {
		t.Fatalf("begin fenced retirement = %v, %v; want true", deleteBlob, err)
	}
	assertRetiredReferenceRejected := func(stage string) {
		t.Helper()
		_, addErr := itemStore.AddImage(ctx, db.CreateItemImageParams{
			ItemID: itemID, Sequence: 1, ImageURL: imageURL, StorageBytes: 80,
			CanvasURI: "https://source.example/canvas/" + suffix + "/blocked",
		})
		if !errors.Is(addErr, store.ErrUploadBlobRetired) {
			t.Fatalf("%s AddImage error = %v, want ErrUploadBlobRetired", stage, addErr)
		}
		var references int
		if countErr := database.QueryRow(`SELECT COUNT(*) FROM item_images WHERE image_url = ?`, imageURL).Scan(&references); countErr != nil {
			t.Fatalf("%s count blocked references: %v", stage, countErr)
		}
		if references != 0 {
			t.Fatalf("%s blocked reference count = %d, want 0", stage, references)
		}
	}
	assertRetiredReferenceRejected("active deletion")
	if err := itemStore.RetryResourceCleanup(ctx, *delivery, errors.New("object store unavailable"), time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("retry fenced upload cleanup: %v", err)
	}
	assertRetiredReferenceRejected("pending retry")
	var fencedAt sql.NullTime
	if err := database.QueryRow(`
SELECT delete_fenced_at
FROM resource_cleanup_outbox
WHERE kind = 'upload_blob' AND resource_key = ?`, uploadName).Scan(&fencedAt); err != nil {
		t.Fatalf("load upload retirement tombstone: %v", err)
	}
	if !fencedAt.Valid {
		t.Fatal("upload retirement tombstone was cleared by retry")
	}
}

func TestTripletCleanupStillStopsAtItsAttemptLimit(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	itemStore := store.NewItemStore(database)
	resourceKey := fmt.Sprint(time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = database.Exec(`DELETE FROM resource_cleanup_outbox WHERE resource_key = ?`, resourceKey)
	})
	result, err := database.Exec(`
INSERT INTO resource_cleanup_outbox (
  kind, resource_key, status, attempt_count, max_attempts, next_attempt_at, lease_until, locked_by
)
VALUES (
  'triplet_presentation_image', ?, 'processing', 1, 1, NOW(), DATE_ADD(NOW(), INTERVAL 1 MINUTE), 'triplet-owner'
)`, resourceKey)
	if err != nil {
		t.Fatalf("insert Triplet cleanup: %v", err)
	}
	cleanupID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("load Triplet cleanup id: %v", err)
	}
	delivery := store.ResourceCleanupDelivery{ID: uint64(cleanupID), Generation: 1, LeaseOwner: "triplet-owner"} // #nosec G115 -- positive test identifier.
	untrustedFailure := strings.Repeat(
		"Triplet upstream returned 403 forbidden; "+
			"url=https://user:TOKEN-SENTINEL@triplet.example/path?api_key=QUERY-SENTINEL; "+
			"response_body=BODY-SENTINEL; SQL=SELECT * FROM secrets; Vault=VAULT-SENTINEL; ",
		32,
	)
	if err := itemStore.RetryResourceCleanup(ctx, delivery, errors.New(untrustedFailure), time.Now()); err != nil {
		t.Fatalf("retry Triplet cleanup: %v", err)
	}
	var status string
	var attempts int
	var lastError string
	if err := database.QueryRow(`SELECT status, attempt_count, last_error FROM resource_cleanup_outbox WHERE id = ?`, delivery.ID).Scan(&status, &attempts, &lastError); err != nil {
		t.Fatalf("load exhausted Triplet cleanup: %v", err)
	}
	if status != "failed" || attempts != 1 {
		t.Fatalf("exhausted Triplet cleanup = %s/%d, want failed/1", status, attempts)
	}
	assertBoundedFailureCategory(t, lastError, "resource cleanup authorization failed")
}

func TestExpiredUploadCleanupLeaseRemainsRetryableBeyondAttemptLimit(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	uploadName := "expired-cleanup-" + uuid.NewString() + ".png"
	t.Cleanup(func() { _, _ = database.Exec(`DELETE FROM resource_cleanup_outbox WHERE resource_key = ?`, uploadName) })
	result, err := database.Exec(`
INSERT INTO resource_cleanup_outbox (
  kind, resource_key, status, attempt_count, max_attempts, next_attempt_at, lease_until, locked_by
)
VALUES (
  'upload_blob', ?, 'processing', 20, 20, NOW(), DATE_SUB(NOW(), INTERVAL 1 SECOND), 'expired-owner'
)`, uploadName)
	if err != nil {
		t.Fatalf("insert expired upload cleanup: %v", err)
	}
	cleanupID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("load expired cleanup id: %v", err)
	}
	if err := db.New(database).FailExhaustedResourceCleanups(ctx); err != nil {
		t.Fatalf("recover expired cleanup: %v", err)
	}
	var status string
	var attempts int
	var nextAttempt time.Time
	if err := database.QueryRow(
		`SELECT status, attempt_count, next_attempt_at FROM resource_cleanup_outbox WHERE id = ?`,
		cleanupID,
	).Scan(&status, &attempts, &nextAttempt); err != nil {
		t.Fatalf("load recovered cleanup: %v", err)
	}
	if status != "pending" || attempts != 20 {
		t.Fatalf("recovered cleanup = %s/%d, want pending/20", status, attempts)
	}
	if nextAttempt.Before(time.Now().Add(50 * time.Minute)) {
		t.Fatalf("recovered cleanup next attempt = %s, want approximately one hour", nextAttempt)
	}
}

func TestItemDeletionQueuesEveryImageCleanupWithinWorkspaceTransaction(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	workspaceID, imageID := createAnnotationTestResource(t, database, suffix+"-item-delete", "https://source.example/canvas/"+suffix)
	uploadName := immutableUploadTestName(strings.Repeat("e", 64))
	if _, err := database.Exec(`UPDATE item_images SET image_url = ?, storage_bytes = 0 WHERE id = ?`, "/static/uploads/"+uploadName, imageID); err != nil {
		t.Fatalf("set item upload: %v", err)
	}
	var itemID string
	if err := database.QueryRow(`SELECT item_id FROM item_images WHERE id = ?`, imageID).Scan(&itemID); err != nil {
		t.Fatalf("load item id: %v", err)
	}
	secondImage, err := store.NewItemStore(database).AddImage(ctx, db.CreateItemImageParams{
		ItemID:       itemID,
		Sequence:     1,
		ImageURL:     "/static/uploads/" + uploadName,
		StorageBytes: 80,
		CanvasURI:    "https://source.example/canvas/" + suffix + "/second",
	})
	if err != nil {
		t.Fatalf("create second item image: %v", err)
	}
	t.Cleanup(func() {
		_, _ = database.Exec(`DELETE FROM resource_cleanup_outbox WHERE resource_key IN (?, ?, ?)`, uploadName, fmt.Sprint(imageID), fmt.Sprint(secondImage.ID))
	})

	itemStore := store.NewItemStore(database)
	if err := itemStore.DeleteForWorkspace(ctx, itemID, store.AnonymousWorkspaceID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-workspace item delete = %v, want sql.ErrNoRows", err)
	}
	if err := itemStore.DeleteForWorkspace(ctx, itemID, workspaceID); err != nil {
		t.Fatalf("DeleteForWorkspace: %v", err)
	}
	var itemCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM items WHERE id = ?`, itemID).Scan(&itemCount); err != nil {
		t.Fatalf("count deleted item: %v", err)
	}
	if itemCount != 0 {
		t.Fatalf("item count after delete = %d, want 0", itemCount)
	}
	assertCleanupCount(t, database, uploadName, 1)
	var cleanupWorkspaceID, cleanupStorageBytes uint64
	if err := database.QueryRow(`SELECT workspace_id, storage_bytes FROM resource_cleanup_outbox WHERE kind = 'upload_blob' AND resource_key = ?`, uploadName).Scan(&cleanupWorkspaceID, &cleanupStorageBytes); err != nil {
		t.Fatalf("load item upload cleanup accounting: %v", err)
	}
	if cleanupWorkspaceID != workspaceID || cleanupStorageBytes != 80 {
		t.Fatalf("item upload cleanup accounting = workspace %d / %d bytes, want %d / 80", cleanupWorkspaceID, cleanupStorageBytes, workspaceID)
	}
	assertCleanupCount(t, database, fmt.Sprint(imageID), 1)
	assertCleanupCount(t, database, fmt.Sprint(secondImage.ID), 1)
}

func assertCleanupCount(t *testing.T, database *sql.DB, resourceKey string, want int) {
	t.Helper()
	var got int
	if err := database.QueryRow(`SELECT COUNT(*) FROM resource_cleanup_outbox WHERE resource_key = ?`, resourceKey).Scan(&got); err != nil {
		t.Fatalf("count cleanup %q: %v", resourceKey, err)
	}
	if got != want {
		t.Fatalf("cleanup count for %q = %d, want %d", resourceKey, got, want)
	}
}

func makeCleanupsOldest(t *testing.T, database *sql.DB, resourceKeys ...string) {
	t.Helper()
	for _, resourceKey := range resourceKeys {
		if _, err := database.Exec(`UPDATE resource_cleanup_outbox SET next_attempt_at = '2000-01-01 00:00:00' WHERE resource_key = ?`, resourceKey); err != nil {
			t.Fatalf("make cleanup %q due: %v", resourceKey, err)
		}
	}
}

func fenceUploadCleanupForTest(t *testing.T, ctx context.Context, itemStore *store.ItemStore, delivery store.ResourceCleanupDelivery) {
	t.Helper()
	if delivery.Kind != store.ResourceCleanupUploadBlob {
		return
	}
	deleteBlob, err := itemStore.BeginUploadBlobRetirement(ctx, delivery)
	if err != nil || !deleteBlob {
		t.Fatalf("begin upload blob retirement for %+v = %v, %v; want true", delivery, deleteBlob, err)
	}
}
