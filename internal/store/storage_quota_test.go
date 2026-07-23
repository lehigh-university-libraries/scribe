package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	dbgen "github.com/lehigh-university-libraries/scribe/internal/db"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

func TestStorageQuotaAdmissionIsRaceFreeAndDeletionDerived(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	workspaceID, imageID := createAnnotationTestResource(t, database, uuid.NewString()+"-storage-quota", "https://source.example/canvas/quota")
	itemStore := store.NewItemStore(database)
	limits := storageQuotaTestLimits()
	limits.MaxBytesPerWorkspace = 100
	limits.MaxItemsPerWorkspace = 2
	limits.MaxImagesPerWorkspace = 2

	const contenders = 12
	start := make(chan struct{})
	results := make(chan struct {
		reservation store.StorageQuotaReservation
		err         error
	}, contenders)
	var workers sync.WaitGroup
	for range contenders {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			reservation, err := itemStore.ReserveStorageQuota(context.Background(), workspaceID, store.StorageQuotaRequest{Bytes: 60, Images: 1}, limits)
			results <- struct {
				reservation store.StorageQuotaReservation
				err         error
			}{reservation: reservation, err: err}
		}()
	}
	close(start)
	workers.Wait()
	close(results)

	var admitted []store.StorageQuotaReservation
	for result := range results {
		if result.err == nil {
			admitted = append(admitted, result.reservation)
			continue
		}
		if !errors.Is(result.err, store.ErrStorageQuotaExceeded) {
			t.Fatalf("parallel reservation error = %v, want ErrStorageQuotaExceeded", result.err)
		}
	}
	if len(admitted) != 1 {
		t.Fatalf("parallel storage admissions = %d, want 1", len(admitted))
	}

	var itemID string
	if err := database.QueryRow("SELECT item_id FROM item_images WHERE id = ?", imageID).Scan(&itemID); err != nil {
		t.Fatal(err)
	}
	uploadName := immutableUploadTestName(strings.Repeat("a", 64))
	imageURL := "/static/uploads/" + uploadName
	if err := itemStore.StageStorageQuotaUpload(ctx, admitted[0], imageURL, 60, limits); err != nil {
		t.Fatalf("stage admitted upload: %v", err)
	}
	persisted, err := itemStore.AddImage(ctx, dbgen.CreateItemImageParams{
		ItemID:       itemID,
		Sequence:     1,
		ImageURL:     imageURL,
		StorageBytes: 60,
		CanvasURI:    "https://source.example/canvas/quota/persisted/" + uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("persist admitted image: %v", err)
	}
	t.Cleanup(func() {
		_, _ = database.Exec("DELETE FROM resource_cleanup_outbox WHERE resource_key IN (?, ?)", uploadName, fmt.Sprint(persisted.ID))
	})
	if err := itemStore.ReleaseStorageQuotaReservation(ctx, admitted[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := itemStore.ReserveStorageQuota(ctx, workspaceID, store.StorageQuotaRequest{Bytes: 41}, limits); !errors.Is(err, store.ErrStorageQuotaExceeded) {
		t.Fatalf("reservation beyond persisted byte limit error = %v, want ErrStorageQuotaExceeded", err)
	}
	if err := itemStore.DeleteItemImageForWorkspace(ctx, persisted.ID, workspaceID); err != nil {
		t.Fatalf("delete persisted image: %v", err)
	}
	if _, err := itemStore.ReserveStorageQuota(ctx, workspaceID, store.StorageQuotaRequest{Bytes: 41}, limits); !errors.Is(err, store.ErrStorageQuotaExceeded) {
		t.Fatalf("reservation before blob cleanup error = %v, want ErrStorageQuotaExceeded", err)
	}
	makeCleanupsOldest(t, database, uploadName)
	delivery, err := itemStore.ClaimResourceCleanup(ctx, time.Minute)
	if err != nil || delivery == nil || delivery.ResourceKey != uploadName {
		t.Fatalf("claim persisted upload cleanup = %+v, %v", delivery, err)
	}
	fenceUploadCleanupForTest(t, ctx, itemStore, *delivery)
	if err := itemStore.CompleteResourceCleanup(ctx, *delivery); err != nil {
		t.Fatalf("complete persisted upload cleanup: %v", err)
	}
	replacement, err := itemStore.ReserveStorageQuota(ctx, workspaceID, store.StorageQuotaRequest{Bytes: 100, Images: 1}, limits)
	if err != nil {
		t.Fatalf("reservation after canonical delete: %v", err)
	}
	if err := itemStore.ReleaseStorageQuotaReservation(ctx, replacement); err != nil {
		t.Fatal(err)
	}
}

func TestStorageQuotaGlobalLastSlotIsRaceFreeAcrossWorkspaces(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	workspaceA, _ := createAnnotationTestResource(t, database, uuid.NewString()+"-quota-global-a", "https://source.example/canvas/quota-global-a")
	workspaceB, _ := createAnnotationTestResource(t, database, uuid.NewString()+"-quota-global-b", "https://source.example/canvas/quota-global-b")
	itemStore := store.NewItemStore(database)
	global, err := itemStore.GetStorageQuotaUsage(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	globalBytes, ok := globalBytes(global)
	if !ok {
		t.Fatal("global byte accounting overflow")
	}
	limits := storageQuotaTestLimits()
	limits.MaxBytesPerWorkspace = globalBytes + 60
	limits.MaxBytesTotal = globalBytes + 60

	type result struct {
		reservation store.StorageQuotaReservation
		err         error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for _, workspaceID := range []uint64{workspaceA, workspaceB} {
		go func() {
			<-start
			reservation, reserveErr := itemStore.ReserveStorageQuota(ctx, workspaceID, store.StorageQuotaRequest{Bytes: 60}, limits)
			results <- result{reservation: reservation, err: reserveErr}
		}()
	}
	close(start)
	var admitted []store.StorageQuotaReservation
	for range 2 {
		result := <-results
		if result.err == nil {
			admitted = append(admitted, result.reservation)
			continue
		}
		if !errors.Is(result.err, store.ErrStorageQuotaExceeded) {
			t.Fatalf("global last-slot admission error = %v", result.err)
		}
	}
	if len(admitted) != 1 {
		t.Fatalf("global last-slot admissions = %d, want 1", len(admitted))
	}
	if err := itemStore.ReleaseStorageQuotaReservation(ctx, admitted[0]); err != nil {
		t.Fatal(err)
	}
}

func TestQuotaTailDoesNotBlockAnotherWorkspaceAndRebuildConverges(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	workspaceA, imageA := createAnnotationTestResource(t, database, uuid.NewString()+"-quota-tail-a", "https://source.example/canvas/quota-tail-a")
	workspaceB, _ := createAnnotationTestResource(t, database, uuid.NewString()+"-quota-tail-b", "https://source.example/canvas/quota-tail-b")
	itemStore := store.NewItemStore(database)
	usageBefore, err := itemStore.GetStorageQuotaUsage(ctx, workspaceA)
	if err != nil {
		t.Fatal(err)
	}
	var itemID string
	if err := database.QueryRow(`SELECT item_id FROM item_images WHERE id = ?`, imageA).Scan(&itemID); err != nil {
		t.Fatal(err)
	}

	blocker, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback() }()
	var lockedItem string
	if err := blocker.QueryRow(`SELECT id FROM items WHERE id = ? FOR UPDATE`, itemID).Scan(&lockedItem); err != nil {
		t.Fatal(err)
	}
	addResult := make(chan error, 1)
	go func() {
		_, addErr := itemStore.AddImage(context.Background(), dbgen.CreateItemImageParams{
			ItemID: itemID, Sequence: 1,
			ImageURL:  "https://source.example/image/quota-tail-" + uuid.NewString() + ".jpg",
			CanvasURI: "https://source.example/canvas/quota-tail-added-" + uuid.NewString(),
		})
		addResult <- addErr
	}()
	waitForStorageQuotaRowLock(t, database, workspaceA)

	reserveResult := make(chan struct {
		reservation store.StorageQuotaReservation
		err         error
	}, 1)
	go func() {
		reservation, reserveErr := itemStore.ReserveStorageQuota(context.Background(), workspaceB, store.StorageQuotaRequest{Bytes: 1}, storageQuotaTestLimits())
		reserveResult <- struct {
			reservation store.StorageQuotaReservation
			err         error
		}{reservation: reservation, err: reserveErr}
	}()
	select {
	case result := <-reserveResult:
		if result.err != nil {
			t.Fatalf("workspace B admission while A is before quota tail: %v", result.err)
		}
		if err := itemStore.ReleaseStorageQuotaReservation(ctx, result.reservation); err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("workspace B admission blocked behind workspace A domain transaction")
	}

	rebuildResult := make(chan error, 1)
	go func() { rebuildResult <- itemStore.RebuildStorageQuotaUsage(context.Background()) }()
	select {
	case rebuildErr := <-rebuildResult:
		t.Fatalf("rebuild crossed an in-flight tenant mutation: %v", rebuildErr)
	case <-time.After(100 * time.Millisecond):
	}
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-addResult; err != nil {
		t.Fatalf("complete workspace A aggregate: %v", err)
	}
	if err := <-rebuildResult; err != nil {
		t.Fatalf("rebuild after workspace A aggregate: %v", err)
	}
	usageAfter, err := itemStore.GetStorageQuotaUsage(ctx, workspaceA)
	if err != nil {
		t.Fatal(err)
	}
	if usageAfter.Images != usageBefore.Images+1 {
		t.Fatalf("rebuilt workspace A images = %d, want %d", usageAfter.Images, usageBefore.Images+1)
	}
}

func TestExpiredQuotaSweepSkipsLockedTenantAndIsBounded(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	workspaceA, _ := createAnnotationTestResource(t, database, uuid.NewString()+"-quota-sweep-a", "https://source.example/canvas/quota-sweep-a")
	workspaceB, _ := createAnnotationTestResource(t, database, uuid.NewString()+"-quota-sweep-b", "https://source.example/canvas/quota-sweep-b")
	itemStore := store.NewItemStore(database)
	for {
		count, sweepErr := itemStore.SweepExpiredStorageQuotaReservations(ctx, 500)
		if sweepErr != nil {
			t.Fatalf("clear prior expired reservations: %v", sweepErr)
		}
		if count == 0 {
			break
		}
	}
	limits := storageQuotaTestLimits()
	reservationA, err := itemStore.ReserveStorageQuota(ctx, workspaceA, store.StorageQuotaRequest{Bytes: 1}, limits)
	if err != nil {
		t.Fatal(err)
	}
	reservationsB := make([]store.StorageQuotaReservation, 0, 3)
	for range 3 {
		reservation, reserveErr := itemStore.ReserveStorageQuota(ctx, workspaceB, store.StorageQuotaRequest{Bytes: 1}, limits)
		if reserveErr != nil {
			t.Fatal(reserveErr)
		}
		reservationsB = append(reservationsB, reservation)
	}
	if _, err := database.Exec(`UPDATE workspace_storage_reservations SET expires_at = DATE_SUB(NOW(6), INTERVAL 1 SECOND) WHERE id = ? OR workspace_id = ?`, reservationA.ID, workspaceB); err != nil {
		t.Fatal(err)
	}
	blocker, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback() }()
	var lockedWorkspace uint64
	if err := blocker.QueryRow(`SELECT workspace_id FROM storage_quota_usage WHERE workspace_id = ? FOR UPDATE`, workspaceA).Scan(&lockedWorkspace); err != nil {
		t.Fatal(err)
	}

	sweepResult := make(chan struct {
		count int
		err   error
	}, 1)
	go func() {
		count, sweepErr := itemStore.SweepExpiredStorageQuotaReservations(context.Background(), 2)
		sweepResult <- struct {
			count int
			err   error
		}{count: count, err: sweepErr}
	}()
	select {
	case result := <-sweepResult:
		if result.err != nil || result.count != 2 {
			t.Fatalf("bounded sweep while tenant A locked = %d, %v; want 2, nil", result.count, result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expired reservation sweep blocked on locked tenant")
	}
	var retainedA, retainedB int
	if err := database.QueryRow(`SELECT COUNT(*) FROM workspace_storage_reservations WHERE id = ?`, reservationA.ID).Scan(&retainedA); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM workspace_storage_reservations WHERE workspace_id = ?`, workspaceB).Scan(&retainedB); err != nil {
		t.Fatal(err)
	}
	if retainedA != 1 || retainedB != 1 {
		t.Fatalf("remaining expired reservations = A:%d B:%d, want A:1 B:1", retainedA, retainedB)
	}
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	for expected := 1; expected <= 2; expected++ {
		count, sweepErr := itemStore.SweepExpiredStorageQuotaReservations(ctx, 2)
		if sweepErr != nil || count != 1 {
			t.Fatalf("follow-up sweep %d = %d, %v; want 1, nil", expected, count, sweepErr)
		}
	}
	for _, reservation := range reservationsB {
		_ = itemStore.ReleaseStorageQuotaReservation(ctx, reservation)
	}
}

func TestStorageQuotaReservationResizeAndExpiry(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	workspaceID, _ := createAnnotationTestResource(t, database, uuid.NewString()+"-storage-resize", "https://source.example/canvas/resize")
	itemStore := store.NewItemStore(database)
	limits := storageQuotaTestLimits()
	limits.MaxBytesPerWorkspace = 100

	reservation, err := itemStore.ReserveStorageQuota(ctx, workspaceID, store.StorageQuotaRequest{Bytes: 20, Images: 1}, limits)
	if err != nil {
		t.Fatal(err)
	}
	resized, err := itemStore.ResizeStorageQuotaReservation(ctx, reservation, store.StorageQuotaRequest{Bytes: 100, Images: 1}, limits)
	if err != nil {
		t.Fatal(err)
	}
	if resized.Request.Bytes != 100 {
		t.Fatalf("resized bytes = %d, want 100", resized.Request.Bytes)
	}
	if _, err := itemStore.ResizeStorageQuotaReservation(ctx, resized, store.StorageQuotaRequest{Bytes: 101, Images: 1}, limits); !errors.Is(err, store.ErrStorageQuotaExceeded) {
		t.Fatalf("oversized resize error = %v, want ErrStorageQuotaExceeded", err)
	}
	var retained uint64
	if err := database.QueryRow("SELECT reserved_bytes FROM workspace_storage_reservations WHERE id = ?", resized.ID).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	if retained != 100 {
		t.Fatalf("failed resize retained %d bytes, want 100", retained)
	}
	if err := itemStore.ReleaseStorageQuotaReservation(ctx, resized); err != nil {
		t.Fatal(err)
	}

	expired, err := itemStore.ReserveStorageQuota(ctx, workspaceID, store.StorageQuotaRequest{Bytes: 100}, limits)
	if err != nil {
		t.Fatalf("create reservation to expire: %v", err)
	}
	if _, err := database.Exec("UPDATE workspace_storage_reservations SET expires_at = ? WHERE id = ?", time.Now().Add(-time.Minute), expired.ID); err != nil {
		t.Fatalf("expire reservation: %v", err)
	}
	if _, err := itemStore.ReserveStorageQuota(ctx, workspaceID, store.StorageQuotaRequest{Bytes: 100}, limits); !errors.Is(err, store.ErrStorageQuotaExceeded) {
		t.Fatalf("reserve before expired reservation is swept error = %v, want ErrStorageQuotaExceeded", err)
	}
	swept, err := itemStore.SweepExpiredStorageQuotaReservations(ctx, 10)
	if err != nil {
		t.Fatalf("sweep expired reservation: %v", err)
	}
	if swept != 1 {
		t.Fatalf("swept reservations = %d, want 1", swept)
	}
	afterExpiry, err := itemStore.ReserveStorageQuota(ctx, workspaceID, store.StorageQuotaRequest{Bytes: 100}, limits)
	if err != nil {
		t.Fatalf("reserve after stale reservation sweep: %v", err)
	}
	if err := itemStore.ReleaseStorageQuotaReservation(ctx, afterExpiry); err != nil {
		t.Fatal(err)
	}
	var expiredCount int
	if err := database.QueryRow("SELECT COUNT(*) FROM workspace_storage_reservations WHERE id = ?", expired.ID).Scan(&expiredCount); err != nil {
		t.Fatal(err)
	}
	if expiredCount != 0 {
		t.Fatalf("expired reservation count = %d, want 0", expiredCount)
	}
}

func TestStorageQuotaCountsUploadBytesUntilDurableCleanupCompletes(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	workspaceID, baseImageID := createAnnotationTestResource(t, database, uuid.NewString()+"-storage-cleanup", "https://source.example/canvas/cleanup-quota")
	itemStore := store.NewItemStore(database)
	limits := storageQuotaTestLimits()
	limits.MaxBytesPerWorkspace = 100
	uploadName := immutableUploadTestName(strings.Repeat("b", 64))
	imageURL := "/static/uploads/" + uploadName
	var itemID string
	if err := database.QueryRow("SELECT item_id FROM item_images WHERE id = ?", baseImageID).Scan(&itemID); err != nil {
		t.Fatal(err)
	}
	uploadReservation, err := itemStore.ReserveStorageQuota(ctx, workspaceID, store.StorageQuotaRequest{Bytes: 80, Images: 1}, limits)
	if err != nil {
		t.Fatalf("reserve canonical upload: %v", err)
	}
	if err := itemStore.StageStorageQuotaUpload(ctx, uploadReservation, imageURL, 80, limits); err != nil {
		t.Fatalf("stage canonical upload: %v", err)
	}
	image, err := itemStore.AddImage(ctx, dbgen.CreateItemImageParams{
		ItemID:       itemID,
		Sequence:     1,
		ImageURL:     imageURL,
		StorageBytes: 80,
		CanvasURI:    "https://source.example/canvas/cleanup-quota/upload/" + uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("persist canonical upload: %v", err)
	}
	if err := itemStore.ReleaseStorageQuotaReservation(ctx, uploadReservation); err != nil {
		t.Fatalf("release canonical upload reservation: %v", err)
	}
	t.Cleanup(func() {
		_, _ = database.Exec("DELETE FROM resource_cleanup_outbox WHERE resource_key IN (?, ?)", uploadName, fmt.Sprint(image.ID))
	})

	// An early compensation row can coexist briefly with a canonical reference.
	// The physical bytes must be counted once, not once per representation.
	if err := itemStore.EnqueueUploadCleanup(ctx, workspaceID, imageURL, 80); err != nil {
		t.Fatal(err)
	}
	exactCapacity, err := itemStore.ReserveStorageQuota(ctx, workspaceID, store.StorageQuotaRequest{Bytes: 20}, limits)
	if err != nil {
		t.Fatalf("reserve with live reference and duplicate cleanup row: %v", err)
	}
	if err := itemStore.ReleaseStorageQuotaReservation(ctx, exactCapacity); err != nil {
		t.Fatal(err)
	}

	if err := itemStore.DeleteItemImageForWorkspace(ctx, image.ID, workspaceID); err != nil {
		t.Fatalf("delete image: %v", err)
	}
	makeCleanupsOldest(t, database, uploadName)
	delivery, err := itemStore.ClaimResourceCleanup(ctx, time.Minute)
	if err != nil || delivery == nil {
		t.Fatalf("claim upload cleanup = %+v, %v", delivery, err)
	}
	if delivery.Kind != store.ResourceCleanupUploadBlob || delivery.ResourceKey != uploadName ||
		delivery.WorkspaceID != workspaceID || delivery.StorageBytes != 80 {
		t.Fatalf("cleanup delivery = %+v, want upload %q for workspace %d with 80 bytes", delivery, uploadName, workspaceID)
	}
	fenceUploadCleanupForTest(t, ctx, itemStore, *delivery)
	if err := itemStore.RetryResourceCleanup(ctx, *delivery, errors.New("object store unavailable"), time.Now().Add(-time.Second)); err != nil {
		t.Fatalf("retry upload cleanup: %v", err)
	}
	if _, err := itemStore.ReserveStorageQuota(ctx, workspaceID, store.StorageQuotaRequest{Bytes: 21}, limits); !errors.Is(err, store.ErrStorageQuotaExceeded) {
		t.Fatalf("reservation while physical cleanup is pending error = %v, want ErrStorageQuotaExceeded", err)
	}

	// Other due cleanup records can exist in the integration database. Restore
	// this retried record as the deterministic oldest claim before completing it.
	makeCleanupsOldest(t, database, uploadName)
	delivery, err = itemStore.ClaimResourceCleanup(ctx, time.Minute)
	if err != nil || delivery == nil {
		t.Fatalf("reclaim upload cleanup = %+v, %v", delivery, err)
	}
	if delivery.Kind != store.ResourceCleanupUploadBlob || delivery.ResourceKey != uploadName ||
		delivery.WorkspaceID != workspaceID || delivery.StorageBytes != 80 {
		t.Fatalf("reclaimed cleanup delivery = %+v, want upload %q for workspace %d with 80 bytes", delivery, uploadName, workspaceID)
	}
	fenceUploadCleanupForTest(t, ctx, itemStore, *delivery)
	if err := itemStore.CompleteResourceCleanup(ctx, *delivery); err != nil {
		t.Fatalf("complete upload cleanup: %v", err)
	}
	afterCleanupUsage, err := itemStore.GetStorageQuotaUsage(ctx, workspaceID)
	if err != nil {
		t.Fatalf("load quota after physical cleanup completion: %v", err)
	}
	if afterCleanupUsage.UploadBlobBytes != 0 || afterCleanupUsage.ReservedUploadBlobBytes != 0 {
		t.Fatalf("upload quota after physical cleanup completion = %+v, want zero upload usage", afterCleanupUsage)
	}
	afterCleanup, err := itemStore.ReserveStorageQuota(ctx, workspaceID, store.StorageQuotaRequest{Bytes: 100}, limits)
	if err != nil {
		t.Fatalf("reserve after physical cleanup completion: %v", err)
	}
	if err := itemStore.ReleaseStorageQuotaReservation(ctx, afterCleanup); err != nil {
		t.Fatal(err)
	}
}

func TestStorageQuotaStagingSurvivesCrashBeforeCanonicalInsert(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	workspaceID, _ := createAnnotationTestResource(t, database, uuid.NewString()+"-storage-stage", "https://source.example/canvas/stage-quota")
	itemStore := store.NewItemStore(database)
	limits := storageQuotaTestLimits()
	limits.MaxBytesPerWorkspace = 100
	uploadName := immutableUploadTestName(strings.Repeat("c", 64))
	imageURL := "/static/uploads/" + uploadName
	t.Cleanup(func() {
		_, _ = database.Exec("DELETE FROM resource_cleanup_outbox WHERE kind = 'upload_blob' AND resource_key = ?", uploadName)
	})

	reservation, err := itemStore.ReserveStorageQuota(ctx, workspaceID, store.StorageQuotaRequest{Bytes: 80}, limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := itemStore.StageStorageQuotaUpload(ctx, reservation, imageURL, 80, limits); err != nil {
		t.Fatalf("stage immutable upload: %v", err)
	}
	var reservedBytes, cleanupBytes uint64
	var resourceKey string
	if err := database.QueryRow("SELECT reserved_bytes, resource_key FROM workspace_storage_reservations WHERE id = ?", reservation.ID).Scan(&reservedBytes, &resourceKey); err != nil {
		t.Fatal(err)
	}
	if reservedBytes != 0 || resourceKey != uploadName {
		t.Fatalf("bound reservation = %d bytes / %q, want 0 / %q", reservedBytes, resourceKey, uploadName)
	}
	if err := database.QueryRow("SELECT storage_bytes FROM resource_cleanup_outbox WHERE kind = 'upload_blob' AND resource_key = ?", uploadName).Scan(&cleanupBytes); err != nil {
		t.Fatal(err)
	}
	if cleanupBytes != 80 {
		t.Fatalf("staged cleanup bytes = %d, want 80", cleanupBytes)
	}

	// Simulate a process crash: the generic reservation expires, but the exact
	// immutable cleanup identity remains authoritative and capacity-safe.
	if _, err := database.Exec("UPDATE workspace_storage_reservations SET expires_at = DATE_SUB(NOW(), INTERVAL 1 SECOND) WHERE id = ?", reservation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := itemStore.ReserveStorageQuota(ctx, workspaceID, store.StorageQuotaRequest{Bytes: 21}, limits); !errors.Is(err, store.ErrStorageQuotaExceeded) {
		t.Fatalf("reserve after staged-upload crash error = %v, want ErrStorageQuotaExceeded", err)
	}
	makeCleanupsOldest(t, database, uploadName)
	delivery, err := itemStore.ClaimResourceCleanup(ctx, time.Minute)
	if err != nil || delivery == nil || delivery.ResourceKey != uploadName {
		t.Fatalf("claim crashed staged upload = %+v, %v", delivery, err)
	}
	fenceUploadCleanupForTest(t, ctx, itemStore, *delivery)
	if err := itemStore.CompleteResourceCleanup(ctx, *delivery); err != nil {
		t.Fatal(err)
	}
	afterRecovery, err := itemStore.ReserveStorageQuota(ctx, workspaceID, store.StorageQuotaRequest{Bytes: 100}, limits)
	if err != nil {
		t.Fatalf("reserve after staged upload recovery: %v", err)
	}
	if err := itemStore.ReleaseStorageQuotaReservation(ctx, afterRecovery); err != nil {
		t.Fatal(err)
	}
}

func TestQuotaRebuildRetainsDeletedWorkspaceUntilPhysicalCleanup(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	workspaceID, baseImageID := createAnnotationTestResource(t, database, uuid.NewString()+"-quota-tombstone", "https://source.example/canvas/quota-tombstone")
	itemStore := store.NewItemStore(database)
	var itemID string
	if err := database.QueryRow(`SELECT item_id FROM item_images WHERE id = ?`, baseImageID).Scan(&itemID); err != nil {
		t.Fatal(err)
	}
	limits := storageQuotaTestLimits()
	uploadName := immutableUploadTestName(strings.Repeat("d", 64))
	imageURL := "/static/uploads/" + uploadName
	reservation, err := itemStore.ReserveStorageQuota(ctx, workspaceID, store.StorageQuotaRequest{Bytes: 80, Images: 1}, limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := itemStore.StageStorageQuotaUpload(ctx, reservation, imageURL, 80, limits); err != nil {
		t.Fatal(err)
	}
	if _, err := itemStore.AddImage(ctx, dbgen.CreateItemImageParams{
		ItemID: itemID, Sequence: 1, ImageURL: imageURL, StorageBytes: 80,
		CanvasURI: "https://source.example/canvas/quota-tombstone-upload",
	}); err != nil {
		t.Fatal(err)
	}
	if err := itemStore.ReleaseStorageQuotaReservation(ctx, reservation); err != nil {
		t.Fatal(err)
	}
	if err := itemStore.DeleteForWorkspace(ctx, itemID, workspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`DELETE FROM workspace_members WHERE workspace_id = ?`, workspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`DELETE FROM workspaces WHERE id = ?`, workspaceID); err != nil {
		t.Fatal(err)
	}
	if err := itemStore.RebuildStorageQuotaUsage(ctx); err != nil {
		t.Fatalf("rebuild deleted-workspace cleanup owner: %v", err)
	}
	usage, err := itemStore.GetStorageQuotaUsage(ctx, workspaceID)
	if err != nil {
		t.Fatalf("load retained cleanup quota row: %v", err)
	}
	if usage.UploadBlobBytes != 80 {
		t.Fatalf("deleted-workspace upload bytes = %d, want 80 until physical cleanup", usage.UploadBlobBytes)
	}

	makeCleanupsOldest(t, database, uploadName)
	delivery, err := itemStore.ClaimResourceCleanup(ctx, time.Minute)
	if err != nil || delivery == nil || delivery.ResourceKey != uploadName {
		t.Fatalf("claim deleted-workspace upload cleanup = %+v, %v", delivery, err)
	}
	fenceUploadCleanupForTest(t, ctx, itemStore, *delivery)
	if err := itemStore.CompleteResourceCleanup(ctx, *delivery); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`DELETE FROM resource_cleanup_outbox WHERE workspace_id = ?`, workspaceID); err != nil {
		t.Fatal(err)
	}
	if err := itemStore.RebuildStorageQuotaUsage(ctx); err != nil {
		t.Fatalf("rebuild after final physical cleanup: %v", err)
	}
	if _, err := itemStore.GetStorageQuotaUsage(ctx, workspaceID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted-workspace quota tombstone error = %v, want sql.ErrNoRows", err)
	}
}

func storageQuotaTestLimits() store.StorageQuotaLimits {
	return store.StorageQuotaLimits{
		MaxBytesPerWorkspace:  1 << 40,
		MaxBytesTotal:         1 << 50,
		MaxItemsPerWorkspace:  1_000_000,
		MaxItemsTotal:         2_000_000,
		MaxImagesPerWorkspace: 1_000_000,
		MaxImagesTotal:        2_000_000,
		ReservationTTL:        time.Hour,
	}
}

func globalBytes(usage store.StorageQuotaUsage) (uint64, bool) {
	if usage.DatabaseBytes > ^uint64(0)-usage.UploadBlobBytes {
		return 0, false
	}
	return usage.UploadBlobBytes + usage.DatabaseBytes, true
}

func waitForStorageQuotaRowLock(t *testing.T, database *sql.DB, workspaceID uint64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		probe, err := database.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		var lockedID uint64
		err = probe.QueryRow(`SELECT workspace_id FROM storage_quota_usage WHERE workspace_id = ? FOR UPDATE NOWAIT`, workspaceID).Scan(&lockedID)
		_ = probe.Rollback()
		if err != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("workspace %d quota row was not locked before the deadline", workspaceID)
}
