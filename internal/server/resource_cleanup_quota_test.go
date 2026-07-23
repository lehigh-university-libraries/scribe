package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	dbstore "github.com/lehigh-university-libraries/scribe/internal/db"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

func TestUploadCleanupWorkerReleasesQuotaOnlyAfterBlobDeleteSucceeds(t *testing.T) {
	database := openTestDB(t)
	ctx := context.Background()
	workspaceID, userID := createServerTestWorkspace(t, database)
	baseImage := createServerTestItemImage(t, database, workspaceID, userID, "https://source.example/canvas/cleanup-quota")
	itemStore := store.NewItemStore(database)
	limits := store.StorageQuotaLimits{
		MaxBytesPerWorkspace:  100,
		MaxBytesTotal:         1 << 40,
		MaxItemsPerWorkspace:  100,
		MaxItemsTotal:         1000,
		MaxImagesPerWorkspace: 100,
		MaxImagesTotal:        1000,
		ReservationTTL:        time.Hour,
	}
	uploadName := strings.Repeat("d", 64) + "-" + uuid.NewString() + ".jpg"
	imageURL := "/static/uploads/" + uploadName
	var itemID string
	if err := database.QueryRow("SELECT item_id FROM item_images WHERE id = ?", baseImage.ID).Scan(&itemID); err != nil {
		t.Fatal(err)
	}
	uploadReservation, err := itemStore.ReserveStorageQuota(ctx, workspaceID, store.StorageQuotaRequest{Bytes: 80, Images: 1}, limits)
	if err != nil {
		t.Fatalf("reserve upload quota: %v", err)
	}
	t.Cleanup(func() { _ = itemStore.ReleaseStorageQuotaReservation(context.Background(), uploadReservation) })
	if err := itemStore.StageStorageQuotaUpload(ctx, uploadReservation, imageURL, 80, limits); err != nil {
		t.Fatalf("stage upload quota: %v", err)
	}
	image, err := itemStore.AddImage(ctx, dbstore.CreateItemImageParams{
		ItemID:       itemID,
		Sequence:     2,
		ImageURL:     imageURL,
		StorageBytes: 80,
		CanvasURI:    "https://source.example/canvas/cleanup-quota/upload/" + uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("persist uploaded image: %v", err)
	}
	if err := itemStore.ReleaseStorageQuotaReservation(ctx, uploadReservation); err != nil {
		t.Fatalf("release upload quota reservation: %v", err)
	}
	t.Cleanup(func() {
		_, _ = database.Exec("DELETE FROM resource_cleanup_outbox WHERE resource_key IN (?, ?)", uploadName, fmt.Sprint(image.ID))
	})
	if err := itemStore.DeleteItemImageForWorkspace(ctx, image.ID, workspaceID); err != nil {
		t.Fatalf("delete item image: %v", err)
	}
	if _, err := database.Exec("UPDATE resource_cleanup_outbox SET next_attempt_at = '2000-01-01 00:00:00' WHERE kind = 'upload_blob' AND resource_key = ?", uploadName); err != nil {
		t.Fatal(err)
	}

	deleteErr := errors.New("blob backend unavailable")
	deleteCalls := 0
	handler := &Handler{
		items:  itemStore,
		appCtx: context.Background(),
		deleteUploadBlob: func(context.Context, string) error {
			deleteCalls++
			return deleteErr
		},
	}
	handler.dispatchResourceCleanups(ctx)
	if deleteCalls != 1 {
		t.Fatalf("failed blob delete calls = %d, want 1", deleteCalls)
	}

	if _, err := itemStore.ReserveStorageQuota(ctx, workspaceID, store.StorageQuotaRequest{Bytes: 21}, limits); !errors.Is(err, store.ErrStorageQuotaExceeded) {
		t.Fatalf("reserve after failed blob delete error = %v, want ErrStorageQuotaExceeded", err)
	}
	var status string
	var cleanupWorkspaceID, cleanupBytes uint64
	if err := database.QueryRow(`SELECT status, workspace_id, storage_bytes
FROM resource_cleanup_outbox
WHERE kind = 'upload_blob' AND resource_key = ?`, uploadName).Scan(&status, &cleanupWorkspaceID, &cleanupBytes); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || cleanupWorkspaceID != workspaceID || cleanupBytes != 80 {
		t.Fatalf("failed cleanup state = %s, workspace %d, bytes %d", status, cleanupWorkspaceID, cleanupBytes)
	}

	if _, err := database.Exec("UPDATE resource_cleanup_outbox SET next_attempt_at = '2000-01-01 00:00:00' WHERE kind = 'upload_blob' AND resource_key = ?", uploadName); err != nil {
		t.Fatal(err)
	}
	handler.deleteUploadBlob = func(context.Context, string) error {
		deleteCalls++
		return nil
	}
	handler.dispatchResourceCleanups(ctx)
	if deleteCalls != 2 {
		t.Fatalf("total blob delete calls = %d, want 2", deleteCalls)
	}
	var cleanupCount int
	if err := database.QueryRow("SELECT COUNT(*) FROM resource_cleanup_outbox WHERE kind = 'upload_blob' AND resource_key = ?", uploadName).Scan(&cleanupCount); err != nil {
		t.Fatal(err)
	}
	if cleanupCount != 0 {
		t.Fatalf("completed upload cleanup rows = %d, want 0", cleanupCount)
	}
	capacityReservation, err := itemStore.ReserveStorageQuota(ctx, workspaceID, store.StorageQuotaRequest{Bytes: 100}, limits)
	if err != nil {
		t.Fatalf("reserve after successful blob delete: %v", err)
	}
	if err := itemStore.ReleaseStorageQuotaReservation(ctx, capacityReservation); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidUploadCleanupTombstoneCannotDeleteOrPoisonWorker(t *testing.T) {
	database := openTestDB(t)
	ctx := context.Background()
	workspaceID, _ := createServerTestWorkspace(t, database)
	resourceKey := "legacy-noncanonical.png"
	result, err := database.ExecContext(ctx, `
INSERT INTO resource_cleanup_outbox (
  kind, resource_key, workspace_id, storage_bytes, next_attempt_at
) VALUES ('upload_blob', ?, ?, 0, '2000-01-01 00:00:00')`, resourceKey, workspaceID)
	if err != nil {
		t.Fatalf("insert invalid cleanup tombstone: %v", err)
	}
	cleanupID, _ := result.LastInsertId()
	t.Cleanup(func() { _, _ = database.Exec(`DELETE FROM resource_cleanup_outbox WHERE id = ?`, cleanupID) })

	deleteCalls := 0
	handler := &Handler{
		items:  store.NewItemStore(database),
		appCtx: context.Background(),
		deleteUploadBlob: func(context.Context, string) error {
			deleteCalls++
			return nil
		},
	}
	handler.dispatchResourceCleanups(ctx)
	if deleteCalls != 0 {
		t.Fatalf("invalid cleanup invoked blob deletion %d times", deleteCalls)
	}
	var remaining int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM resource_cleanup_outbox WHERE id = ?`, cleanupID).Scan(&remaining); err != nil {
		t.Fatalf("count invalid cleanup after dispatch: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("invalid cleanup tombstones remaining = %d, want 0", remaining)
	}
}
