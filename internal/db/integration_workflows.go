package db

// Compatibility wrappers in this file preserve concise store-facing methods
// while delegating SQL execution to sqlc-generated queries.

import (
	"context"
	"database/sql"
	"time"
)

func (q *Queries) SelectExternalRequestForUpdate(ctx context.Context, arg SelectExternalRequestForUpdateManualParams) (SelectExternalRequestForUpdateManualRow, error) {
	return q.SelectExternalRequestForUpdateManual(ctx, arg)
}

func (q *Queries) InsertExternalRequest(ctx context.Context, arg InsertExternalRequestManualParams) (uint64, error) {
	res, err := q.InsertExternalRequestManual(ctx, arg)
	if err != nil {
		return 0, err
	}
	return compatLastInsertID(res)
}

func (q *Queries) ReclaimExternalRequest(ctx context.Context, arg ReclaimExternalRequestManualParams) error {
	return q.ReclaimExternalRequestManual(ctx, arg)
}

func (q *Queries) CompleteExternalRequest(ctx context.Context, arg CompleteExternalRequestManualParams) error {
	return q.CompleteExternalRequestManual(ctx, arg)
}

func (q *Queries) FailExternalRequest(ctx context.Context, arg FailExternalRequestManualParams) error {
	return q.FailExternalRequestManual(ctx, arg)
}

func (q *Queries) ClaimNextLeasedTranscriptionJob(ctx context.Context, leaseUntil time.Time, lockedBy string) (TranscriptionJob, error) {
	row, err := q.ClaimNextLeasedTranscriptionJobManual(ctx)
	if err != nil {
		return TranscriptionJob{}, err
	}
	res, err := q.MarkTranscriptionJobLeasedManual(ctx, MarkTranscriptionJobLeasedManualParams{
		LeaseUntil: sql.NullTime{Time: leaseUntil, Valid: true},
		LockedBy:   compatNullableString(lockedBy),
		ID:         row.ID,
	})
	if err := requireAffectedRow(res, err); err != nil {
		return TranscriptionJob{}, err
	}
	return leasedRowToTranscriptionJob(row), nil
}

func (q *Queries) ClaimNextLeasedTranscriptionJobOlderThan(ctx context.Context, cutoff, leaseUntil time.Time, lockedBy string) (TranscriptionJob, error) {
	row, err := q.ClaimNextLeasedTranscriptionJobOlderThanManual(ctx, cutoff)
	if err != nil {
		return TranscriptionJob{}, err
	}
	res, err := q.MarkTranscriptionJobLeasedManual(ctx, MarkTranscriptionJobLeasedManualParams{
		LeaseUntil: sql.NullTime{Time: leaseUntil, Valid: true},
		LockedBy:   compatNullableString(lockedBy),
		ID:         row.ID,
	})
	if err := requireAffectedRow(res, err); err != nil {
		return TranscriptionJob{}, err
	}
	return row, nil
}

func (q *Queries) ClaimLeasedTranscriptionJobByID(ctx context.Context, id uint64, leaseUntil time.Time, lockedBy string) (TranscriptionJob, error) {
	row, err := q.ClaimLeasedTranscriptionJobByIDManual(ctx, id)
	if err != nil {
		return TranscriptionJob{}, err
	}
	res, err := q.MarkTranscriptionJobLeasedManual(ctx, MarkTranscriptionJobLeasedManualParams{
		LeaseUntil: sql.NullTime{Time: leaseUntil, Valid: true},
		LockedBy:   compatNullableString(lockedBy),
		ID:         row.ID,
	})
	if err := requireAffectedRow(res, err); err != nil {
		return TranscriptionJob{}, err
	}
	return leasedByIDRowToTranscriptionJob(row), nil
}

func (q *Queries) CompleteTranscriptionJobLeased(ctx context.Context, id uint64, lockedBy string) error {
	res, err := q.CompleteTranscriptionJobLeasedManual(ctx, CompleteTranscriptionJobLeasedManualParams{
		ID:       id,
		LockedBy: compatNullableString(lockedBy),
	})
	return requireAffectedRow(res, err)
}

func (q *Queries) ExtendTranscriptionJobLease(ctx context.Context, id uint64, lockedBy string, leaseUntil time.Time) error {
	res, err := q.ExtendTranscriptionJobLeaseManual(ctx, ExtendTranscriptionJobLeaseManualParams{
		LeaseUntil: sql.NullTime{Time: leaseUntil, Valid: true},
		ID:         id,
		LockedBy:   compatNullableString(lockedBy),
	})
	return requireAffectedRow(res, err)
}

func (q *Queries) RetryOrFailTranscriptionJob(ctx context.Context, id uint64, lockedBy string, errorMessage sql.NullString) error {
	res, err := q.RetryOrFailTranscriptionJobManual(ctx, RetryOrFailTranscriptionJobManualParams{
		ErrorMessage: errorMessage,
		ID:           id,
		LockedBy:     compatNullableString(lockedBy),
	})
	return requireAffectedRow(res, err)
}

func (q *Queries) DeferTranscriptionJobLease(ctx context.Context, id uint64, lockedBy string, retryAfter time.Time, errorMessage sql.NullString) error {
	res, err := q.DeferTranscriptionJobLeaseManual(ctx, DeferTranscriptionJobLeaseManualParams{
		RetryAfter:   sql.NullTime{Time: retryAfter, Valid: true},
		ErrorMessage: errorMessage,
		ID:           id,
		LockedBy:     compatNullableString(lockedBy),
	})
	return requireAffectedRow(res, err)
}

func (q *Queries) InsertEventOutbox(ctx context.Context, eventID, eventType string, workspaceID sql.NullInt64, subject sql.NullString, bodyJSON string) error {
	return q.InsertEventOutboxManual(ctx, InsertEventOutboxManualParams{
		EventID:     eventID,
		EventType:   eventType,
		WorkspaceID: workspaceID,
		Subject:     subject,
		BodyJson:    bodyJSON,
	})
}

func (q *Queries) InsertWebhookDeliveryIfMissing(ctx context.Context, eventID, targetURL, targetHash string) error {
	return q.InsertWebhookDeliveryIfMissingManual(ctx, InsertWebhookDeliveryIfMissingManualParams{
		EventID:    eventID,
		TargetUrl:  targetURL,
		TargetHash: targetHash,
	})
}

func (q *Queries) ClaimWebhookDeliveries(ctx context.Context, limit int, leaseUntil time.Time, lockedBy string) ([]ClaimWebhookDeliveriesManualRow, error) {
	convertedLimit, err := compatInt32(limit)
	if err != nil {
		return nil, err
	}
	rows, err := q.ClaimWebhookDeliveriesManual(ctx, convertedLimit)
	if err != nil {
		return nil, err
	}
	for i, row := range rows {
		res, err := q.MarkWebhookDeliveryProcessingManual(ctx, MarkWebhookDeliveryProcessingManualParams{
			LeaseUntil: sql.NullTime{Time: leaseUntil, Valid: true},
			LockedBy:   compatNullableString(lockedBy),
			ID:         row.ID,
		})
		if err := requireAffectedRow(res, err); err != nil {
			return nil, err
		}
		rows[i].LockedBy = compatNullableString(lockedBy)
	}
	return rows, nil
}

func (q *Queries) MarkWebhookDeliveryDelivered(ctx context.Context, id uint64, lockedBy string) error {
	res, err := q.MarkWebhookDeliveryDeliveredManual(ctx, MarkWebhookDeliveryDeliveredManualParams{
		ID:       id,
		LockedBy: compatNullableString(lockedBy),
	})
	return requireAffectedRow(res, err)
}

func (q *Queries) MarkWebhookDeliveryFailed(ctx context.Context, id uint64, lockedBy string, lastError sql.NullString) error {
	res, err := q.MarkWebhookDeliveryFailedManual(ctx, MarkWebhookDeliveryFailedManualParams{
		LastError: lastError,
		LockedBy:  compatNullableString(lockedBy),
		ID:        id,
	})
	return requireAffectedRow(res, err)
}

func (q *Queries) GetEventOutboxHighWater(ctx context.Context) (uint64, error) {
	raw, err := q.GetEventOutboxHighWaterManual(ctx)
	if err != nil {
		return 0, err
	}
	converted, err := compatInt64(raw)
	if err != nil {
		return 0, err
	}
	return compatUint64FromInt64(converted)
}

func (q *Queries) ListEventOutboxAfterID(ctx context.Context, afterID uint64, limit int) ([]EventOutbox, error) {
	convertedLimit, err := compatInt32(limit)
	if err != nil {
		return nil, err
	}
	return q.ListEventOutboxAfterIDManual(ctx, ListEventOutboxAfterIDManualParams{
		AfterID: afterID,
		Limit:   convertedLimit,
	})
}

func (q *Queries) ListEventOutboxAfterIDForWorkspace(ctx context.Context, afterID, workspaceID uint64, limit int) ([]EventOutbox, error) {
	convertedLimit, err := compatInt32(limit)
	if err != nil {
		return nil, err
	}
	convertedWorkspaceID, err := compatUint64ToInt64(workspaceID)
	if err != nil {
		return nil, err
	}
	return q.ListEventOutboxAfterIDForWorkspaceManual(ctx, ListEventOutboxAfterIDForWorkspaceManualParams{
		AfterID:     afterID,
		WorkspaceID: sql.NullInt64{Int64: convertedWorkspaceID, Valid: true},
		Limit:       convertedLimit,
	})
}

func leasedRowToTranscriptionJob(row TranscriptionJob) TranscriptionJob {
	return row
}

func leasedByIDRowToTranscriptionJob(row TranscriptionJob) TranscriptionJob {
	return row
}

func requireAffectedRow(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	if res == nil {
		return nil
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}
