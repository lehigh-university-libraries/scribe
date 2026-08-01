package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

func TestClaimableQueueSnapshotUsesWorkerEligibilityAndDatabaseAge(t *testing.T) {
	databasePool := annotationTestDB(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	canvasURI := "https://source.example/canvas/queue-observability-" + suffix
	workspaceID, imageID := createAnnotationTestResource(t, databasePool, suffix, canvasURI)
	processingContext := createAnnotationTestContext(t, databasePool, suffix)
	if _, err := store.NewAnnotationStore(databasePool).SavePage(
		ctx,
		canonicalTestPage(t, workspaceID, imageID, canvasURI, "queued"),
		0,
	); err != nil {
		t.Fatalf("SavePage: %v", err)
	}
	jobStore := store.NewTranscriptionJobStore(databasePool)
	baseline, err := jobStore.ClaimableQueueSnapshot(ctx)
	if err != nil {
		t.Fatalf("ClaimableQueueSnapshot(baseline): %v", err)
	}
	jobID, err := jobStore.Create(ctx, imageID, processingContext)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := databasePool.ExecContext(ctx, `UPDATE transcription_jobs SET created_at = CURRENT_TIMESTAMP() - INTERVAL 2 MINUTE WHERE id = ?`, jobID); err != nil {
		t.Fatalf("age queued job: %v", err)
	}

	snapshot, err := jobStore.ClaimableQueueSnapshot(ctx)
	if err != nil {
		t.Fatalf("ClaimableQueueSnapshot(pending): %v", err)
	}
	if snapshot.Depth != baseline.Depth+1 || snapshot.OldestAge < 2*time.Minute || snapshot.ExpiredLeases != baseline.ExpiredLeases {
		t.Fatalf("pending snapshot = %+v, baseline = %+v; want one additional old claimable job", snapshot, baseline)
	}

	claimed, err := jobStore.ClaimPendingByID(ctx, jobID)
	if err != nil || claimed == nil {
		t.Fatalf("ClaimPendingByID = %+v, %v", claimed, err)
	}
	snapshot, err = jobStore.ClaimableQueueSnapshot(ctx)
	if err != nil {
		t.Fatalf("ClaimableQueueSnapshot(running): %v", err)
	}
	if snapshot.Depth != baseline.Depth || snapshot.ExpiredLeases != baseline.ExpiredLeases {
		t.Fatalf("leased snapshot = %+v, baseline = %+v; leased job must not remain claimable", snapshot, baseline)
	}
	if baseline.Depth == 0 && snapshot.OldestAge != 0 {
		t.Fatalf("leased snapshot oldest age = %s, want zero with an empty baseline", snapshot.OldestAge)
	}

	if _, err := databasePool.ExecContext(ctx, `UPDATE transcription_jobs SET lease_until = CURRENT_TIMESTAMP() - INTERVAL 1 SECOND WHERE id = ?`, jobID); err != nil {
		t.Fatalf("expire job lease: %v", err)
	}
	snapshot, err = jobStore.ClaimableQueueSnapshot(ctx)
	if err != nil {
		t.Fatalf("ClaimableQueueSnapshot(expired): %v", err)
	}
	if snapshot.Depth != baseline.Depth+1 || snapshot.OldestAge < 2*time.Minute || snapshot.ExpiredLeases != baseline.ExpiredLeases+1 {
		t.Fatalf("expired snapshot = %+v, baseline = %+v; want one additional old claimable lease", snapshot, baseline)
	}
}

func TestClaimableQueueSnapshotRequiresStore(t *testing.T) {
	t.Parallel()

	if _, err := (*store.TranscriptionJobStore)(nil).ClaimableQueueSnapshot(context.Background()); err == nil {
		t.Fatal("nil store snapshot succeeded")
	}
}
