package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
	"github.com/lehigh-university-libraries/scribe/proto/scribe/v1/scribev1connect"
	"github.com/lehigh-university-libraries/scribe/internal/store"
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
		v := req.Msg.GetContextId()
		contextID = &v
	}

	jobID, err := h.transcriptionJobs.Create(ctx, itemImageID, contextID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create job: %w", err))
	}

	return connect.NewResponse(&scribev1.CreateTranscriptionJobResponse{JobId: jobID}), nil
}

func (h *Handler) GetTranscriptionJob(
	ctx context.Context,
	req *connect.Request[scribev1.GetTranscriptionJobRequest],
) (*connect.Response[scribev1.TranscriptionJob], error) {
	job, err := h.transcriptionJobs.Get(ctx, req.Msg.GetJobId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewResponse(storeJobToProto(job)), nil
}

func (h *Handler) ListTranscriptionJobs(
	ctx context.Context,
	req *connect.Request[scribev1.ListTranscriptionJobsRequest],
) (*connect.Response[scribev1.ListTranscriptionJobsResponse], error) {
	jobs, err := h.transcriptionJobs.ListByItemImage(ctx, req.Msg.GetItemImageId())
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
// is updated (polling the DB every 500 ms) until the job reaches a terminal
// state or the client disconnects.
func (h *Handler) StreamTranscriptionJob(
	ctx context.Context,
	req *connect.Request[scribev1.StreamTranscriptionJobRequest],
	stream *connect.ServerStream[scribev1.TranscriptionJob],
) error {
	jobID := req.Msg.GetJobId()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	var lastUpdatedAt time.Time

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
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
				continue
			}
			lastUpdatedAt = job.UpdatedAt

			if err := stream.Send(storeJobToProto(job)); err != nil {
				return err
			}

			if job.Status == store.TranscriptionJobStatusCompleted || job.Status == store.TranscriptionJobStatusFailed {
				return nil
			}
		}
	}
}

// --- background worker ---

// StartTranscriptionWorker launches the background job worker. Call once at
// startup; it runs until ctx is cancelled.
func (h *Handler) StartTranscriptionWorker(ctx context.Context) {
	go h.transcriptionWorkerLoop(ctx)
}

func (h *Handler) transcriptionWorkerLoop(ctx context.Context) {
	slog.Info("Transcription job worker started")
	for {
		select {
		case <-ctx.Done():
			slog.Info("Transcription job worker stopped")
			return
		default:
		}

		job, err := h.transcriptionJobs.ClaimNextPending(ctx)
		if err != nil {
			slog.Error("Failed to claim transcription job", "error", err)
			time.Sleep(5 * time.Second)
			continue
		}
		if job == nil {
			time.Sleep(3 * time.Second)
			continue
		}

		slog.Info("Processing transcription job", "job_id", job.ID, "item_image_id", job.ItemImageID)
		if err := h.processTranscriptionJob(ctx, job); err != nil {
			slog.Error("Transcription job failed", "job_id", job.ID, "error", err)
			_ = h.transcriptionJobs.Fail(ctx, job.ID, err.Error())
			h.publishEvent("dev.scribe.transcription.failed", subjectForItemImage(job.ItemImageID), map[string]any{
				"jobId":       job.ID,
				"itemImageId": job.ItemImageID,
				"error":       err.Error(),
			})
		}
	}
}

func (h *Handler) processTranscriptionJob(ctx context.Context, job *store.TranscriptionJob) error {
	// Resolve the context (transcription model/provider).
	var pctx store.Context
	if job.ContextID != nil && *job.ContextID > 0 {
		c, err := h.contexts.Get(ctx, *job.ContextID)
		if err != nil {
			return fmt.Errorf("get context %d: %w", *job.ContextID, err)
		}
		pctx = c
	} else {
		c, _, err := h.contexts.Resolve(ctx, nil)
		if err != nil {
			return fmt.Errorf("resolve context: %w", err)
		}
		pctx = c
	}

	// Wait for the canvas URI and annotations to be populated by Mirador loading
	// the manifest. This happens shortly after the editor page loads, so we poll
	// with a backoff rather than failing immediately.
	var img store.ItemImage
	var payloads []string
	const maxWait = 20
	for attempt := 1; attempt <= maxWait; attempt++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		var imgErr error
		img, imgErr = h.items.GetImage(ctx, job.ItemImageID)
		if imgErr != nil {
			return fmt.Errorf("get item image %d: %w", job.ItemImageID, imgErr)
		}

		if img.CanvasURI != "" {
			var searchErr error
			payloads, searchErr = h.annotations.SearchByCanvas(ctx, img.CanvasURI)
			if searchErr != nil {
				return fmt.Errorf("search annotations for canvas %s: %w", img.CanvasURI, searchErr)
			}
			if len(payloads) == 0 {
				base := h.annotationBaseURL
				if base == "" {
					base = strings.TrimRight(strings.TrimSpace(os.Getenv("ANNOTATION_API_BASE")), "/")
				}
				if base == "" {
					base = "http://localhost:8080"
				}
				bootstrap, bootstrapErr := h.bootstrapAnnotationsForCanvas(ctx, img.CanvasURI, base)
				if bootstrapErr == nil {
					payloads, searchErr = h.persistAnnotationItems(ctx, img.CanvasURI, bootstrap)
					if searchErr != nil {
						return fmt.Errorf("persist bootstrapped annotations for canvas %s: %w", img.CanvasURI, searchErr)
					}
				}
			}
			if len(payloads) > 0 {
				break
			}
		}

		slog.Info("Waiting for canvas and annotations to be ready",
			"job_id", job.ID, "item_image_id", job.ItemImageID,
			"attempt", attempt, "max", maxWait,
			"has_canvas_uri", img.CanvasURI != "")
		time.Sleep(3 * time.Second)
	}

	if img.CanvasURI == "" {
		return fmt.Errorf("item image %d canvas URI never set after %d retries", job.ItemImageID, maxWait)
	}
	if len(payloads) == 0 {
		return fmt.Errorf("no annotations found for canvas %s after %d retries", img.CanvasURI, maxWait)
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
	if err := h.transcriptionJobs.SetTotalSegments(ctx, job.ID, total); err != nil {
		slog.Warn("Failed to set total segments", "job_id", job.ID, "error", err)
	}

	completed, failed := 0, 0
	for i, entry := range lines {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		slog.Info("Transcribing segment", "job_id", job.ID, "index", i+1, "total", total, "annotation_id", entry.id)

		// Mark current segment in progress.
		if err := h.transcriptionJobs.UpdateProgress(ctx, job.ID,
			completed, failed, entry.id, entry.payload, ""); err != nil {
			slog.Warn("Failed to update progress (pre-segment)", "job_id", job.ID, "error", err)
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
			if err := h.transcriptionJobs.UpdateProgress(ctx, job.ID,
				completed, failed, "", "", ""); err != nil {
				slog.Warn("Failed to update progress (after failure)", "job_id", job.ID, "error", err)
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
		if err := h.transcriptionJobs.UpdateProgress(ctx, job.ID,
			completed, failed, "", "", enriched); err != nil {
			slog.Warn("Failed to update progress (after success)", "job_id", job.ID, "error", err)
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
	h.publishEvent("dev.scribe.transcription.completed", subjectForItemImage(job.ItemImageID), map[string]any{
		"jobId":             job.ID,
		"itemImageId":       job.ItemImageID,
		"completedSegments": completed,
		"failedSegments":    failed,
		"totalSegments":     total,
	})
	return h.transcriptionJobs.Complete(ctx, job.ID)
}

// --- proto conversion ---

func storeJobToProto(j store.TranscriptionJob) *scribev1.TranscriptionJob {
	p := &scribev1.TranscriptionJob{
		Id:                       j.ID,
		ItemImageId:              j.ItemImageID,
		TotalSegments:            int32(j.TotalSegments),
		CompletedSegments:        int32(j.CompletedSegments),
		FailedSegments:           int32(j.FailedSegments),
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
