package db

// Compatibility wrappers in this file preserve the older store-facing API while
// delegating SQL execution to sqlc-generated queries in transcription_jobs.sql.

import (
	"context"
	"database/sql"
)

type CreateTranscriptionJobParams struct {
	ItemImageID uint64
	ContextID   *uint64
}

func (q *Queries) CreateTranscriptionJob(ctx context.Context, arg CreateTranscriptionJobParams) (uint64, error) {
	contextID, err := compatNullUint64(arg.ContextID)
	if err != nil {
		return 0, err
	}
	res, err := q.CreateTranscriptionJobManual(ctx, CreateTranscriptionJobManualParams{
		ItemImageID: arg.ItemImageID,
		ContextID:   contextID,
	})
	if err != nil {
		return 0, err
	}
	return compatLastInsertID(res)
}

func (q *Queries) GetTranscriptionJob(ctx context.Context, id uint64) (TranscriptionJob, error) {
	return q.GetTranscriptionJobManual(ctx, id)
}

func (q *Queries) ListTranscriptionJobsByItemImage(ctx context.Context, itemImageID uint64) ([]TranscriptionJob, error) {
	return q.ListTranscriptionJobsByItemImageManual(ctx, itemImageID)
}

func (q *Queries) ListTranscriptionJobsByWorkspace(ctx context.Context, workspaceID uint64) ([]TranscriptionJob, error) {
	return q.ListTranscriptionJobsByWorkspaceManual(ctx, workspaceID)
}

func (q *Queries) ClaimNextPendingTranscriptionJob(ctx context.Context) (TranscriptionJob, error) {
	return q.ClaimNextPendingTranscriptionJobManual(ctx)
}

func (q *Queries) MarkTranscriptionJobRunning(ctx context.Context, id uint64) error {
	return q.MarkTranscriptionJobRunningManual(ctx, id)
}

func (q *Queries) SetTranscriptionJobTotalSegments(ctx context.Context, id uint64, lockedBy string, totalSegments int32) error {
	res, err := q.SetTranscriptionJobTotalSegmentsManual(ctx, SetTranscriptionJobTotalSegmentsManualParams{
		ID:            id,
		LockedBy:      compatNullableString(lockedBy),
		TotalSegments: totalSegments,
	})
	return requireAffectedRow(res, err)
}

type UpdateTranscriptionJobProgressParams struct {
	ID                       uint64
	LockedBy                 sql.NullString
	CompletedSegments        int32
	FailedSegments           int32
	CurrentAnnotationID      string
	CurrentAnnotationJSON    string
	LastResultAnnotationJSON string
}

func (q *Queries) UpdateTranscriptionJobProgress(ctx context.Context, arg UpdateTranscriptionJobProgressParams) error {
	res, err := q.UpdateTranscriptionJobProgressManual(ctx, UpdateTranscriptionJobProgressManualParams{
		ID:                       arg.ID,
		LockedBy:                 arg.LockedBy,
		CompletedSegments:        arg.CompletedSegments,
		FailedSegments:           arg.FailedSegments,
		CurrentAnnotationID:      compatNullableString(arg.CurrentAnnotationID),
		CurrentAnnotationJson:    compatNullableString(arg.CurrentAnnotationJSON),
		LastResultAnnotationJson: compatNullableString(arg.LastResultAnnotationJSON),
	})
	return requireAffectedRow(res, err)
}

func (q *Queries) CompleteTranscriptionJob(ctx context.Context, id uint64) error {
	return q.CompleteTranscriptionJobManual(ctx, id)
}

func (q *Queries) FailTranscriptionJob(ctx context.Context, id uint64, errorMessage string) error {
	return q.FailTranscriptionJobManual(ctx, FailTranscriptionJobManualParams{
		ID:           id,
		ErrorMessage: compatNullableString(errorMessage),
	})
}

func (q *Queries) WorkspaceOwnsTranscriptionJob(ctx context.Context, workspaceID, jobID uint64) (bool, error) {
	return q.WorkspaceOwnsTranscriptionJobManual(ctx, WorkspaceOwnsTranscriptionJobManualParams{
		JobID:       jobID,
		WorkspaceID: workspaceID,
	})
}
