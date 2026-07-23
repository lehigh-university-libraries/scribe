package store_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

func TestTranscriptionJobListIsBoundedTenantScopedAndOmitsAttempts(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	workspaceID, imageID := createAnnotationTestResource(t, database, suffix+"-jobs", "https://source.example/canvas/"+suffix)
	otherWorkspaceID, otherImageID := createAnnotationTestResource(t, database, suffix+"-other-jobs", "https://source.example/canvas/other-"+suffix)
	createdAt := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)

	jobIDs := make([]uint64, 0, 3)
	for index := range 3 {
		result, err := database.ExecContext(ctx, `INSERT INTO transcription_jobs
  (workspace_id, item_image_id, context_snapshot, input_revision, status, attempt_count, created_at, updated_at)
VALUES (?, ?, '{}', 1, 'completed', 1, ?, ?)`, workspaceID, imageID, createdAt, createdAt)
		if err != nil {
			t.Fatalf("insert job %d: %v", index, err)
		}
		jobID, err := result.LastInsertId()
		if err != nil {
			t.Fatalf("read job id %d: %v", index, err)
		}
		jobIDs = append(jobIDs, uint64(jobID)) // #nosec G115 -- positive auto-increment test identifier.
	}
	largePayload := "never-return-list-payload-" + strings.Repeat("x", 1<<20)
	largeSnapshot := `{"secret":"` + strings.Repeat("s", 1<<20) + `"}`
	if _, err := database.ExecContext(ctx, `UPDATE transcription_jobs
SET context_snapshot = ?, current_annotation_json = ?, last_result_annotation_json = ?
WHERE id = ?`, largeSnapshot, largePayload, largePayload, jobIDs[2]); err != nil {
		t.Fatalf("seed large point-read payloads: %v", err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO transcription_job_attempts
  (job_id, attempt_number, context_snapshot, input_revision, lease_owner, lease_token, outcome, result_revision, started_at, finished_at)
VALUES (?, 1, '{}', 1, 'pagination-test', ?, 'completed', 1, ?, ?)`,
		jobIDs[2], "pagination-"+uuid.NewString(), createdAt, createdAt); err != nil {
		t.Fatalf("insert job attempt: %v", err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO transcription_jobs
  (workspace_id, item_image_id, context_snapshot, input_revision, status, created_at, updated_at)
VALUES (?, ?, '{}', 1, 'completed', ?, ?)`, otherWorkspaceID, otherImageID, createdAt, createdAt); err != nil {
		t.Fatalf("insert other-workspace job: %v", err)
	}

	jobs := store.NewTranscriptionJobStore(database)
	first, err := jobs.ListPage(ctx, workspaceID, 0, 2, nil)
	if err != nil {
		t.Fatalf("ListPage first: %v", err)
	}
	if len(first.Jobs) != 2 || first.NextCursor == nil || first.Jobs[0].ID != jobIDs[2] || first.Jobs[1].ID != jobIDs[1] {
		t.Fatalf("first page = %+v, cursor %+v", first.Jobs, first.NextCursor)
	}
	for _, job := range first.Jobs {
		if job.WorkspaceID != workspaceID {
			t.Fatalf("list summary for job %d has workspace %d, want %d", job.ID, job.WorkspaceID, workspaceID)
		}
	}
	second, err := jobs.ListPage(ctx, workspaceID, 0, 2, first.NextCursor)
	if err != nil {
		t.Fatalf("ListPage second: %v", err)
	}
	if len(second.Jobs) != 1 || second.NextCursor != nil || second.Jobs[0].ID != jobIDs[0] {
		t.Fatalf("second page = %+v, cursor %+v", second.Jobs, second.NextCursor)
	}
	filtered, err := jobs.ListPage(ctx, workspaceID, imageID, store.MaxTranscriptionJobPageSize, nil)
	if err != nil || len(filtered.Jobs) != 3 {
		t.Fatalf("image-filtered jobs = %+v/%v", filtered.Jobs, err)
	}
	crossTenantFilter, err := jobs.ListPage(ctx, workspaceID, otherImageID, store.MaxTranscriptionJobPageSize, nil)
	if err != nil || len(crossTenantFilter.Jobs) != 0 {
		t.Fatalf("cross-tenant image-filtered jobs = %+v/%v, want none", crossTenantFilter.Jobs, err)
	}
	other, err := jobs.ListPage(ctx, otherWorkspaceID, 0, store.MaxTranscriptionJobPageSize, nil)
	if err != nil || len(other.Jobs) != 1 || other.Jobs[0].ItemImageID != otherImageID {
		t.Fatalf("other workspace jobs = %+v/%v", other.Jobs, err)
	}
	loaded, err := jobs.Get(ctx, jobIDs[2])
	if err != nil || len(loaded.Attempts) != 1 || loaded.CurrentAnnotationJSON != largePayload || loaded.LastResultAnnotationJSON != largePayload {
		t.Fatalf("Get job attempt audit = %+v/%v", loaded.Attempts, err)
	}
	if _, err := jobs.ListPage(ctx, workspaceID, 0, store.MaxTranscriptionJobPageSize+1, nil); err == nil {
		t.Fatal("store accepted an oversized transcription job page")
	}
	if _, err := jobs.ListPage(ctx, workspaceID, 0, 1, &store.TranscriptionJobPageCursor{}); err == nil {
		t.Fatal("store accepted an empty transcription job cursor")
	}
}
