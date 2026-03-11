package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type TranscriptionJobStatus string

const (
	TranscriptionJobStatusPending   TranscriptionJobStatus = "pending"
	TranscriptionJobStatusRunning   TranscriptionJobStatus = "running"
	TranscriptionJobStatusCompleted TranscriptionJobStatus = "completed"
	TranscriptionJobStatusFailed    TranscriptionJobStatus = "failed"
)

type TranscriptionJob struct {
	ID                       uint64                 `json:"id"`
	ItemImageID              uint64                 `json:"item_image_id"`
	ContextID                *uint64                `json:"context_id,omitempty"`
	Status                   TranscriptionJobStatus `json:"status"`
	TotalSegments            int                    `json:"total_segments"`
	CompletedSegments        int                    `json:"completed_segments"`
	FailedSegments           int                    `json:"failed_segments"`
	CurrentAnnotationID      string                 `json:"current_annotation_id,omitempty"`
	CurrentAnnotationJSON    string                 `json:"current_annotation_json,omitempty"`
	LastResultAnnotationJSON string                 `json:"last_result_annotation_json,omitempty"`
	ErrorMessage             string                 `json:"error_message,omitempty"`
	CreatedAt                time.Time              `json:"created_at"`
	UpdatedAt                time.Time              `json:"updated_at"`
}

type TranscriptionJobStore struct {
	pool *sql.DB
}

func NewTranscriptionJobStore(pool *sql.DB) *TranscriptionJobStore {
	return &TranscriptionJobStore{pool: pool}
}

func (s *TranscriptionJobStore) Create(ctx context.Context, itemImageID uint64, contextID *uint64) (uint64, error) {
	res, err := s.pool.ExecContext(ctx,
		`INSERT INTO transcription_jobs (item_image_id, context_id, status) VALUES (?, ?, 'pending')`,
		itemImageID, contextID,
	)
	if err != nil {
		return 0, fmt.Errorf("create transcription job: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("get job id: %w", err)
	}
	return uint64(id), nil
}

func (s *TranscriptionJobStore) Get(ctx context.Context, id uint64) (TranscriptionJob, error) {
	row := s.pool.QueryRowContext(ctx, `
		SELECT id, item_image_id, context_id, status,
		       total_segments, completed_segments, failed_segments,
		       COALESCE(current_annotation_id, ''), COALESCE(current_annotation_json, ''),
		       COALESCE(last_result_annotation_json, ''), COALESCE(error_message, ''),
		       created_at, updated_at
		FROM transcription_jobs WHERE id = ?`, id)
	return scanJob(row)
}

func (s *TranscriptionJobStore) ListByItemImage(ctx context.Context, itemImageID uint64) ([]TranscriptionJob, error) {
	rows, err := s.pool.QueryContext(ctx, `
		SELECT id, item_image_id, context_id, status,
		       total_segments, completed_segments, failed_segments,
		       COALESCE(current_annotation_id, ''), COALESCE(current_annotation_json, ''),
		       COALESCE(last_result_annotation_json, ''), COALESCE(error_message, ''),
		       created_at, updated_at
		FROM transcription_jobs WHERE item_image_id = ?
		ORDER BY created_at DESC`, itemImageID)
	if err != nil {
		return nil, fmt.Errorf("list transcription jobs: %w", err)
	}
	defer rows.Close()
	var jobs []TranscriptionJob
	for rows.Next() {
		j, err := scanJobRow(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// ClaimNextPending atomically claims the oldest pending job, marking it as
// running. Returns nil when no pending jobs exist.
func (s *TranscriptionJobStore) ClaimNextPending(ctx context.Context) (*TranscriptionJob, error) {
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var id uint64
	err = tx.QueryRowContext(ctx,
		`SELECT id FROM transcription_jobs WHERE status = 'pending' ORDER BY created_at ASC LIMIT 1 FOR UPDATE`,
	).Scan(&id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query pending job: %w", err)
	}

	_, err = tx.ExecContext(ctx,
		`UPDATE transcription_jobs SET status = 'running', updated_at = NOW() WHERE id = ?`, id)
	if err != nil {
		return nil, fmt.Errorf("claim job: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit claim: %w", err)
	}

	job, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return &job, nil
}

// SetTotalSegments sets the total segment count at the start of a job run.
func (s *TranscriptionJobStore) SetTotalSegments(ctx context.Context, id uint64, total int) error {
	_, err := s.pool.ExecContext(ctx,
		`UPDATE transcription_jobs SET total_segments = ?, updated_at = NOW() WHERE id = ?`, total, id)
	return err
}

// UpdateProgress records per-segment progress after each annotation is processed.
func (s *TranscriptionJobStore) UpdateProgress(ctx context.Context, id uint64,
	completed, failed int,
	currentAnnotationID, currentAnnotationJSON, lastResultAnnotationJSON string,
) error {
	_, err := s.pool.ExecContext(ctx, `
		UPDATE transcription_jobs
		SET completed_segments        = ?,
		    failed_segments           = ?,
		    current_annotation_id     = ?,
		    current_annotation_json   = ?,
		    last_result_annotation_json = ?,
		    updated_at                = NOW()
		WHERE id = ?`,
		completed, failed,
		currentAnnotationID, currentAnnotationJSON, lastResultAnnotationJSON,
		id,
	)
	return err
}

// Complete marks the job as completed and clears the current-segment fields.
func (s *TranscriptionJobStore) Complete(ctx context.Context, id uint64) error {
	_, err := s.pool.ExecContext(ctx, `
		UPDATE transcription_jobs
		SET status = 'completed', current_annotation_id = NULL, current_annotation_json = NULL, updated_at = NOW()
		WHERE id = ?`, id)
	return err
}

// Fail marks the job as failed with an error message.
func (s *TranscriptionJobStore) Fail(ctx context.Context, id uint64, errMsg string) error {
	_, err := s.pool.ExecContext(ctx, `
		UPDATE transcription_jobs
		SET status = 'failed', error_message = ?, current_annotation_id = NULL, current_annotation_json = NULL, updated_at = NOW()
		WHERE id = ?`, errMsg, id)
	return err
}

// --- scanner helpers ---

type rowScanner interface {
	Scan(dest ...any) error
}

func scanJob(row rowScanner) (TranscriptionJob, error) {
	var j TranscriptionJob
	var contextID sql.NullInt64
	err := row.Scan(
		&j.ID, &j.ItemImageID, &contextID, &j.Status,
		&j.TotalSegments, &j.CompletedSegments, &j.FailedSegments,
		&j.CurrentAnnotationID, &j.CurrentAnnotationJSON,
		&j.LastResultAnnotationJSON, &j.ErrorMessage,
		&j.CreatedAt, &j.UpdatedAt,
	)
	if err != nil {
		return j, fmt.Errorf("scan transcription job: %w", err)
	}
	if contextID.Valid {
		v := uint64(contextID.Int64)
		j.ContextID = &v
	}
	return j, nil
}

func scanJobRow(rows *sql.Rows) (TranscriptionJob, error) {
	var j TranscriptionJob
	var contextID sql.NullInt64
	err := rows.Scan(
		&j.ID, &j.ItemImageID, &contextID, &j.Status,
		&j.TotalSegments, &j.CompletedSegments, &j.FailedSegments,
		&j.CurrentAnnotationID, &j.CurrentAnnotationJSON,
		&j.LastResultAnnotationJSON, &j.ErrorMessage,
		&j.CreatedAt, &j.UpdatedAt,
	)
	if err != nil {
		return j, fmt.Errorf("scan transcription job row: %w", err)
	}
	if contextID.Valid {
		v := uint64(contextID.Int64)
		j.ContextID = &v
	}
	return j, nil
}
