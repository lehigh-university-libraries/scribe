package db

// Workflow query adapters in this file are the sole mapping boundary from
// application-shaped operations to sqlc-generated statements.

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
	return lastInsertID(res)
}

func (q *Queries) ReclaimExternalRequest(ctx context.Context, arg ReclaimExternalRequestManualParams) error {
	return q.ReclaimExternalRequestManual(ctx, arg)
}

func (q *Queries) CompleteExternalRequest(ctx context.Context, arg CompleteExternalRequestManualParams) error {
	res, err := q.CompleteExternalRequestManual(ctx, arg)
	return requireAffectedRow(res, err)
}

func (q *Queries) FailExternalRequest(ctx context.Context, arg FailExternalRequestManualParams) error {
	res, err := q.FailExternalRequestManual(ctx, arg)
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
	convertedLimit, err := intToInt32(limit)
	if err != nil {
		return nil, err
	}
	if err := q.FailExpiredExhaustedWebhookDeliveriesManual(ctx); err != nil {
		return nil, err
	}
	rows, err := q.ClaimWebhookDeliveriesManual(ctx, convertedLimit)
	if err != nil {
		return nil, err
	}
	for i, row := range rows {
		res, err := q.MarkWebhookDeliveryProcessingManual(ctx, MarkWebhookDeliveryProcessingManualParams{
			LeaseUntil: sql.NullTime{Time: leaseUntil, Valid: true},
			LockedBy:   nullableString(lockedBy),
			ID:         row.ID,
		})
		if err := requireAffectedRow(res, err); err != nil {
			return nil, err
		}
		rows[i].LockedBy = nullableString(lockedBy)
		rows[i].AttemptCount++
	}
	return rows, nil
}

func (q *Queries) MarkWebhookDeliveryDelivered(ctx context.Context, id uint64, lockedBy string) error {
	res, err := q.MarkWebhookDeliveryDeliveredManual(ctx, MarkWebhookDeliveryDeliveredManualParams{
		ID:       id,
		LockedBy: nullableString(lockedBy),
	})
	return requireAffectedRow(res, err)
}

func (q *Queries) MarkWebhookDeliveryFailed(ctx context.Context, id uint64, lockedBy string, lastError sql.NullString) error {
	res, err := q.MarkWebhookDeliveryFailedManual(ctx, MarkWebhookDeliveryFailedManualParams{
		LastError: lastError,
		LockedBy:  nullableString(lockedBy),
		ID:        id,
	})
	return requireAffectedRow(res, err)
}

func (q *Queries) GetEventOutboxHighWater(ctx context.Context) (uint64, error) {
	raw, err := q.GetEventOutboxHighWaterManual(ctx)
	if err != nil {
		return 0, err
	}
	converted, err := scanInt64(raw)
	if err != nil {
		return 0, err
	}
	return uint64FromInt64(converted)
}

func (q *Queries) GetEventOutboxHighWaterForWorkspace(ctx context.Context, workspaceID uint64) (uint64, error) {
	convertedWorkspaceID, err := uint64ToInt64(workspaceID)
	if err != nil {
		return 0, err
	}
	raw, err := q.GetEventOutboxHighWaterForWorkspaceManual(ctx, sql.NullInt64{Int64: convertedWorkspaceID, Valid: true})
	if err != nil {
		return 0, err
	}
	converted, err := scanInt64(raw)
	if err != nil {
		return 0, err
	}
	return uint64FromInt64(converted)
}

func (q *Queries) ListEventOutboxAfterID(ctx context.Context, afterID uint64, limit int) ([]EventOutbox, error) {
	convertedLimit, err := intToInt32(limit)
	if err != nil {
		return nil, err
	}
	return q.ListEventOutboxAfterIDManual(ctx, ListEventOutboxAfterIDManualParams{
		AfterID: afterID,
		Limit:   convertedLimit,
	})
}

func (q *Queries) ListEventOutboxAfterIDForWorkspace(ctx context.Context, afterID, workspaceID uint64, limit int) ([]EventOutbox, error) {
	convertedLimit, err := intToInt32(limit)
	if err != nil {
		return nil, err
	}
	convertedWorkspaceID, err := uint64ToInt64(workspaceID)
	if err != nil {
		return nil, err
	}
	return q.ListEventOutboxAfterIDForWorkspaceManual(ctx, ListEventOutboxAfterIDForWorkspaceManualParams{
		AfterID:     afterID,
		WorkspaceID: sql.NullInt64{Int64: convertedWorkspaceID, Valid: true},
		Limit:       convertedLimit,
	})
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
