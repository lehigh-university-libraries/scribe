-- name: InsertEventOutboxManual :exec
INSERT IGNORE INTO event_outbox (event_id, event_type, workspace_id, subject, body_json)
VALUES (
  sqlc.arg(event_id),
  sqlc.arg(event_type),
  sqlc.narg(workspace_id),
  sqlc.narg(subject),
  sqlc.arg(body_json)
);

-- name: GetWorkspaceIDForItemImageManual :one
SELECT i.workspace_id
FROM item_images ii
JOIN items i ON i.id = ii.item_id
WHERE ii.id = sqlc.arg(item_image_id)
LIMIT 1;

-- name: InsertWebhookDeliveryIfMissingManual :exec
INSERT IGNORE INTO webhook_deliveries (
  event_id,
  target_url,
  target_hash,
  status
) VALUES (
  sqlc.arg(event_id),
  sqlc.arg(target_url),
  sqlc.arg(target_hash),
  'pending'
);

-- name: ClaimWebhookDeliveriesManual :many
SELECT
  wd.id,
  wd.event_id,
  eo.event_type,
  eo.subject,
  eo.body_json,
  wd.target_url,
  wd.locked_by,
  wd.attempt_count,
  wd.max_attempts
FROM webhook_deliveries wd
JOIN event_outbox eo ON eo.event_id = wd.event_id
WHERE (
    wd.status = 'pending'
    AND (wd.next_attempt_at IS NULL OR wd.next_attempt_at <= NOW())
  ) OR (
    wd.status = 'processing'
    AND wd.lease_until IS NOT NULL
    AND wd.lease_until < NOW()
    AND wd.attempt_count < wd.max_attempts
  )
ORDER BY wd.created_at ASC
LIMIT ?
FOR UPDATE SKIP LOCKED;

-- name: MarkWebhookDeliveryProcessingManual :execresult
UPDATE webhook_deliveries
SET
  status = 'processing',
  lease_until = sqlc.arg(lease_until),
  locked_by = sqlc.arg(locked_by),
  updated_at = NOW()
WHERE id = sqlc.arg(id);

-- name: DeleteDeliveredWebhookDeliveriesBeforeManual :exec
DELETE FROM webhook_deliveries
WHERE status = 'delivered'
  AND updated_at < sqlc.arg(cutoff);

-- name: DeleteOrphanedEventOutboxBeforeManual :exec
DELETE eo
FROM event_outbox eo
LEFT JOIN webhook_deliveries wd ON wd.event_id = eo.event_id
WHERE eo.created_at < sqlc.arg(cutoff)
  AND wd.id IS NULL;

-- name: DeleteEventOutboxBeforeManual :exec
DELETE FROM event_outbox
WHERE created_at < sqlc.arg(cutoff);

-- name: MarkWebhookDeliveryDeliveredManual :execresult
UPDATE webhook_deliveries
SET status = 'delivered', lease_until = NULL, locked_by = NULL, last_error = NULL, updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND locked_by = sqlc.arg(locked_by)
  AND status = 'processing';

-- name: MarkWebhookDeliveryFailedManual :execresult
UPDATE webhook_deliveries
SET
  attempt_count = attempt_count + 1,
  status = CASE WHEN attempt_count + 1 >= max_attempts THEN 'failed' ELSE 'pending' END,
  next_attempt_at = CASE WHEN attempt_count + 1 >= max_attempts THEN NULL ELSE DATE_ADD(NOW(), INTERVAL LEAST(900, POW(2, attempt_count + 1) * 5) SECOND) END,
  lease_until = NULL,
  locked_by = NULL,
  last_error = sqlc.narg(last_error),
  updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND locked_by = sqlc.arg(locked_by)
  AND status = 'processing';

-- name: GetEventOutboxHighWaterManual :one
SELECT COALESCE(MAX(id), 0) AS high_water_id
FROM event_outbox;

-- name: ListEventOutboxAfterIDManual :many
SELECT
  id,
  event_id,
  event_type,
  workspace_id,
  subject,
  body_json,
  created_at
FROM event_outbox
WHERE id > sqlc.arg(after_id)
ORDER BY id ASC
LIMIT ?;

-- name: ListEventOutboxAfterIDForWorkspaceManual :many
SELECT
  id,
  event_id,
  event_type,
  workspace_id,
  subject,
  body_json,
  created_at
FROM event_outbox
WHERE id > sqlc.arg(after_id)
  AND (workspace_id IS NULL OR workspace_id = sqlc.arg(workspace_id))
ORDER BY id ASC
LIMIT ?;
