package server

import (
	"context"
	"testing"
)

func TestTranscriptionAttemptCompletionToleratesBackwardClock(t *testing.T) {
	ocr := &checkpointOCR{transcribe: func(context.Context, int) (string, error) { return "unused", nil }}
	h, id := checkpointJob(t, ocr, 1, 2)
	ctx := context.Background()
	if _, err := h.transcriptionJobs.ClaimPendingByID(ctx, id); err != nil {
		t.Fatal(err)
	}
	// Emulate the database wall clock moving backward after the claim.
	if _, err := openTestDB(t).ExecContext(ctx, "UPDATE transcription_job_attempts SET started_at = DATE_ADD(NOW(6), INTERVAL 1 SECOND) WHERE job_id = ?", id); err != nil {
		t.Fatal(err)
	}
	if err := h.transcriptionJobs.Cancel(ctx, id); err != nil {
		t.Fatal(err)
	}
	job, err := h.transcriptionJobs.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(job.Attempts) != 1 || job.Attempts[0].FinishedAt == nil || job.Attempts[0].FinishedAt.Before(job.Attempts[0].StartedAt) {
		t.Fatal("attempt audit finish precedes start")
	}
}
