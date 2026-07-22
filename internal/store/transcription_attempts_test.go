package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

func TestTranscriptionAttemptsFenceExpiredWorkersAndCompleteAtomically(t *testing.T) {
	databasePool := annotationTestDB(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	canvasURI := "https://source.example/canvas/attempt-fence-" + suffix
	processingContext := createAnnotationTestContext(t, databasePool, suffix+"-attempt-fence")
	workspaceID, imageID := createAnnotationTestResource(t, databasePool, suffix+"-attempt-fence", canvasURI)
	annotationStore := store.NewAnnotationStore(databasePool)
	jobStore := store.NewTranscriptionJobStore(databasePool)

	page, err := annotationStore.SavePage(ctx, canonicalTestPage(t, workspaceID, imageID, canvasURI, "original"), 0)
	if err != nil {
		t.Fatalf("SavePage: %v", err)
	}
	jobID, err := jobStore.Create(ctx, imageID, processingContext)
	if err != nil {
		t.Fatalf("Create transcription job: %v", err)
	}
	first, err := jobStore.ClaimPendingByID(ctx, jobID)
	if err != nil || first == nil {
		t.Fatalf("first ClaimPendingByID = %+v, %v", first, err)
	}
	firstFence, err := first.Fence()
	if err != nil {
		t.Fatalf("first Fence: %v", err)
	}
	if firstFence.InputRevision != page.Revision || firstFence.AttemptNumber != 1 {
		t.Fatalf("first fence = %+v, want revision %d attempt 1", firstFence, page.Revision)
	}

	result, err := databasePool.ExecContext(ctx, `
		UPDATE transcription_jobs
		SET lease_until = DATE_SUB(NOW(), INTERVAL 1 SECOND)
		WHERE id = ? AND locked_by = ?`, jobID, firstFence.LeaseToken)
	if err != nil {
		t.Fatalf("expire first lease: %v", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		t.Fatalf("expired lease rows = %d, want 1", affected)
	}

	second, err := jobStore.ClaimPendingByID(ctx, jobID)
	if err != nil || second == nil {
		t.Fatalf("second ClaimPendingByID = %+v, %v", second, err)
	}
	secondFence, err := second.Fence()
	if err != nil {
		t.Fatalf("second Fence: %v", err)
	}
	if secondFence.AttemptNumber != 2 || secondFence.LeaseToken == firstFence.LeaseToken || secondFence.InputRevision != firstFence.InputRevision {
		t.Fatalf("second fence = %+v, first = %+v", secondFence, firstFence)
	}
	assertAttemptHistory(t, second.Attempts, []store.TranscriptionAttemptOutcome{
		store.TranscriptionAttemptLeaseExpired,
		store.TranscriptionAttemptRunning,
	})
	if second.Attempts[0].SafeErrorMessage != "worker lease expired" || second.Attempts[0].FinishedAt == nil {
		t.Fatalf("expired attempt audit = %+v", second.Attempts[0])
	}
	if second.Attempts[0].InputRevision != second.Attempts[1].InputRevision ||
		string(second.Attempts[0].ContextSnapshot) != string(second.Attempts[1].ContextSnapshot) {
		t.Fatalf("attempt immutable inputs drifted: %+v", second.Attempts)
	}
	otherWorkspaceID, otherImageID := createAnnotationTestResource(t, databasePool, suffix+"-attempt-fence-other", "https://source.example/canvas/attempt-fence-other-"+suffix)
	otherPage, err := annotationStore.SavePage(ctx, canonicalTestPage(
		t,
		otherWorkspaceID,
		otherImageID,
		"https://source.example/canvas/attempt-fence-other-"+suffix,
		"other workspace",
	), 0)
	if err != nil {
		t.Fatalf("SavePage for other workspace: %v", err)
	}
	otherMutation := otherPage
	otherMutation.Payload = replacePageText(t, otherPage.Payload, "cross-workspace worker output")
	if _, err := annotationStore.SavePageAndCompleteTranscriptionJob(ctx, otherMutation, otherPage.Revision, store.AnnotationJobCompletion{
		TranscriptionAttemptFence: secondFence,
	}); !errors.Is(err, store.ErrAnnotationJobFence) {
		t.Fatalf("cross-workspace completion error = %v, want ErrAnnotationJobFence", err)
	}
	otherUnchanged, err := annotationStore.LoadPage(ctx, otherWorkspaceID, otherImageID)
	if err != nil {
		t.Fatalf("LoadPage for other workspace: %v", err)
	}
	assertPageText(t, otherUnchanged.Payload, "other workspace")
	var storedOwner, storedToken string
	if err := databasePool.QueryRowContext(ctx, `
		SELECT lease_owner, lease_token
		FROM transcription_job_attempts
		WHERE job_id = ? AND attempt_number = 2`, jobID).Scan(&storedOwner, &storedToken); err != nil {
		t.Fatalf("load stored attempt identity: %v", err)
	}
	if storedOwner == "" || storedToken != secondFence.LeaseToken {
		t.Fatalf("stored attempt owner/token = %q/%q", storedOwner, storedToken)
	}

	if err := jobStore.UpdateProgress(ctx, firstFence, 1, 0, "stale", "{}", "{}"); !errors.Is(err, store.ErrTranscriptionJobFence) {
		t.Fatalf("stale UpdateProgress error = %v, want ErrTranscriptionJobFence", err)
	}
	if err := jobStore.Fail(ctx, firstFence, "untrusted stale failure details"); !errors.Is(err, store.ErrTranscriptionJobFence) {
		t.Fatalf("stale Fail error = %v, want ErrTranscriptionJobFence", err)
	}

	stalePage := page
	stalePage.Payload = replacePageText(t, page.Payload, "stale worker output")
	if _, err := annotationStore.SavePageAndCompleteTranscriptionJob(ctx, stalePage, page.Revision, store.AnnotationJobCompletion{
		TranscriptionAttemptFence: firstFence,
	}); !errors.Is(err, store.ErrAnnotationJobFence) {
		t.Fatalf("stale completion error = %v, want ErrAnnotationJobFence", err)
	}
	unchanged, err := annotationStore.LoadPage(ctx, workspaceID, imageID)
	if err != nil {
		t.Fatalf("LoadPage after stale completion: %v", err)
	}
	assertPageText(t, unchanged.Payload, "original")

	currentPage := page
	currentPage.Payload = replacePageText(t, page.Payload, "current worker output")
	completedPage, err := annotationStore.SavePageAndCompleteTranscriptionJob(ctx, currentPage, page.Revision, store.AnnotationJobCompletion{
		TranscriptionAttemptFence: secondFence,
	})
	if err != nil {
		t.Fatalf("current completion: %v", err)
	}
	completed, err := jobStore.Get(ctx, jobID)
	if err != nil {
		t.Fatalf("Get completed job: %v", err)
	}
	if completed.Status != store.TranscriptionJobStatusCompleted || completedPage.Revision != page.Revision+1 {
		t.Fatalf("completed job/page = %+v/%d", completed, completedPage.Revision)
	}
	assertAttemptHistory(t, completed.Attempts, []store.TranscriptionAttemptOutcome{
		store.TranscriptionAttemptLeaseExpired,
		store.TranscriptionAttemptCompleted,
	})
	if completed.Attempts[1].ResultRevision == nil || *completed.Attempts[1].ResultRevision != completedPage.Revision || completed.Attempts[1].FinishedAt == nil {
		t.Fatalf("completed attempt audit = %+v", completed.Attempts[1])
	}
}

func TestTranscriptionRetryAttemptsRemainImmutableAndErrorsAreCategorical(t *testing.T) {
	databasePool := annotationTestDB(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	canvasURI := "https://source.example/canvas/attempt-retry-" + suffix
	processingContext := createAnnotationTestContext(t, databasePool, suffix+"-attempt-retry")
	workspaceID, imageID := createAnnotationTestResource(t, databasePool, suffix+"-attempt-retry", canvasURI)
	annotationStore := store.NewAnnotationStore(databasePool)
	jobStore := store.NewTranscriptionJobStore(databasePool)
	if _, err := annotationStore.SavePage(ctx, canonicalTestPage(t, workspaceID, imageID, canvasURI, "input"), 0); err != nil {
		t.Fatalf("SavePage: %v", err)
	}
	jobID, err := jobStore.Create(ctx, imageID, processingContext)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := databasePool.ExecContext(ctx, `UPDATE transcription_jobs SET max_attempts = 2 WHERE id = ?`, jobID); err != nil {
		t.Fatalf("set retry budget: %v", err)
	}
	first, err := jobStore.ClaimPendingByID(ctx, jobID)
	if err != nil || first == nil {
		t.Fatalf("first claim = %+v, %v", first, err)
	}
	firstFence, _ := first.Fence()
	if err := jobStore.SetTotalSegments(ctx, firstFence, 7); err != nil {
		t.Fatalf("seed first-attempt total: %v", err)
	}
	if err := jobStore.UpdateProgress(ctx, firstFence, 3, 2, "line-4", `{"id":"line-4"}`, `{"body":"partial"}`); err != nil {
		t.Fatalf("seed first-attempt progress: %v", err)
	}
	secretDetail := "provider response contained secret-token-123"
	if err := jobStore.Fail(ctx, firstFence, secretDetail); err != nil {
		t.Fatalf("first Fail: %v", err)
	}
	if _, err := databasePool.ExecContext(ctx, `UPDATE transcription_jobs SET retry_after = DATE_SUB(NOW(), INTERVAL 1 SECOND) WHERE id = ?`, jobID); err != nil {
		t.Fatalf("make retry due: %v", err)
	}
	second, err := jobStore.ClaimPendingByID(ctx, jobID)
	if err != nil || second == nil {
		t.Fatalf("second claim = %+v, %v", second, err)
	}
	if second.TotalSegments != 0 || second.CompletedSegments != 0 || second.FailedSegments != 0 ||
		second.CurrentAnnotationID != "" || second.CurrentAnnotationJSON != "" || second.LastResultAnnotationJSON != "" {
		t.Fatalf("second attempt retained mutable progress from first attempt: %+v", second)
	}
	assertAttemptHistory(t, second.Attempts, []store.TranscriptionAttemptOutcome{
		store.TranscriptionAttemptRetryableFailed,
		store.TranscriptionAttemptRunning,
	})
	if second.Attempts[0].SafeErrorMessage != "transcription provider request failed" {
		t.Fatalf("safe retry error = %q", second.Attempts[0].SafeErrorMessage)
	}
	if second.Attempts[0].SafeErrorMessage == secretDetail {
		t.Fatal("attempt history persisted untrusted provider content")
	}
	secondFence, _ := second.Fence()
	if err := jobStore.Fail(ctx, secondFence, secretDetail); err != nil {
		t.Fatalf("terminal Fail: %v", err)
	}
	failed, err := jobStore.Get(ctx, jobID)
	if err != nil {
		t.Fatalf("Get failed job: %v", err)
	}
	if failed.Status != store.TranscriptionJobStatusFailed || failed.ErrorMessage != "transcription provider request failed" {
		t.Fatalf("failed job = %+v", failed)
	}
	assertAttemptHistory(t, failed.Attempts, []store.TranscriptionAttemptOutcome{
		store.TranscriptionAttemptRetryableFailed,
		store.TranscriptionAttemptFailed,
	})
}

func assertAttemptHistory(t *testing.T, attempts []store.TranscriptionJobAttempt, outcomes []store.TranscriptionAttemptOutcome) {
	t.Helper()
	if len(attempts) != len(outcomes) {
		t.Fatalf("attempt history length = %d, want %d: %+v", len(attempts), len(outcomes), attempts)
	}
	for index, outcome := range outcomes {
		if attempts[index].AttemptNumber != uint32(index+1) || attempts[index].Outcome != outcome {
			t.Fatalf("attempt[%d] = %+v, want number %d outcome %q", index, attempts[index], index+1, outcome)
		}
	}
}
