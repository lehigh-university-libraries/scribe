package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

func TestTranscriptionAdmissionBoundsTerminalHistoryAndDeletesChildrenFirst(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	canvasURI := "https://source.example/canvas/terminal-retention-" + suffix
	workspaceID, imageID := createAnnotationTestResource(t, database, suffix+"-terminal-retention", canvasURI)
	processingContext := createAnnotationTestContext(t, database, suffix+"-terminal-retention")
	if _, err := store.NewAnnotationStore(database).SavePage(ctx, canonicalTestPage(t, workspaceID, imageID, canvasURI, "baseline"), 0); err != nil {
		t.Fatalf("create terminal-retention canonical page: %v", err)
	}
	contextSnapshot, err := json.Marshal(processingContext)
	if err != nil {
		t.Fatalf("marshal terminal-retention context: %v", err)
	}

	jobIDs := make([]uint64, 0, store.MaxTerminalTranscriptionJobsPerWorkspace+1)
	for index := int64(0); index <= store.MaxTerminalTranscriptionJobsPerWorkspace; index++ {
		result, err := database.ExecContext(ctx, `
INSERT INTO transcription_jobs (
  workspace_id, item_image_id, context_id, context_scope_id, context_snapshot,
  input_revision, status, attempt_count, last_result_annotation_json, created_at, updated_at
) VALUES (?, ?, ?, 0, ?, 1, 'completed', 1, '{"transient":true}',
          DATE_ADD('2000-01-01 00:00:00', INTERVAL ? SECOND),
          DATE_ADD('2000-01-01 00:00:00', INTERVAL ? SECOND))`,
			workspaceID, imageID, processingContext.ID, contextSnapshot, index, index)
		if err != nil {
			t.Fatalf("seed terminal job %d: %v", index, err)
		}
		jobIDRaw, _ := result.LastInsertId()
		jobID := uint64(jobIDRaw)
		jobIDs = append(jobIDs, jobID)
		if _, err := database.ExecContext(ctx, `
INSERT INTO transcription_job_attempts (
  job_id, attempt_number, context_snapshot, input_revision, lease_owner, lease_token,
  outcome, result_revision, started_at, finished_at
) VALUES (?, 1, ?, 1, 'retention-test', ?, 'completed', 1,
          '2000-01-01 00:00:00', '2000-01-01 00:00:01')`,
			jobID, contextSnapshot, fmt.Sprintf("retention-%d-%s", index, suffix)); err != nil {
			t.Fatalf("seed terminal attempt %d: %v", index, err)
		}
	}

	var itemID string
	if err := database.QueryRowContext(ctx, `SELECT item_id FROM item_images WHERE id = ?`, imageID).Scan(&itemID); err != nil {
		t.Fatalf("load terminal-retention item: %v", err)
	}
	batchID := "retention-" + suffix
	if _, err := database.ExecContext(ctx, `
INSERT INTO upload_batches (
  workspace_id, id, item_id, context_id, context_scope_id, context_snapshot,
  request_hash, creation_token, status
) VALUES (?, ?, ?, ?, 0, ?, ?, ?, 'completed')`,
		workspaceID, batchID, itemID, processingContext.ID, contextSnapshot,
		fmt.Sprintf("%064x", 1), "retention-token-"+suffix); err != nil {
		t.Fatalf("seed retained upload batch: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO upload_batch_files (
  workspace_id, batch_id, sequence, filename, size, content_sha256, status,
  item_image_id, transcription_job_id
) VALUES (?, ?, 0, 'page.png', 1, ?, 'completed', ?, ?)`,
		workspaceID, batchID, fmt.Sprintf("%064x", 2), imageID, jobIDs[0]); err != nil {
		t.Fatalf("seed retained upload batch file: %v", err)
	}
	externalKey := "retention-" + suffix
	if _, err := database.ExecContext(ctx, `
INSERT INTO external_requests (
  workspace_id, source, idempotency_key, request_hash, status,
  item_id, item_image_id, transcription_job_id
) VALUES (?, 'retention-test', ?, ?, 'completed', ?, ?, ?)`,
		workspaceID, externalKey, fmt.Sprintf("%064x", 3), itemID, imageID, jobIDs[0]); err != nil {
		t.Fatalf("seed retained external request: %v", err)
	}

	activeJobID, err := store.NewTranscriptionJobStore(database).Create(ctx, imageID, processingContext)
	if err != nil || activeJobID == 0 {
		t.Fatalf("admit job with terminal retention = %d/%v", activeJobID, err)
	}
	var terminalCount, attemptCount, oldestCount int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcription_jobs WHERE workspace_id = ? AND status IN ('completed','failed','canceled','superseded')`, workspaceID).Scan(&terminalCount); err != nil {
		t.Fatalf("count retained terminal jobs: %v", err)
	}
	if terminalCount != int(store.MaxTerminalTranscriptionJobsPerWorkspace-1) {
		t.Fatalf("retained terminal jobs = %d, want %d", terminalCount, store.MaxTerminalTranscriptionJobsPerWorkspace-1)
	}
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcription_jobs WHERE id = ?`, jobIDs[0]).Scan(&oldestCount); err != nil {
		t.Fatalf("count oldest retained job: %v", err)
	}
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcription_job_attempts WHERE job_id = ?`, jobIDs[0]).Scan(&attemptCount); err != nil {
		t.Fatalf("count oldest retained attempts: %v", err)
	}
	if oldestCount != 0 || attemptCount != 0 {
		t.Fatalf("oldest retained graph = job:%d attempts:%d, want 0/0", oldestCount, attemptCount)
	}
	var batchJobID, externalJobID sql.NullInt64
	if err := database.QueryRowContext(ctx, `SELECT transcription_job_id FROM upload_batch_files WHERE workspace_id = ? AND batch_id = ? AND sequence = 0`, workspaceID, batchID).Scan(&batchJobID); err != nil {
		t.Fatalf("load retained upload batch link: %v", err)
	}
	if err := database.QueryRowContext(ctx, `SELECT transcription_job_id FROM external_requests WHERE workspace_id = ? AND source = 'retention-test' AND idempotency_key = ?`, workspaceID, externalKey).Scan(&externalJobID); err != nil {
		t.Fatalf("load retained external request link: %v", err)
	}
	if batchJobID.Valid || externalJobID.Valid {
		t.Fatalf("retained historical job links = batch:%+v external:%+v, want detached", batchJobID, externalJobID)
	}
	if violations, err := store.AuditRelationshipIntegrity(ctx, database); err != nil {
		t.Fatalf("audit retained terminal job graph: %v", err)
	} else if len(violations) != 0 {
		t.Fatalf("terminal retention left relationship violations: %+v", violations)
	}
}
