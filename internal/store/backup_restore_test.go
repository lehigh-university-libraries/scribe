package store_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/lehigh-university-libraries/scribe/internal/database"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

func TestBackupRestoreIntegrityAndJobRecovery(t *testing.T) {
	if os.Getenv("SCRIBE_RESTORE_SMOKE") != "1" {
		t.Skip("SCRIBE_RESTORE_SMOKE is not enabled")
	}
	dsn := strings.TrimSpace(os.Getenv("TEST_DSN"))
	if dsn == "" {
		t.Fatal("TEST_DSN is required for the restore smoke test")
	}

	db, err := database.NewPool(dsn, database.DefaultConfig())
	if err != nil {
		t.Fatalf("open restored database: %v", err)
	}
	defer db.Close()
	if err := database.Migrate(db); err != nil {
		t.Fatalf("restored schema is not reusable: %v", err)
	}

	ctx := context.Background()
	annotationStore := store.NewAnnotationStore(db)
	page, err := annotationStore.LoadPage(ctx, store.AnonymousWorkspaceID, 99001)
	if err != nil {
		t.Fatalf("load restored canonical page: %v", err)
	}
	if page.Revision != 7 || !strings.Contains(page.Payload, "Restored correction") {
		t.Fatalf("restored page revision/payload = %d/%q", page.Revision, page.Payload)
	}
	identity := iiif.PageIdentity{
		PublicBaseURL: "https://scribe.example",
		ItemImageID:   99001,
		CanvasURI:     "https://source.example/canvas/restore-smoke",
	}
	if err := iiif.ValidateCanonicalAnnotationPage([]byte(page.Payload), identity); err != nil {
		t.Fatalf("restored page is not valid canonical IIIF: %v", err)
	}
	published, err := annotationStore.LoadPublishedPage(ctx, 99001)
	if err != nil {
		t.Fatalf("load restored published page: %v", err)
	}
	if published.PublishedRevision != page.Revision || published.Payload != page.Payload {
		t.Fatalf("restored publication revision/payload = %d/%q, want canonical %d/%q", published.PublishedRevision, published.Payload, page.Revision, page.Payload)
	}
	if err := iiif.ValidateCanonicalAnnotationPage([]byte(published.Payload), identity); err != nil {
		t.Fatalf("restored publication is not valid canonical IIIF: %v", err)
	}
	index, err := annotationStore.SearchIndex(ctx, store.AnonymousWorkspaceID, 99001)
	if err != nil || len(index) != 1 || index[0].TextGranularity != "line" {
		t.Fatalf("restored derived index = %+v, %v", index, err)
	}
	mirror, err := annotationStore.ClaimAnnotationMirror(ctx, 0)
	if err != nil {
		t.Fatalf("run restored annotation mirror recovery: %v", err)
	}
	if mirror != nil {
		t.Fatalf("exhausted restored annotation mirror was reclaimed: %+v", mirror)
	}
	var mirrorStatus string
	if err := db.QueryRowContext(ctx, `SELECT status FROM annotation_mirror_outbox WHERE item_image_id = 99001`).Scan(&mirrorStatus); err != nil {
		t.Fatalf("load recovered annotation mirror: %v", err)
	}
	if mirrorStatus != "failed" {
		t.Fatalf("recovered annotation mirror status = %q; want failed", mirrorStatus)
	}

	jobStore := store.NewTranscriptionJobStore(db)
	job, err := jobStore.Get(ctx, 99001)
	if err != nil || job.Status != store.TranscriptionJobStatusRunning {
		t.Fatalf("restored leased job = %+v, %v", job, err)
	}
	if len(job.Attempts) != 1 || job.Attempts[0].AttemptNumber != 3 || job.Attempts[0].Outcome != store.TranscriptionAttemptRunning {
		t.Fatalf("restored attempt audit = %+v; want running attempt 3", job.Attempts)
	}
	claimed, err := jobStore.ClaimNextPending(ctx)
	if err != nil {
		t.Fatalf("run restored job recovery: %v", err)
	}
	if claimed != nil {
		t.Fatalf("exhausted restored job was reclaimed: %+v", claimed)
	}
	job, err = jobStore.Get(ctx, 99001)
	if err != nil || job.Status != store.TranscriptionJobStatusFailed {
		t.Fatalf("recovered job = %+v, %v; want failed", job, err)
	}
	if len(job.Attempts) != 1 || job.Attempts[0].AttemptNumber != 3 ||
		job.Attempts[0].Outcome != store.TranscriptionAttemptLeaseExpired ||
		job.Attempts[0].SafeErrorMessage != "worker lease expired" ||
		job.Attempts[0].FinishedAt == nil {
		t.Fatalf("recovered attempt audit = %+v; want terminal lease-expired attempt 3", job.Attempts)
	}
}
