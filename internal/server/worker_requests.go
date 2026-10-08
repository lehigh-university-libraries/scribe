package server

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/config"
)

// ProcessTranscriptionDelivery completes one authenticated cloud push request.
// It uses the same durable claim and revision fence as local pull workers.
func (h *Handler) ProcessTranscriptionDelivery(ctx context.Context, jobID uint64) error {
	return h.processQueuedTranscriptionJob(ctx, jobID)
}

// RunWorkerMaintenance performs a bounded pass; it starts no background loops.
// Scheduled wake-ups keep outboxes and recovery live when workers scale to zero.
func (h *Handler) RunWorkerMaintenance(ctx context.Context) error {
	cfg := config.Get().Config
	// Separate durable queues must not starve each other when one destination
	// is slow. Every pass is joined before the HTTP response is sent.
	var workers sync.WaitGroup
	for _, dispatch := range []func(context.Context) int{
		h.dispatchWebhookBatch, h.dispatchAnnotationMirrors, h.dispatchResourceCleanups,
	} {
		workers.Go(func() {
			for ctx.Err() == nil && dispatch(ctx) > 0 {
			}
		})
	}
	workers.Go(func() {
		h.retainWebhookEvents(ctx)
		h.retainExternalRequests(ctx, cfg.Processing.ExternalRequestRetention)
		h.retainProviderCallAudits(ctx, cfg.Audit.ProviderCallRetention)
	})
	minAge := cfg.Transcription.Queue.RecoveryMinAge
	if minAge <= 0 {
		minAge = 20 * time.Second
	}
	ids, err := h.transcriptionJobs.RecoverableJobIDs(ctx, time.Now().UTC().Add(-minAge))
	for _, id := range ids {
		if err != nil {
			break
		}
		err = h.transcriptionQueue.PublishTranscriptionJob(ctx, id)
	}
	workers.Wait()
	return errors.Join(err, ctx.Err())
}
