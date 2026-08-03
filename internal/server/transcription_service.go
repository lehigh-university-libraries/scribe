package server

import (
	"container/heap"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/hocr"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/providerregistry"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
	"github.com/lehigh-university-libraries/scribe/proto/scribe/v1/scribev1connect"
)

// Ensure Handler implements the TranscriptionService interface.
var _ scribev1connect.TranscriptionServiceHandler = (*Handler)(nil)

// --- ConnectRPC handlers ---

func (h *Handler) CreateTranscriptionJob(
	ctx context.Context,
	req *connect.Request[scribev1.CreateTranscriptionJobRequest],
) (*connect.Response[scribev1.CreateTranscriptionJobResponse], error) {
	itemImageID := req.Msg.GetItemImageId()
	if itemImageID == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("item_image_id is required"))
	}

	var contextID *uint64
	if req.Msg.GetContextId() > 0 {
		if _, err := h.contextForRead(ctx, req.Msg.GetContextId()); err != nil {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("context not found"))
		}
		v := req.Msg.GetContextId()
		contextID = &v
	}

	jobID, err := h.createTranscriptionJob(ctx, itemImageID, contextID)
	if err != nil {
		return nil, transcriptionJobConnectError("create job", err)
	}

	return connect.NewResponse(&scribev1.CreateTranscriptionJobResponse{JobId: jobID}), nil
}

func transcriptionJobConnectError(operation string, err error) error {
	var quotaErr *store.TranscriptionJobQuotaExceededError
	switch {
	case errors.As(err, &quotaErr):
		return connect.NewError(connect.CodeResourceExhausted, quotaErr)
	case errors.Is(err, store.ErrTranscriptionCanonicalPageRequired):
		return connect.NewError(connect.CodeFailedPrecondition, store.ErrTranscriptionCanonicalPageRequired)
	default:
		return connect.NewError(connect.CodeInternal, fmt.Errorf("%s: %w", operation, err))
	}
}

func transcriptionJobReadConnectError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("transcription job not found"))
	}
	return connect.NewError(connect.CodeInternal, fmt.Errorf("load transcription job: %w", err))
}

func (h *Handler) CancelTranscriptionJob(
	ctx context.Context,
	req *connect.Request[scribev1.CancelTranscriptionJobRequest],
) (*connect.Response[scribev1.CancelTranscriptionJobResponse], error) {
	jobID := req.Msg.GetJobId()
	job, err := h.transcriptionJobs.Get(ctx, jobID)
	if err != nil {
		return nil, transcriptionJobReadConnectError(err)
	}
	if job.Status == store.TranscriptionJobStatusCanceled {
		return connect.NewResponse(&scribev1.CancelTranscriptionJobResponse{Job: storeJobToProto(job)}), nil
	}
	if job.Status == store.TranscriptionJobStatusCompleted || job.Status == store.TranscriptionJobStatusFailed || job.Status == store.TranscriptionJobStatusSuperseded {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("transcription job is already terminal"))
	}
	if err := h.transcriptionJobs.Cancel(ctx, jobID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("transcription job is already terminal"))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("cancel transcription job: %w", err))
	}
	job, err = h.transcriptionJobs.Get(ctx, jobID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("reload canceled transcription job: %w", err))
	}
	h.publishEvent("dev.scribe.transcription.canceled", subjectForItemImage(job.ItemImageID), map[string]any{
		"jobId":       job.ID,
		"itemImageId": job.ItemImageID,
	})
	return connect.NewResponse(&scribev1.CancelTranscriptionJobResponse{Job: storeJobToProto(job)}), nil
}

func (h *Handler) GetTranscriptionJob(
	ctx context.Context,
	req *connect.Request[scribev1.GetTranscriptionJobRequest],
) (*connect.Response[scribev1.GetTranscriptionJobResponse], error) {
	job, err := h.transcriptionJobs.Get(ctx, req.Msg.GetJobId())
	if err != nil {
		return nil, transcriptionJobReadConnectError(err)
	}
	return connect.NewResponse(&scribev1.GetTranscriptionJobResponse{Job: storeJobToProto(job)}), nil
}

func (h *Handler) ListTranscriptionJobs(
	ctx context.Context,
	req *connect.Request[scribev1.ListTranscriptionJobsRequest],
) (*connect.Response[scribev1.ListTranscriptionJobsResponse], error) {
	if h.itemPageTokens == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("transcription job pagination is not configured"))
	}
	workspaceID := h.currentWorkspaceID(ctx)
	itemImageID := req.Msg.GetItemImageId()
	if itemImageID != 0 {
		if _, err := h.itemImageForRequest(ctx, itemImageID); err != nil {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("item image not found"))
		}
	}
	pageSize, cursor, err := normalizeTranscriptionJobPageRequest(
		req.Msg.GetPageSize(), req.Msg.GetPageToken(), workspaceID, itemImageID, h.itemPageTokens,
	)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	page, err := h.transcriptionJobs.ListPage(ctx, workspaceID, itemImageID, pageSize, cursor)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list transcription jobs"))
	}
	nextPageToken, err := h.itemPageTokens.encodeTranscriptionJobPage(page.NextCursor, workspaceID, itemImageID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("encode transcription job page token"))
	}
	protoJobs := make([]*scribev1.TranscriptionJobSummary, 0, len(page.Jobs))
	for _, j := range page.Jobs {
		protoJobs = append(protoJobs, storeJobSummaryToProto(j))
	}
	return connect.NewResponse(&scribev1.ListTranscriptionJobsResponse{
		Jobs:          protoJobs,
		NextPageToken: nextPageToken,
	}), nil
}

// StreamTranscriptionJob sends a TranscriptionJob message every time the job
// is updated until the job reaches a terminal state or the client disconnects.
func (h *Handler) StreamTranscriptionJob(
	ctx context.Context,
	req *connect.Request[scribev1.StreamTranscriptionJobRequest],
	stream *connect.ServerStream[scribev1.StreamTranscriptionJobResponse],
) error {
	if h.sseLimiter != nil {
		release, allowed := h.sseLimiter.Acquire(h.currentWorkspaceID(ctx))
		if !allowed {
			return connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("stream connection limit exceeded"))
		}
		defer release()
	}
	jobID := req.Msg.GetJobId()
	pollInterval := 500 * time.Millisecond
	timer := time.NewTimer(0)
	defer timer.Stop()

	var lastUpdatedAt time.Time

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			job, err := h.transcriptionJobs.Get(ctx, jobID)
			if err != nil {
				return transcriptionJobReadConnectError(err)
			}

			// Only send when the job has actually changed.
			if job.UpdatedAt.Equal(lastUpdatedAt) {
				// If job is terminal, we're done.
				if transcriptionJobTerminal(job.Status) {
					return nil
				}
				pollInterval *= 2
				if pollInterval > 10*time.Second {
					pollInterval = 10 * time.Second
				}
				timer.Reset(pollInterval)
				continue
			}
			lastUpdatedAt = job.UpdatedAt
			pollInterval = 500 * time.Millisecond

			if err := stream.Send(&scribev1.StreamTranscriptionJobResponse{Job: storeJobToProto(job)}); err != nil {
				return err
			}

			if transcriptionJobTerminal(job.Status) {
				return nil
			}
			timer.Reset(pollInterval)
		}
	}
}

// --- background worker ---

// StartTranscriptionWorker launches the background job worker. Call once at
// startup; it runs until ctx is cancelled.
func (h *Handler) StartTranscriptionWorker(ctx context.Context) {
	workerCount := transcriptionJobWorkerCount()
	if h.transcriptionQueue != nil {
		slog.Info("Starting Pub/Sub transcription job worker", "workers", workerCount)
		sem := make(chan struct{}, workerCount)
		h.startTranscriptionWorker(func() {
			if err := h.transcriptionQueue.ReceiveTranscriptionJobs(ctx, func(msgCtx context.Context, jobID uint64) error {
				if !acquireTranscriptionSlot(msgCtx, sem) {
					return msgCtx.Err()
				}
				defer releaseTranscriptionSlot(sem)
				return h.processQueuedTranscriptionJob(msgCtx, jobID)
			}, func(msgCtx context.Context, messageID string, _ error, body []byte) {
				eventData := poisonedTranscriptionEventData(messageID, body)
				slog.Warn("Rejected invalid transcription queue message",
					"message_id", messageID,
					"failure", "invalid transcription queue message",
					"body_bytes", eventData["bodyBytes"],
					"body_sha256", eventData["bodySha256"],
				)
				h.publishEvent("dev.scribe.transcription.poisoned", "transcription/poisoned", eventData)
			}); err != nil {
				slog.Error("Pub/Sub transcription worker stopped with error", "error_type", safeLogErrorType(err), "category", safeLogErrorCategory(err))
			}
		})
		h.startTranscriptionWorker(func() { h.transcriptionRecoveryLoop(ctx, sem) })
		return
	}
	slog.Info("Starting transcription job worker pool", "workers", workerCount)
	for i := 0; i < workerCount; i++ {
		workerID := i + 1
		h.startTranscriptionWorker(func() { h.transcriptionWorkerLoop(ctx, workerID) })
	}
}

func (h *Handler) startTranscriptionWorker(run func()) {
	if h == nil || run == nil {
		return
	}
	h.transcriptionWorkerWG.Add(1)
	go func() {
		defer h.transcriptionWorkerWG.Done()
		run()
	}()
}

func (h *Handler) startBackgroundWorker(run func()) {
	if h == nil || run == nil {
		return
	}
	h.backgroundWorkerWG.Add(1)
	go func() {
		defer h.backgroundWorkerWG.Done()
		run()
	}()
}

// WaitForTranscriptionWorkers waits until all worker loops and their active
// lease attempt have observed cancellation. The caller owns the bounded
// shutdown context.
func (h *Handler) WaitForTranscriptionWorkers(ctx context.Context) error {
	if h == nil {
		return nil
	}
	done := make(chan struct{})
	go func() {
		h.transcriptionWorkerWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// WaitForBackgroundWorkers waits for every handler-owned worker and maintenance
// loop. The caller must cancel the contexts passed to the Start methods before
// waiting so no new recovery attempt can be admitted during the drain.
func (h *Handler) WaitForBackgroundWorkers(ctx context.Context) error {
	if h == nil {
		return nil
	}
	done := make(chan struct{})
	go func() {
		h.transcriptionWorkerWG.Wait()
		h.backgroundWorkerWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func poisonedTranscriptionEventData(messageID string, body []byte) map[string]any {
	digest := sha256.Sum256(body)
	return map[string]any{
		"messageId":  strings.TrimSpace(messageID),
		"error":      "invalid transcription queue message",
		"bodyBytes":  len(body),
		"bodySha256": fmt.Sprintf("%x", digest),
	}
}

func (h *Handler) createTranscriptionJob(ctx context.Context, itemImageID uint64, contextID *uint64) (uint64, error) {
	img, err := h.items.GetImage(ctx, itemImageID)
	if err != nil {
		return 0, fmt.Errorf("get item image %d: %w", itemImageID, err)
	}
	item, err := h.items.Get(ctx, img.ItemID)
	if err != nil {
		return 0, fmt.Errorf("get item %s: %w", img.ItemID, err)
	}
	var processingContext store.Context
	if contextID != nil && *contextID > 0 {
		processingContext, err = h.contexts.GetForWorkspace(ctx, *contextID, item.WorkspaceID)
	} else {
		processingContext, _, err = h.contexts.ResolveForWorkspace(ctx, item.WorkspaceID, nil)
	}
	if err != nil {
		return 0, fmt.Errorf("resolve transcription context: %w", err)
	}
	jobID, err := h.transcriptionJobs.Create(ctx, itemImageID, processingContext)
	if err != nil {
		return 0, err
	}
	h.publishTranscriptionJob(ctx, jobID)
	return jobID, nil
}

func (h *Handler) publishTranscriptionJob(ctx context.Context, jobID uint64) {
	if h.transcriptionQueue != nil {
		if err := h.transcriptionQueue.PublishTranscriptionJob(ctx, jobID); err != nil {
			slog.Warn("Failed to publish transcription job message; recovery poller will pick it up", "job_id", jobID, "error_type", safeLogErrorType(err), "category", safeLogErrorCategory(err))
		}
	}
}

func (h *Handler) transcriptionWorkerLoop(ctx context.Context, workerID int) {
	slog.Info("Transcription job worker started", "worker_id", workerID)
	for {
		select {
		case <-ctx.Done():
			slog.Info("Transcription job worker stopped", "worker_id", workerID)
			return
		default:
		}

		job, err := h.transcriptionJobs.ClaimNextPending(ctx)
		if err != nil {
			slog.Error("Failed to claim transcription job", "worker_id", workerID, "error_type", safeLogErrorType(err), "category", safeLogErrorCategory(err))
			if !sleepOrDone(ctx, 5*time.Second) {
				return
			}
			continue
		}
		if job == nil {
			if !sleepOrDone(ctx, 3*time.Second) {
				return
			}
			continue
		}

		slog.Info("Processing transcription job", "worker_id", workerID, "job_id", job.ID, "item_image_id", job.ItemImageID)
		if err := h.processClaimedTranscriptionJob(ctx, job); err != nil {
			slog.Error("Transcription job failed", "worker_id", workerID, "job_id", job.ID, "failure", store.SafeTranscriptionFailureMessage(err))
			if recordErr := h.recordClaimedTranscriptionJobFailure(ctx, job, err); recordErr != nil {
				slog.Error("Failed to persist transcription job failure", "worker_id", workerID, "job_id", job.ID, "error_type", safeLogErrorType(recordErr))
			}
		}
	}
}

func (h *Handler) processQueuedTranscriptionJob(ctx context.Context, jobID uint64) error {
	job, err := h.transcriptionJobs.ClaimPendingByID(ctx, jobID)
	if err != nil {
		return err
	}
	if job == nil {
		return nil
	}
	slog.Info("Processing queued transcription job", "job_id", job.ID, "item_image_id", job.ItemImageID)
	if err := h.processClaimedTranscriptionJob(ctx, job); err != nil {
		slog.Error("Queued transcription job failed", "job_id", job.ID, "failure", store.SafeTranscriptionFailureMessage(err))
		if recordErr := h.recordClaimedTranscriptionJobFailure(ctx, job, err); recordErr != nil {
			return recordErr
		}
		return err
	}
	return nil
}

func (h *Handler) transcriptionRecoveryLoop(ctx context.Context, sem chan struct{}) {
	if sem == nil {
		sem = make(chan struct{}, 1)
	}
	interval := config.Get().Config.Transcription.Queue.RecoveryPollInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	minAge := config.Get().Config.Transcription.Queue.RecoveryMinAge
	if minAge <= 0 {
		minAge = 20 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for {
				if !acquireTranscriptionSlot(ctx, sem) {
					return
				}
				job, err := h.transcriptionJobs.ClaimNextPendingOlderThan(ctx, time.Now().UTC().Add(-minAge))
				if err != nil {
					releaseTranscriptionSlot(sem)
					slog.Error("Failed to claim recovery transcription job", "error_type", safeLogErrorType(err), "category", safeLogErrorCategory(err))
					break
				}
				if job == nil {
					releaseTranscriptionSlot(sem)
					break
				}
				claimedJob := job
				h.startTranscriptionWorker(func() {
					defer releaseTranscriptionSlot(sem)
					slog.Info("Processing recovery transcription job", "job_id", claimedJob.ID, "item_image_id", claimedJob.ItemImageID)
					if err := h.processClaimedTranscriptionJob(ctx, claimedJob); err != nil {
						slog.Error("Recovery transcription job failed", "job_id", claimedJob.ID, "failure", store.SafeTranscriptionFailureMessage(err))
						if recordErr := h.recordClaimedTranscriptionJobFailure(ctx, claimedJob, err); recordErr != nil {
							slog.Error("Failed to persist recovery transcription job failure", "job_id", claimedJob.ID, "error_type", safeLogErrorType(recordErr))
						}
					}
				})
			}
		}
	}
}

func acquireTranscriptionSlot(ctx context.Context, sem chan struct{}) bool {
	select {
	case sem <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func releaseTranscriptionSlot(sem chan struct{}) {
	select {
	case <-sem:
	default:
	}
}

func (h *Handler) processClaimedTranscriptionJob(ctx context.Context, job *store.TranscriptionJob) error {
	fence, err := job.Fence()
	if err != nil {
		return fmt.Errorf("claimed transcription job %d has an invalid attempt fence: %w", job.ID, err)
	}
	jobCtx, stopHeartbeat := h.startTranscriptionJobLeaseHeartbeat(ctx, fence)
	defer stopHeartbeat()
	err = h.processTranscriptionJob(jobCtx, job)
	if errors.Is(err, store.ErrTranscriptionJobFence) {
		return fmt.Errorf("%w: %v", errTranscriptionJobLeaseLost, err)
	}
	if errors.Is(err, context.Canceled) {
		if cause := context.Cause(jobCtx); cause != nil && !errors.Is(cause, context.Canceled) {
			return cause
		}
	}
	return err
}

var errTranscriptionJobLeaseLost = errors.New("transcription job lease lost")

type permanentTranscriptionError struct {
	message string
}

func (e permanentTranscriptionError) Error() string {
	return e.message
}

func permanentTranscriptionFailure(message string) error {
	return permanentTranscriptionError{message: message}
}

func transcriptionJobFailureForSegment(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return err
	case errors.Is(err, hocr.ErrPermanentProviderRequest):
		return permanentTranscriptionFailure("transcription provider request failed")
	case errors.Is(err, hocr.ErrRetryableProviderRequest):
		return fmt.Errorf("transcription provider request failed: %w", err)
	default:
		return nil
	}
}

func (h *Handler) recordClaimedTranscriptionJobFailure(ctx context.Context, job *store.TranscriptionJob, err error) error {
	if errors.Is(err, errTranscriptionJobLeaseLost) {
		// Another worker owns the lease now. It alone may mutate job state.
		return nil
	}
	fence, fenceErr := job.Fence()
	if fenceErr != nil {
		return fenceErr
	}
	safeMessage := store.SafeTranscriptionFailureMessage(err)
	if errors.Is(err, context.Canceled) {
		deferCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return h.transcriptionJobs.Defer(deferCtx, fence, safeMessage, time.Now().UTC().Add(5*time.Second))
	}
	eventData := map[string]any{
		"jobId":       job.ID,
		"itemImageId": job.ItemImageID,
		"error":       safeMessage,
		"workspaceId": job.WorkspaceID,
		"revision":    job.InputRevision,
	}
	if h.items != nil {
		if image, imageErr := h.items.GetImageForWorkspace(ctx, job.ItemImageID, job.WorkspaceID); imageErr == nil {
			if item, itemErr := h.items.GetForWorkspace(ctx, image.ItemID, job.WorkspaceID); itemErr == nil {
				eventData = mergeEventData(eventData, itemEventData(item, image, job.InputRevision))
			}
		}
	}
	evt := h.newCloudEvent("dev.scribe.transcription.failed", subjectForItemImage(job.ItemImageID), eventData)
	body, marshalErr := json.Marshal(evt)
	if marshalErr != nil {
		return fmt.Errorf("marshal failure event: %w", marshalErr)
	}
	var permanent permanentTranscriptionError
	if errors.As(err, &permanent) {
		if failErr := h.transcriptionJobs.PermanentlyFailWithWebhookEvent(ctx, fence, safeMessage, evt.ID, evt.Type, evt.Subject, string(body)); failErr != nil {
			return failErr
		}
		h.publishCloudEvent(evt, false)
		return nil
	}
	terminal, failErr := h.transcriptionJobs.FailWithWebhookEvent(ctx, fence, safeMessage, evt.ID, evt.Type, evt.Subject, string(body))
	if failErr != nil {
		return failErr
	}
	if terminal {
		h.publishCloudEvent(evt, false)
	}
	return nil
}

func (h *Handler) startTranscriptionJobLeaseHeartbeat(
	parent context.Context,
	fence store.TranscriptionAttemptFence,
) (context.Context, func()) {
	ticker := time.NewTicker(2 * time.Minute)
	leaseCtx, stop := newTranscriptionJobLeaseHeartbeat(parent, ticker.C, func(renewCtx context.Context) error {
		return h.transcriptionJobs.ExtendLease(renewCtx, fence, 10*time.Minute)
	})
	return leaseCtx, func() {
		ticker.Stop()
		stop()
	}
}

const transcriptionJobLeaseRenewTimeout = 15 * time.Second

func newTranscriptionJobLeaseHeartbeat(
	parent context.Context,
	ticks <-chan time.Time,
	renew func(context.Context) error,
) (context.Context, func()) {
	leaseCtx, cancel := context.WithCancelCause(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-leaseCtx.Done():
				return
			case _, ok := <-ticks:
				if !ok {
					return
				}
				renewCtx, stopRenew := context.WithTimeout(leaseCtx, transcriptionJobLeaseRenewTimeout)
				err := renew(renewCtx)
				stopRenew()
				if err == nil {
					continue
				}
				if leaseCtx.Err() != nil {
					return
				}
				cancel(fmt.Errorf("%w: %v", errTranscriptionJobLeaseLost, err))
				return
			}
		}
	}()
	var once sync.Once
	return leaseCtx, func() {
		once.Do(func() { cancel(context.Canceled) })
		<-done
	}
}

func transcriptionJobWorkerCount() int {
	if v := config.Get().Config.Transcription.JobWorkers; v > 0 {
		return v
	}
	return config.DefaultTranscriptionJobWorkers
}

func transcriptionJobSegmentLimit() int {
	if value := config.Get().Config.Transcription.MaxSegmentsPerJob; value > 0 {
		return value
	}
	return config.DefaultMaxTranscriptionSegmentsPerJob
}

func sleepOrDone(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (h *Handler) resolveTranscriptionJobContext(ctx context.Context, job *store.TranscriptionJob) (store.Context, uint64, error) {
	img, err := h.items.GetImage(ctx, job.ItemImageID)
	if err != nil {
		return store.Context{}, 0, fmt.Errorf("get item image %d: %w", job.ItemImageID, err)
	}
	item, err := h.items.Get(ctx, img.ItemID)
	if err != nil {
		return store.Context{}, 0, fmt.Errorf("get item %s: %w", img.ItemID, err)
	}
	if len(job.ContextSnapshot) == 0 {
		return store.Context{}, 0, fmt.Errorf("transcription job %d has no context snapshot", job.ID)
	}
	var contextValue store.Context
	if err := json.Unmarshal(job.ContextSnapshot, &contextValue); err != nil {
		return store.Context{}, 0, fmt.Errorf("decode transcription job %d context snapshot: %w", job.ID, err)
	}
	if contextValue.ID == 0 || (job.ContextID != nil && *job.ContextID != contextValue.ID) {
		return store.Context{}, 0, fmt.Errorf("transcription job %d context snapshot identity is invalid", job.ID)
	}
	if contextValue.WorkspaceID != nil && *contextValue.WorkspaceID != item.WorkspaceID {
		return store.Context{}, 0, fmt.Errorf("transcription job %d context snapshot belongs to another workspace", job.ID)
	}
	return contextValue, item.WorkspaceID, nil
}

func (h *Handler) processTranscriptionJob(ctx context.Context, job *store.TranscriptionJob) error {
	fence, err := job.Fence()
	if err != nil {
		return err
	}
	pctx, workspaceID, err := h.resolveTranscriptionJobContext(ctx, job)
	if err != nil {
		return err
	}

	img, err := h.items.GetImage(ctx, job.ItemImageID)
	if err != nil {
		return fmt.Errorf("get item image %d: %w", job.ItemImageID, err)
	}
	if strings.TrimSpace(img.CanvasURI) == "" {
		return permanentTranscriptionFailure("canonical annotation page is unavailable")
	}

	page, err := h.annotations.LoadPage(ctx, workspaceID, job.ItemImageID)
	if err != nil {
		if errors.Is(err, store.ErrAnnotationPageNotFound) {
			return permanentTranscriptionFailure("canonical annotation page is unavailable")
		}
		return fmt.Errorf("load canonical annotation page: %w", err)
	}
	if job.InputRevision != page.Revision {
		return permanentTranscriptionFailure(fmt.Sprintf(
			"canonical annotation page changed from revision %d to %d before transcription began",
			job.InputRevision,
			page.Revision,
		))
	}
	var pageDocument map[string]any
	if err := iiif.DecodeJSON([]byte(page.Payload), &pageDocument); err != nil {
		return fmt.Errorf("decode canonical annotation page: %w", err)
	}
	pageItems, ok := pageDocument["items"].([]any)
	if !ok {
		return fmt.Errorf("canonical annotation page items are not an array")
	}
	if !containsLineAnnotation(pageItems) {
		releaseSegmentation, acquireErr := h.processingLimiter.Acquire(ctx, workspaceID, processingLimitSegmentor(pctx))
		if acquireErr != nil {
			return fmt.Errorf("acquire segmentation processing capacity: %w", acquireErr)
		}
		segmentedItems, segmentErr := h.segmentTranscriptionJobPage(ctx, job, pctx, img)
		releaseSegmentation()
		if segmentErr != nil {
			return segmentErr
		}
		pageItems = append(preserveNonOCRAnnotations(pageItems), segmentedItems...)
		pageDocument["items"] = pageItems
	}

	type annotationEntry struct {
		id       string
		payload  string
		position int
	}
	var lines []annotationEntry
	for position, value := range pageItems {
		anno, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if granularity, _ := anno["textGranularity"].(string); strings.EqualFold(granularity, "line") {
			id, _ := anno["id"].(string)
			payload, marshalErr := json.Marshal(anno)
			if marshalErr != nil {
				return fmt.Errorf("encode line annotation %q: %w", id, marshalErr)
			}
			lines = append(lines, annotationEntry{id: id, payload: string(payload), position: position})
		}
	}

	total := len(lines)
	maxSegments := transcriptionJobSegmentLimit()
	if total > maxSegments {
		return permanentTranscriptionFailure(fmt.Sprintf(
			"transcription job contains %d segments; configured maximum is %d",
			total,
			maxSegments,
		))
	}
	registry := providerregistry.New(config.Get().Config)
	if err := registry.ValidateSelection(pctx.TranscriptionProvider, pctx.TranscriptionModel, pctx.SystemPrompt, pctx.Temperature); err != nil {
		return permanentTranscriptionFailure("transcription context snapshot is invalid")
	}
	provider, ok := registry.Provider(pctx.TranscriptionProvider)
	if !ok {
		return permanentTranscriptionFailure("transcription context snapshot is invalid")
	}
	ctx, err = h.contextWithWorkspaceProviderSecret(ctx, workspaceID, pctx.TranscriptionProvider)
	if err != nil {
		switch {
		case errors.Is(err, errWorkspaceProviderCredentialNotStored):
			return permanentTranscriptionFailure("workspace provider credential is not configured")
		case errors.Is(err, errWorkspaceProviderCredentialInvalid):
			return permanentTranscriptionFailure("workspace provider credential is invalid")
		default:
			return fmt.Errorf("resolve workspace provider credential: %w", err)
		}
	}
	for _, field := range provider.Credentials.Fields {
		if field.Required && strings.TrimSpace(provider.Credential(ctx, field.ID)) == "" {
			return permanentTranscriptionFailure("workspace provider credential is not configured")
		}
	}
	releaseTranscription, err := h.processingLimiter.Acquire(ctx, workspaceID, processingLimitProvider(pctx))
	if err != nil {
		return fmt.Errorf("acquire transcription processing capacity: %w", err)
	}
	defer releaseTranscription()
	slog.Info("Transcription job: found line annotations", "job_id", job.ID, "count", total)
	if err := h.transcriptionJobs.SetTotalSegments(ctx, fence, total); err != nil {
		return fmt.Errorf("set total segments: %w", err)
	}
	// Results are committed only as a full page. A retry therefore starts from
	// the newly loaded canonical revision instead of pretending prior counters
	// represent durable per-segment output. Claiming the attempt atomically
	// clears its mutable progress, so the first worker update always represents
	// actual work rather than an idempotent reset that MySQL can report as zero
	// affected rows.
	completed, failed := 0, 0
	for i, entry := range lines {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		slog.Info("Transcribing segment", "job_id", job.ID, "index", i+1, "total", total, "annotation_id", entry.id)

		// Mark current segment in progress.
		if err := h.transcriptionJobs.UpdateProgress(ctx, fence,
			completed, failed, entry.id, entry.payload, ""); err != nil {
			if errors.Is(err, store.ErrTranscriptionJobFence) {
				return fmt.Errorf("job lease lost before segment: %w", err)
			}
			return fmt.Errorf("update progress before segment: %w", err)
		}
		h.publishEvent("dev.scribe.transcription.task.started", subjectForAnnotation(job.ItemImageID, entry.id), map[string]any{
			"jobId":             job.ID,
			"itemImageId":       job.ItemImageID,
			"annotationId":      entry.id,
			"completedSegments": completed,
			"failedSegments":    failed,
			"totalSegments":     total,
			"annotationJson":    entry.payload,
		})

		enriched, err := h.enrichSingleAnnotationInWorkspace(ctx, job.ItemImageID, entry.payload, pctx, workspaceID, nil)
		if err != nil {
			slog.Warn("Segment transcription failed", "job_id", job.ID, "annotation_id", entry.id, "failure", store.SafeTranscriptionFailureMessage(err))
			if jobFailure := transcriptionJobFailureForSegment(err); jobFailure != nil {
				return jobFailure
			}
			failed++
			if err := h.transcriptionJobs.UpdateProgress(ctx, fence,
				completed, failed, "", "", ""); err != nil {
				if errors.Is(err, store.ErrTranscriptionJobFence) {
					return fmt.Errorf("job lease lost after segment failure: %w", err)
				}
				return fmt.Errorf("update progress after segment failure: %w", err)
			}
			continue
		}

		var enrichedAnno map[string]any
		if err := iiif.DecodeJSON([]byte(enriched), &enrichedAnno); err != nil {
			return fmt.Errorf("decode enriched annotation %q: %w", entry.id, err)
		}
		pageItems[entry.position] = enrichedAnno

		completed++
		if err := h.transcriptionJobs.UpdateProgress(ctx, fence,
			completed, failed, "", "", enriched); err != nil {
			if errors.Is(err, store.ErrTranscriptionJobFence) {
				return fmt.Errorf("job lease lost after segment success: %w", err)
			}
			return fmt.Errorf("update progress after segment success: %w", err)
		}
		h.publishEvent("dev.scribe.transcription.task.completed", subjectForAnnotation(job.ItemImageID, entry.id), map[string]any{
			"jobId":             job.ID,
			"itemImageId":       job.ItemImageID,
			"annotationId":      entry.id,
			"completedSegments": completed,
			"failedSegments":    failed,
			"totalSegments":     total,
			"annotationJson":    enriched,
			"persisted":         false,
		})
	}
	if failed > 0 {
		return fmt.Errorf("transcription failed for %d of %d segments", failed, total)
	}

	pageItems = reconcileTranscribedWords(pageItems)
	pageDocument["items"] = pageItems
	pageJSON, err := json.Marshal(pageDocument)
	if err != nil {
		return fmt.Errorf("encode enriched annotation page: %w", err)
	}
	normalized, err := iiif.NormalizeAnnotationPage(pageJSON, iiif.PageIdentity{
		PublicBaseURL: h.publicAnnotationBaseURL(),
		ItemImageID:   job.ItemImageID,
		CanvasURI:     img.CanvasURI,
	})
	if err != nil {
		return fmt.Errorf("validate enriched annotation page: %w", err)
	}
	if err := iiif.ValidateAnnotationPageGeometry(normalized, img.Width, img.Height); err != nil {
		return permanentTranscriptionFailure("generated annotation geometry is invalid")
	}
	page.Payload = string(normalized)
	// A durable worker is a system actor. The item creator may not be the user
	// who requested this job, and attributing the save to that creator would
	// create a false audit record.
	page.UpdatedByUserID = nil
	item, err := h.items.GetForWorkspace(ctx, img.ItemID, workspaceID)
	if err != nil {
		return fmt.Errorf("load transcription event item: %w", err)
	}
	eventData := itemEventData(item, img, page.Revision+1)
	eventData = mergeEventData(eventData, map[string]any{
		"jobId":             job.ID,
		"completedSegments": completed,
		"failedSegments":    failed,
		"totalSegments":     total,
	})
	evt := h.newCloudEvent("dev.scribe.transcription.completed", subjectForItemImage(job.ItemImageID), eventData)
	body, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("marshal completion event: %w", err)
	}
	provenance, err := h.transcriptionJobOCRRun(ctx, job, pctx, img, page, completed)
	if err != nil {
		return fmt.Errorf("build transcription OCR provenance: %w", err)
	}
	_, err = h.annotations.SavePageAndCompleteTranscriptionJob(ctx, page, page.Revision, store.AnnotationJobCompletion{
		TranscriptionAttemptFence: fence,
		EventID:                   evt.ID,
		EventType:                 evt.Type,
		Subject:                   evt.Subject,
		BodyJSON:                  string(body),
		OCRRun:                    provenance,
	})
	if errors.Is(err, store.ErrAnnotationJobFence) {
		return fmt.Errorf("%w: canonical page commit", errTranscriptionJobLeaseLost)
	}
	if errors.Is(err, store.ErrAnnotationRevisionConflict) {
		return permanentTranscriptionFailure("canonical annotation page changed while transcription was running")
	}
	if err != nil {
		return fmt.Errorf("commit enriched annotation page: %w", err)
	}
	slog.Info("Transcription job complete", "job_id", job.ID, "completed", completed, "failed", failed)
	h.publishCloudEvent(evt, false)
	return nil
}

func containsLineAnnotation(items []any) bool {
	for _, value := range items {
		annotation, ok := value.(map[string]any)
		if ok && strings.EqualFold(strings.TrimSpace(annStringValue(annotation, "textGranularity")), "line") {
			return true
		}
	}
	return false
}

// preserveNonOCRAnnotations keeps extension annotations that do not participate
// in Scribe's OCR text model. A no-line processing job replaces incomplete OCR
// granularities with one coherent segmentation result while retaining unrelated
// IIIF resources and their unknown properties.
func preserveNonOCRAnnotations(items []any) []any {
	preserved := make([]any, 0, len(items))
	for _, value := range items {
		annotation, ok := value.(map[string]any)
		if !ok || strings.TrimSpace(annStringValue(annotation, "textGranularity")) == "" {
			preserved = append(preserved, value)
		}
	}
	return preserved
}

func (h *Handler) segmentTranscriptionJobPage(
	ctx context.Context,
	job *store.TranscriptionJob,
	pctx store.Context,
	image store.ItemImage,
) ([]any, error) {
	processingContext := processingContextFromStore(pctx)
	processingContext.SegmentOnly = true
	contextID := pctx.ID
	itemImageID := image.ID
	callContext := hocr.WithProviderCallMetadata(ctx, job.WorkspaceID, "", &itemImageID, &contextID)
	result, err := h.ocr.ProcessImageURLTransientWithContext(callContext, image.ImageURL, processingContext)
	if err != nil {
		return nil, fmt.Errorf("segment image-only transcription input: %w", err)
	}
	if result == nil || strings.TrimSpace(result.HOCR) == "" {
		return nil, permanentTranscriptionFailure("segmentation produced no hOCR")
	}
	parsedHOCR, err := parsedHOCRDocument(result.HOCR, result.ParsedHOCR)
	if err != nil {
		return nil, permanentTranscriptionFailure("segmentation produced invalid hOCR")
	}
	if len(parsedHOCR.Lines) == 0 {
		return nil, permanentTranscriptionFailure("segmentation produced no line annotations")
	}
	scope := fmt.Sprintf("transcription-job-%d-segmentation", job.ID)
	items := buildLineAnnotations(scope, image.CanvasURI, parsedHOCR.Lines)
	items = append(items, buildWordAnnotations(scope, image.CanvasURI, parsedHOCR.Words)...)
	if len(items) == 0 {
		return nil, permanentTranscriptionFailure("segmentation produced no usable annotations")
	}
	return items, nil
}

// reconcileTranscribedWords prevents stale word annotations from overriding a
// newly transcribed line in derived exports. Exact token matches retain the
// original word geometry and IDs; otherwise the stale words are removed and
// exporters derive words from the canonical line text.
func reconcileTranscribedWords(items []any) []any {
	authoritative := make(map[string]struct{})
	for _, value := range items {
		line, ok := value.(map[string]any)
		if !ok || !strings.EqualFold(strings.TrimSpace(annStringValue(line, "textGranularity")), "line") {
			continue
		}
		authoritative[annStringValue(line, "id")] = struct{}{}
	}
	return reconcileTranscribedLineWords(items, authoritative)
}

type positionedAnnotationWord struct {
	index      int
	x1         int
	annotation map[string]any
}

type spatialLine struct {
	id             string
	x1, y1, x2, y2 int
	order          int
}

type spatialWord struct {
	positionedAnnotationWord
	centerX int
	centerY int
}

type lineOrderEntry struct {
	lineIndex int
	order     int
}

type lineOrderHeap []lineOrderEntry

func (h lineOrderHeap) Len() int           { return len(h) }
func (h lineOrderHeap) Less(i, j int) bool { return h[i].order < h[j].order }
func (h lineOrderHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *lineOrderHeap) Push(value any)    { *h = append(*h, value.(lineOrderEntry)) }
func (h *lineOrderHeap) Pop() any {
	old := *h
	last := old[len(old)-1]
	*h = old[:len(old)-1]
	return last
}

type lineEndEntry struct {
	lineIndex int
	y2        int
}

type lineEndHeap []lineEndEntry

func (h lineEndHeap) Len() int           { return len(h) }
func (h lineEndHeap) Less(i, j int) bool { return h[i].y2 < h[j].y2 }
func (h lineEndHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *lineEndHeap) Push(value any)    { *h = append(*h, value.(lineEndEntry)) }
func (h *lineEndHeap) Pop() any {
	old := *h
	last := old[len(old)-1]
	*h = old[:len(old)-1]
	return last
}

type lineCoverageIndex struct {
	size   int
	nodes  []lineOrderHeap
	active []bool
}

func newLineCoverageIndex(coordinateCount, lineCount int) *lineCoverageIndex {
	size := 1
	for size < coordinateCount {
		size *= 2
	}
	return &lineCoverageIndex{size: size, nodes: make([]lineOrderHeap, size*2), active: make([]bool, lineCount)}
}

func (index *lineCoverageIndex) add(lineIndex, order, left, right int) {
	if index == nil || left > right {
		return
	}
	index.active[lineIndex] = true
	left += index.size
	right += index.size
	for left <= right {
		if left%2 == 1 {
			heap.Push(&index.nodes[left], lineOrderEntry{lineIndex: lineIndex, order: order})
			left++
		}
		if right%2 == 0 {
			heap.Push(&index.nodes[right], lineOrderEntry{lineIndex: lineIndex, order: order})
			right--
		}
		left /= 2
		right /= 2
	}
}

func (index *lineCoverageIndex) remove(lineIndex int) {
	if index != nil {
		index.active[lineIndex] = false
	}
}

func (index *lineCoverageIndex) owner(position int) (int, bool) {
	bestOrder := int(^uint(0) >> 1)
	bestLine := -1
	for node := position + index.size; node > 0; node /= 2 {
		candidates := &index.nodes[node]
		for candidates.Len() > 0 && !index.active[(*candidates)[0].lineIndex] {
			heap.Pop(candidates)
		}
		if candidates.Len() > 0 && (*candidates)[0].order < bestOrder {
			bestOrder = (*candidates)[0].order
			bestLine = (*candidates)[0].lineIndex
		}
	}
	return bestLine, bestLine >= 0
}

// assignWordsToLines performs a vertical sweep per Canvas. A range-index over
// compressed word-center X coordinates makes each line add/remove and each
// word lookup logarithmic. If boxes overlap, the earliest eligible line in
// canonical item order owns the word, matching structural reconciliation's
// deterministic first-line behavior.
func assignSpatialWordsToLines(items []any, eligible map[string]struct{}) map[string][]positionedAnnotationWord {
	linesByCanvas := make(map[string][]spatialLine)
	wordsByCanvas := make(map[string][]spatialWord)
	for position, value := range items {
		annotation, ok := value.(map[string]any)
		if !ok {
			continue
		}
		granularity := strings.ToLower(strings.TrimSpace(annStringValue(annotation, "textGranularity")))
		canvasURI := extractCanvasURI(annotation)
		x1, y1, x2, y2, err := parseXYWH(extractFragment(annotation))
		if err != nil || canvasURI == "" {
			continue
		}
		switch granularity {
		case "line":
			id := annStringValue(annotation, "id")
			if eligible != nil {
				if _, ok := eligible[id]; !ok {
					continue
				}
			}
			linesByCanvas[canvasURI] = append(linesByCanvas[canvasURI], spatialLine{id: id, x1: x1, y1: y1, x2: x2, y2: y2, order: position})
		case "word":
			wordsByCanvas[canvasURI] = append(wordsByCanvas[canvasURI], spatialWord{
				positionedAnnotationWord: positionedAnnotationWord{index: position, x1: x1, annotation: annotation},
				centerX:                  x1 + (x2-x1)/2,
				centerY:                  y1 + (y2-y1)/2,
			})
		}
	}

	assigned := make(map[string][]positionedAnnotationWord)
	for canvasURI, words := range wordsByCanvas {
		lines := linesByCanvas[canvasURI]
		if len(lines) == 0 || len(words) == 0 {
			continue
		}
		coordinates := make([]int, 0, len(words))
		for _, word := range words {
			coordinates = append(coordinates, word.centerX)
		}
		sort.Ints(coordinates)
		coordinates = compactSortedInts(coordinates)
		sort.SliceStable(lines, func(i, j int) bool {
			if lines[i].y1 == lines[j].y1 {
				return lines[i].order < lines[j].order
			}
			return lines[i].y1 < lines[j].y1
		})
		sort.SliceStable(words, func(i, j int) bool {
			if words[i].centerY == words[j].centerY {
				return words[i].index < words[j].index
			}
			return words[i].centerY < words[j].centerY
		})
		coverage := newLineCoverageIndex(len(coordinates), len(lines))
		ending := &lineEndHeap{}
		nextLine := 0
		for _, word := range words {
			for nextLine < len(lines) && lines[nextLine].y1 <= word.centerY {
				line := lines[nextLine]
				left := sort.SearchInts(coordinates, line.x1)
				right := sort.Search(len(coordinates), func(index int) bool { return coordinates[index] > line.x2 }) - 1
				if left <= right {
					coverage.add(nextLine, line.order, left, right)
					heap.Push(ending, lineEndEntry{lineIndex: nextLine, y2: line.y2})
				}
				nextLine++
			}
			for ending.Len() > 0 && (*ending)[0].y2 < word.centerY {
				coverage.remove(heap.Pop(ending).(lineEndEntry).lineIndex)
			}
			coordinate := sort.SearchInts(coordinates, word.centerX)
			if owner, ok := coverage.owner(coordinate); ok {
				lineID := lines[owner].id
				assigned[lineID] = append(assigned[lineID], word.positionedAnnotationWord)
			}
		}
	}
	for lineID := range assigned {
		words := assigned[lineID]
		sort.SliceStable(words, func(i, j int) bool { return words[i].x1 < words[j].x1 })
		assigned[lineID] = words
	}
	return assigned
}

func compactSortedInts(values []int) []int {
	if len(values) < 2 {
		return values
	}
	write := 1
	for read := 1; read < len(values); read++ {
		if values[read] == values[write-1] {
			continue
		}
		values[write] = values[read]
		write++
	}
	return values[:write]
}

// reconcileTranscribedLineWords is deliberately limited to a newly generated
// model result. Interactive saves use ID-based reconciliation in
// annotation_reconciliation.go; applying this spatial, destructive fallback to
// an editor draft would allow a moved or overlapping line to steal words.
func reconcileTranscribedLineWords(items []any, authoritative map[string]struct{}) []any {
	remove := make(map[int]struct{})
	wordsByLine := assignSpatialWordsToLines(items, authoritative)

	for _, value := range items {
		line, ok := value.(map[string]any)
		if !ok || !strings.EqualFold(strings.TrimSpace(annStringValue(line, "textGranularity")), "line") {
			continue
		}
		if _, ok := authoritative[annStringValue(line, "id")]; !ok {
			continue
		}
		words := wordsByLine[annStringValue(line, "id")]
		if len(words) == 0 {
			continue
		}
		tokens := strings.Fields(extractAnnotationText(line))
		if len(tokens) != len(words) {
			for _, word := range words {
				remove[word.index] = struct{}{}
			}
			continue
		}
		for index, wordPosition := range words {
			word := items[wordPosition.index].(map[string]any)
			setAnnotationText(word, tokens[index])
		}
	}

	if len(remove) == 0 {
		return items
	}
	reconciled := make([]any, 0, len(items)-len(remove))
	for index, value := range items {
		if _, shouldRemove := remove[index]; !shouldRemove {
			reconciled = append(reconciled, value)
		}
	}
	return reconciled
}

func setAnnotationText(annotation map[string]any, value string) {
	setBody := func(body map[string]any) bool {
		if !strings.EqualFold(strings.TrimSpace(annStringValue(body, "type")), "TextualBody") {
			return false
		}
		body["value"] = strings.TrimSpace(value)
		return true
	}
	switch bodies := annotation["body"].(type) {
	case []any:
		for _, candidate := range bodies {
			if body, ok := candidate.(map[string]any); ok && setBody(body) {
				return
			}
		}
		annotation["body"] = append(bodies, map[string]any{
			"type": "TextualBody", "purpose": "supplementing", "format": "text/plain", "value": strings.TrimSpace(value),
		})
		return
	case map[string]any:
		if setBody(bodies) {
			return
		}
	}
	annotation["body"] = []any{map[string]any{
		"type":    "TextualBody",
		"purpose": "supplementing",
		"format":  "text/plain",
		"value":   strings.TrimSpace(value),
	}}
}

func (h *Handler) transcriptionJobOCRRun(ctx context.Context, job *store.TranscriptionJob, pctx store.Context, img store.ItemImage, page store.AnnotationPage, completed int) (*store.OCRRun, error) {
	if h.ocrRuns == nil {
		return nil, nil
	}
	if completed == 0 {
		return nil, nil
	}
	if _, err := h.ocrRuns.GetByItemImageID(ctx, job.ItemImageID); err == nil {
		return nil, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	lines, pageW, pageH, err := annotationPageToHOCRLinesWithDimensions(page.Payload, int(img.Width), int(img.Height))
	if err != nil {
		return nil, fmt.Errorf("crosswalk completed annotations: %w", err)
	}
	hocrXML := hocr.NewConverter().ConvertHOCRLinesToXML(lines, pageW, pageH)
	plainText := linesToPlainText(lines)
	contextID := pctx.ID
	itemImageID := job.ItemImageID
	return &store.OCRRun{
		SessionID:    fmt.Sprintf("transcription-job-%d", job.ID),
		ItemImageID:  &itemImageID,
		ContextID:    &contextID,
		ImageURL:     img.ImageURL,
		Provider:     pctx.TranscriptionProvider,
		Model:        pctx.TranscriptionModel,
		OriginalHOCR: hocrXML,
		OriginalText: plainText,
	}, nil
}

// --- proto conversion ---

func storeJobToProto(j store.TranscriptionJob) *scribev1.TranscriptionJob {
	p := &scribev1.TranscriptionJob{
		Id:                       j.ID,
		ItemImageId:              j.ItemImageID,
		TotalSegments:            int32FromIntBounded(j.TotalSegments),
		CompletedSegments:        int32FromIntBounded(j.CompletedSegments),
		FailedSegments:           int32FromIntBounded(j.FailedSegments),
		CurrentAnnotationId:      j.CurrentAnnotationID,
		CurrentAnnotationJson:    j.CurrentAnnotationJSON,
		LastResultAnnotationJson: j.LastResultAnnotationJSON,
		ErrorMessage:             j.ErrorMessage,
		CreatedAt:                j.CreatedAt.Format(time.RFC3339),
		UpdatedAt:                j.UpdatedAt.Format(time.RFC3339),
		AttemptCount:             int32FromIntBounded(j.AttemptCount),
		MaxAttempts:              int32FromIntBounded(j.MaxAttempts),
		InputRevision:            j.InputRevision,
	}
	for _, attempt := range j.Attempts {
		protoAttempt := &scribev1.TranscriptionJobAttempt{
			JobId:               attempt.JobID,
			AttemptNumber:       attempt.AttemptNumber,
			InputRevision:       attempt.InputRevision,
			ContextSnapshotJson: string(attempt.ContextSnapshot),
			LeaseOwner:          attempt.LeaseOwner,
			SafeErrorMessage:    attempt.SafeErrorMessage,
			StartedAt:           attempt.StartedAt.Format(time.RFC3339Nano),
		}
		if attempt.ResultRevision != nil {
			protoAttempt.ResultRevision = *attempt.ResultRevision
		}
		if attempt.FinishedAt != nil {
			protoAttempt.FinishedAt = attempt.FinishedAt.Format(time.RFC3339Nano)
		}
		switch attempt.Outcome {
		case store.TranscriptionAttemptRunning:
			protoAttempt.Outcome = scribev1.TranscriptionJobAttemptOutcome_TRANSCRIPTION_JOB_ATTEMPT_OUTCOME_RUNNING
		case store.TranscriptionAttemptCompleted:
			protoAttempt.Outcome = scribev1.TranscriptionJobAttemptOutcome_TRANSCRIPTION_JOB_ATTEMPT_OUTCOME_COMPLETED
		case store.TranscriptionAttemptRetryableFailed:
			protoAttempt.Outcome = scribev1.TranscriptionJobAttemptOutcome_TRANSCRIPTION_JOB_ATTEMPT_OUTCOME_RETRYABLE_FAILED
		case store.TranscriptionAttemptFailed:
			protoAttempt.Outcome = scribev1.TranscriptionJobAttemptOutcome_TRANSCRIPTION_JOB_ATTEMPT_OUTCOME_FAILED
		case store.TranscriptionAttemptCanceled:
			protoAttempt.Outcome = scribev1.TranscriptionJobAttemptOutcome_TRANSCRIPTION_JOB_ATTEMPT_OUTCOME_CANCELED
		case store.TranscriptionAttemptSuperseded:
			protoAttempt.Outcome = scribev1.TranscriptionJobAttemptOutcome_TRANSCRIPTION_JOB_ATTEMPT_OUTCOME_SUPERSEDED
		case store.TranscriptionAttemptLeaseExpired:
			protoAttempt.Outcome = scribev1.TranscriptionJobAttemptOutcome_TRANSCRIPTION_JOB_ATTEMPT_OUTCOME_LEASE_EXPIRED
		}
		p.Attempts = append(p.Attempts, protoAttempt)
	}
	if j.ContextID != nil {
		p.ContextId = *j.ContextID
	}
	p.Status = transcriptionJobStatusToProto(j.Status)
	return p
}

func storeJobSummaryToProto(j store.TranscriptionJobSummary) *scribev1.TranscriptionJobSummary {
	p := &scribev1.TranscriptionJobSummary{
		Id:                  j.ID,
		ItemImageId:         j.ItemImageID,
		Status:              transcriptionJobStatusToProto(j.Status),
		TotalSegments:       int32FromIntBounded(j.TotalSegments),
		CompletedSegments:   int32FromIntBounded(j.CompletedSegments),
		FailedSegments:      int32FromIntBounded(j.FailedSegments),
		CurrentAnnotationId: j.CurrentAnnotationID,
		ErrorMessage:        j.ErrorMessage,
		CreatedAt:           j.CreatedAt.Format(time.RFC3339),
		UpdatedAt:           j.UpdatedAt.Format(time.RFC3339),
		InputRevision:       j.InputRevision,
		AttemptCount:        int32FromIntBounded(j.AttemptCount),
		MaxAttempts:         int32FromIntBounded(j.MaxAttempts),
	}
	if j.ContextID != nil {
		p.ContextId = *j.ContextID
	}
	return p
}

func transcriptionJobStatusToProto(status store.TranscriptionJobStatus) scribev1.TranscriptionJobStatus {
	switch status {
	case store.TranscriptionJobStatusPending:
		return scribev1.TranscriptionJobStatus_TRANSCRIPTION_JOB_STATUS_PENDING
	case store.TranscriptionJobStatusRunning:
		return scribev1.TranscriptionJobStatus_TRANSCRIPTION_JOB_STATUS_RUNNING
	case store.TranscriptionJobStatusCompleted:
		return scribev1.TranscriptionJobStatus_TRANSCRIPTION_JOB_STATUS_COMPLETED
	case store.TranscriptionJobStatusFailed:
		return scribev1.TranscriptionJobStatus_TRANSCRIPTION_JOB_STATUS_FAILED
	case store.TranscriptionJobStatusCanceled:
		return scribev1.TranscriptionJobStatus_TRANSCRIPTION_JOB_STATUS_CANCELED
	case store.TranscriptionJobStatusSuperseded:
		return scribev1.TranscriptionJobStatus_TRANSCRIPTION_JOB_STATUS_SUPERSEDED
	default:
		return scribev1.TranscriptionJobStatus_TRANSCRIPTION_JOB_STATUS_UNSPECIFIED
	}
}

func transcriptionJobTerminal(status store.TranscriptionJobStatus) bool {
	switch status {
	case store.TranscriptionJobStatusCompleted,
		store.TranscriptionJobStatusFailed,
		store.TranscriptionJobStatusCanceled,
		store.TranscriptionJobStatusSuperseded:
		return true
	default:
		return false
	}
}
