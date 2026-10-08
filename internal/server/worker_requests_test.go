package server

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/jobqueue"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

func TestMaintenanceWakeupDrainsMoreThanOneCleanupBatch(t *testing.T) {
	database := openTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	workspace, _ := createServerTestWorkspace(t, database)
	for i := range 25 {
		// Noncanonical tombstones need only fenced bookkeeping, with no blob
		// deletion or external service. More than one batch must be drained.
		result, err := database.ExecContext(ctx, `INSERT INTO resource_cleanup_outbox (kind, resource_key, workspace_id, storage_bytes, next_attempt_at) VALUES ('upload_blob', ?, ?, 0, '2000-01-01 00:00:00')`, fmt.Sprintf("legacy-worker-%d-%d.png", workspace, i), workspace)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := result.LastInsertId()
		t.Cleanup(func() { _, _ = database.Exec(`DELETE FROM resource_cleanup_outbox WHERE id = ?`, id) })
	}
	handler := NewHandler(store.NewOCRRunStore(database), store.NewItemStore(database), store.NewContextStore(database), store.NewAnnotationStore(database), store.NewTranscriptionJobStore(database), nil, nil, nil, store.NewProviderCallAuditStore(database))
	handler.SetTranscriptionJobQueue(&jobqueue.PubSubTranscriptionQueue{})
	if err := handler.RunWorkerMaintenance(ctx); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM resource_cleanup_outbox WHERE workspace_id = ?`, workspace).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("maintenance responded with %d pending cleanups", remaining)
	}
}
