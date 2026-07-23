package server

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	ocrhandlers "github.com/lehigh-university-libraries/scribe/internal/handlers"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

func TestProcessingFailureQueuesCleanupAfterRequestCancellation(t *testing.T) {
	database := openTestDB(t)
	itemStore := store.NewItemStore(database)
	handler := &Handler{items: itemStore, appCtx: context.Background()}
	uploadName := strings.Repeat("a", 64) + "-" + uuid.NewString() + ".png"
	imageURL := "/static/uploads/" + uploadName
	t.Cleanup(func() {
		_, _ = database.Exec(`DELETE FROM resource_cleanup_outbox WHERE kind = 'upload_blob' AND resource_key = ?`, uploadName)
	})

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	handler.queueUploadFromProcessingError(canceledCtx, &ocrhandlers.StoredUploadError{
		ImageURL:    imageURL,
		StoredBytes: 4096,
		Err:         errors.New("processing failed after storage write"),
	})

	var workspaceID, storageBytes uint64
	if err := database.QueryRow(
		`SELECT workspace_id, storage_bytes FROM resource_cleanup_outbox WHERE kind = 'upload_blob' AND resource_key = ?`,
		uploadName,
	).Scan(&workspaceID, &storageBytes); err != nil {
		t.Fatalf("load durable cleanup: %v", err)
	}
	if workspaceID != store.AnonymousWorkspaceID || storageBytes != 4096 {
		t.Fatalf("durable cleanup accounting = workspace %d / %d bytes, want %d / 4096", workspaceID, storageBytes, store.AnonymousWorkspaceID)
	}
}
