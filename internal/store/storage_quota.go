package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	db "github.com/lehigh-university-libraries/scribe/internal/db"
	"github.com/lehigh-university-libraries/scribe/internal/uploadref"
)

var ErrStorageQuotaExceeded = errors.New("storage quota exceeded")

type StorageQuotaLimits struct {
	MaxBytesPerWorkspace  uint64
	MaxBytesTotal         uint64
	MaxItemsPerWorkspace  uint64
	MaxItemsTotal         uint64
	MaxImagesPerWorkspace uint64
	MaxImagesTotal        uint64
	ReservationTTL        time.Duration
}

// StorageQuotaRequest distinguishes immutable upload bytes from durable
// relational payload bytes. Both contribute to the configured total byte
// limit, while separate counters make lifecycle transfers and integrity
// rebuilding exact.
type StorageQuotaRequest struct {
	Bytes        uint64
	DurableBytes uint64
	Items        uint64
	Images       uint64
}

type StorageQuotaReservation struct {
	ID          string
	WorkspaceID uint64
	Request     StorageQuotaRequest
	ExpiresAt   time.Time
}

// StorageQuotaUsage is the inspectable maintained materialization for one
// workspace. Workspace ID zero represents the global aggregate.
type StorageQuotaUsage struct {
	WorkspaceID             uint64
	UploadBlobBytes         uint64
	DatabaseBytes           uint64
	Items                   uint64
	Images                  uint64
	ReservedUploadBlobBytes uint64
	ReservedDatabaseBytes   uint64
	ReservedItems           uint64
	ReservedImages          uint64
}

func (u StorageQuotaUsage) usedBytes() (uint64, bool) {
	return addUint64(u.UploadBlobBytes, u.DatabaseBytes)
}

func (u StorageQuotaUsage) reservedBytes() (uint64, bool) {
	return addUint64(u.ReservedUploadBlobBytes, u.ReservedDatabaseBytes)
}

func (s *ItemStore) ReserveStorageQuota(ctx context.Context, workspaceID uint64, request StorageQuotaRequest, limits StorageQuotaLimits) (StorageQuotaReservation, error) {
	if s == nil || s.pool == nil {
		return StorageQuotaReservation{}, fmt.Errorf("reserve storage quota: item store is not configured")
	}
	if workspaceID == 0 || storageQuotaRequestEmpty(request) {
		return StorageQuotaReservation{}, fmt.Errorf("reserve storage quota: workspace and capacity are required")
	}
	if err := validateStorageQuotaRequest(request); err != nil {
		return StorageQuotaReservation{}, err
	}
	if err := validateStorageQuotaLimits(limits); err != nil {
		return StorageQuotaReservation{}, err
	}
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return StorageQuotaReservation{}, fmt.Errorf("begin storage quota reservation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.q.WithTx(tx)
	if _, err := queries.LockWorkspaceForUseManual(ctx, workspaceID); err != nil {
		return StorageQuotaReservation{}, fmt.Errorf("reserve storage quota: lock workspace: %w", err)
	}
	global, workspace, err := lockStorageQuotaUsage(ctx, queries, workspaceID)
	if err != nil {
		return StorageQuotaReservation{}, err
	}
	if err := checkStorageQuotaCounters(storageQuotaUsageFromRow(global), storageQuotaUsageFromRow(workspace), request, limits); err != nil {
		return StorageQuotaReservation{}, err
	}
	now := time.Now().UTC()
	reservation := StorageQuotaReservation{
		ID:          uuid.NewString(),
		WorkspaceID: workspaceID,
		Request:     request,
		ExpiresAt:   now.Add(limits.ReservationTTL),
	}
	params, err := storageReservationInsertParams(reservation, sql.NullString{})
	if err != nil {
		return StorageQuotaReservation{}, err
	}
	if err := queries.InsertStorageQuotaReservation(ctx, params); err != nil {
		return StorageQuotaReservation{}, fmt.Errorf("insert storage quota reservation: %w", err)
	}
	if err := addStorageQuotaReserved(ctx, queries, workspaceID, request); err != nil {
		return StorageQuotaReservation{}, fmt.Errorf("account storage quota reservation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return StorageQuotaReservation{}, fmt.Errorf("commit storage quota reservation: %w", err)
	}
	return reservation, nil
}

func (s *ItemStore) ResizeStorageQuotaReservation(ctx context.Context, reservation StorageQuotaReservation, request StorageQuotaRequest, limits StorageQuotaLimits) (StorageQuotaReservation, error) {
	if s == nil || s.pool == nil || reservation.WorkspaceID == 0 || reservation.ID == "" {
		return StorageQuotaReservation{}, fmt.Errorf("resize storage quota reservation: reservation is required")
	}
	if storageQuotaRequestEmpty(request) {
		return StorageQuotaReservation{}, fmt.Errorf("resize storage quota reservation: capacity is required")
	}
	if err := validateStorageQuotaRequest(request); err != nil {
		return StorageQuotaReservation{}, err
	}
	if err := validateStorageQuotaLimits(limits); err != nil {
		return StorageQuotaReservation{}, err
	}
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return StorageQuotaReservation{}, fmt.Errorf("begin storage quota resize: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.q.WithTx(tx)
	workspaceRow, err := lockWorkspaceStorageQuotaUsage(ctx, queries, reservation.WorkspaceID)
	if err != nil {
		return StorageQuotaReservation{}, err
	}
	currentRow, err := queries.LockStorageQuotaReservation(ctx, db.LockStorageQuotaReservationParams{
		ID: reservation.ID, WorkspaceID: reservation.WorkspaceID,
	})
	if err != nil {
		return StorageQuotaReservation{}, fmt.Errorf("load storage quota reservation: %w", err)
	}
	current := storageQuotaRequestFromReservation(currentRow)
	newReserved := request
	usedAddition := StorageQuotaRequest{}
	resourceKey := currentRow.ResourceKey
	now := time.Now().UTC()
	if resourceKey.Valid {
		cleanup, cleanupErr := queries.LockResourceCleanupByKindKey(ctx, db.LockResourceCleanupByKindKeyParams{
			Kind: db.ResourceCleanupOutboxKindUploadBlob, ResourceKey: resourceKey.String,
		})
		if cleanupErr != nil {
			return StorageQuotaReservation{}, fmt.Errorf("resize staged upload accounting: %w", cleanupErr)
		}
		if cleanup.WorkspaceID != reservation.WorkspaceID {
			return StorageQuotaReservation{}, fmt.Errorf("resize staged upload accounting: workspace identity changed")
		}
		if cleanup.DeleteFencedAt.Valid {
			return StorageQuotaReservation{}, fmt.Errorf("resize staged upload accounting: %w", ErrUploadBlobRetired)
		}
		if request.Bytes > cleanup.StorageBytes {
			usedAddition.Bytes = request.Bytes - cleanup.StorageBytes
		}
		newReserved.Bytes = 0
	}
	globalRow, err := queries.LockStorageQuotaUsage(ctx, 0)
	if err != nil {
		return StorageQuotaReservation{}, fmt.Errorf("lock global storage quota usage: %w", err)
	}
	global := storageQuotaUsageFromRow(globalRow)
	workspace := storageQuotaUsageFromRow(workspaceRow)
	if err := checkStorageQuotaReplacement(global, workspace, current, newReserved, usedAddition, limits); err != nil {
		return StorageQuotaReservation{}, err
	}
	if resourceKey.Valid {
		resizeErr := queries.ResizeStagedUploadCleanup(ctx, db.ResizeStagedUploadCleanupParams{
			WorkspaceID:   reservation.WorkspaceID,
			StorageBytes:  request.Bytes,
			NextAttemptAt: now.Add(limits.ReservationTTL),
			ResourceKey:   resourceKey.String,
		})
		// The exact cleanup row is locked above. MySQL reports changed rather
		// than matched rows, so an idempotent same-size resize within the same
		// DATETIME second legitimately reports zero affected rows.
		if resizeErr != nil {
			return StorageQuotaReservation{}, fmt.Errorf("resize staged upload accounting: %w", resizeErr)
		}
	}
	if err := replaceStorageQuotaReservationAccounting(ctx, queries, reservation.WorkspaceID, current, newReserved, usedAddition); err != nil {
		return StorageQuotaReservation{}, err
	}
	resized := StorageQuotaReservation{
		ID: reservation.ID, WorkspaceID: reservation.WorkspaceID, Request: request, ExpiresAt: now.Add(limits.ReservationTTL),
	}
	params, err := storageReservationUpdateParams(resized, newReserved, resourceKey)
	if err != nil {
		return StorageQuotaReservation{}, err
	}
	updateResult, err := queries.UpdateStorageQuotaReservation(ctx, params)
	if err := requireOneAffected(updateResult, err); err != nil {
		return StorageQuotaReservation{}, fmt.Errorf("replace storage quota reservation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return StorageQuotaReservation{}, fmt.Errorf("commit storage quota resize: %w", err)
	}
	return resized, nil
}

func (s *ItemStore) StageStorageQuotaUpload(ctx context.Context, reservation StorageQuotaReservation, imageURL string, storageBytes uint64, limits StorageQuotaLimits) error {
	if s == nil || s.pool == nil || reservation.WorkspaceID == 0 || reservation.ID == "" || storageBytes == 0 {
		return fmt.Errorf("stage storage quota upload: reservation and bytes are required")
	}
	if err := validateStorageQuotaLimits(limits); err != nil {
		return err
	}
	resourceKey, ok := uploadref.ImmutableNameFromURL(imageURL)
	if !ok {
		return fmt.Errorf("stage storage quota upload: immutable upload URL is invalid")
	}
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("stage storage quota upload: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.q.WithTx(tx)
	workspaceRow, err := lockWorkspaceStorageQuotaUsage(ctx, queries, reservation.WorkspaceID)
	if err != nil {
		return fmt.Errorf("stage storage quota upload: %w", err)
	}
	currentRow, err := queries.LockLiveStorageQuotaReservation(ctx, db.LockLiveStorageQuotaReservationParams{
		ID: reservation.ID, WorkspaceID: reservation.WorkspaceID,
	})
	if err != nil {
		return fmt.Errorf("stage storage quota upload: load reservation: %w", err)
	}
	if currentRow.ResourceKey.Valid && currentRow.ResourceKey.String != resourceKey {
		return fmt.Errorf("stage storage quota upload: reservation is already bound")
	}
	current := storageQuotaRequestFromReservation(currentRow)
	newReserved := current
	newReserved.Bytes = 0
	usedAddition := StorageQuotaRequest{Bytes: storageBytes}
	existing, staged, existingErr := lockUploadCleanupForReference(ctx, queries, reservation.WorkspaceID, resourceKey)
	if existingErr == nil && staged {
		if existing.StorageBytes >= storageBytes {
			usedAddition.Bytes = 0
		} else {
			usedAddition.Bytes = storageBytes - existing.StorageBytes
		}
	} else if existingErr != nil {
		return fmt.Errorf("stage storage quota upload: inspect cleanup identity: %w", existingErr)
	}
	globalRow, err := queries.LockStorageQuotaUsage(ctx, 0)
	if err != nil {
		return fmt.Errorf("stage storage quota upload: lock global storage usage: %w", err)
	}
	if err := checkStorageQuotaReplacement(storageQuotaUsageFromRow(globalRow), storageQuotaUsageFromRow(workspaceRow), current, newReserved, usedAddition, limits); err != nil {
		return fmt.Errorf("stage storage quota upload: %w", err)
	}
	if err := queries.UpsertResourceCleanup(ctx, db.UpsertResourceCleanupParams{
		Kind: db.ResourceCleanupOutboxKindUploadBlob, ResourceKey: resourceKey,
		WorkspaceID: reservation.WorkspaceID, StorageBytes: storageBytes,
		NextAttemptAt: currentRow.ExpiresAt.UTC(),
	}); err != nil {
		return fmt.Errorf("stage storage quota upload: enqueue recovery: %w", err)
	}
	if err := replaceStorageQuotaReservationAccounting(ctx, queries, reservation.WorkspaceID, current, newReserved, usedAddition); err != nil {
		return fmt.Errorf("stage storage quota upload: %w", err)
	}
	result, err := queries.BindStorageQuotaReservationResource(ctx, db.BindStorageQuotaReservationResourceParams{
		ResourceKey: sql.NullString{String: resourceKey, Valid: true}, ID: reservation.ID, WorkspaceID: reservation.WorkspaceID,
	})
	if err := requireOneAffected(result, err); err != nil {
		return fmt.Errorf("stage storage quota upload: bind reservation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("stage storage quota upload: commit: %w", err)
	}
	return nil
}

func (s *ItemStore) ReleaseStorageQuotaReservation(ctx context.Context, reservation StorageQuotaReservation) error {
	if s == nil || s.pool == nil || reservation.WorkspaceID == 0 || reservation.ID == "" {
		return nil
	}
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("release storage quota reservation: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.q.WithTx(tx)
	if _, err := lockWorkspaceStorageQuotaUsage(ctx, queries, reservation.WorkspaceID); err != nil {
		return fmt.Errorf("release storage quota reservation: %w", err)
	}
	current, err := queries.LockStorageQuotaReservation(ctx, db.LockStorageQuotaReservationParams{
		ID: reservation.ID, WorkspaceID: reservation.WorkspaceID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("release storage quota reservation: load reservation: %w", err)
	}
	retireStagedUpload := false
	if current.ResourceKey.Valid {
		imageURL := "/static/uploads/" + current.ResourceKey.String
		references, countErr := queries.CountItemImagesByURL(ctx, imageURL)
		if countErr != nil {
			return fmt.Errorf("release storage quota reservation: count canonical upload references: %w", countErr)
		}
		retireStagedUpload = references > 0
	}
	if _, err := queries.LockStorageQuotaUsage(ctx, 0); err != nil {
		return fmt.Errorf("release storage quota reservation: lock global storage usage: %w", err)
	}
	if err := subtractStorageQuotaReserved(ctx, queries, reservation.WorkspaceID, storageQuotaRequestFromReservation(current)); err != nil {
		return fmt.Errorf("release storage quota reservation: %w", err)
	}
	result, err := queries.DeleteStorageQuotaReservation(ctx, db.DeleteStorageQuotaReservationParams{ID: reservation.ID, WorkspaceID: reservation.WorkspaceID})
	if err := requireOneAffected(result, err); err != nil {
		return fmt.Errorf("release storage quota reservation: %w", err)
	}
	if retireStagedUpload {
		if _, deleteErr := queries.DeleteResourceCleanupByKindKey(ctx, db.DeleteResourceCleanupByKindKeyParams{
			Kind: db.ResourceCleanupOutboxKindUploadBlob, ResourceKey: current.ResourceKey.String,
		}); deleteErr != nil {
			return fmt.Errorf("release storage quota reservation: retire staged upload: %w", deleteErr)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("release storage quota reservation: commit: %w", err)
	}
	return nil
}

func validateStorageQuotaLimits(limits StorageQuotaLimits) error {
	if limits.MaxBytesPerWorkspace == 0 || limits.MaxBytesTotal < limits.MaxBytesPerWorkspace ||
		limits.MaxItemsPerWorkspace == 0 || limits.MaxItemsTotal < limits.MaxItemsPerWorkspace ||
		limits.MaxImagesPerWorkspace == 0 || limits.MaxImagesTotal < limits.MaxImagesPerWorkspace ||
		limits.ReservationTTL <= 0 {
		return fmt.Errorf("storage quota limits are invalid")
	}
	return nil
}

func validateStorageQuotaRequest(request StorageQuotaRequest) error {
	if request.Items > math.MaxUint32 || request.Images > math.MaxUint32 {
		return fmt.Errorf("storage quota item or image request exceeds database range")
	}
	if _, ok := addUint64(request.Bytes, request.DurableBytes); !ok {
		return fmt.Errorf("storage quota byte request overflows")
	}
	return nil
}

func storageQuotaRequestEmpty(request StorageQuotaRequest) bool {
	return request.Bytes == 0 && request.DurableBytes == 0 && request.Items == 0 && request.Images == 0
}

func lockStorageQuotaGuards(ctx context.Context, tx *sql.Tx, workspaceID uint64) error {
	if tx == nil || workspaceID == 0 {
		return fmt.Errorf("lock storage quota usage: transaction and workspace are required")
	}
	_, err := lockWorkspaceStorageQuotaUsage(ctx, db.New(tx), workspaceID)
	return err
}

func lockStorageQuotaUsage(ctx context.Context, queries *db.Queries, workspaceID uint64) (db.StorageQuotaUsage, db.StorageQuotaUsage, error) {
	if workspaceID == 0 {
		return db.StorageQuotaUsage{}, db.StorageQuotaUsage{}, fmt.Errorf("lock storage quota usage: workspace is required")
	}
	workspace, err := lockWorkspaceStorageQuotaUsage(ctx, queries, workspaceID)
	if err != nil {
		return db.StorageQuotaUsage{}, db.StorageQuotaUsage{}, err
	}
	global, err := queries.LockStorageQuotaUsage(ctx, 0)
	if err != nil {
		return db.StorageQuotaUsage{}, db.StorageQuotaUsage{}, fmt.Errorf("lock global storage quota usage: %w", err)
	}
	return global, workspace, nil
}

func lockWorkspaceStorageQuotaUsage(ctx context.Context, queries *db.Queries, workspaceID uint64) (db.StorageQuotaUsage, error) {
	if queries == nil || workspaceID == 0 {
		return db.StorageQuotaUsage{}, fmt.Errorf("lock workspace storage quota usage: workspace is required")
	}
	workspace, err := queries.LockStorageQuotaUsage(ctx, workspaceID)
	if err != nil {
		return db.StorageQuotaUsage{}, fmt.Errorf("lock workspace storage quota usage: %w", err)
	}
	return workspace, nil
}

const maxStorageQuotaReservationSweepBatch = 500

// SweepExpiredStorageQuotaReservations removes one bounded, unlocked tenant
// batch. Expiry is eligibility only: admission counts a reservation until this
// transaction owns and deletes its row. An aggregate transaction holds the
// tenant quota row for its duration, so SKIP LOCKED selects another tenant
// instead of waiting behind active work.
func (s *ItemStore) SweepExpiredStorageQuotaReservations(ctx context.Context, batchSize int) (int, error) {
	if s == nil || s.pool == nil {
		return 0, fmt.Errorf("sweep storage quota reservations: item store is not configured")
	}
	if batchSize <= 0 || batchSize > maxStorageQuotaReservationSweepBatch {
		return 0, fmt.Errorf("sweep storage quota reservations: batch size must be between 1 and %d", maxStorageQuotaReservationSweepBatch)
	}
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("sweep storage quota reservations: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.q.WithTx(tx)
	now := time.Now().UTC()
	workspaceID, err := queries.LockStorageQuotaSweepWorkspace(ctx, now)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("sweep storage quota reservations: lock tenant candidate: %w", err)
	}
	rows, err := queries.LockExpiredStorageQuotaReservations(ctx, db.LockExpiredStorageQuotaReservationsParams{
		ExpiresAt:   now,
		WorkspaceID: workspaceID,
		Limit:       int32(batchSize), // #nosec G115 -- batchSize is bounded above.
	})
	if err != nil {
		return 0, fmt.Errorf("sweep storage quota reservations: lock expired batch: %w", err)
	}
	if len(rows) == 0 {
		if err := tx.Commit(); err != nil {
			return 0, fmt.Errorf("sweep storage quota reservations: commit empty batch: %w", err)
		}
		return 0, nil
	}
	total := StorageQuotaRequest{}
	for _, row := range rows {
		var ok bool
		if total.Bytes, ok = addUint64(total.Bytes, row.ReservedBytes); !ok {
			return 0, fmt.Errorf("sweep storage quota reservations: upload-byte total overflows")
		}
		if total.DurableBytes, ok = addUint64(total.DurableBytes, row.ReservedDatabaseBytes); !ok {
			return 0, fmt.Errorf("sweep storage quota reservations: database-byte total overflows")
		}
		if total.Items, ok = addUint64(total.Items, uint64(row.ReservedItems)); !ok {
			return 0, fmt.Errorf("sweep storage quota reservations: item total overflows")
		}
		if total.Images, ok = addUint64(total.Images, uint64(row.ReservedImages)); !ok {
			return 0, fmt.Errorf("sweep storage quota reservations: image total overflows")
		}
	}
	if _, err := queries.LockStorageQuotaUsage(ctx, 0); err != nil {
		return 0, fmt.Errorf("sweep storage quota reservations: lock global storage usage: %w", err)
	}
	if err := subtractStorageQuotaReserved(ctx, queries, workspaceID, total); err != nil {
		return 0, fmt.Errorf("sweep storage quota reservations: release counters: %w", err)
	}
	for _, row := range rows {
		result, deleteErr := queries.DeleteStorageQuotaReservation(ctx, db.DeleteStorageQuotaReservationParams{
			ID: row.ID, WorkspaceID: workspaceID,
		})
		if deleteErr := requireOneAffected(result, deleteErr); deleteErr != nil {
			return 0, fmt.Errorf("sweep storage quota reservations: delete %q: %w", row.ID, deleteErr)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("sweep storage quota reservations: commit: %w", err)
	}
	return len(rows), nil
}

func checkStorageQuotaCounters(global, workspace StorageQuotaUsage, request StorageQuotaRequest, limits StorageQuotaLimits) error {
	workspaceUsed, ok := workspace.usedBytes()
	if !ok {
		return fmt.Errorf("%w: workspace byte accounting overflow", ErrStorageQuotaExceeded)
	}
	workspaceReserved, ok := workspace.reservedBytes()
	if !ok {
		return fmt.Errorf("%w: workspace reserved byte accounting overflow", ErrStorageQuotaExceeded)
	}
	globalUsed, ok := global.usedBytes()
	if !ok {
		return fmt.Errorf("%w: global byte accounting overflow", ErrStorageQuotaExceeded)
	}
	globalReserved, ok := global.reservedBytes()
	if !ok {
		return fmt.Errorf("%w: global reserved byte accounting overflow", ErrStorageQuotaExceeded)
	}
	requestedBytes, ok := addUint64(request.Bytes, request.DurableBytes)
	if !ok {
		return fmt.Errorf("%w: requested byte accounting overflow", ErrStorageQuotaExceeded)
	}
	checks := []struct {
		name      string
		used      uint64
		reserved  uint64
		requested uint64
		limit     uint64
	}{
		{name: "workspace bytes", used: workspaceUsed, reserved: workspaceReserved, requested: requestedBytes, limit: limits.MaxBytesPerWorkspace},
		{name: "global bytes", used: globalUsed, reserved: globalReserved, requested: requestedBytes, limit: limits.MaxBytesTotal},
		{name: "workspace items", used: workspace.Items, reserved: workspace.ReservedItems, requested: request.Items, limit: limits.MaxItemsPerWorkspace},
		{name: "global items", used: global.Items, reserved: global.ReservedItems, requested: request.Items, limit: limits.MaxItemsTotal},
		{name: "workspace images", used: workspace.Images, reserved: workspace.ReservedImages, requested: request.Images, limit: limits.MaxImagesPerWorkspace},
		{name: "global images", used: global.Images, reserved: global.ReservedImages, requested: request.Images, limit: limits.MaxImagesTotal},
	}
	for _, check := range checks {
		if exceedsStorageLimit(check.used, check.reserved, check.requested, check.limit) {
			return fmt.Errorf("%w: %s capacity is exhausted", ErrStorageQuotaExceeded, check.name)
		}
	}
	return nil
}

func checkStorageQuotaReplacement(global, workspace StorageQuotaUsage, current, replacement, usedAddition StorageQuotaRequest, limits StorageQuotaLimits) error {
	adjust := func(usage StorageQuotaUsage) (StorageQuotaUsage, error) {
		if usage.ReservedUploadBlobBytes < current.Bytes || usage.ReservedDatabaseBytes < current.DurableBytes ||
			usage.ReservedItems < current.Items || usage.ReservedImages < current.Images {
			return StorageQuotaUsage{}, fmt.Errorf("storage quota reservation counters are inconsistent")
		}
		usage.ReservedUploadBlobBytes -= current.Bytes
		usage.ReservedDatabaseBytes -= current.DurableBytes
		usage.ReservedItems -= current.Items
		usage.ReservedImages -= current.Images
		var ok bool
		if usage.UploadBlobBytes, ok = addUint64(usage.UploadBlobBytes, usedAddition.Bytes); !ok {
			return StorageQuotaUsage{}, fmt.Errorf("storage quota upload accounting overflow")
		}
		if usage.DatabaseBytes, ok = addUint64(usage.DatabaseBytes, usedAddition.DurableBytes); !ok {
			return StorageQuotaUsage{}, fmt.Errorf("storage quota database accounting overflow")
		}
		if usage.Items, ok = addUint64(usage.Items, usedAddition.Items); !ok {
			return StorageQuotaUsage{}, fmt.Errorf("storage quota item accounting overflow")
		}
		if usage.Images, ok = addUint64(usage.Images, usedAddition.Images); !ok {
			return StorageQuotaUsage{}, fmt.Errorf("storage quota image accounting overflow")
		}
		return usage, nil
	}
	globalAdjusted, err := adjust(global)
	if err != nil {
		return err
	}
	workspaceAdjusted, err := adjust(workspace)
	if err != nil {
		return err
	}
	return checkStorageQuotaCounters(globalAdjusted, workspaceAdjusted, replacement, limits)
}

func replaceStorageQuotaReservationAccounting(ctx context.Context, queries *db.Queries, workspaceID uint64, current, replacement, usedAddition StorageQuotaRequest) error {
	if err := subtractStorageQuotaReserved(ctx, queries, workspaceID, current); err != nil {
		return err
	}
	if !storageQuotaRequestEmpty(replacement) {
		if err := addStorageQuotaReserved(ctx, queries, workspaceID, replacement); err != nil {
			return err
		}
	}
	if !storageQuotaRequestEmpty(usedAddition) {
		if err := addStorageQuotaUsed(ctx, queries, workspaceID, usedAddition); err != nil {
			return err
		}
	}
	return nil
}

// transferStorageQuotaReservationToUsed moves already-admitted capacity from
// one live reservation into canonical usage. Callers must hold the workspace
// quota guard and the reservation row in the surrounding transaction.
func transferStorageQuotaReservationToUsed(ctx context.Context, queries *db.Queries, reservation db.WorkspaceStorageReservation, consumed StorageQuotaRequest) error {
	if queries == nil || reservation.ID == "" || reservation.WorkspaceID == 0 || storageQuotaRequestEmpty(consumed) {
		return fmt.Errorf("transfer storage quota reservation: reservation and capacity are required")
	}
	if err := validateStorageQuotaRequest(consumed); err != nil {
		return fmt.Errorf("transfer storage quota reservation: %w", err)
	}
	current := storageQuotaRequestFromReservation(reservation)
	if current.Bytes < consumed.Bytes || current.DurableBytes < consumed.DurableBytes ||
		current.Items < consumed.Items || current.Images < consumed.Images {
		return fmt.Errorf("transfer storage quota reservation: reserved capacity does not cover committed usage")
	}
	remaining := StorageQuotaRequest{
		Bytes:        current.Bytes - consumed.Bytes,
		DurableBytes: current.DurableBytes - consumed.DurableBytes,
		Items:        current.Items - consumed.Items,
		Images:       current.Images - consumed.Images,
	}
	workspaceRow, err := queries.LockStorageQuotaUsage(ctx, reservation.WorkspaceID)
	if err != nil {
		return fmt.Errorf("transfer storage quota reservation: lock workspace usage: %w", err)
	}
	globalRow, err := queries.LockStorageQuotaUsage(ctx, 0)
	if err != nil {
		return fmt.Errorf("transfer storage quota reservation: lock global usage: %w", err)
	}
	// A transfer adds no capacity. Unbounded limits preserve an already-admitted
	// operation if configuration was lowered while still detecting counter drift
	// and arithmetic overflow.
	if err := checkStorageQuotaReplacement(
		storageQuotaUsageFromRow(globalRow),
		storageQuotaUsageFromRow(workspaceRow),
		current,
		remaining,
		consumed,
		unboundedStorageQuotaLimits(),
	); err != nil {
		return fmt.Errorf("transfer storage quota reservation: %w", err)
	}
	if err := replaceStorageQuotaReservationAccounting(ctx, queries, reservation.WorkspaceID, current, remaining, consumed); err != nil {
		return fmt.Errorf("transfer storage quota reservation: account committed usage: %w", err)
	}
	params, err := storageReservationUpdateParams(StorageQuotaReservation{
		ID:          reservation.ID,
		WorkspaceID: reservation.WorkspaceID,
		ExpiresAt:   reservation.ExpiresAt,
	}, remaining, reservation.ResourceKey)
	if err != nil {
		return fmt.Errorf("transfer storage quota reservation: update reservation: %w", err)
	}
	result, err := queries.UpdateStorageQuotaReservation(ctx, params)
	if err := requireOneAffected(result, err); err != nil {
		return fmt.Errorf("transfer storage quota reservation: update reservation: %w", err)
	}
	return nil
}

func addStorageQuotaUsed(ctx context.Context, queries *db.Queries, workspaceID uint64, request StorageQuotaRequest) error {
	return queries.AddStorageQuotaUsed(ctx, db.AddStorageQuotaUsedParams{
		UploadBlobBytes: request.Bytes, DatabaseBytes: request.DurableBytes,
		ItemCount: request.Items, ImageCount: request.Images, WorkspaceID: workspaceID,
	})
}

func subtractStorageQuotaUsed(ctx context.Context, queries *db.Queries, workspaceID uint64, request StorageQuotaRequest) error {
	if storageQuotaRequestEmpty(request) {
		return nil
	}
	result, err := queries.SubtractStorageQuotaUsed(ctx, db.SubtractStorageQuotaUsedParams{
		UploadBlobBytes: request.Bytes, DatabaseBytes: request.DurableBytes,
		ItemCount: request.Items, ImageCount: request.Images, WorkspaceID: workspaceID,
	})
	return requireQuotaUsageRows(result, err)
}

func addStorageQuotaReserved(ctx context.Context, queries *db.Queries, workspaceID uint64, request StorageQuotaRequest) error {
	return queries.AddStorageQuotaReserved(ctx, db.AddStorageQuotaReservedParams{
		UploadBlobBytes: request.Bytes, DatabaseBytes: request.DurableBytes,
		ItemCount: request.Items, ImageCount: request.Images, WorkspaceID: workspaceID,
	})
}

func subtractStorageQuotaReserved(ctx context.Context, queries *db.Queries, workspaceID uint64, request StorageQuotaRequest) error {
	if storageQuotaRequestEmpty(request) {
		return nil
	}
	result, err := queries.SubtractStorageQuotaReserved(ctx, db.SubtractStorageQuotaReservedParams{
		UploadBlobBytes: request.Bytes, DatabaseBytes: request.DurableBytes,
		ItemCount: request.Items, ImageCount: request.Images, WorkspaceID: workspaceID,
	})
	return requireQuotaUsageRows(result, err)
}

func requireQuotaUsageRows(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 2 {
		return fmt.Errorf("storage quota usage changed %d rows, want global and workspace", affected)
	}
	return nil
}

func storageReservationInsertParams(reservation StorageQuotaReservation, resourceKey sql.NullString) (db.InsertStorageQuotaReservationParams, error) {
	if err := validateStorageQuotaRequest(reservation.Request); err != nil {
		return db.InsertStorageQuotaReservationParams{}, err
	}
	return db.InsertStorageQuotaReservationParams{
		ID: reservation.ID, WorkspaceID: reservation.WorkspaceID,
		ReservedBytes: reservation.Request.Bytes, ReservedDatabaseBytes: reservation.Request.DurableBytes,
		ReservedItems: uint32(reservation.Request.Items), ReservedImages: uint32(reservation.Request.Images), // #nosec G115 -- validateStorageQuotaRequest bounds both at MaxUint32.
		ResourceKey: resourceKey, ExpiresAt: reservation.ExpiresAt,
	}, nil
}

func storageReservationUpdateParams(reservation StorageQuotaReservation, accounted StorageQuotaRequest, resourceKey sql.NullString) (db.UpdateStorageQuotaReservationParams, error) {
	if err := validateStorageQuotaRequest(accounted); err != nil {
		return db.UpdateStorageQuotaReservationParams{}, err
	}
	return db.UpdateStorageQuotaReservationParams{
		ReservedBytes: accounted.Bytes, ReservedDatabaseBytes: accounted.DurableBytes,
		ReservedItems: uint32(accounted.Items), ReservedImages: uint32(accounted.Images), // #nosec G115 -- validateStorageQuotaRequest bounds both at MaxUint32.
		ResourceKey: resourceKey, ExpiresAt: reservation.ExpiresAt,
		ID: reservation.ID, WorkspaceID: reservation.WorkspaceID,
	}, nil
}

func storageQuotaRequestFromReservation(row db.WorkspaceStorageReservation) StorageQuotaRequest {
	return StorageQuotaRequest{
		Bytes: row.ReservedBytes, DurableBytes: row.ReservedDatabaseBytes,
		Items: uint64(row.ReservedItems), Images: uint64(row.ReservedImages),
	}
}

func storageQuotaUsageFromRow(row db.StorageQuotaUsage) StorageQuotaUsage {
	return StorageQuotaUsage{
		WorkspaceID: row.WorkspaceID, UploadBlobBytes: row.UploadBlobBytes, DatabaseBytes: row.DatabaseBytes,
		Items: row.ItemCount, Images: row.ImageCount,
		ReservedUploadBlobBytes: row.ReservedUploadBlobBytes, ReservedDatabaseBytes: row.ReservedDatabaseBytes,
		ReservedItems: row.ReservedItemCount, ReservedImages: row.ReservedImageCount,
	}
}

func itemImageDurableDatabaseBytes(ctx context.Context, queries *db.Queries, workspaceID, itemImageID uint64) (uint64, error) {
	raw, err := queries.GetItemImageDurableDatabaseBytes(ctx, db.GetItemImageDurableDatabaseBytesParams{
		ItemImageID: itemImageID, WorkspaceID: workspaceID,
	})
	if err != nil {
		return 0, err
	}
	value, ok := uint64FromNonNegativeInt64(raw)
	if !ok {
		return 0, fmt.Errorf("item image durable database bytes are negative")
	}
	return value, nil
}

func itemDurableDatabaseBytes(ctx context.Context, queries *db.Queries, workspaceID uint64, itemID string) (uint64, error) {
	raw, err := queries.GetItemDurableDatabaseBytes(ctx, db.GetItemDurableDatabaseBytesParams{
		WorkspaceID: workspaceID, ItemID: itemID,
	})
	if err != nil {
		return 0, err
	}
	value, ok := uint64FromNonNegativeInt64(raw)
	if !ok {
		return 0, fmt.Errorf("item durable database bytes are negative")
	}
	return value, nil
}

func applyStorageQuotaUsedDelta(ctx context.Context, queries *db.Queries, workspaceID uint64, before, after uint64) error {
	if after > before {
		return addStorageQuotaUsed(ctx, queries, workspaceID, StorageQuotaRequest{DurableBytes: after - before})
	}
	if before > after {
		return subtractStorageQuotaUsed(ctx, queries, workspaceID, StorageQuotaRequest{DurableBytes: before - after})
	}
	return nil
}

func applyStorageQuotaUsedDeltaWithLimits(ctx context.Context, queries *db.Queries, workspaceID uint64, before, after uint64, limits StorageQuotaLimits) error {
	if after > before {
		addition := StorageQuotaRequest{DurableBytes: after - before}
		workspace, err := queries.LockStorageQuotaUsage(ctx, workspaceID)
		if err != nil {
			return err
		}
		global, err := queries.LockStorageQuotaUsage(ctx, 0)
		if err != nil {
			return err
		}
		if err := checkStorageQuotaCounters(storageQuotaUsageFromRow(global), storageQuotaUsageFromRow(workspace), addition, limits); err != nil {
			return err
		}
	}
	return applyStorageQuotaUsedDelta(ctx, queries, workspaceID, before, after)
}

func unboundedStorageQuotaLimits() StorageQuotaLimits {
	return StorageQuotaLimits{
		MaxBytesPerWorkspace: math.MaxUint64, MaxBytesTotal: math.MaxUint64,
		MaxItemsPerWorkspace: math.MaxUint64, MaxItemsTotal: math.MaxUint64,
		MaxImagesPerWorkspace: math.MaxUint64, MaxImagesTotal: math.MaxUint64,
		ReservationTTL: time.Hour,
	}
}

func (s *ItemStore) GetStorageQuotaUsage(ctx context.Context, workspaceID uint64) (StorageQuotaUsage, error) {
	if s == nil || s.q == nil {
		return StorageQuotaUsage{}, fmt.Errorf("get storage quota usage: store is not configured")
	}
	row, err := s.q.GetStorageQuotaUsage(ctx, workspaceID)
	if err != nil {
		return StorageQuotaUsage{}, fmt.Errorf("get storage quota usage: %w", err)
	}
	return storageQuotaUsageFromRow(row), nil
}

// RebuildStorageQuotaUsage repairs the materialization from canonical rows,
// immutable OCR baselines, publication snapshots, cleanup ownership, and every
// extant reservation. It locks tenant rows in key order before the global row;
// the ordered InnoDB range lock also prevents a new tenant quota row from
// appearing inside the rebuild snapshot.
func (s *ItemStore) RebuildStorageQuotaUsage(ctx context.Context) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("rebuild storage quota usage: store is not configured")
	}
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("rebuild storage quota usage: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.q.WithTx(tx)
	lockedRows, err := queries.LockAllTenantStorageQuotaUsage(ctx)
	if err != nil {
		return fmt.Errorf("rebuild storage quota usage: lock tenant rows: %w", err)
	}
	lockedWorkspaceIDs := make(map[uint64]struct{}, len(lockedRows))
	for _, row := range lockedRows {
		lockedWorkspaceIDs[row.WorkspaceID] = struct{}{}
	}
	if _, err := queries.LockStorageQuotaUsage(ctx, 0); err != nil {
		return fmt.Errorf("rebuild storage quota usage: lock global usage: %w", err)
	}
	rows, err := queries.RebuildStorageQuotaUsageRows(ctx)
	if err != nil {
		return fmt.Errorf("rebuild storage quota usage: derive rows: %w", err)
	}
	global := db.ReplaceStorageQuotaUsageParams{WorkspaceID: 0}
	for _, row := range rows {
		if _, locked := lockedWorkspaceIDs[row.WorkspaceID]; !locked {
			return fmt.Errorf("rebuild storage quota usage: owner %d has no transactionally created quota row", row.WorkspaceID)
		}
		itemCount, ok := uint64FromNonNegativeInt64(row.ItemCount)
		if !ok {
			return fmt.Errorf("rebuild storage quota usage: workspace %d item count is negative", row.WorkspaceID)
		}
		imageCount, ok := uint64FromNonNegativeInt64(row.ImageCount)
		if !ok {
			return fmt.Errorf("rebuild storage quota usage: workspace %d image count is negative", row.WorkspaceID)
		}
		uploadBytes, ok := uint64FromNonNegativeInt64(row.UploadBlobBytes)
		if !ok {
			return fmt.Errorf("rebuild storage quota usage: workspace %d upload bytes are negative", row.WorkspaceID)
		}
		databaseBytes, ok := uint64FromNonNegativeInt64(row.DatabaseBytes)
		if !ok {
			return fmt.Errorf("rebuild storage quota usage: workspace %d database bytes are negative", row.WorkspaceID)
		}
		reservedUploadBytes, ok := uint64FromNonNegativeInt64(row.ReservedUploadBlobBytes)
		if !ok {
			return fmt.Errorf("rebuild storage quota usage: workspace %d reserved upload bytes are negative", row.WorkspaceID)
		}
		reservedDatabaseBytes, ok := uint64FromNonNegativeInt64(row.ReservedDatabaseBytes)
		if !ok {
			return fmt.Errorf("rebuild storage quota usage: workspace %d reserved database bytes are negative", row.WorkspaceID)
		}
		reservedItems, ok := uint64FromNonNegativeInt64(row.ReservedItemCount)
		if !ok {
			return fmt.Errorf("rebuild storage quota usage: workspace %d reserved items are negative", row.WorkspaceID)
		}
		reservedImages, ok := uint64FromNonNegativeInt64(row.ReservedImageCount)
		if !ok {
			return fmt.Errorf("rebuild storage quota usage: workspace %d reserved images are negative", row.WorkspaceID)
		}
		params := db.ReplaceStorageQuotaUsageParams{
			WorkspaceID: row.WorkspaceID, UploadBlobBytes: uploadBytes, DatabaseBytes: databaseBytes,
			ItemCount: itemCount, ImageCount: imageCount,
			ReservedUploadBlobBytes: reservedUploadBytes, ReservedDatabaseBytes: reservedDatabaseBytes,
			ReservedItemCount: reservedItems, ReservedImageCount: reservedImages,
		}
		if err := queries.ReplaceStorageQuotaUsage(ctx, params); err != nil {
			return fmt.Errorf("rebuild storage quota usage: replace workspace %d: %w", row.WorkspaceID, err)
		}
		if err := addUsageParams(&global, params); err != nil {
			return fmt.Errorf("rebuild storage quota usage: %w", err)
		}
	}
	if err := queries.ReplaceStorageQuotaUsage(ctx, global); err != nil {
		return fmt.Errorf("rebuild storage quota usage: replace global row: %w", err)
	}
	if err := queries.DeleteOrphanStorageQuotaUsage(ctx); err != nil {
		return fmt.Errorf("rebuild storage quota usage: delete orphan rows: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("rebuild storage quota usage: commit: %w", err)
	}
	return nil
}

func addUsageParams(total *db.ReplaceStorageQuotaUsageParams, value db.ReplaceStorageQuotaUsageParams) error {
	fields := []struct {
		destination *uint64
		value       uint64
	}{
		{&total.UploadBlobBytes, value.UploadBlobBytes}, {&total.DatabaseBytes, value.DatabaseBytes},
		{&total.ItemCount, value.ItemCount}, {&total.ImageCount, value.ImageCount},
		{&total.ReservedUploadBlobBytes, value.ReservedUploadBlobBytes}, {&total.ReservedDatabaseBytes, value.ReservedDatabaseBytes},
		{&total.ReservedItemCount, value.ReservedItemCount}, {&total.ReservedImageCount, value.ReservedImageCount},
	}
	for _, field := range fields {
		updated, ok := addUint64(*field.destination, field.value)
		if !ok {
			return fmt.Errorf("global storage quota usage overflows")
		}
		*field.destination = updated
	}
	return nil
}

func addUint64(left, right uint64) (uint64, bool) {
	if right > math.MaxUint64-left {
		return 0, false
	}
	return left + right, true
}

func exceedsStorageLimit(used, reserved, requested, limit uint64) bool {
	if used > limit || reserved > limit-used {
		return true
	}
	return requested > limit-used-reserved
}
