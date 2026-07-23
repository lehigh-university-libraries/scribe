package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	db "github.com/lehigh-university-libraries/scribe/internal/db"
)

var (
	ErrResourceCleanupLease = errors.New("resource cleanup lease lost")
	ErrUploadBlobRetired    = errors.New("upload blob identity is retired")
)

type ResourceCleanupKind string

const (
	ResourceCleanupUploadBlob               ResourceCleanupKind = "upload_blob"
	ResourceCleanupTripletPresentationImage ResourceCleanupKind = "triplet_presentation_image"
	ResourceCleanupTripletPresentationItem  ResourceCleanupKind = "triplet_presentation_item"
)

// ResourceCleanupDelivery is a leased external deletion. Generation and lease
// owner jointly fence completion when the same resource is re-enqueued.
type ResourceCleanupDelivery struct {
	ID           uint64
	Kind         ResourceCleanupKind
	ResourceKey  string
	WorkspaceID  uint64
	StorageBytes uint64
	Generation   uint64
	LeaseOwner   string
	Attempt      int
	MaxAttempts  int
}

func (s *ItemStore) ClaimResourceCleanup(ctx context.Context, leaseDuration time.Duration) (*ResourceCleanupDelivery, error) {
	if s == nil || s.pool == nil {
		return nil, nil
	}
	if leaseDuration <= 0 {
		leaseDuration = 30 * time.Second
	}
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin resource cleanup claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.q.WithTx(tx)
	if err := queries.FailExhaustedResourceCleanups(ctx); err != nil {
		return nil, fmt.Errorf("fail exhausted resource cleanups: %w", err)
	}
	row, err := queries.SelectResourceCleanupForUpdate(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit exhausted resource cleanups: %w", err)
		}
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select resource cleanup: %w", err)
	}
	leaseOwner := newLeaseOwner("resource-cleanup")
	result, err := queries.MarkResourceCleanupProcessing(ctx, db.MarkResourceCleanupProcessingParams{
		LeaseUntil: sql.NullTime{Time: time.Now().UTC().Add(leaseDuration), Valid: true},
		LockedBy:   nullableString(leaseOwner),
		ID:         row.ID,
		Generation: row.Generation,
	})
	if err != nil {
		return nil, fmt.Errorf("mark resource cleanup processing: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("read resource cleanup claim result: %w", err)
	}
	if affected != 1 {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit resource cleanup recovery: %w", err)
		}
		return nil, nil
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit resource cleanup claim: %w", err)
	}
	return &ResourceCleanupDelivery{
		ID:           row.ID,
		Kind:         ResourceCleanupKind(row.Kind),
		ResourceKey:  row.ResourceKey,
		WorkspaceID:  row.WorkspaceID,
		StorageBytes: row.StorageBytes,
		Generation:   row.Generation,
		LeaseOwner:   leaseOwner,
		Attempt:      int(row.AttemptCount) + 1,
		MaxAttempts:  int(row.MaxAttempts),
	}, nil
}

// BeginUploadBlobRetirement establishes the durable half of the blob deletion
// handshake. Every transaction that can create a local-upload reference first
// takes the same quota guards and rejects a fenced cleanup identity. Once this
// method returns true, no new reference can commit until the claimed worker has
// completed the physical deletion and removed the tombstone.
func (s *ItemStore) BeginUploadBlobRetirement(ctx context.Context, delivery ResourceCleanupDelivery) (bool, error) {
	if s == nil || s.pool == nil || delivery.Kind != ResourceCleanupUploadBlob ||
		delivery.ID == 0 || delivery.WorkspaceID == 0 || delivery.ResourceKey == "" ||
		delivery.Generation == 0 || delivery.LeaseOwner == "" {
		return false, fmt.Errorf("begin upload blob retirement: active upload cleanup delivery is required")
	}
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin upload blob retirement: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockStorageQuotaGuards(ctx, tx, delivery.WorkspaceID); err != nil {
		return false, fmt.Errorf("begin upload blob retirement: lock reference guards: %w", err)
	}
	queries := s.q.WithTx(tx)
	cleanup, err := queries.LockResourceCleanupByKindKey(ctx, db.LockResourceCleanupByKindKeyParams{
		Kind: db.ResourceCleanupOutboxKindUploadBlob, ResourceKey: delivery.ResourceKey,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrResourceCleanupLease
		}
		return false, fmt.Errorf("begin upload blob retirement: lock cleanup: %w", err)
	}
	if cleanup.ID != delivery.ID || cleanup.Generation != delivery.Generation ||
		cleanup.WorkspaceID != delivery.WorkspaceID || cleanup.Status != db.ResourceCleanupOutboxStatusProcessing ||
		!cleanup.LockedBy.Valid || cleanup.LockedBy.String != delivery.LeaseOwner {
		return false, ErrResourceCleanupLease
	}
	if cleanup.DeleteFencedAt.Valid {
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("begin upload blob retirement: commit existing fence: %w", err)
		}
		return true, nil
	}
	imageURL := "/static/uploads/" + delivery.ResourceKey
	references, err := queries.LockItemImageIDsByURL(ctx, imageURL)
	if err != nil {
		return false, fmt.Errorf("begin upload blob retirement: lock canonical references: %w", err)
	}
	if len(references) != 0 {
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("begin upload blob retirement: commit reference check: %w", err)
		}
		return false, nil
	}
	result, err := queries.FenceUploadResourceCleanup(ctx, db.FenceUploadResourceCleanupParams{
		ID: delivery.ID, Generation: delivery.Generation, LockedBy: nullableString(delivery.LeaseOwner),
	})
	if err := requireCleanupAffected(result, err); err != nil {
		return false, fmt.Errorf("begin upload blob retirement: establish fence: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("begin upload blob retirement: commit fence: %w", err)
	}
	return true, nil
}

func lockUploadCleanupForReference(ctx context.Context, queries *db.Queries, workspaceID uint64, resourceKey string) (db.ResourceCleanupOutbox, bool, error) {
	cleanup, err := queries.LockResourceCleanupByKindKey(ctx, db.LockResourceCleanupByKindKeyParams{
		Kind: db.ResourceCleanupOutboxKindUploadBlob, ResourceKey: resourceKey,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return db.ResourceCleanupOutbox{}, false, nil
	}
	if err != nil {
		return db.ResourceCleanupOutbox{}, false, err
	}
	if cleanup.WorkspaceID != workspaceID {
		return db.ResourceCleanupOutbox{}, false, fmt.Errorf("immutable upload identity belongs to another workspace")
	}
	if cleanup.DeleteFencedAt.Valid {
		return db.ResourceCleanupOutbox{}, false, ErrUploadBlobRetired
	}
	return cleanup, true, nil
}

func (s *ItemStore) CompleteResourceCleanup(ctx context.Context, delivery ResourceCleanupDelivery, releasePhysicalBytes ...bool) error {
	releaseBytes := true
	if len(releasePhysicalBytes) > 0 {
		releaseBytes = releasePhysicalBytes[0]
	}
	if delivery.Kind != ResourceCleanupUploadBlob || !releaseBytes {
		result, err := s.q.CompleteResourceCleanup(ctx, db.CompleteResourceCleanupParams{
			ID: delivery.ID, Generation: delivery.Generation, LockedBy: nullableString(delivery.LeaseOwner),
		})
		return requireCleanupAffected(result, err)
	}
	if delivery.WorkspaceID == 0 {
		return fmt.Errorf("complete resource cleanup: upload workspace is required")
	}
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("complete resource cleanup: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockStorageQuotaGuards(ctx, tx, delivery.WorkspaceID); err != nil {
		return fmt.Errorf("complete resource cleanup: lock storage usage: %w", err)
	}
	queries := s.q.WithTx(tx)
	result, err := queries.CompleteFencedUploadResourceCleanup(ctx, db.CompleteFencedUploadResourceCleanupParams{
		ID:         delivery.ID,
		Generation: delivery.Generation,
		LockedBy:   nullableString(delivery.LeaseOwner),
	})
	if err := requireCleanupAffected(result, err); err != nil {
		return err
	}
	if delivery.StorageBytes > 0 {
		if err := subtractStorageQuotaUsed(ctx, queries, delivery.WorkspaceID, StorageQuotaRequest{Bytes: delivery.StorageBytes}); err != nil {
			return fmt.Errorf("complete resource cleanup: release upload bytes: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("complete resource cleanup: commit: %w", err)
	}
	return nil
}

func (s *ItemStore) RetryResourceCleanup(ctx context.Context, delivery ResourceCleanupDelivery, cause error, nextAttempt time.Time) error {
	message := SafeResourceCleanupFailureMessage(cause)
	result, err := s.q.RetryResourceCleanup(ctx, db.RetryResourceCleanupParams{
		NextAttemptAt: nextAttempt.UTC(),
		LastError:     nullableString(message),
		ID:            delivery.ID,
		Generation:    delivery.Generation,
		LockedBy:      nullableString(delivery.LeaseOwner),
	})
	return requireCleanupAffected(result, err)
}

func requireCleanupAffected(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrResourceCleanupLease
	}
	return nil
}
