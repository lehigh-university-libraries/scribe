package store

import (
	"context"
	"database/sql"
	"errors"

	db "github.com/lehigh-university-libraries/scribe/internal/db"
)

// Checkpoint is private job state, scoped to the immutable input revision and
// context. It survives retries but never becomes a canonical page on its own.
func (s *TranscriptionJobStore) Checkpoint(ctx context.Context, fence TranscriptionAttemptFence) (string, error) {
	attempt, err := transcriptionAttemptNumberToDB(fence)
	if err != nil {
		return "", err
	}
	value, err := s.q.GetTranscriptionCheckpoint(ctx, db.GetTranscriptionCheckpointParams{
		ID: fence.JobID, AttemptNumber: attempt, InputRevision: fence.InputRevision,
		LeaseToken: nullableString(fence.LeaseToken),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrTranscriptionJobFence
	}
	return value.String, err
}

func (s *TranscriptionJobStore) withCheckpointLease(ctx context.Context, fence TranscriptionAttemptFence, operation func(*db.Queries) error) error {
	attempt, err := transcriptionAttemptNumberToDB(fence)
	if err != nil {
		return err
	}
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.q.WithTx(tx)
	if _, err := queries.LockActiveTranscriptionJobLeaseManual(ctx, db.LockActiveTranscriptionJobLeaseManualParams{
		ID: fence.JobID, AttemptNumber: attempt, InputRevision: fence.InputRevision, LeaseToken: nullableString(fence.LeaseToken),
	}); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrTranscriptionJobFence
		}
		return err
	}
	if err := operation(queries); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *TranscriptionJobStore) SaveCheckpoint(ctx context.Context, fence TranscriptionAttemptFence, lineID, payload string) error {
	return s.withCheckpointLease(ctx, fence, func(q *db.Queries) error {
		return q.SaveTranscriptionCheckpoint(ctx, db.SaveTranscriptionCheckpointParams{JobID: fence.JobID, LineID: []byte(lineID), Payload: payload})
	})
}

func (s *TranscriptionJobStore) ClearCheckpoints(ctx context.Context, fence TranscriptionAttemptFence) error {
	return s.withCheckpointLease(ctx, fence, func(q *db.Queries) error { return q.DeleteTranscriptionCheckpoints(ctx, fence.JobID) })
}

// LineCheckpoints reads results only for an active fenced attempt, never as
// part of job status or queue polling.
func (s *TranscriptionJobStore) LineCheckpoints(ctx context.Context, fence TranscriptionAttemptFence) (map[string]string, error) {
	var results map[string]string
	err := s.withCheckpointLease(ctx, fence, func(q *db.Queries) error {
		rows, err := q.ListTranscriptionLineCheckpoints(ctx, fence.JobID)
		if err != nil {
			return err
		}
		results = make(map[string]string, len(rows))
		for _, row := range rows {
			results[string(row.LineID)] = row.Payload
		}
		return nil
	})
	return results, err
}
