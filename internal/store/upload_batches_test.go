package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

func TestUploadBatchJobAdmissionUsesImmutableBatchContext(t *testing.T) {
	databasePool := annotationTestDB(t)
	ctx := context.Background()
	userID, workspaceID := createUploadBatchIdentity(t, databasePool)
	contextStore := store.NewContextStore(databasePool)
	original, err := contextStore.Create(ctx, store.Context{
		UserID:                &userID,
		WorkspaceID:           &workspaceID,
		Name:                  "immutable-batch-job-context-" + uuid.NewString(),
		SegmentationModel:     "tesseract",
		TranscriptionProvider: "tesseract",
		TranscriptionModel:    "eng",
		SystemPrompt:          "selected at batch creation",
	})
	if err != nil {
		t.Fatalf("create processing context: %v", err)
	}
	itemStore := store.NewItemStore(databasePool)
	jobStore := store.NewTranscriptionJobStore(databasePool)
	batchID := "immutable-context-batch-" + uuid.NewString()
	digestOne := fmt.Sprintf("%064x", 71)
	digestTwo := fmt.Sprintf("%064x", 72)
	if _, err := itemStore.StartUploadBatch(ctx, store.StartUploadBatchParams{
		WorkspaceID: workspaceID,
		UserID:      userID,
		BatchID:     batchID,
		ItemID:      "item_" + uuid.NewString(),
		Name:        "Immutable context batch",
		Context:     original,
		RequestHash: fmt.Sprintf("%064x", 73),
		Files: []store.UploadBatchFileInput{
			{Filename: "one.png", Size: 4, ContentSHA256: digestOne},
			{Filename: "two.png", Size: 4, ContentSHA256: digestTwo},
		},
	}); err != nil {
		t.Fatalf("start upload batch: %v", err)
	}

	mutated := original
	mutated.SegmentationModel = "changed-segmentor"
	mutated.TranscriptionModel = "changed-model"
	mutated.SystemPrompt = "changed after batch creation"
	if _, err := contextStore.UpdateForWorkspace(ctx, mutated, workspaceID, userID); err != nil {
		t.Fatalf("mutate live context: %v", err)
	}

	_, firstFile, claimed, err := itemStore.ClaimUploadBatchFile(ctx, workspaceID, batchID, 1, 4, digestOne)
	if err != nil || !claimed {
		t.Fatalf("claim first file = %+v/%t/%v", firstFile, claimed, err)
	}
	firstImage, err := itemStore.EnsureUploadBatchImage(
		ctx, workspaceID, batchID, 1, firstFile.LeaseOwner,
		"https://images.example/immutable-context-one.png", 0, 100, 100, "https://scribe.example",
	)
	if err != nil {
		t.Fatalf("ensure first image: %v", err)
	}
	initializeUploadBatchTestPage(t, databasePool, workspaceID, firstImage.ID)
	if _, err := jobStore.CreateForUploadBatchFile(ctx, workspaceID, batchID, 1, firstFile.LeaseOwner+"-forged", firstImage.ID); !errors.Is(err, store.ErrUploadBatchFileFence) {
		t.Fatalf("forged file lease error = %v, want ErrUploadBatchFileFence", err)
	}
	firstJobID, err := jobStore.CreateForUploadBatchFile(ctx, workspaceID, batchID, 1, firstFile.LeaseOwner, firstImage.ID)
	if err != nil {
		t.Fatalf("create first batch job: %v", err)
	}
	assertUploadBatchJobContext(t, jobStore, firstJobID, original)
	if _, err := itemStore.CompleteUploadBatchFile(ctx, workspaceID, batchID, 1, firstFile.LeaseOwner, firstImage.ID, firstJobID); err != nil {
		t.Fatalf("complete first file: %v", err)
	}

	if err := contextStore.DeleteForWorkspace(ctx, original.ID, workspaceID); err != nil {
		t.Fatalf("delete selected context: %v", err)
	}
	_, secondFile, claimed, err := itemStore.ClaimUploadBatchFile(ctx, workspaceID, batchID, 2, 4, digestTwo)
	if err != nil || !claimed {
		t.Fatalf("claim second file = %+v/%t/%v", secondFile, claimed, err)
	}
	secondImage, err := itemStore.EnsureUploadBatchImage(
		ctx, workspaceID, batchID, 2, secondFile.LeaseOwner,
		"https://images.example/immutable-context-two.png", 0, 100, 100, "https://scribe.example",
	)
	if err != nil {
		t.Fatalf("ensure second image: %v", err)
	}
	initializeUploadBatchTestPage(t, databasePool, workspaceID, secondImage.ID)
	secondJobID, err := jobStore.CreateForUploadBatchFile(ctx, workspaceID, batchID, 2, secondFile.LeaseOwner, secondImage.ID)
	if err != nil {
		t.Fatalf("create detached-context batch job: %v", err)
	}
	secondJob := assertUploadBatchJobContext(t, jobStore, secondJobID, original)
	if secondJob.ContextID != nil {
		t.Fatalf("detached batch job retained deleted context id %d", *secondJob.ContextID)
	}
}

func assertUploadBatchJobContext(t *testing.T, jobs *store.TranscriptionJobStore, jobID uint64, want store.Context) store.TranscriptionJob {
	t.Helper()
	job, err := jobs.Get(context.Background(), jobID)
	if err != nil {
		t.Fatalf("load upload batch job %d: %v", jobID, err)
	}
	var snapshot store.Context
	if err := json.Unmarshal(job.ContextSnapshot, &snapshot); err != nil {
		t.Fatalf("decode upload batch job %d context: %v", jobID, err)
	}
	if snapshot.ID != want.ID || snapshot.Name != want.Name || snapshot.SegmentationModel != want.SegmentationModel ||
		snapshot.TranscriptionProvider != want.TranscriptionProvider || snapshot.TranscriptionModel != want.TranscriptionModel ||
		snapshot.SystemPrompt != want.SystemPrompt {
		t.Fatalf("upload batch job %d context = %+v, want immutable %+v", jobID, snapshot, want)
	}
	return job
}

func TestUploadBatchDurableResumeRetryAndCancellationFence(t *testing.T) {
	databasePool := annotationTestDB(t)
	ctx := context.Background()
	userID, workspaceID := createUploadBatchIdentity(t, databasePool)
	contextStore := store.NewContextStore(databasePool)
	processingContext, err := contextStore.Create(ctx, store.Context{
		UserID:                &userID,
		WorkspaceID:           &workspaceID,
		Name:                  "batch-context-" + uuid.NewString(),
		SegmentationModel:     "tesseract",
		TranscriptionProvider: "tesseract",
		TranscriptionModel:    "eng",
	})
	if err != nil {
		t.Fatalf("create processing context: %v", err)
	}
	itemStore := store.NewItemStore(databasePool)
	jobStore := store.NewTranscriptionJobStore(databasePool)
	batchID := "batch-" + uuid.NewString()
	requestHash := fmt.Sprintf("%064x", 11)
	params := store.StartUploadBatchParams{
		WorkspaceID: workspaceID,
		UserID:      userID,
		BatchID:     batchID,
		ItemID:      "item_" + uuid.NewString(),
		Name:        "Durable batch",
		Context:     processingContext,
		RequestHash: requestHash,
		Files: []store.UploadBatchFileInput{
			{Filename: "page-1.png", Size: 5, ContentSHA256: fmt.Sprintf("%064x", 1)},
			{Filename: "page-2.png", Size: 6, ContentSHA256: fmt.Sprintf("%064x", 2)},
		},
	}
	batch, err := itemStore.StartUploadBatch(ctx, params)
	if err != nil {
		t.Fatalf("StartUploadBatch: %v", err)
	}
	if batch.Status != store.UploadBatchStatusInProgress || len(batch.Files) != 2 || batch.ContextID != processingContext.ID {
		t.Fatalf("initial batch = %+v", batch)
	}

	replayParams := params
	replayParams.ItemID = "item_" + uuid.NewString()
	replayed, err := itemStore.StartUploadBatch(ctx, replayParams)
	if err != nil {
		t.Fatalf("replay StartUploadBatch: %v", err)
	}
	if replayed.ItemID != batch.ItemID {
		t.Fatalf("replayed item = %q, want %q", replayed.ItemID, batch.ItemID)
	}
	mismatchParams := replayParams
	mismatchParams.ItemID = "item_" + uuid.NewString()
	mismatchParams.RequestHash = fmt.Sprintf("%064x", 99)
	if _, err := itemStore.StartUploadBatch(ctx, mismatchParams); !errors.Is(err, store.ErrUploadBatchRequestMismatch) {
		t.Fatalf("mismatched StartUploadBatch error = %v, want ErrUploadBatchRequestMismatch", err)
	}

	_, claimedFile, claimed, err := itemStore.ClaimUploadBatchFile(ctx, workspaceID, batchID, 1, 5, fmt.Sprintf("%064x", 1))
	if err != nil || !claimed || claimedFile.AttemptCount != 1 || claimedFile.LeaseOwner == "" {
		t.Fatalf("first ClaimUploadBatchFile = %+v/%t/%v", claimedFile, claimed, err)
	}
	failedUploadName := immutableUploadTestName(fmt.Sprintf("%064x", 1))
	failedImage, err := itemStore.EnsureUploadBatchImage(ctx, workspaceID, batchID, 1, claimedFile.LeaseOwner, "/static/uploads/"+failedUploadName, 5, 100, 100, "https://scribe.example")
	if err != nil {
		t.Fatalf("ensure failed attempt image: %v", err)
	}
	failedJobID := createUploadBatchTestJob(t, databasePool, workspaceID, failedImage.ID, processingContext)
	t.Cleanup(func() {
		_, _ = databasePool.Exec(`DELETE FROM resource_cleanup_outbox WHERE resource_key IN (?, ?)`, failedUploadName, fmt.Sprint(failedImage.ID))
	})
	if err := itemStore.AbortUploadBatchFileAttempt(ctx, workspaceID, batchID, 1, claimedFile.LeaseOwner, "processing failed"); err != nil {
		t.Fatalf("AbortUploadBatchFileAttempt: %v", err)
	}
	if _, err := itemStore.GetImageForWorkspace(ctx, failedImage.ID, workspaceID); err == nil {
		t.Fatal("owned attempt abort left its provisional image")
	}
	if _, err := jobStore.Get(ctx, failedJobID); err == nil {
		t.Fatal("owned attempt abort left its provisional job")
	}
	partial, err := itemStore.GetUploadBatch(ctx, workspaceID, batchID)
	if err != nil || partial.FailedFiles() != 1 || partial.CompletedFiles() != 0 {
		t.Fatalf("partial batch = %+v/%v", partial, err)
	}
	_, retriedFile, claimed, err := itemStore.ClaimUploadBatchFile(ctx, workspaceID, batchID, 1, 5, fmt.Sprintf("%064x", 1))
	if err != nil || !claimed || retriedFile.AttemptCount != 2 || retriedFile.LeaseOwner == claimedFile.LeaseOwner {
		t.Fatalf("retry ClaimUploadBatchFile = %+v/%t/%v", retriedFile, claimed, err)
	}

	completedUploadName := immutableUploadTestName(fmt.Sprintf("%064x", 1))
	image, err := itemStore.EnsureUploadBatchImage(ctx, workspaceID, batchID, 1, retriedFile.LeaseOwner, "/static/uploads/"+completedUploadName, 5, 100, 100, "https://scribe.example")
	if err != nil {
		t.Fatalf("AddImage: %v", err)
	}
	jobID := createUploadBatchTestJob(t, databasePool, workspaceID, image.ID, processingContext)
	batch, err = itemStore.CompleteUploadBatchFile(ctx, workspaceID, batchID, 1, retriedFile.LeaseOwner, image.ID, jobID)
	if err != nil || batch.CompletedFiles() != 1 {
		t.Fatalf("CompleteUploadBatchFile = %+v/%v", batch, err)
	}
	replayedBatch, replayedFile, claimed, err := itemStore.ClaimUploadBatchFile(ctx, workspaceID, batchID, 1, 5, fmt.Sprintf("%064x", 1))
	if err != nil || claimed || replayedFile.ItemImageID != image.ID || replayedFile.TranscriptionJobID != jobID || replayedBatch.CompletedFiles() != 1 {
		t.Fatalf("completed file replay = %+v/%+v/%t/%v", replayedBatch, replayedFile, claimed, err)
	}
	runningJob, err := jobStore.ClaimPendingByID(ctx, jobID)
	if err != nil || runningJob == nil {
		t.Fatalf("claim completed batch file job = %+v/%v", runningJob, err)
	}
	runningFence, err := runningJob.Fence()
	if err != nil {
		t.Fatalf("fence completed batch file job: %v", err)
	}

	_, inFlight, claimed, err := itemStore.ClaimUploadBatchFile(ctx, workspaceID, batchID, 2, 6, fmt.Sprintf("%064x", 2))
	if err != nil || !claimed {
		t.Fatalf("claim second file = %+v/%t/%v", inFlight, claimed, err)
	}
	incompleteUploadName := immutableUploadTestName(fmt.Sprintf("%064x", 2))
	incompleteImage, err := itemStore.EnsureUploadBatchImage(ctx, workspaceID, batchID, 2, inFlight.LeaseOwner, "/static/uploads/"+incompleteUploadName, 6, 100, 100, "https://scribe.example")
	if err != nil {
		t.Fatalf("ensure incomplete second image: %v", err)
	}
	t.Cleanup(func() {
		_, _ = databasePool.Exec(
			`DELETE FROM resource_cleanup_outbox WHERE resource_key IN (?, ?)`,
			incompleteUploadName,
			fmt.Sprint(incompleteImage.ID),
		)
	})
	canceled, err := itemStore.CancelUploadBatch(ctx, workspaceID, batchID)
	if err != nil {
		t.Fatalf("CancelUploadBatch: %v", err)
	}
	if canceled.Status != store.UploadBatchStatusCanceled || canceled.CompletedFiles() != 1 || canceled.Files[1].Status != store.UploadBatchFileStatusCanceled {
		t.Fatalf("canceled batch = %+v", canceled)
	}
	if _, err := itemStore.CompleteUploadBatchFile(ctx, workspaceID, batchID, 2, inFlight.LeaseOwner, 0, 0); !errors.Is(err, store.ErrUploadBatchFileFence) {
		t.Fatalf("stale completion error = %v, want ErrUploadBatchFileFence", err)
	}
	if _, err := itemStore.GetImageForWorkspace(ctx, incompleteImage.ID, workspaceID); err == nil {
		t.Fatal("incomplete image survived upload batch cancellation")
	}
	var uploadCleanupCount, tripletCleanupCount int
	if err := databasePool.QueryRow(
		`SELECT COUNT(*) FROM resource_cleanup_outbox WHERE kind = 'upload_blob' AND resource_key = ?`,
		incompleteUploadName,
	).Scan(&uploadCleanupCount); err != nil {
		t.Fatalf("count canceled upload cleanup: %v", err)
	}
	if err := databasePool.QueryRow(
		`SELECT COUNT(*) FROM resource_cleanup_outbox WHERE kind = 'triplet_presentation_image' AND resource_key = ?`,
		fmt.Sprint(incompleteImage.ID),
	).Scan(&tripletCleanupCount); err != nil {
		t.Fatalf("count canceled Triplet cleanup: %v", err)
	}
	if uploadCleanupCount != 1 || tripletCleanupCount != 1 {
		t.Fatalf("canceled image cleanups = upload:%d Triplet:%d, want 1/1", uploadCleanupCount, tripletCleanupCount)
	}
	job, err := jobStore.Get(ctx, jobID)
	if err != nil || job.Status != store.TranscriptionJobStatusCanceled {
		t.Fatalf("job after batch cancel = %+v/%v", job, err)
	}
	if len(job.Attempts) != 1 || job.Attempts[0].Outcome != store.TranscriptionAttemptCanceled ||
		job.Attempts[0].SafeErrorMessage != "canceled with upload batch" || job.Attempts[0].FinishedAt == nil {
		t.Fatalf("job attempt after batch cancel = %+v", job.Attempts)
	}
	if err := jobStore.UpdateProgress(ctx, runningFence, 1, 0, "stale", "{}", "{}"); !errors.Is(err, store.ErrTranscriptionJobFence) {
		t.Fatalf("stale batch worker progress error = %v, want ErrTranscriptionJobFence", err)
	}
	idempotentCancel, err := itemStore.CancelUploadBatch(ctx, workspaceID, batchID)
	if err != nil || idempotentCancel.Status != store.UploadBatchStatusCanceled {
		t.Fatalf("idempotent CancelUploadBatch = %+v/%v", idempotentCancel, err)
	}
}

func TestUploadBatchCancellationAtomicallyReleasesProvisionalQuotaWithinTenant(t *testing.T) {
	databasePool := annotationTestDB(t)
	ctx := context.Background()
	userA, workspaceA := createUploadBatchIdentity(t, databasePool)
	userB, workspaceB := createUploadBatchIdentity(t, databasePool)
	contextStore := store.NewContextStore(databasePool)
	contextA, err := contextStore.Create(ctx, store.Context{
		UserID:                &userA,
		WorkspaceID:           &workspaceA,
		Name:                  "cancel-quota-a-" + uuid.NewString(),
		SegmentationModel:     "tesseract",
		TranscriptionProvider: "tesseract",
		TranscriptionModel:    "eng",
	})
	if err != nil {
		t.Fatalf("create workspace A context: %v", err)
	}
	contextB, err := contextStore.Create(ctx, store.Context{
		UserID:                &userB,
		WorkspaceID:           &workspaceB,
		Name:                  "cancel-quota-b-" + uuid.NewString(),
		SegmentationModel:     "tesseract",
		TranscriptionProvider: "tesseract",
		TranscriptionModel:    "eng",
	})
	if err != nil {
		t.Fatalf("create workspace B context: %v", err)
	}

	itemStore := store.NewItemStore(databasePool)
	batchA := "batch-cancel-quota-a-" + uuid.NewString()
	batchB := "batch-cancel-quota-b-" + uuid.NewString()
	for _, params := range []store.StartUploadBatchParams{
		{
			WorkspaceID: workspaceA, UserID: userA, BatchID: batchA, ItemID: "item_" + uuid.NewString(),
			Name: "Cancel quota A", Context: contextA, RequestHash: fmt.Sprintf("%064x", 61),
			Files: []store.UploadBatchFileInput{{Filename: "a.png", Size: 4, ContentSHA256: fmt.Sprintf("%064x", 62)}},
		},
		{
			WorkspaceID: workspaceB, UserID: userB, BatchID: batchB, ItemID: "item_" + uuid.NewString(),
			Name: "Cancel quota B", Context: contextB, RequestHash: fmt.Sprintf("%064x", 63),
			Files: []store.UploadBatchFileInput{{Filename: "b.png", Size: 4, ContentSHA256: fmt.Sprintf("%064x", 64)}},
		},
	} {
		if _, err := itemStore.StartUploadBatch(ctx, params); err != nil {
			t.Fatalf("StartUploadBatch(%d): %v", params.WorkspaceID, err)
		}
	}

	_, fileA, claimed, err := itemStore.ClaimUploadBatchFile(ctx, workspaceA, batchA, 1, 4, fmt.Sprintf("%064x", 62))
	if err != nil || !claimed {
		t.Fatalf("claim workspace A file = %+v/%t/%v", fileA, claimed, err)
	}
	imageA, err := itemStore.EnsureUploadBatchImage(
		ctx, workspaceA, batchA, 1, fileA.LeaseOwner,
		"https://images.example/cancel-quota-"+uuid.NewString()+".png", 0, 100, 100, "https://scribe.example",
	)
	if err != nil {
		t.Fatalf("ensure workspace A provisional image: %v", err)
	}
	createUploadBatchTestJob(t, databasePool, workspaceA, imageA.ID, contextA)
	t.Cleanup(func() {
		_, _ = databasePool.Exec(`DELETE FROM resource_cleanup_outbox WHERE kind = 'triplet_presentation_image' AND resource_key = ?`, fmt.Sprint(imageA.ID))
	})

	beforeA, err := itemStore.GetStorageQuotaUsage(ctx, workspaceA)
	if err != nil {
		t.Fatalf("load workspace A quota before cancel: %v", err)
	}
	beforeB, err := itemStore.GetStorageQuotaUsage(ctx, workspaceB)
	if err != nil {
		t.Fatalf("load workspace B quota before cancel: %v", err)
	}
	if beforeA.Images == 0 || beforeA.DatabaseBytes == 0 {
		t.Fatalf("workspace A provisional quota was not materialized: %+v", beforeA)
	}

	if _, err := itemStore.CancelUploadBatch(ctx, workspaceB, batchA); !errors.Is(err, store.ErrUploadBatchNotFound) {
		t.Fatalf("cross-workspace cancellation error = %v, want ErrUploadBatchNotFound", err)
	}
	afterDeniedB, err := itemStore.GetStorageQuotaUsage(ctx, workspaceB)
	if err != nil || afterDeniedB != beforeB {
		t.Fatalf("cross-workspace cancellation changed workspace B quota = %+v/%v, want %+v", afterDeniedB, err, beforeB)
	}

	if _, err := itemStore.CancelUploadBatch(ctx, workspaceA, batchA); err != nil {
		t.Fatalf("CancelUploadBatch workspace A: %v", err)
	}
	afterA, err := itemStore.GetStorageQuotaUsage(ctx, workspaceA)
	if err != nil {
		t.Fatalf("load workspace A quota after cancel: %v", err)
	}
	afterB, err := itemStore.GetStorageQuotaUsage(ctx, workspaceB)
	if err != nil {
		t.Fatalf("load workspace B quota after cancel: %v", err)
	}
	if afterA.Images+1 != beforeA.Images || afterA.DatabaseBytes >= beforeA.DatabaseBytes {
		t.Fatalf("workspace A canceled quota = %+v, want one fewer image and fewer durable bytes than %+v", afterA, beforeA)
	}
	if afterB != beforeB {
		t.Fatalf("workspace A cancellation changed workspace B quota = %+v, want %+v", afterB, beforeB)
	}

	if _, err := itemStore.CancelUploadBatch(ctx, workspaceA, batchA); err != nil {
		t.Fatalf("idempotent CancelUploadBatch workspace A: %v", err)
	}
	afterReplayA, err := itemStore.GetStorageQuotaUsage(ctx, workspaceA)
	if err != nil || afterReplayA != afterA {
		t.Fatalf("idempotent cancellation quota = %+v/%v, want %+v", afterReplayA, err, afterA)
	}
	afterReplayB, err := itemStore.GetStorageQuotaUsage(ctx, workspaceB)
	if err != nil || afterReplayB != beforeB {
		t.Fatalf("idempotent cancellation changed workspace B quota = %+v/%v, want %+v", afterReplayB, err, beforeB)
	}
}

func TestUploadBatchExpiredLeaseReclaimFencesStaleCleanup(t *testing.T) {
	databasePool := annotationTestDB(t)
	ctx := context.Background()
	userID, workspaceID := createUploadBatchIdentity(t, databasePool)
	processingContext, err := store.NewContextStore(databasePool).Create(ctx, store.Context{
		UserID:                &userID,
		WorkspaceID:           &workspaceID,
		Name:                  "lease-reclaim-context-" + uuid.NewString(),
		SegmentationModel:     "tesseract",
		TranscriptionProvider: "tesseract",
		TranscriptionModel:    "eng",
	})
	if err != nil {
		t.Fatalf("create processing context: %v", err)
	}
	itemStore := store.NewItemStore(databasePool)
	jobStore := store.NewTranscriptionJobStore(databasePool)
	batchID := "batch-" + uuid.NewString()
	digest := fmt.Sprintf("%064x", 51)
	if _, err := itemStore.StartUploadBatch(ctx, store.StartUploadBatchParams{
		WorkspaceID: workspaceID,
		UserID:      userID,
		BatchID:     batchID,
		ItemID:      "item_" + uuid.NewString(),
		Name:        "Lease reclaim batch",
		Context:     processingContext,
		RequestHash: fmt.Sprintf("%064x", 52),
		Files:       []store.UploadBatchFileInput{{Filename: "page.png", Size: 4, ContentSHA256: digest}},
	}); err != nil {
		t.Fatalf("StartUploadBatch: %v", err)
	}

	_, firstAttempt, claimed, err := itemStore.ClaimUploadBatchFile(ctx, workspaceID, batchID, 1, 4, digest)
	if err != nil || !claimed {
		t.Fatalf("claim first attempt = %+v/%t/%v", firstAttempt, claimed, err)
	}
	firstUploadName := immutableUploadTestName(digest)
	firstImage, err := itemStore.EnsureUploadBatchImage(ctx, workspaceID, batchID, 1, firstAttempt.LeaseOwner, "/static/uploads/"+firstUploadName, 4, 100, 100, "https://scribe.example")
	if err != nil {
		t.Fatalf("ensure first attempt image: %v", err)
	}
	firstJobID := createUploadBatchTestJob(t, databasePool, workspaceID, firstImage.ID, processingContext)
	t.Cleanup(func() {
		_, _ = databasePool.Exec(`DELETE FROM resource_cleanup_outbox WHERE resource_key IN (?, ?)`, firstUploadName, fmt.Sprint(firstImage.ID))
	})
	if result, err := databasePool.Exec(
		`UPDATE upload_batch_files SET lease_until = DATE_SUB(NOW(), INTERVAL 1 SECOND) WHERE workspace_id = ? AND batch_id = ? AND sequence = 1 AND locked_by = ?`,
		workspaceID,
		batchID,
		firstAttempt.LeaseOwner,
	); err != nil {
		t.Fatalf("expire first attempt lease: %v", err)
	} else if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
		t.Fatalf("expired lease rows = %d/%v, want 1", rows, rowsErr)
	}
	if err := itemStore.RenewUploadBatchFileLease(ctx, workspaceID, batchID, 1, firstAttempt.LeaseOwner); !errors.Is(err, store.ErrUploadBatchFileFence) {
		t.Fatalf("expired lease renewal error = %v, want ErrUploadBatchFileFence", err)
	}

	_, secondAttempt, claimed, err := itemStore.ClaimUploadBatchFile(ctx, workspaceID, batchID, 1, 4, digest)
	if err != nil || !claimed || secondAttempt.LeaseOwner == firstAttempt.LeaseOwner {
		t.Fatalf("claim second attempt = %+v/%t/%v", secondAttempt, claimed, err)
	}
	if _, err := itemStore.GetImageForWorkspace(ctx, firstImage.ID, workspaceID); err == nil {
		t.Fatal("reclaimed attempt inherited the prior provisional image")
	}
	if _, err := jobStore.Get(ctx, firstJobID); err == nil {
		t.Fatal("reclaimed attempt inherited the prior provisional job")
	}

	secondUploadName := immutableUploadTestName(digest)
	secondImage, err := itemStore.EnsureUploadBatchImage(ctx, workspaceID, batchID, 1, secondAttempt.LeaseOwner, "/static/uploads/"+secondUploadName, 4, 100, 100, "https://scribe.example")
	if err != nil {
		t.Fatalf("ensure second attempt image: %v", err)
	}
	secondJobID := createUploadBatchTestJob(t, databasePool, workspaceID, secondImage.ID, processingContext)
	if err := itemStore.AbortUploadBatchFileAttempt(ctx, workspaceID, batchID, 1, firstAttempt.LeaseOwner, "stale attempt"); !errors.Is(err, store.ErrUploadBatchFileFence) {
		t.Fatalf("stale abort error = %v, want ErrUploadBatchFileFence", err)
	}
	if _, err := itemStore.GetImageForWorkspace(ctx, secondImage.ID, workspaceID); err != nil {
		t.Fatalf("stale abort deleted current image: %v", err)
	}
	if _, err := jobStore.Get(ctx, secondJobID); err != nil {
		t.Fatalf("stale abort deleted current job: %v", err)
	}
	if err := itemStore.RenewUploadBatchFileLease(ctx, workspaceID, batchID, 1, secondAttempt.LeaseOwner); err != nil {
		t.Fatalf("renew current attempt: %v", err)
	}
	completed, err := itemStore.CompleteUploadBatchFile(ctx, workspaceID, batchID, 1, secondAttempt.LeaseOwner, secondImage.ID, secondJobID)
	if err != nil || completed.Status != store.UploadBatchStatusCompleted {
		t.Fatalf("complete current attempt = %+v/%v", completed, err)
	}
	if err := itemStore.AbortUploadBatchFileAttempt(ctx, workspaceID, batchID, 1, firstAttempt.LeaseOwner, "stale attempt"); !errors.Is(err, store.ErrUploadBatchFileFence) {
		t.Fatalf("stale abort after completion error = %v, want ErrUploadBatchFileFence", err)
	}
}

func TestConcurrentUploadBatchStartCreatesOneItem(t *testing.T) {
	databasePool := annotationTestDB(t)
	ctx := context.Background()
	userID, workspaceID := createUploadBatchIdentity(t, databasePool)
	processingContext, err := store.NewContextStore(databasePool).Create(ctx, store.Context{
		UserID:                &userID,
		WorkspaceID:           &workspaceID,
		Name:                  "concurrent-batch-context-" + uuid.NewString(),
		SegmentationModel:     "tesseract",
		TranscriptionProvider: "tesseract",
		TranscriptionModel:    "eng",
	})
	if err != nil {
		t.Fatalf("create processing context: %v", err)
	}
	itemStore := store.NewItemStore(databasePool)
	batchID := "batch-" + uuid.NewString()
	const callers = 6
	results := make(chan store.UploadBatch, callers)
	errorsSeen := make(chan error, callers)
	var wait sync.WaitGroup
	for index := 0; index < callers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			batch, startErr := itemStore.StartUploadBatch(ctx, store.StartUploadBatchParams{
				WorkspaceID: workspaceID,
				UserID:      userID,
				BatchID:     batchID,
				ItemID:      "item_" + uuid.NewString(),
				Name:        "Concurrent batch",
				Context:     processingContext,
				RequestHash: fmt.Sprintf("%064x", 21),
				Files:       []store.UploadBatchFileInput{{Filename: "page.png", Size: 4, ContentSHA256: fmt.Sprintf("%064x", 3)}},
			})
			if startErr != nil {
				errorsSeen <- startErr
				return
			}
			results <- batch
		}()
	}
	wait.Wait()
	close(results)
	close(errorsSeen)
	for startErr := range errorsSeen {
		t.Errorf("concurrent StartUploadBatch: %v", startErr)
	}
	var itemID string
	var count int
	for batch := range results {
		count++
		if itemID == "" {
			itemID = batch.ItemID
		}
		if batch.ItemID != itemID {
			t.Errorf("concurrent batch item = %q, want %q", batch.ItemID, itemID)
		}
	}
	if count != callers {
		t.Fatalf("successful callers = %d, want %d", count, callers)
	}
	var itemCount int
	if err := databasePool.QueryRow(`SELECT COUNT(*) FROM items WHERE workspace_id = ? AND source_type = 'upload'`, workspaceID).Scan(&itemCount); err != nil {
		t.Fatalf("count upload batch items: %v", err)
	}
	if itemCount != 1 {
		t.Fatalf("upload batch item count = %d, want 1", itemCount)
	}
}

func TestCancelCompletedUploadBatchIsRejectedAndLeavesJobUntouched(t *testing.T) {
	databasePool := annotationTestDB(t)
	ctx := context.Background()
	userID, workspaceID := createUploadBatchIdentity(t, databasePool)
	processingContext, err := store.NewContextStore(databasePool).Create(ctx, store.Context{
		UserID:                &userID,
		WorkspaceID:           &workspaceID,
		Name:                  "lost-response-context-" + uuid.NewString(),
		SegmentationModel:     "tesseract",
		TranscriptionProvider: "tesseract",
		TranscriptionModel:    "eng",
	})
	if err != nil {
		t.Fatalf("create processing context: %v", err)
	}
	itemStore := store.NewItemStore(databasePool)
	jobStore := store.NewTranscriptionJobStore(databasePool)
	batchID := "batch-" + uuid.NewString()
	_, err = itemStore.StartUploadBatch(ctx, store.StartUploadBatchParams{
		WorkspaceID: workspaceID,
		UserID:      userID,
		BatchID:     batchID,
		ItemID:      "item_" + uuid.NewString(),
		Name:        "Lost response batch",
		Context:     processingContext,
		RequestHash: fmt.Sprintf("%064x", 31),
		Files:       []store.UploadBatchFileInput{{Filename: "page.png", Size: 4, ContentSHA256: fmt.Sprintf("%064x", 4)}},
	})
	if err != nil {
		t.Fatalf("StartUploadBatch: %v", err)
	}
	_, file, claimed, err := itemStore.ClaimUploadBatchFile(ctx, workspaceID, batchID, 1, 4, fmt.Sprintf("%064x", 4))
	if err != nil || !claimed {
		t.Fatalf("ClaimUploadBatchFile = %+v/%t/%v", file, claimed, err)
	}
	image, err := itemStore.EnsureUploadBatchImage(ctx, workspaceID, batchID, 1, file.LeaseOwner, "https://images.example/lost-response.png", 0, 100, 100, "https://scribe.example")
	if err != nil {
		t.Fatalf("AddImage: %v", err)
	}
	jobID := createUploadBatchTestJob(t, databasePool, workspaceID, image.ID, processingContext)
	completed, err := itemStore.CompleteUploadBatchFile(ctx, workspaceID, batchID, 1, file.LeaseOwner, image.ID, jobID)
	if err != nil || completed.Status != store.UploadBatchStatusCompleted {
		t.Fatalf("CompleteUploadBatchFile = %+v/%v", completed, err)
	}

	_, err = itemStore.CancelUploadBatch(ctx, workspaceID, batchID)
	if !errors.Is(err, store.ErrUploadBatchCompleted) {
		t.Fatalf("CancelUploadBatch after completion error = %v, want ErrUploadBatchCompleted", err)
	}
	job, err := jobStore.Get(ctx, jobID)
	if err != nil || job.Status != store.TranscriptionJobStatusPending {
		t.Fatalf("job after rejected completed batch cancel = %+v/%v", job, err)
	}
}

func TestExternalRequestReservationIsConcurrentAndPayloadBound(t *testing.T) {
	databasePool := annotationTestDB(t)
	ctx := context.Background()
	_, workspaceID := createUploadBatchIdentity(t, databasePool)
	jobStore := store.NewTranscriptionJobStore(databasePool)
	key := fmt.Sprintf("%064x", 41)
	requestHash := fmt.Sprintf("%064x", 42)
	const callers = 6
	type result struct {
		reservation store.ExternalRequest
		created     bool
		err         error
	}
	results := make(chan result, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			reservation, created, err := jobStore.ReserveExternalRequest(ctx, workspaceID, "batch-test", key, requestHash, "")
			results <- result{reservation: reservation, created: created, err: err}
		}()
	}
	wait.Wait()
	close(results)
	var owner store.ExternalRequest
	var createdCount int
	for outcome := range results {
		if outcome.err != nil {
			t.Errorf("ReserveExternalRequest: %v", outcome.err)
			continue
		}
		if outcome.created {
			createdCount++
			owner = outcome.reservation
		}
	}
	if createdCount != 1 {
		t.Fatalf("created reservations = %d, want 1", createdCount)
	}
	if _, _, err := jobStore.ReserveExternalRequest(ctx, workspaceID, "batch-test", key, fmt.Sprintf("%064x", 43), ""); !errors.Is(err, store.ErrExternalRequestMismatch) {
		t.Fatalf("mismatched reservation error = %v, want ErrExternalRequestMismatch", err)
	}
	if err := jobStore.FailExternalRequest(ctx, workspaceID, "batch-test", key, owner.LeaseOwner, "retryable"); err != nil {
		t.Fatalf("FailExternalRequest: %v", err)
	}
	retried, created, err := jobStore.ReserveExternalRequest(ctx, workspaceID, "batch-test", key, requestHash, "")
	if err != nil || !created || retried.AttemptCount != 2 {
		t.Fatalf("retry reservation = %+v/%t/%v", retried, created, err)
	}
	if err := jobStore.CompleteExternalRequest(ctx, workspaceID, "batch-test", key, retried.LeaseOwner, "", 0, 0); err != nil {
		t.Fatalf("CompleteExternalRequest: %v", err)
	}
	replayed, created, err := jobStore.ReserveExternalRequest(ctx, workspaceID, "batch-test", key, requestHash, "")
	if err != nil || created || replayed.Status != store.ExternalRequestStatusCompleted {
		t.Fatalf("completed reservation replay = %+v/%t/%v", replayed, created, err)
	}
}

func createUploadBatchTestJob(t *testing.T, databasePool *sql.DB, workspaceID, imageID uint64, processingContext store.Context) uint64 {
	t.Helper()
	initializeUploadBatchTestPage(t, databasePool, workspaceID, imageID)
	jobID, err := store.NewTranscriptionJobStore(databasePool).Create(context.Background(), imageID, processingContext)
	if err != nil {
		t.Fatalf("create upload batch transcription job: %v", err)
	}
	return jobID
}

func initializeUploadBatchTestPage(t *testing.T, databasePool *sql.DB, workspaceID, imageID uint64) {
	t.Helper()
	ctx := context.Background()
	if _, err := databasePool.ExecContext(ctx, `UPDATE item_images SET width = 10000, height = 10000 WHERE workspace_id = ? AND id = ?`, workspaceID, imageID); err != nil {
		t.Fatalf("initialize upload batch image dimensions: %v", err)
	}
	canvasURI := fmt.Sprintf("https://source.example/upload-batches/%d/canvas", imageID)
	if _, err := store.NewItemStore(databasePool).EnsureImageCanvasURI(ctx, imageID, canvasURI); err != nil {
		t.Fatalf("initialize upload batch canvas: %v", err)
	}
	if _, err := store.NewAnnotationStore(databasePool).SavePage(
		ctx,
		canonicalTestPage(t, workspaceID, imageID, canvasURI, "segmented"),
		0,
	); err != nil {
		t.Fatalf("initialize upload batch canonical page: %v", err)
	}
}

func immutableUploadTestName(contentSHA256 string) string {
	return contentSHA256 + "-" + uuid.NewString() + ".png"
}

func createUploadBatchIdentity(t *testing.T, databasePool *sql.DB) (uint64, uint64) {
	t.Helper()
	suffix := uuid.NewString()
	userResult, err := databasePool.Exec(`INSERT INTO users (name) VALUES (?)`, "upload-batch-"+suffix)
	if err != nil {
		t.Fatalf("insert upload batch user: %v", err)
	}
	userIDRaw, err := userResult.LastInsertId()
	if err != nil {
		t.Fatalf("upload batch user id: %v", err)
	}
	workspaceResult, err := databasePool.Exec(
		`INSERT INTO workspaces (owner_user_id, name, slug, is_personal, created_by_user_id) VALUES (?, ?, ?, TRUE, ?)`,
		userIDRaw,
		"upload-batch-"+suffix,
		"upload-batch-"+suffix,
		userIDRaw,
	)
	if err != nil {
		t.Fatalf("insert upload batch workspace: %v", err)
	}
	workspaceIDRaw, err := workspaceResult.LastInsertId()
	if err != nil {
		t.Fatalf("upload batch workspace id: %v", err)
	}
	if _, err := databasePool.Exec(`INSERT INTO storage_quota_usage (workspace_id) VALUES (?)`, workspaceIDRaw); err != nil {
		t.Fatalf("insert upload batch workspace quota row: %v", err)
	}
	if _, err := databasePool.Exec(`INSERT INTO workspace_members (workspace_id, user_id, role) VALUES (?, ?, 'admin')`, workspaceIDRaw, userIDRaw); err != nil {
		t.Fatalf("insert upload batch workspace membership: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		itemStore := store.NewItemStore(databasePool)
		itemRows, _ := databasePool.Query(`SELECT id FROM items WHERE workspace_id = ? ORDER BY id`, workspaceIDRaw)
		if itemRows != nil {
			var itemIDs []string
			for itemRows.Next() {
				var itemID string
				if itemRows.Scan(&itemID) == nil {
					itemIDs = append(itemIDs, itemID)
				}
			}
			_ = itemRows.Close()
			for _, itemID := range itemIDs {
				_ = itemStore.DeleteForWorkspace(cleanupCtx, itemID, uint64(workspaceIDRaw)) // #nosec G115 -- positive test fixture identifier.
			}
		}
		contextRows, _ := databasePool.Query(`SELECT id FROM contexts WHERE workspace_id = ? ORDER BY id`, workspaceIDRaw)
		if contextRows != nil {
			var contextIDs []uint64
			for contextRows.Next() {
				var contextID uint64
				if contextRows.Scan(&contextID) == nil {
					contextIDs = append(contextIDs, contextID)
				}
			}
			_ = contextRows.Close()
			contextStore := store.NewContextStore(databasePool)
			for _, contextID := range contextIDs {
				_ = contextStore.DeleteForWorkspace(cleanupCtx, contextID, uint64(workspaceIDRaw)) // #nosec G115 -- positive test fixture identifier.
			}
		}
		_, _ = databasePool.Exec(`DELETE wd FROM webhook_deliveries wd JOIN event_outbox eo ON eo.event_id = wd.event_id WHERE eo.workspace_id = ?`, workspaceIDRaw)
		for _, table := range []string{"event_outbox", "provider_call_audits", "provider_secrets", "api_keys", "external_requests", "workspace_storage_reservations", "resource_cleanup_outbox"} {
			_, _ = databasePool.Exec(`DELETE FROM `+table+` WHERE workspace_id = ?`, workspaceIDRaw) // #nosec G202 -- table names are a closed test-only constant list.
		}
		if err := itemStore.RebuildStorageQuotaUsage(cleanupCtx); err != nil {
			t.Errorf("rebuild storage quota usage before upload-batch fixture owner cleanup: %v", err)
		}
		_, _ = databasePool.Exec(`DELETE FROM workspace_members WHERE workspace_id = ?`, workspaceIDRaw)
		_, _ = databasePool.Exec(`DELETE FROM workspaces WHERE id = ?`, workspaceIDRaw)
		_, _ = databasePool.Exec(`DELETE FROM auth_sessions WHERE user_id = ?`, userIDRaw)
		_, _ = databasePool.Exec(`DELETE FROM users WHERE id = ?`, userIDRaw)
		if err := itemStore.RebuildStorageQuotaUsage(cleanupCtx); err != nil {
			t.Errorf("rebuild storage quota usage after upload-batch fixture cleanup: %v", err)
		}
	})
	return uint64(userIDRaw), uint64(workspaceIDRaw)
}
