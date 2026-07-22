package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	ocrhandlers "github.com/lehigh-university-libraries/scribe/internal/handlers"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	"github.com/lehigh-university-libraries/scribe/internal/uploadblob"
	"github.com/lehigh-university-libraries/scribe/internal/uploadref"
)

const (
	resourceCleanupLease            = 90 * time.Second
	resourceCleanupOperationTimeout = 75 * time.Second
	resourceCleanupClaimBatch       = 10
)

// StartResourceCleanupDispatcher delivers external DELETE operations recorded
// by item transactions. The worker process owns this loop; database leases
// additionally make duplicate worker instances safe during rolling deploys.
func (h *Handler) StartResourceCleanupDispatcher(ctx context.Context) {
	if h == nil || h.items == nil {
		return
	}
	if err := ocrhandlers.CleanupStaleUploadTemps("uploads", time.Now().UTC(), ocrhandlers.UploadTempRecoveryAge); err != nil {
		slog.Warn("failed to clean stale upload temporary files", "error_type", safeLogErrorType(err))
	}
	h.startBackgroundWorker(func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			h.dispatchResourceCleanups(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})
}

func (h *Handler) dispatchResourceCleanups(ctx context.Context) {
	deliveries := make([]store.ResourceCleanupDelivery, 0, resourceCleanupClaimBatch)
	for range resourceCleanupClaimBatch {
		delivery, err := h.items.ClaimResourceCleanup(ctx, resourceCleanupLease)
		if err != nil {
			slog.Warn("failed to claim resource cleanup", "error_type", safeLogErrorType(err))
			break
		}
		if delivery == nil {
			break
		}
		deliveries = append(deliveries, *delivery)
	}
	var workers sync.WaitGroup
	for _, delivery := range deliveries {
		delivery := delivery
		workers.Add(1)
		go func() {
			defer workers.Done()
			operationCtx, cancel := context.WithTimeout(ctx, resourceCleanupOperationTimeout)
			defer cancel()
			releasePhysicalBytes, err := h.performResourceCleanup(operationCtx, delivery)
			if err != nil {
				delay := resourceCleanupRetryDelay(delivery.Attempt)
				retryErr := h.items.RetryResourceCleanup(ctx, delivery, err, time.Now().UTC().Add(delay))
				if retryErr != nil && !errors.Is(retryErr, store.ErrResourceCleanupLease) {
					slog.Warn("failed to defer resource cleanup", "kind", delivery.Kind, "resource_id", safeLogValueID(delivery.ResourceKey), "error_type", safeLogErrorType(retryErr))
				}
				slog.Warn("resource cleanup failed", "kind", delivery.Kind, "resource_id", safeLogValueID(delivery.ResourceKey), "attempt", delivery.Attempt, "max_attempts", delivery.MaxAttempts, "retry_in", delay, "failure", store.SafeResourceCleanupFailureMessage(err))
				return
			}
			if err := h.items.CompleteResourceCleanup(ctx, delivery, releasePhysicalBytes); err != nil && !errors.Is(err, store.ErrResourceCleanupLease) {
				slog.Warn("failed to complete resource cleanup", "kind", delivery.Kind, "resource_id", safeLogValueID(delivery.ResourceKey), "error_type", safeLogErrorType(err))
			}
		}()
	}
	workers.Wait()
}

func (h *Handler) performResourceCleanup(ctx context.Context, delivery store.ResourceCleanupDelivery) (bool, error) {
	switch delivery.Kind {
	case store.ResourceCleanupUploadBlob:
		deleteBlob, err := h.items.BeginUploadBlobRetirement(ctx, delivery)
		if err != nil {
			return false, fmt.Errorf("fence upload deletion: %w", err)
		}
		if !deleteBlob {
			return false, nil
		}
		// A corrupt noncanonical tombstone must never authorize a filesystem or
		// object-store delete. Complete the fenced bookkeeping without issuing an
		// external delete so the row cannot poison the worker forever.
		if !uploadref.IsImmutableName(delivery.ResourceKey) {
			return true, nil
		}
		if h.deleteUploadBlob != nil {
			return true, h.deleteUploadBlob(ctx, delivery.ResourceKey)
		}
		return true, deleteStoredUpload(ctx, delivery.ResourceKey)

	case store.ResourceCleanupTripletPresentationImage:
		itemImageID, err := strconv.ParseUint(delivery.ResourceKey, 10, 64)
		if err != nil || itemImageID == 0 {
			return false, fmt.Errorf("invalid Triplet cleanup resource")
		}
		if h.deleteTripletImageGraphFn != nil {
			return false, h.deleteTripletImageGraphFn(ctx, itemImageID)
		}
		return false, h.deleteTripletImageGraph(ctx, itemImageID)

	case store.ResourceCleanupTripletPresentationItem:
		itemID := strings.TrimSpace(delivery.ResourceKey)
		if itemID == "" {
			return false, fmt.Errorf("invalid Triplet item cleanup resource")
		}
		if h.reconcileTripletItemGraphFn != nil {
			return false, h.reconcileTripletItemGraphFn(ctx, itemID)
		}
		return false, h.reconcileTripletItemGraph(ctx, itemID)

	default:
		return false, fmt.Errorf("unsupported resource cleanup kind %q", delivery.Kind)
	}
}

func deleteStoredUpload(ctx context.Context, name string) error {
	// Validate the name independently before constructing a local path.
	validated, ok := uploadref.ImmutableNameFromURL("/static/uploads/" + name)
	if !ok || validated != name {
		return fmt.Errorf("invalid upload cleanup resource")
	}
	var cleanupErrors []error
	if err := uploadblob.Delete(ctx, name); err != nil {
		cleanupErrors = append(cleanupErrors, err)
	}
	if err := os.Remove(filepath.Join("uploads", name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		cleanupErrors = append(cleanupErrors, fmt.Errorf("delete local upload: %w", err))
	}
	return errors.Join(cleanupErrors...)
}

func resourceCleanupRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 13 {
		attempt = 13
	}
	delay := time.Second * time.Duration(1<<(attempt-1))
	if delay > time.Hour {
		return time.Hour
	}
	return delay
}
