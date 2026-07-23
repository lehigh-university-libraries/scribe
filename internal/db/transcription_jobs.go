package db

// Store query adapters in this file are the sole mapping boundary from
// domain-shaped job values to sqlc-generated queries in transcription_jobs.sql.

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

type CreateTranscriptionJobParams struct {
	ItemImageID     uint64
	ContextID       *uint64
	ContextSnapshot json.RawMessage
}

func (q *Queries) CreateTranscriptionJob(ctx context.Context, arg CreateTranscriptionJobParams) (uint64, error) {
	contextID, err := nullUint64(arg.ContextID)
	if err != nil {
		return 0, err
	}
	res, err := q.CreateTranscriptionJobManual(ctx, CreateTranscriptionJobManualParams{
		ItemImageID:     arg.ItemImageID,
		ContextID:       contextID,
		ContextSnapshot: arg.ContextSnapshot,
	})
	if err != nil {
		return 0, err
	}
	id, err := lastInsertID(res)
	if err != nil {
		return 0, err
	}
	if id == 0 {
		return 0, sql.ErrNoRows
	}
	return id, nil
}

func (q *Queries) GetTranscriptionJob(ctx context.Context, id uint64) (TranscriptionJob, error) {
	return q.GetTranscriptionJobManual(ctx, id)
}

func (q *Queries) GetActiveTranscriptionJobByItemImage(ctx context.Context, itemImageID uint64) (TranscriptionJob, error) {
	value, err := nullUint64(&itemImageID)
	if err != nil {
		return TranscriptionJob{}, err
	}
	return q.GetActiveTranscriptionJobByItemImageManual(ctx, value)
}

// TranscriptionJobPageParams identifies a bounded tenant-scoped keyset page.
type TranscriptionJobPageParams struct {
	WorkspaceID     uint64
	ItemImageID     uint64
	CursorCreatedAt sql.NullTime
	CursorID        uint64
	PageLimit       int32
}

// TranscriptionJobSummary is the scalar-only list projection. Large context
// and annotation payloads are available from the point Get operation only.
type TranscriptionJobSummary struct {
	ID                  uint64
	WorkspaceID         uint64
	ItemImageID         uint64
	ContextID           sql.NullInt64
	ContextScopeID      sql.NullInt64
	InputRevision       uint64
	Status              TranscriptionJobsStatus
	TotalSegments       int32
	CompletedSegments   int32
	FailedSegments      int32
	AttemptCount        int32
	MaxAttempts         int32
	CurrentAnnotationID sql.NullString
	ErrorMessage        sql.NullString
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// ListTranscriptionJobsPage selects the index-matched workspace or image query
// rather than forcing the database through an optional-filter OR.
func (q *Queries) ListTranscriptionJobsPage(ctx context.Context, arg TranscriptionJobPageParams) ([]TranscriptionJobSummary, error) {
	if arg.ItemImageID == 0 {
		rows, err := q.ListTranscriptionJobsByWorkspacePageManual(ctx, ListTranscriptionJobsByWorkspacePageManualParams{
			WorkspaceID:     arg.WorkspaceID,
			CursorCreatedAt: arg.CursorCreatedAt,
			CursorID:        arg.CursorID,
			Limit:           arg.PageLimit,
		})
		if err != nil {
			return nil, err
		}
		out := make([]TranscriptionJobSummary, 0, len(rows))
		for _, row := range rows {
			out = append(out, transcriptionJobSummaryFromWorkspaceRow(row))
		}
		return out, nil
	}
	rows, err := q.ListTranscriptionJobsByItemImagePageManual(ctx, ListTranscriptionJobsByItemImagePageManualParams{
		WorkspaceID:     arg.WorkspaceID,
		ItemImageID:     arg.ItemImageID,
		CursorCreatedAt: arg.CursorCreatedAt,
		CursorID:        arg.CursorID,
		Limit:           arg.PageLimit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]TranscriptionJobSummary, 0, len(rows))
	for _, row := range rows {
		out = append(out, transcriptionJobSummaryFromItemImageRow(row))
	}
	return out, nil
}

func transcriptionJobSummaryFromWorkspaceRow(row ListTranscriptionJobsByWorkspacePageManualRow) TranscriptionJobSummary {
	return TranscriptionJobSummary(row)
}

func transcriptionJobSummaryFromItemImageRow(row ListTranscriptionJobsByItemImagePageManualRow) TranscriptionJobSummary {
	return TranscriptionJobSummary(row)
}

func (q *Queries) CancelTranscriptionJob(ctx context.Context, id uint64) error {
	res, err := q.CancelTranscriptionJobManual(ctx, id)
	return requireAffectedRow(res, err)
}

func (q *Queries) WorkspaceOwnsTranscriptionJob(ctx context.Context, workspaceID, jobID uint64) (bool, error) {
	return q.WorkspaceOwnsTranscriptionJobManual(ctx, WorkspaceOwnsTranscriptionJobManualParams{
		JobID:       jobID,
		WorkspaceID: workspaceID,
	})
}
