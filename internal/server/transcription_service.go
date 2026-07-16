package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/hocr"
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
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create job: %w", err))
	}

	return connect.NewResponse(&scribev1.CreateTranscriptionJobResponse{JobId: jobID}), nil
}

func (h *Handler) GetTranscriptionJob(
	ctx context.Context,
	req *connect.Request[scribev1.GetTranscriptionJobRequest],
) (*connect.Response[scribev1.GetTranscriptionJobResponse], error) {
	job, err := h.transcriptionJobs.Get(ctx, req.Msg.GetJobId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewResponse(&scribev1.GetTranscriptionJobResponse{Job: storeJobToProto(job)}), nil
}

func (h *Handler) ListTranscriptionJobs(
	ctx context.Context,
	req *connect.Request[scribev1.ListTranscriptionJobsRequest],
) (*connect.Response[scribev1.ListTranscriptionJobsResponse], error) {
	var (
		jobs []store.TranscriptionJob
		err  error
	)
	if req.Msg.GetItemImageId() == 0 {
		jobs, err = h.transcriptionJobs.ListByWorkspace(ctx, h.currentWorkspaceID(ctx))
	} else {
		if _, err := h.itemImageForRequest(ctx, req.Msg.GetItemImageId()); err != nil {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("item image not found"))
		}
		jobs, err = h.transcriptionJobs.ListByItemImage(ctx, req.Msg.GetItemImageId())
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	protoJobs := make([]*scribev1.TranscriptionJob, 0, len(jobs))
	for _, j := range jobs {
		protoJobs = append(protoJobs, storeJobToProto(j))
	}
	return connect.NewResponse(&scribev1.ListTranscriptionJobsResponse{Jobs: protoJobs}), nil
}

// StreamTranscriptionJob sends a TranscriptionJob message every time the job
// is updated until the job reaches a terminal state or the client disconnects.
func (h *Handler) StreamTranscriptionJob(
	ctx context.Context,
	req *connect.Request[scribev1.StreamTranscriptionJobRequest],
	stream *connect.ServerStream[scribev1.StreamTranscriptionJobResponse],
) error {
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
				return connect.NewError(connect.CodeNotFound, err)
			}

			// Only send when the job has actually changed.
			if job.UpdatedAt.Equal(lastUpdatedAt) {
				// If job is terminal, we're done.
				if job.Status == store.TranscriptionJobStatusCompleted || job.Status == store.TranscriptionJobStatusFailed {
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

			if job.Status == store.TranscriptionJobStatusCompleted || job.Status == store.TranscriptionJobStatusFailed {
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
		go func() {
			if err := h.transcriptionQueue.ReceiveTranscriptionJobs(ctx, func(msgCtx context.Context, jobID uint64) error {
				if !acquireTranscriptionSlot(msgCtx, sem) {
					return msgCtx.Err()
				}
				defer releaseTranscriptionSlot(sem)
				return h.processQueuedTranscriptionJob(msgCtx, jobID)
			}, func(msgCtx context.Context, messageID string, parseErr error, body []byte) {
				h.publishEvent("dev.scribe.transcription.poisoned", "transcription/poisoned", map[string]any{
					"messageId": messageID,
					"error":     parseErr.Error(),
					"body":      string(body),
				})
			}); err != nil {
				slog.Error("Pub/Sub transcription worker stopped with error", "error", err)
			}
		}()
		go h.transcriptionRecoveryLoop(ctx, sem)
		return
	}
	slog.Info("Starting transcription job worker pool", "workers", workerCount)
	for i := 0; i < workerCount; i++ {
		go h.transcriptionWorkerLoop(ctx, i+1)
	}
}

func (h *Handler) createTranscriptionJob(ctx context.Context, itemImageID uint64, contextID *uint64) (uint64, error) {
	jobID, err := h.transcriptionJobs.Create(ctx, itemImageID, contextID)
	if err != nil {
		return 0, err
	}
	if h.transcriptionQueue != nil {
		if err := h.transcriptionQueue.PublishTranscriptionJob(ctx, jobID); err != nil {
			slog.Warn("Failed to publish transcription job message; recovery poller will pick it up", "job_id", jobID, "error", err)
		}
	}
	return jobID, nil
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
			slog.Error("Failed to claim transcription job", "worker_id", workerID, "error", err)
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
			slog.Error("Transcription job failed", "worker_id", workerID, "job_id", job.ID, "error", err)
			_ = h.recordClaimedTranscriptionJobFailure(ctx, job, err)
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
		slog.Error("Queued transcription job failed", "job_id", job.ID, "error", err)
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
					slog.Error("Failed to claim recovery transcription job", "error", err)
					break
				}
				if job == nil {
					releaseTranscriptionSlot(sem)
					break
				}
				go func(job *store.TranscriptionJob) {
					defer releaseTranscriptionSlot(sem)
					slog.Info("Processing recovery transcription job", "job_id", job.ID, "item_image_id", job.ItemImageID)
					if err := h.processClaimedTranscriptionJob(ctx, job); err != nil {
						slog.Error("Recovery transcription job failed", "job_id", job.ID, "error", err)
						_ = h.recordClaimedTranscriptionJobFailure(ctx, job, err)
					}
				}(job)
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
	if strings.TrimSpace(job.LeaseOwner) == "" {
		return fmt.Errorf("claimed transcription job %d has no lease owner", job.ID)
	}
	stopHeartbeat := h.startTranscriptionJobLeaseHeartbeat(ctx, job.ID, job.LeaseOwner)
	defer stopHeartbeat()
	return h.processTranscriptionJob(ctx, job)
}

type transcriptionRetryLaterError struct {
	message    string
	retryAfter time.Duration
}

func (e transcriptionRetryLaterError) Error() string {
	return e.message
}

func retryTranscriptionLater(message string, retryAfter time.Duration) error {
	if retryAfter <= 0 {
		retryAfter = 5 * time.Second
	}
	return transcriptionRetryLaterError{message: message, retryAfter: retryAfter}
}

func (h *Handler) recordClaimedTranscriptionJobFailure(ctx context.Context, job *store.TranscriptionJob, err error) error {
	if errors.Is(err, context.Canceled) {
		deferCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return h.transcriptionJobs.Defer(deferCtx, job.ID, job.LeaseOwner, "worker shutting down", time.Now().UTC().Add(5*time.Second))
	}
	var retryLater transcriptionRetryLaterError
	if errors.As(err, &retryLater) {
		return h.transcriptionJobs.Defer(ctx, job.ID, job.LeaseOwner, retryLater.Error(), time.Now().UTC().Add(retryLater.retryAfter))
	}
	evt := h.newCloudEvent("dev.scribe.transcription.failed", subjectForItemImage(job.ItemImageID), map[string]any{
		"jobId":       job.ID,
		"itemImageId": job.ItemImageID,
		"error":       err.Error(),
	})
	body, marshalErr := json.Marshal(evt)
	if marshalErr != nil {
		return fmt.Errorf("marshal failure event: %w", marshalErr)
	}
	if failErr := h.transcriptionJobs.FailWithWebhookEvent(ctx, job.ID, job.LeaseOwner, err.Error(), evt.ID, evt.Type, evt.Subject, string(body), h.webhookURLs); failErr != nil {
		return failErr
	}
	h.publishCloudEvent(evt, false)
	return nil
}

func (h *Handler) startTranscriptionJobLeaseHeartbeat(ctx context.Context, jobID uint64, leaseOwner string) func() {
	done := make(chan struct{})
	ticker := time.NewTicker(2 * time.Minute)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				if err := h.transcriptionJobs.ExtendLease(ctx, jobID, leaseOwner, 10*time.Minute); err != nil {
					slog.Warn("Failed to extend transcription job lease", "job_id", jobID, "error", err)
				}
			}
		}
	}()
	return func() {
		close(done)
	}
}

func transcriptionJobWorkerCount() int {
	if v := config.Get().Config.Transcription.JobWorkers; v > 0 {
		return v
	}
	return 3
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

func (h *Handler) resolveTranscriptionJobContext(ctx context.Context, job *store.TranscriptionJob) (store.Context, uint64, *uint64, error) {
	img, err := h.items.GetImage(ctx, job.ItemImageID)
	if err != nil {
		return store.Context{}, 0, nil, fmt.Errorf("get item image %d: %w", job.ItemImageID, err)
	}
	item, err := h.items.Get(ctx, img.ItemID)
	if err != nil {
		return store.Context{}, 0, nil, fmt.Errorf("get item %s: %w", img.ItemID, err)
	}
	itemUserID := item.UserID
	itemUserIDPtr := &itemUserID
	if job.ContextID != nil && *job.ContextID > 0 {
		contextValue, err := h.contexts.GetForWorkspace(ctx, *job.ContextID, item.WorkspaceID)
		if err != nil {
			return store.Context{}, 0, nil, fmt.Errorf("get context %d: %w", *job.ContextID, err)
		}
		return contextValue, item.WorkspaceID, itemUserIDPtr, nil
	}
	contextValue, _, err := h.contexts.ResolveForWorkspace(ctx, item.WorkspaceID, nil)
	if err != nil {
		return store.Context{}, 0, nil, fmt.Errorf("resolve context: %w", err)
	}
	return contextValue, item.WorkspaceID, itemUserIDPtr, nil
}

func (h *Handler) processTranscriptionJob(ctx context.Context, job *store.TranscriptionJob) error {
	// Resolve the context (transcription model/provider).
	pctx, workspaceID, ownerUserID, err := h.resolveTranscriptionJobContext(ctx, job)
	if err != nil {
		return err
	}
	ctx = h.contextWithProviderSecret(ctx, workspaceID, ownerUserID, pctx.TranscriptionProvider)

	var img store.ItemImage
	var payloads []string
	var imgErr error
	img, imgErr = h.items.GetImage(ctx, job.ItemImageID)
	if imgErr != nil {
		return fmt.Errorf("get item image %d: %w", job.ItemImageID, imgErr)
	}
	if img.CanvasURI == "" {
		slog.Info("Deferring transcription job until canvas URI is ready", "job_id", job.ID, "item_image_id", job.ItemImageID)
		return retryTranscriptionLater(fmt.Sprintf("item image %d canvas URI is not ready", job.ItemImageID), 5*time.Second)
	}

	var searchErr error
	payloads, searchErr = h.annotations.SearchByCanvas(ctx, img.CanvasURI)
	if searchErr != nil {
		return fmt.Errorf("search annotations for canvas %s: %w", img.CanvasURI, searchErr)
	}
	if len(payloads) == 0 {
		base := h.internalAnnotationBaseURL()
		bootstrap, bootstrapErr := h.bootstrapAnnotationsForCanvas(ctx, img.CanvasURI, base)
		if bootstrapErr == nil {
			payloads, searchErr = h.persistAnnotationItems(ctx, img.CanvasURI, bootstrap)
			if searchErr != nil {
				return fmt.Errorf("persist bootstrapped annotations for canvas %s: %w", img.CanvasURI, searchErr)
			}
		}
	}
	if len(payloads) == 0 {
		slog.Info("Deferring transcription job until annotations are ready", "job_id", job.ID, "item_image_id", job.ItemImageID, "canvas_uri", img.CanvasURI)
		return retryTranscriptionLater(fmt.Sprintf("no annotations found for canvas %s", img.CanvasURI), 5*time.Second)
	}

	// Filter to line-level annotations only.
	type annotationEntry struct {
		id      string
		payload string
	}
	var lines []annotationEntry
	for _, payload := range payloads {
		var anno map[string]any
		if err := json.Unmarshal([]byte(payload), &anno); err != nil {
			continue
		}
		if granularity, _ := anno["textGranularity"].(string); strings.EqualFold(granularity, "line") {
			id, _ := anno["id"].(string)
			lines = append(lines, annotationEntry{id: id, payload: payload})
		}
	}

	total := len(lines)
	slog.Info("Transcription job: found line annotations", "job_id", job.ID, "count", total)
	if err := h.transcriptionJobs.SetTotalSegments(ctx, job.ID, job.LeaseOwner, total); err != nil {
		return fmt.Errorf("set total segments: %w", err)
	}

	completed, failed, startIndex := resumedTranscriptionProgress(job, total)
	if startIndex > 0 {
		slog.Info("Resuming transcription job from persisted progress",
			"job_id", job.ID,
			"completed", completed,
			"failed", failed,
			"start_index", startIndex+1,
			"total", total,
		)
	}
	for i, entry := range lines {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if i < startIndex {
			continue
		}

		slog.Info("Transcribing segment", "job_id", job.ID, "index", i+1, "total", total, "annotation_id", entry.id)

		// Mark current segment in progress.
		if err := h.transcriptionJobs.UpdateProgress(ctx, job.ID, job.LeaseOwner,
			completed, failed, entry.id, entry.payload, ""); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
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

		enriched, err := h.enrichSingleAnnotation(ctx, entry.payload, pctx)
		if err != nil {
			slog.Warn("Segment transcription failed", "job_id", job.ID, "annotation_id", entry.id, "error", err)
			failed++
			if err := h.transcriptionJobs.UpdateProgress(ctx, job.ID, job.LeaseOwner,
				completed, failed, "", "", ""); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return fmt.Errorf("job lease lost after segment failure: %w", err)
				}
				return fmt.Errorf("update progress after segment failure: %w", err)
			}
			continue
		}

		// Persist the enriched annotation.
		var enrichedAnno map[string]any
		if jsonErr := json.Unmarshal([]byte(enriched), &enrichedAnno); jsonErr == nil {
			id, _ := enrichedAnno["id"].(string)
			if id != "" {
				if upsertErr := h.annotations.Upsert(ctx, id, img.CanvasURI, enriched); upsertErr != nil {
					slog.Warn("Failed to upsert enriched annotation", "annotation_id", id, "error", upsertErr)
				}
			}
		}

		completed++
		if err := h.transcriptionJobs.UpdateProgress(ctx, job.ID, job.LeaseOwner,
			completed, failed, "", "", enriched); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
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
		})
	}

	slog.Info("Transcription job complete", "job_id", job.ID, "completed", completed, "failed", failed)
	if err := h.seedTranscriptionJobOCRRun(ctx, job, pctx, img); err != nil {
		slog.Warn("Failed to seed OCR run metrics for transcription job", "job_id", job.ID, "item_image_id", job.ItemImageID, "error", err)
	}
	evt := h.newCloudEvent("dev.scribe.transcription.completed", subjectForItemImage(job.ItemImageID), map[string]any{
		"jobId":             job.ID,
		"itemImageId":       job.ItemImageID,
		"completedSegments": completed,
		"failedSegments":    failed,
		"totalSegments":     total,
	})
	body, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("marshal completion event: %w", err)
	}
	if err := h.transcriptionJobs.CompleteWithWebhookEvent(ctx, job.ID, job.LeaseOwner, evt.ID, evt.Type, evt.Subject, string(body), h.webhookURLs); err != nil {
		return err
	}
	h.publishCloudEvent(evt, false)
	return nil
}

func (h *Handler) seedTranscriptionJobOCRRun(ctx context.Context, job *store.TranscriptionJob, pctx store.Context, img store.ItemImage) error {
	if h.ocrRuns == nil || h.annotations == nil {
		return nil
	}
	if job.CompletedSegments == 0 {
		return nil
	}
	if _, err := h.ocrRuns.GetByItemImageID(ctx, job.ItemImageID); err == nil {
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	payloads, err := h.annotations.SearchByCanvas(ctx, img.CanvasURI)
	if err != nil {
		return fmt.Errorf("search completed annotations: %w", err)
	}
	items := make([]any, 0, len(payloads))
	for _, payload := range payloads {
		var item any
		if err := json.Unmarshal([]byte(payload), &item); err != nil {
			continue
		}
		items = append(items, item)
	}
	page := map[string]any{
		"@context": annotationPageContexts(),
		"id":       annotationPageID(img.CanvasURI),
		"type":     "AnnotationPage",
		"items":    items,
	}
	pageJSON, err := json.Marshal(page)
	if err != nil {
		return fmt.Errorf("marshal completed annotation page: %w", err)
	}
	lines, pageW, pageH, err := annotationPageToHOCRLines(string(pageJSON))
	if err != nil {
		return fmt.Errorf("crosswalk completed annotations: %w", err)
	}
	hocrXML := hocr.NewConverter().ConvertHOCRLinesToXML(lines, pageW, pageH)
	plainText := linesToPlainText(lines)
	contextID := pctx.ID
	itemImageID := job.ItemImageID
	return h.ocrRuns.Create(ctx, store.OCRRun{
		SessionID:    fmt.Sprintf("transcription-job-%d", job.ID),
		ItemImageID:  &itemImageID,
		ContextID:    &contextID,
		ImageURL:     img.ImageURL,
		Provider:     pctx.TranscriptionProvider,
		Model:        pctx.TranscriptionModel,
		OriginalHOCR: hocrXML,
		OriginalText: plainText,
	})
}

func resumedTranscriptionProgress(job *store.TranscriptionJob, total int) (completed, failed, startIndex int) {
	if job == nil || total <= 0 {
		return 0, 0, 0
	}
	completed = job.CompletedSegments
	if completed < 0 {
		completed = 0
	}
	if completed > total {
		completed = total
	}
	failed = job.FailedSegments
	if failed < 0 {
		failed = 0
	}
	remaining := total - completed
	if failed > remaining {
		failed = remaining
	}
	return completed, failed, completed + failed
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
	}
	if j.ContextID != nil {
		p.ContextId = *j.ContextID
	}
	switch j.Status {
	case store.TranscriptionJobStatusPending:
		p.Status = scribev1.TranscriptionJobStatus_TRANSCRIPTION_JOB_STATUS_PENDING
	case store.TranscriptionJobStatusRunning:
		p.Status = scribev1.TranscriptionJobStatus_TRANSCRIPTION_JOB_STATUS_RUNNING
	case store.TranscriptionJobStatusCompleted:
		p.Status = scribev1.TranscriptionJobStatus_TRANSCRIPTION_JOB_STATUS_COMPLETED
	case store.TranscriptionJobStatusFailed:
		p.Status = scribev1.TranscriptionJobStatus_TRANSCRIPTION_JOB_STATUS_FAILED
	}
	return p
}
