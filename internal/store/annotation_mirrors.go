package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	db "github.com/lehigh-university-libraries/scribe/internal/db"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
)

var ErrAnnotationMirrorLease = errors.New("annotation mirror lease lost")

// AnnotationMirrorDelivery is one revision-fenced Triplet delivery attempt.
// A newer publication replaces the row and invalidates this lease.
type AnnotationMirrorDelivery struct {
	ItemImageID uint64
	Revision    uint64
	Payload     string
	LeaseOwner  string
	Attempt     int
	MaxAttempts int
}

// AnnotationMirrorTombstones is the durable set of independently stored
// Triplet Annotation resources that are no longer present in the published
// parent page. Generation is diagnostic; repository row locks merge concurrent
// publication and delivery acknowledgements without relying on a SQL foreign
// key or cascade.
type AnnotationMirrorTombstones struct {
	ItemImageID   uint64
	Generation    uint64
	AnnotationIDs []string
}

func annotationIDsFromPublishedPage(payload string) (map[string]struct{}, error) {
	ids := make(map[string]struct{})
	if payload == "" {
		return ids, nil
	}
	annotations, err := iiif.AnnotationsFromPage([]byte(payload))
	if err != nil {
		return nil, err
	}
	for _, annotation := range annotations {
		if annotation.ID == "" {
			return nil, fmt.Errorf("published Annotation has no id")
		}
		ids[annotation.ID] = struct{}{}
	}
	return ids, nil
}

func decodeAnnotationMirrorTombstoneIDs(raw string) (map[string]struct{}, error) {
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, fmt.Errorf("decode annotation mirror tombstones: %w", err)
	}
	ids := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			return nil, fmt.Errorf("decode annotation mirror tombstones: empty Annotation id")
		}
		ids[value] = struct{}{}
	}
	return ids, nil
}

func encodeAnnotationMirrorTombstoneIDs(ids map[string]struct{}) (string, error) {
	values := make([]string, 0, len(ids))
	for id := range ids {
		values = append(values, id)
	}
	sort.Strings(values)
	payload, err := json.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("encode annotation mirror tombstones: %w", err)
	}
	return string(payload), nil
}

// replaceAnnotationMirrorTombstones merges children removed from the previous
// public snapshot with any undelivered removals, then subtracts children that
// the new snapshot reintroduces. Callers hold the item-image publication lock.
func replaceAnnotationMirrorTombstones(ctx context.Context, queries *db.Queries, itemImageID uint64, previousPayload, nextPayload string) error {
	if queries == nil || itemImageID == 0 {
		return fmt.Errorf("replace annotation mirror tombstones: repository and item image are required")
	}
	stale := make(map[string]struct{})
	row, err := queries.GetAnnotationMirrorTombstonesForUpdate(ctx, itemImageID)
	if err == nil {
		stale, err = decodeAnnotationMirrorTombstoneIDs(row.AnnotationIds)
		if err != nil {
			return err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("load annotation mirror tombstones: %w", err)
	}
	previous, err := annotationIDsFromPublishedPage(previousPayload)
	if err != nil {
		return fmt.Errorf("decode previous published AnnotationPage: %w", err)
	}
	next, err := annotationIDsFromPublishedPage(nextPayload)
	if err != nil {
		return fmt.Errorf("decode replacement published AnnotationPage: %w", err)
	}
	for id := range previous {
		stale[id] = struct{}{}
	}
	for id := range next {
		delete(stale, id)
	}
	if len(stale) == 0 {
		return queries.DeleteAnnotationMirrorTombstones(ctx, itemImageID)
	}
	payload, err := encodeAnnotationMirrorTombstoneIDs(stale)
	if err != nil {
		return err
	}
	return queries.UpsertAnnotationMirrorTombstones(ctx, db.UpsertAnnotationMirrorTombstonesParams{
		ItemImageID:   itemImageID,
		AnnotationIds: payload,
	})
}

// LoadAnnotationMirrorTombstones returns a stable sorted snapshot. A missing
// row means no standalone child requires deletion.
func (s *AnnotationStore) LoadAnnotationMirrorTombstones(ctx context.Context, itemImageID uint64) (AnnotationMirrorTombstones, error) {
	if s == nil || s.q == nil || itemImageID == 0 {
		return AnnotationMirrorTombstones{}, fmt.Errorf("load annotation mirror tombstones: repository and item image are required")
	}
	row, err := s.q.GetAnnotationMirrorTombstones(ctx, itemImageID)
	if errors.Is(err, sql.ErrNoRows) {
		return AnnotationMirrorTombstones{ItemImageID: itemImageID}, nil
	}
	if err != nil {
		return AnnotationMirrorTombstones{}, fmt.Errorf("load annotation mirror tombstones: %w", err)
	}
	ids, err := decodeAnnotationMirrorTombstoneIDs(row.AnnotationIds)
	if err != nil {
		return AnnotationMirrorTombstones{}, err
	}
	values := make([]string, 0, len(ids))
	for id := range ids {
		values = append(values, id)
	}
	sort.Strings(values)
	return AnnotationMirrorTombstones{ItemImageID: row.ItemImageID, Generation: row.Generation, AnnotationIDs: values}, nil
}

// AcknowledgeAnnotationMirrorTombstones removes only IDs confirmed absent from
// Triplet. A publication that ran concurrently may have changed the row; the
// locking merge preserves every newer, unrelated deletion intent.
func (s *AnnotationStore) AcknowledgeAnnotationMirrorTombstones(ctx context.Context, itemImageID uint64, delivered []string) error {
	if s == nil || s.pool == nil || itemImageID == 0 {
		return fmt.Errorf("acknowledge annotation mirror tombstones: repository and item image are required")
	}
	if len(delivered) == 0 {
		return nil
	}
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("acknowledge annotation mirror tombstones: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.q.WithTx(tx)
	row, err := queries.GetAnnotationMirrorTombstonesForUpdate(ctx, itemImageID)
	if errors.Is(err, sql.ErrNoRows) {
		return tx.Commit()
	}
	if err != nil {
		return fmt.Errorf("acknowledge annotation mirror tombstones: load current row: %w", err)
	}
	ids, err := decodeAnnotationMirrorTombstoneIDs(row.AnnotationIds)
	if err != nil {
		return err
	}
	for _, id := range delivered {
		delete(ids, id)
	}
	if len(ids) == 0 {
		if err := queries.DeleteAnnotationMirrorTombstones(ctx, itemImageID); err != nil {
			return fmt.Errorf("acknowledge annotation mirror tombstones: delete row: %w", err)
		}
	} else {
		payload, err := encodeAnnotationMirrorTombstoneIDs(ids)
		if err != nil {
			return err
		}
		if err := queries.UpsertAnnotationMirrorTombstones(ctx, db.UpsertAnnotationMirrorTombstonesParams{
			ItemImageID:   itemImageID,
			AnnotationIds: payload,
		}); err != nil {
			return fmt.Errorf("acknowledge annotation mirror tombstones: update row: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("acknowledge annotation mirror tombstones: commit: %w", err)
	}
	return nil
}

// ClaimAnnotationMirror claims the oldest due mirror revision. Expired leases
// are reclaimed, while exhausted rows remain failed for operational diagnosis
// until a newer published revision resets the coalescing outbox row.
func (s *AnnotationStore) ClaimAnnotationMirror(ctx context.Context, leaseDuration time.Duration) (*AnnotationMirrorDelivery, error) {
	if s == nil || s.pool == nil {
		return nil, nil
	}
	if leaseDuration <= 0 {
		leaseDuration = 30 * time.Second
	}
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin annotation mirror claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.q.WithTx(tx)
	if err := queries.FailExhaustedAnnotationMirrors(ctx); err != nil {
		return nil, fmt.Errorf("fail exhausted annotation mirrors: %w", err)
	}
	row, err := queries.SelectAnnotationMirrorForUpdate(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		// Persist any exhausted-lease transitions even when there is no
		// deliverable revision left to claim.
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit exhausted annotation mirrors: %w", err)
		}
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select annotation mirror: %w", err)
	}
	leaseOwner := newLeaseOwner("annotation-mirror")
	result, err := queries.MarkAnnotationMirrorProcessing(ctx, db.MarkAnnotationMirrorProcessingParams{
		LeaseUntil:  sql.NullTime{Time: time.Now().UTC().Add(leaseDuration), Valid: true},
		LockedBy:    nullableString(leaseOwner),
		ItemImageID: row.ItemImageID,
		Revision:    row.Revision,
	})
	if err != nil {
		return nil, fmt.Errorf("mark annotation mirror processing: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("read annotation mirror claim result: %w", err)
	}
	if affected != 1 {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit annotation mirror recovery: %w", err)
		}
		return nil, nil
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit annotation mirror claim: %w", err)
	}
	return &AnnotationMirrorDelivery{
		ItemImageID: row.ItemImageID,
		Revision:    row.Revision,
		Payload:     row.Payload,
		LeaseOwner:  leaseOwner,
		Attempt:     int(row.AttemptCount) + 1,
		MaxAttempts: int(row.MaxAttempts),
	}, nil
}

func (s *AnnotationStore) CompleteAnnotationMirror(ctx context.Context, delivery AnnotationMirrorDelivery) error {
	result, err := s.q.CompleteAnnotationMirror(ctx, db.CompleteAnnotationMirrorParams{
		ItemImageID: delivery.ItemImageID,
		Revision:    delivery.Revision,
		LockedBy:    nullableString(delivery.LeaseOwner),
	})
	return requireMirrorAffected(result, err)
}

func (s *AnnotationStore) RetryAnnotationMirror(ctx context.Context, delivery AnnotationMirrorDelivery, cause error, nextAttempt time.Time) error {
	message := SafeAnnotationMirrorFailureMessage(cause)
	result, err := s.q.RetryAnnotationMirror(ctx, db.RetryAnnotationMirrorParams{
		NextAttemptAt: nextAttempt.UTC(),
		LastError:     nullableString(message),
		ItemImageID:   delivery.ItemImageID,
		Revision:      delivery.Revision,
		LockedBy:      nullableString(delivery.LeaseOwner),
	})
	return requireMirrorAffected(result, err)
}

func requireMirrorAffected(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrAnnotationMirrorLease
	}
	return nil
}
