package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

type storageQuotaReservationContextKey struct{}

func withStorageQuotaReservation(ctx context.Context, reservation store.StorageQuotaReservation) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, storageQuotaReservationContextKey{}, reservation)
}

func storageQuotaReservationFromContext(ctx context.Context) (store.StorageQuotaReservation, bool) {
	if ctx == nil {
		return store.StorageQuotaReservation{}, false
	}
	reservation, ok := ctx.Value(storageQuotaReservationContextKey{}).(store.StorageQuotaReservation)
	return reservation, ok && reservation.ID != "" && reservation.WorkspaceID != 0
}

func (h *Handler) stageImmutableUpload(ctx context.Context, imageURL string, storageBytes uint64) error {
	if h == nil || h.items == nil {
		return fmt.Errorf("stage immutable upload: item repository is not configured")
	}
	reservation, ok := storageQuotaReservationFromContext(ctx)
	if !ok {
		return fmt.Errorf("stage immutable upload: storage quota reservation is missing")
	}
	return h.items.StageStorageQuotaUpload(ctx, reservation, imageURL, storageBytes, configuredStorageQuotaLimits())
}

func configuredStorageQuotaLimits() store.StorageQuotaLimits {
	value := config.Get().Config.Storage
	if value.MaxBytesPerWorkspace == 0 {
		value.MaxBytesPerWorkspace = config.DefaultMaxStorageBytesPerWorkspace
	}
	if value.MaxBytesTotal == 0 {
		value.MaxBytesTotal = config.DefaultMaxStorageBytesTotal
	}
	if value.MaxItemsPerWorkspace == 0 {
		value.MaxItemsPerWorkspace = config.DefaultMaxStorageItemsPerWorkspace
	}
	if value.MaxItemsTotal == 0 {
		value.MaxItemsTotal = config.DefaultMaxStorageItemsTotal
	}
	if value.MaxImagesPerWorkspace == 0 {
		value.MaxImagesPerWorkspace = config.DefaultMaxStorageImagesPerWorkspace
	}
	if value.MaxImagesTotal == 0 {
		value.MaxImagesTotal = config.DefaultMaxStorageImagesTotal
	}
	if value.ReservationTTL == 0 {
		value.ReservationTTL = config.DefaultStorageReservationTTL
	}
	return store.StorageQuotaLimits{
		MaxBytesPerWorkspace:  value.MaxBytesPerWorkspace,
		MaxBytesTotal:         value.MaxBytesTotal,
		MaxItemsPerWorkspace:  value.MaxItemsPerWorkspace,
		MaxItemsTotal:         value.MaxItemsTotal,
		MaxImagesPerWorkspace: value.MaxImagesPerWorkspace,
		MaxImagesTotal:        value.MaxImagesTotal,
		ReservationTTL:        value.ReservationTTL,
	}
}

func (h *Handler) reserveStorageQuota(ctx context.Context, request store.StorageQuotaRequest) (store.StorageQuotaReservation, error) {
	if h == nil || h.items == nil {
		return store.StorageQuotaReservation{}, connect.NewError(connect.CodeInternal, fmt.Errorf("storage quota repository is not configured"))
	}
	reservation, err := h.items.ReserveStorageQuota(ctx, h.currentWorkspaceID(ctx), request, configuredStorageQuotaLimits())
	if err != nil {
		return store.StorageQuotaReservation{}, storageQuotaConnectError(err)
	}
	return reservation, nil
}

func (h *Handler) resizeStorageQuota(ctx context.Context, reservation store.StorageQuotaReservation, request store.StorageQuotaRequest) (store.StorageQuotaReservation, error) {
	resized, err := h.items.ResizeStorageQuotaReservation(ctx, reservation, request, configuredStorageQuotaLimits())
	if err != nil {
		return store.StorageQuotaReservation{}, storageQuotaConnectError(err)
	}
	return resized, nil
}

func storageQuotaConnectError(err error) error {
	if errors.Is(err, store.ErrStorageQuotaExceeded) {
		return connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("workspace storage quota is exhausted"))
	}
	return connect.NewError(connect.CodeInternal, fmt.Errorf("storage quota admission failed: %w", err))
}

func (h *Handler) releaseStorageQuota(reservation store.StorageQuotaReservation) {
	if h == nil || h.items == nil || reservation.ID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(h.backgroundContext(), 10*time.Second)
	defer cancel()
	if err := h.items.ReleaseStorageQuotaReservation(ctx, reservation); err != nil {
		// A stale reservation is capacity-safe and expires automatically. Do not
		// fail a committed ingest after its canonical rows already account for it.
		slog.Warn("failed to release storage quota reservation", "reservation_id", reservation.ID, "workspace_id", reservation.WorkspaceID, "error_type", safeLogErrorType(err))
	}
}
