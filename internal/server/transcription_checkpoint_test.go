package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/models"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	"github.com/lehigh-university-libraries/scribe/internal/worklimit"
)

type checkpointOCR struct {
	segmentCapOCR
	calls      atomic.Int32
	transcribe func(context.Context, int) (string, error)
}

func (f *checkpointOCR) TranscribeImageFileWithContext(ctx context.Context, _, _, _ string) (string, error) {
	return f.transcribe(ctx, int(f.calls.Add(1)))
}

func checkpointJob(t *testing.T, ocr *checkpointOCR, concurrency, lineCount int) (*Handler, uint64) {
	t.Helper()
	database := openTestDB(t)
	ctx := context.Background()
	workspace, user := createServerTestWorkspace(t, database)
	canvas := "https://source.example/canvas/" + uniqueName("checkpoint")
	image := createServerTestItemImage(t, database, workspace, user, canvas)
	processing, err := store.NewContextStore(database).Create(ctx, store.Context{
		UserID: &user, WorkspaceID: &workspace, Name: uniqueName("checkpoint"),
		SegmentationModel: "kraken", TranscriptionProvider: "ollama", TranscriptionModel: "glm-ocr:bf16",
	})
	if err != nil {
		t.Fatal(err)
	}
	identity := iiif.PageIdentity{PublicBaseURL: (&Handler{}).publicAnnotationBaseURL(), ItemImageID: image.ID, CanvasURI: canvas}
	lines := []any{
		transcriptionAnnotation("line-1", "line", "original first", canvas, models.BBox{X1: 0, Y1: 0, X2: 100, Y2: 20}),
		transcriptionAnnotation("line-2", "line", "original second", canvas, models.BBox{X1: 0, Y1: 30, X2: 100, Y2: 50}),
	}
	if lineCount == 3 {
		lines = append(lines, transcriptionAnnotation("line-3", "line", "original third", canvas, models.BBox{X1: 0, Y1: 60, X2: 100, Y2: 80}))
	}
	if lineCount == 0 {
		lines = nil
	}
	payload, err := iiif.NewAnnotationPage(identity, lines)
	if err != nil {
		t.Fatal(err)
	}
	pageID, err := iiif.CanonicalPageID(identity.PublicBaseURL, image.ID)
	if err != nil {
		t.Fatal(err)
	}
	annotations := store.NewAnnotationStore(database)
	if _, err := annotations.SavePage(ctx, store.AnnotationPage{WorkspaceID: workspace, ItemImageID: image.ID, PageID: pageID, CanvasURI: canvas, Payload: string(payload)}, 0); err != nil {
		t.Fatal(err)
	}
	jobs := store.NewTranscriptionJobStore(database)
	id, err := jobs.Create(ctx, image.ID, processing)
	if err != nil {
		t.Fatal(err)
	}
	limiter, err := worklimit.NewHierarchical(concurrency, concurrency, concurrency)
	if err != nil {
		t.Fatal(err)
	}
	return &Handler{items: store.NewItemStore(database), annotations: annotations, transcriptionJobs: jobs,
		processingLimiter: limiter, ocr: ocr, ocrRuns: store.NewOCRRunStore(database),
		imageRegionFetcher: func(context.Context, string, int, int, int, int) (string, func(), error) {
			return "fixture", func() {}, nil
		},
	}, id
}

func awaitCheckpointCall(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("provider call did not start")
	}
}

func claimCheckpointTestJob(t *testing.T, jobs *store.TranscriptionJobStore, imageID uint64, processing store.Context) *store.TranscriptionJob {
	t.Helper()
	id, err := jobs.Create(context.Background(), imageID, processing)
	if err != nil {
		t.Fatal(err)
	}
	job, err := jobs.ClaimPendingByID(context.Background(), id)
	if err != nil || job == nil {
		t.Fatalf("claim test job: %v", err)
	}
	return job
}

func TestQueuedTranscriptionCallsRunConcurrently(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	ocr := &checkpointOCR{transcribe: func(ctx context.Context, _ int) (string, error) {
		entered <- struct{}{}
		select {
		case <-release:
			return "concurrent result", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}}
	h, id := checkpointJob(t, ocr, 2, 3)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.processQueuedTranscriptionJob(ctx, id) }()
	awaitCheckpointCall(t, entered)
	awaitCheckpointCall(t, entered) // Cannot happen while the first call is blocked in a serial implementation.
	if ocr.calls.Load() != 2 {
		t.Fatal("processing quota allowed more than two concurrent calls")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	job, err := h.transcriptionJobs.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != store.TranscriptionJobStatusCompleted || job.CompletedSegments != 3 || ocr.calls.Load() != 3 {
		t.Fatalf("job = %+v; calls = %d", job, ocr.calls.Load())
	}
}

func TestQueuedTranscriptionResumesAfterCancellation(t *testing.T) {
	entered := make(chan struct{}, 1)
	ocr := &checkpointOCR{transcribe: func(ctx context.Context, call int) (string, error) {
		if call == 2 {
			entered <- struct{}{}
			<-ctx.Done()
			return "", ctx.Err()
		}
		return "checkpoint result", nil
	}}
	h, id := checkpointJob(t, ocr, 1, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.processQueuedTranscriptionJob(ctx, id) }()
	awaitCheckpointCall(t, entered)
	job, err := h.transcriptionJobs.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	fence, err := job.Fence()
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := h.transcriptionJobs.Checkpoint(context.Background(), fence)
	if err != nil {
		t.Fatal(err)
	}
	results, err := h.transcriptionJobs.LineCheckpoints(context.Background(), fence)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("checkpoint successes = %d", len(results))
	}
	if strings.Contains(checkpoint, "checkpoint result") {
		t.Fatal("line success rewrote the segmented-page checkpoint")
	}
	page, err := h.annotations.LoadPage(context.Background(), job.WorkspaceID, job.ItemImageID)
	if err != nil || page.Revision != job.InputRevision {
		t.Fatalf("checkpoint modified canonical page: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancellation succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled provider was not joined")
	}
	if _, err := openTestDB(t).ExecContext(context.Background(), "UPDATE transcription_jobs SET lease_until = CASE WHEN status = 'running' THEN DATE_SUB(NOW(), INTERVAL 1 SECOND) ELSE NULL END, retry_after = NULL WHERE id = ?", id); err != nil {
		t.Fatal(err)
	}
	if err := h.processQueuedTranscriptionJob(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if ocr.calls.Load() != 3 {
		t.Fatalf("provider calls = %d, want 3 (saved line must not repeat)", ocr.calls.Load())
	}
	if ocr.segmentationCalls != 1 {
		t.Fatalf("segmentation calls = %d, want one across retries", ocr.segmentationCalls)
	}
	if err := h.transcriptionJobs.SaveCheckpoint(context.Background(), fence, "", checkpoint); !errors.Is(err, store.ErrTranscriptionJobFence) {
		t.Fatalf("stale write = %v", err)
	}
	job, err = h.transcriptionJobs.Get(context.Background(), id)
	if err != nil || job.CompletedSegments != 2 || job.AttemptCount != 2 || job.Status != store.TranscriptionJobStatusCompleted {
		t.Fatalf("resumed job = %+v / %v", job, err)
	}
}

func TestAllCheckpointedLinesCompleteWithoutProviderCalls(t *testing.T) {
	ocr := &checkpointOCR{transcribe: func(context.Context, int) (string, error) { return "unexpected", nil }}
	h, id := checkpointJob(t, ocr, 1, 2)
	ctx := context.Background()
	job, err := h.transcriptionJobs.ClaimPendingByID(ctx, id)
	if err != nil || job == nil {
		t.Fatalf("claim: %v", err)
	}
	fence, err := job.Fence()
	if err != nil {
		t.Fatal(err)
	}
	page, err := h.annotations.LoadPage(ctx, job.WorkspaceID, job.ItemImageID)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(page.Payload), &document); err != nil {
		t.Fatal(err)
	}
	if err := h.transcriptionJobs.SaveCheckpoint(ctx, fence, "", page.Payload); err != nil {
		t.Fatal(err)
	}
	for _, item := range document["items"].([]any) {
		payload, err := json.Marshal(item)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.transcriptionJobs.SaveCheckpoint(ctx, fence, item.(map[string]any)["id"].(string), string(payload)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := openTestDB(t).ExecContext(ctx, "UPDATE transcription_jobs SET lease_until = DATE_SUB(NOW(), INTERVAL 1 SECOND) WHERE id = ?", id); err != nil {
		t.Fatal(err)
	}
	if err := h.processQueuedTranscriptionJob(ctx, id); err != nil {
		t.Fatal(err)
	}
	completed, err := h.transcriptionJobs.Get(ctx, id)
	if err != nil || completed.CompletedSegments != 2 || completed.AttemptCount != 2 || completed.Status != store.TranscriptionJobStatusCompleted || ocr.calls.Load() != 0 {
		t.Fatalf("checkpoint completion = %+v / %v; calls %d", completed, err, ocr.calls.Load())
	}
}

func TestTranscriptionCheckpointEnforcesLeaseAndRevisionFence(t *testing.T) {
	ocr := &checkpointOCR{transcribe: func(context.Context, int) (string, error) { return "unused", nil }}
	h, id := checkpointJob(t, ocr, 1, 2)
	ctx := context.Background()
	job, err := h.transcriptionJobs.ClaimPendingByID(ctx, id)
	if err != nil || job == nil {
		t.Fatalf("claim: %v", err)
	}
	fence, err := job.Fence()
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*store.TranscriptionAttemptFence){
		func(f *store.TranscriptionAttemptFence) { f.AttemptNumber++ },
		func(f *store.TranscriptionAttemptFence) { f.InputRevision++ },
		func(f *store.TranscriptionAttemptFence) { f.LeaseToken = "stale" },
	} {
		stale := fence
		change(&stale)
		if err := h.transcriptionJobs.SaveCheckpoint(ctx, stale, "", "{}"); !errors.Is(err, store.ErrTranscriptionJobFence) {
			t.Fatalf("stale write: %v", err)
		}
		if _, err := h.transcriptionJobs.Checkpoint(ctx, stale); !errors.Is(err, store.ErrTranscriptionJobFence) {
			t.Fatalf("stale read: %v", err)
		}
	}
	if _, err := openTestDB(t).ExecContext(ctx, "UPDATE transcription_jobs SET lease_until = DATE_SUB(NOW(), INTERVAL 1 SECOND) WHERE id = ?", id); err != nil {
		t.Fatal(err)
	}
	if err := h.transcriptionJobs.SaveCheckpoint(ctx, fence, "", "{}"); !errors.Is(err, store.ErrTranscriptionJobFence) {
		t.Fatalf("expired lease write: %v", err)
	}
	if _, err := h.transcriptionJobs.Checkpoint(ctx, fence); !errors.Is(err, store.ErrTranscriptionJobFence) {
		t.Fatalf("expired lease read: %v", err)
	}
	if err := h.transcriptionJobs.Cancel(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := h.transcriptionJobs.SaveCheckpoint(ctx, fence, "", "{}"); !errors.Is(err, store.ErrTranscriptionJobFence) {
		t.Fatalf("canceled write: %v", err)
	}
}

func TestCheckpointCannotOverwriteNewHumanRevision(t *testing.T) {
	ocr := &checkpointOCR{transcribe: func(context.Context, int) (string, error) { return "unexpected", nil }}
	h, id := checkpointJob(t, ocr, 1, 2)
	ctx := context.Background()
	job, err := h.transcriptionJobs.ClaimPendingByID(ctx, id)
	if err != nil || job == nil {
		t.Fatalf("claim: %v", err)
	}
	page, err := h.annotations.LoadPage(ctx, job.WorkspaceID, job.ItemImageID)
	if err != nil {
		t.Fatal(err)
	}
	page.Payload = strings.ReplaceAll(page.Payload, "original first", "human correction")
	if _, err := h.annotations.SavePage(ctx, page, page.Revision); err != nil {
		t.Fatal(err)
	}
	if err := h.processClaimedTranscriptionJob(ctx, job); err == nil {
		t.Fatal("accepted a newer canonical revision")
	}
	if ocr.calls.Load() != 0 {
		t.Fatal("provider called for stale input")
	}
	current, err := h.annotations.LoadPage(ctx, job.WorkspaceID, job.ItemImageID)
	if err != nil || !strings.Contains(current.Payload, "human correction") {
		t.Fatalf("human correction lost: %v", err)
	}
}

func TestQueuedTranscriptionDeadlineJoinsProviderCalls(t *testing.T) {
	entered := make(chan struct{}, 2)
	var active atomic.Int32
	ocr := &checkpointOCR{transcribe: func(ctx context.Context, _ int) (string, error) {
		active.Add(1)
		defer active.Add(-1)
		entered <- struct{}{}
		<-ctx.Done()
		return "", ctx.Err()
	}}
	h, id := checkpointJob(t, ocr, 2, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.processQueuedTranscriptionJob(ctx, id) }()
	awaitCheckpointCall(t, entered)
	awaitCheckpointCall(t, entered)
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("deadline result: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("deadline did not stop worker")
	}
	if active.Load() != 0 {
		t.Fatal("provider calls outlived the request")
	}
}

func TestCorruptCheckpointFallsBackToCanonicalPage(t *testing.T) {
	for _, payload := range []string{"broken json", "null", `{"page":null}`, `{"items":null}`} {
		t.Run(payload, func(t *testing.T) {
			ocr := &checkpointOCR{transcribe: func(context.Context, int) (string, error) { return "fresh result", nil }}
			h, id := checkpointJob(t, ocr, 2, 2)
			ctx := context.Background()
			job, err := h.transcriptionJobs.ClaimPendingByID(ctx, id)
			if err != nil || job == nil {
				t.Fatalf("claim: %v", err)
			}
			fence, err := job.Fence()
			if err != nil {
				t.Fatal(err)
			}
			if err := h.transcriptionJobs.SaveCheckpoint(ctx, fence, "", payload); err != nil {
				t.Fatal(err)
			}
			if err := h.processClaimedTranscriptionJob(ctx, job); err != nil {
				t.Fatal(err)
			}
			if ocr.calls.Load() != 2 {
				t.Fatalf("calls = %d, want fresh canonical transcription", ocr.calls.Load())
			}
			assertNoCheckpointRows(t, id)
		})
	}
}

func assertNoCheckpointRows(t *testing.T, id uint64) {
	t.Helper()
	var count int
	if err := openTestDB(t).QueryRow("SELECT COUNT(*) FROM transcription_job_checkpoints WHERE job_id = ?", id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("job %d retains %d checkpoints", id, count)
	}
}

func TestCheckpointTerminalAndDeletionCleanup(t *testing.T) {
	for _, outcome := range []string{"cancel-running", "cancel-pending", "batch-cancel-running", "batch-cancel-pending", "permanent-failure", "expired-final-attempt", "supersede-running", "supersede-pending", "delete-image", "delete-item"} {
		t.Run(outcome, func(t *testing.T) {
			ocr := &checkpointOCR{transcribe: func(context.Context, int) (string, error) { return "unused", nil }}
			h, id := checkpointJob(t, ocr, 1, 2)
			ctx := context.Background()
			job, err := h.transcriptionJobs.ClaimPendingByID(ctx, id)
			if err != nil || job == nil {
				t.Fatalf("claim: %v", err)
			}
			fence, err := job.Fence()
			if err != nil {
				t.Fatal(err)
			}
			if err := h.transcriptionJobs.SaveCheckpoint(ctx, fence, "", `{"items":[]}`); err != nil {
				t.Fatal(err)
			}
			if err := h.transcriptionJobs.SaveCheckpoint(ctx, fence, "line", `{"id":"line"}`); err != nil {
				t.Fatal(err)
			}
			if strings.HasSuffix(outcome, "pending") {
				if err := h.transcriptionJobs.Fail(ctx, fence, "retryable failure"); err != nil {
					t.Fatal(err)
				}
				var count int
				if err := openTestDB(t).QueryRow("SELECT COUNT(*) FROM transcription_job_checkpoints WHERE job_id = ?", id).Scan(&count); err != nil || count != 2 {
					t.Fatalf("retry dropped checkpoints: %d / %v", count, err)
				}
			}
			switch outcome {
			case "batch-cancel-running", "batch-cancel-pending":
				batchID := uniqueName("checkpoint-batch")
				database := openTestDB(t)
				_, err = database.ExecContext(ctx, `INSERT INTO upload_batches
 (workspace_id,id,item_id,context_id,context_scope_id,context_snapshot,request_hash,creation_token,status)
 SELECT j.workspace_id, ?, i.item_id, j.context_id, j.context_scope_id, j.context_snapshot, ?, ?, 'in_progress'
 FROM transcription_jobs j JOIN item_images i ON i.id = j.item_image_id WHERE j.id = ?`, batchID, strings.Repeat("0", 64), batchID, id)
				if err == nil {
					_, err = database.ExecContext(ctx, `INSERT INTO upload_batch_files
 (workspace_id,batch_id,sequence,filename,size,content_sha256,status,item_image_id,transcription_job_id)
 SELECT workspace_id, ?, 1, 'page.png', 1, ?, 'completed', item_image_id, id
 FROM transcription_jobs WHERE id = ?`, batchID, strings.Repeat("0", 64), id)
				}
				if err == nil {
					_, err = h.items.CancelUploadBatch(ctx, job.WorkspaceID, batchID)
				}
			case "cancel-running", "cancel-pending":
				err = h.transcriptionJobs.Cancel(ctx, id)
			case "permanent-failure":
				err = h.transcriptionJobs.PermanentlyFailWithWebhookEvent(ctx, fence, "permanent failure", "", "", "", "")
			case "expired-final-attempt":
				_, err = openTestDB(t).Exec("UPDATE transcription_jobs SET max_attempts = attempt_count, lease_until = DATE_SUB(NOW(),INTERVAL 1 SECOND) WHERE id = ?", id)
				if err == nil {
					_, err = h.transcriptionJobs.ClaimPendingByID(ctx, id)
				}
			case "supersede-running", "supersede-pending":
				var processing store.Context
				err = json.Unmarshal(job.ContextSnapshot, &processing)
				if err == nil {
					processing.ID = 0
					processing.Name = uniqueName("superseding")
					processing, err = store.NewContextStore(openTestDB(t)).Create(ctx, processing)
				}
				if err == nil {
					_, err = h.transcriptionJobs.Create(ctx, job.ItemImageID, processing)
				}
			case "delete-image":
				err = h.items.DeleteItemImageForWorkspace(ctx, job.ItemImageID, job.WorkspaceID)
			case "delete-item":
				image, loadErr := h.items.GetImage(ctx, job.ItemImageID)
				if loadErr != nil {
					t.Fatal(loadErr)
				}
				err = h.items.DeleteForWorkspace(ctx, image.ItemID, job.WorkspaceID)
			}
			if err != nil {
				t.Fatal(err)
			}
			assertNoCheckpointRows(t, id)
			if err := h.transcriptionJobs.SaveCheckpoint(ctx, fence, "line", "stale"); !errors.Is(err, store.ErrTranscriptionJobFence) {
				t.Fatalf("terminal checkpoint resurrected: %v", err)
			}
		})
	}
}

func TestSchedulingDoesNotReserveUnusedCapacity(t *testing.T) {
	previous := config.Get()
	configured := previous
	configured.Config.LLM.LineTranscribeConcurrency = 1
	config.Init(configured)
	t.Cleanup(func() { config.Init(previous) })
	entered := make(chan struct{}, 1)
	ocr := &checkpointOCR{transcribe: func(ctx context.Context, _ int) (string, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return "", ctx.Err()
	}}
	h, id := checkpointJob(t, ocr, 2, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.processQueuedTranscriptionJob(ctx, id) }()
	awaitCheckpointCall(t, entered)
	job, err := h.transcriptionJobs.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var processing store.Context
	if err := json.Unmarshal(job.ContextSnapshot, &processing); err != nil {
		t.Fatal(err)
	}
	otherCtx, otherCancel := context.WithTimeout(context.Background(), time.Second)
	defer otherCancel()
	release, err := h.processingLimiter.Acquire(otherCtx, job.WorkspaceID, processingLimitProvider(processing))
	if err != nil {
		t.Fatalf("scheduler reserved unused provider capacity: %v", err)
	}
	release()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not join")
	}
}

func TestCurrentLineRemainsSetWhileAnotherCallRuns(t *testing.T) {
	entered := make(chan struct{}, 2)
	first := make(chan struct{})
	second := make(chan struct{})
	ocr := &checkpointOCR{transcribe: func(ctx context.Context, call int) (string, error) {
		entered <- struct{}{}
		release := first
		if call == 2 {
			release = second
		}
		select {
		case <-release:
			return "finished line", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}}
	h, id := checkpointJob(t, ocr, 2, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.processQueuedTranscriptionJob(ctx, id) }()
	awaitCheckpointCall(t, entered)
	awaitCheckpointCall(t, entered)
	close(first)
	for {
		job, err := h.transcriptionJobs.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if job.CompletedSegments == 1 {
			if job.CurrentAnnotationID == "" {
				t.Fatal("active line disappeared when another line completed")
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("first line did not finish")
		case <-time.After(10 * time.Millisecond):
		}
	}
	close(second)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	assertNoCheckpointRows(t, id)
}

func TestCorruptLineCheckpointFallsBackToCanonicalPage(t *testing.T) {
	ocr := &checkpointOCR{transcribe: func(context.Context, int) (string, error) { return "fresh", nil }}
	h, id := checkpointJob(t, ocr, 2, 2)
	ctx := context.Background()
	job, err := h.transcriptionJobs.ClaimPendingByID(ctx, id)
	if err != nil || job == nil {
		t.Fatalf("claim: %v", err)
	}
	fence, err := job.Fence()
	if err != nil {
		t.Fatal(err)
	}
	page, err := h.annotations.LoadPage(ctx, job.WorkspaceID, job.ItemImageID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.transcriptionJobs.SaveCheckpoint(ctx, fence, "", page.Payload); err != nil {
		t.Fatal(err)
	}
	if err := h.transcriptionJobs.SaveCheckpoint(ctx, fence, "wrong-line", `null`); err != nil {
		t.Fatal(err)
	}
	if err := h.processClaimedTranscriptionJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	if ocr.calls.Load() != 2 {
		t.Fatalf("calls = %d", ocr.calls.Load())
	}
	assertNoCheckpointRows(t, id)
}

func TestQueuedTranscriptionZeroConcurrencyDefaults(t *testing.T) {
	previous := config.Get()
	configured := previous
	configured.Config.LLM.LineTranscribeConcurrency = 0
	config.Init(configured)
	t.Cleanup(func() { config.Init(previous) })
	ocr := &checkpointOCR{transcribe: func(context.Context, int) (string, error) { return "defaulted", nil }}
	h, id := checkpointJob(t, ocr, 2, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.processQueuedTranscriptionJob(ctx, id) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("zero concurrency hung the job")
	}
}

func TestItemDeletionAndCheckpointWritesUseConsistentLockOrder(t *testing.T) {
	for attempt := 0; attempt < 10; attempt++ {
		ocr := &checkpointOCR{transcribe: func(context.Context, int) (string, error) { return "unused", nil }}
		h, id := checkpointJob(t, ocr, 1, 2)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		job, err := h.transcriptionJobs.ClaimPendingByID(ctx, id)
		if err != nil || job == nil {
			t.Fatalf("claim: %v", err)
		}
		fence, err := job.Fence()
		if err != nil {
			t.Fatal(err)
		}
		image, err := h.items.GetImage(ctx, job.ItemImageID)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.transcriptionJobs.SaveCheckpoint(ctx, fence, "line", "initial"); err != nil {
			t.Fatal(err)
		}
		started := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			close(started)
			for write := 0; write < 25; write++ {
				if err := h.transcriptionJobs.SaveCheckpoint(ctx, fence, "line", strings.Repeat("x", write+1)); err != nil {
					if errors.Is(err, store.ErrTranscriptionJobFence) {
						done <- nil
					} else {
						done <- err
					}
					return
				}
			}
			done <- nil
		}()
		<-started
		if err := h.items.DeleteForWorkspace(ctx, image.ItemID, job.WorkspaceID); err != nil {
			t.Fatalf("delete while checkpointing: %v", err)
		}
		if err := <-done; err != nil {
			t.Fatalf("checkpoint while deleting: %v", err)
		}
		assertNoCheckpointRows(t, id)
	}
}
