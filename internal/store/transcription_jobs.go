package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	db "github.com/lehigh-university-libraries/scribe/internal/db"
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
	q    *db.Queries
	pool *sql.DB
}

func NewTranscriptionJobStore(pool *sql.DB) *TranscriptionJobStore {
	return &TranscriptionJobStore{q: db.New(pool), pool: pool}
}

func (s *TranscriptionJobStore) Create(ctx context.Context, itemImageID uint64, contextID *uint64) (uint64, error) {
	id, err := s.q.CreateTranscriptionJob(ctx, db.CreateTranscriptionJobParams{
		ItemImageID: itemImageID,
		ContextID:   contextID,
	})
	if err != nil {
		return 0, fmt.Errorf("create transcription job: %w", err)
	}
	return id, nil
}

func (s *TranscriptionJobStore) Get(ctx context.Context, id uint64) (TranscriptionJob, error) {
	row, err := s.q.GetTranscriptionJob(ctx, id)
	if err != nil {
		return TranscriptionJob{}, fmt.Errorf("get transcription job: %w", err)
	}
	return rowToTranscriptionJob(row), nil
}

func (s *TranscriptionJobStore) ListByItemImage(ctx context.Context, itemImageID uint64) ([]TranscriptionJob, error) {
	rows, err := s.q.ListTranscriptionJobsByItemImage(ctx, itemImageID)
	if err != nil {
		return nil, fmt.Errorf("list transcription jobs: %w", err)
	}
	jobs := make([]TranscriptionJob, 0, len(rows))
	for _, row := range rows {
		jobs = append(jobs, rowToTranscriptionJob(row))
	}
	return jobs, nil
}

func (s *TranscriptionJobStore) ListByWorkspace(ctx context.Context, workspaceID uint64) ([]TranscriptionJob, error) {
	rows, err := s.q.ListTranscriptionJobsByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list transcription jobs by workspace: %w", err)
	}
	jobs := make([]TranscriptionJob, 0, len(rows))
	for _, row := range rows {
		jobs = append(jobs, rowToTranscriptionJob(row))
	}
	return jobs, nil
}

// ClaimNextPending atomically claims the oldest pending job, marking it as
// running. Returns nil when no pending jobs exist.
func (s *TranscriptionJobStore) ClaimNextPending(ctx context.Context) (*TranscriptionJob, error) {
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	qtx := s.q.WithTx(tx)
	job, err := qtx.ClaimNextPendingTranscriptionJob(ctx)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query pending job: %w", err)
	}

	if err := qtx.MarkTranscriptionJobRunning(ctx, job.ID); err != nil {
		return nil, fmt.Errorf("claim job: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit claim: %w", err)
	}

	claimed, err := s.Get(ctx, job.ID)
	if err != nil {
		return nil, err
	}
	return &claimed, nil
}

// SetTotalSegments sets the total segment count at the start of a job run.
func (s *TranscriptionJobStore) SetTotalSegments(ctx context.Context, id uint64, total int) error {
	return s.q.SetTranscriptionJobTotalSegments(ctx, id, int32(total))
}

// UpdateProgress records per-segment progress after each annotation is processed.
func (s *TranscriptionJobStore) UpdateProgress(ctx context.Context, id uint64,
	completed, failed int,
	currentAnnotationID, currentAnnotationJSON, lastResultAnnotationJSON string,
) error {
	return s.q.UpdateTranscriptionJobProgress(ctx, db.UpdateTranscriptionJobProgressParams{
		ID:                       id,
		CompletedSegments:        int32(completed),
		FailedSegments:           int32(failed),
		CurrentAnnotationID:      currentAnnotationID,
		CurrentAnnotationJSON:    currentAnnotationJSON,
		LastResultAnnotationJSON: lastResultAnnotationJSON,
	})
}

// Complete marks the job as completed and clears the current-segment fields.
func (s *TranscriptionJobStore) Complete(ctx context.Context, id uint64) error {
	return s.q.CompleteTranscriptionJob(ctx, id)
}

// Fail marks the job as failed with an error message.
func (s *TranscriptionJobStore) Fail(ctx context.Context, id uint64, errMsg string) error {
	return s.q.FailTranscriptionJob(ctx, id, errMsg)
}

func (s *TranscriptionJobStore) WorkspaceOwnsJob(ctx context.Context, workspaceID, jobID uint64) (bool, error) {
	return s.q.WorkspaceOwnsTranscriptionJob(ctx, workspaceID, jobID)
}

func rowToTranscriptionJob(row db.TranscriptionJob) TranscriptionJob {
	var j TranscriptionJob
	j.ID = row.ID
	j.ItemImageID = row.ItemImageID
	j.Status = TranscriptionJobStatus(row.Status)
	j.TotalSegments = int(row.TotalSegments)
	j.CompletedSegments = int(row.CompletedSegments)
	j.FailedSegments = int(row.FailedSegments)
	j.CreatedAt = row.CreatedAt
	j.UpdatedAt = row.UpdatedAt
	if row.ContextID.Valid {
		v := uint64(row.ContextID.Int64)
		j.ContextID = &v
	}
	if row.CurrentAnnotationID.Valid {
		j.CurrentAnnotationID = row.CurrentAnnotationID.String
	}
	if row.CurrentAnnotationJson.Valid {
		j.CurrentAnnotationJSON = row.CurrentAnnotationJson.String
	}
	if row.LastResultAnnotationJson.Valid {
		j.LastResultAnnotationJSON = row.LastResultAnnotationJson.String
	}
	if row.ErrorMessage.Valid {
		j.ErrorMessage = row.ErrorMessage.String
	}
	return j
}
