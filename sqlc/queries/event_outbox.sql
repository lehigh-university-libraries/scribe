-- name: InsertEventOutboxManual :exec
INSERT IGNORE INTO event_outbox (event_id, event_type, workspace_id, subject, body_json)
SELECT
  sqlc.arg(event_id),
  sqlc.arg(event_type),
  sqlc.narg(workspace_id),
  sqlc.narg(subject),
  sqlc.arg(body_json)
FROM (SELECT 1 AS seed) seed
LEFT JOIN workspaces w ON w.id = sqlc.narg(workspace_id)
WHERE sqlc.narg(workspace_id) IS NULL OR w.id IS NOT NULL;

-- name: GetWorkspaceIDForItemImageManual :one
SELECT i.workspace_id
FROM item_images ii
JOIN items i ON i.id = ii.item_id
WHERE ii.id = sqlc.arg(item_image_id)
LIMIT 1;

-- name: InsertWorkspaceWebhookDeliveriesManual :exec
INSERT IGNORE INTO webhook_deliveries (
  event_id,
  subscription_id,
  status
) SELECT
  eo.event_id,
  subscription.id,
  'pending'
FROM event_outbox eo
JOIN webhook_subscriptions subscription
  ON subscription.workspace_id = eo.workspace_id
WHERE eo.event_id = sqlc.arg(event_id);

-- name: LockWebhookDeliveryExpansionWorkspaceManual :one
-- Subscription create/delete and event expansion all lock this workspace row.
-- That makes the repository-owned parent/child lifecycle deterministic even
-- when a subscription is deleted while an event transaction is committing.
SELECT workspace.id
FROM event_outbox event
JOIN workspaces workspace ON workspace.id = event.workspace_id
WHERE event.event_id = sqlc.arg(event_id)
LIMIT 1
FOR UPDATE;

-- name: ClaimWebhookDeliveriesManual :many
SELECT
  wd.id,
  wd.event_id,
  eo.event_type,
  eo.subject,
  eo.body_json,
  wd.subscription_id,
  subscription.target_url,
  subscription.signing_secret,
  wd.locked_by,
  wd.attempt_count,
  wd.max_attempts
FROM webhook_deliveries wd
JOIN event_outbox eo ON eo.event_id = wd.event_id
JOIN webhook_subscriptions subscription
  ON subscription.id = wd.subscription_id
 AND subscription.workspace_id = eo.workspace_id
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

-- name: FailExpiredExhaustedWebhookDeliveriesManual :exec
UPDATE webhook_deliveries
SET
  status = 'failed',
  lease_until = NULL,
  locked_by = NULL,
  last_error = COALESCE(last_error, 'delivery lease expired after maximum attempts'),
  updated_at = NOW()
WHERE status = 'processing'
  AND lease_until IS NOT NULL
  AND lease_until < NOW()
  AND attempt_count >= max_attempts;

-- name: MarkWebhookDeliveryProcessingManual :execresult
UPDATE webhook_deliveries
SET
  status = 'processing',
  attempt_count = attempt_count + 1,
  lease_until = sqlc.arg(lease_until),
  locked_by = sqlc.arg(locked_by),
  updated_at = NOW()
WHERE id = sqlc.arg(id);

-- name: DeleteDeliveredWebhookDeliveriesBeforeManual :execresult
DELETE FROM webhook_deliveries
WHERE status = 'delivered'
  AND updated_at < sqlc.arg(cutoff)
ORDER BY id
LIMIT 1000;

-- name: DeleteOrphanedEventOutboxBeforeManual :exec
DELETE eo
FROM event_outbox eo
LEFT JOIN webhook_deliveries wd ON wd.event_id = eo.event_id
WHERE eo.created_at < sqlc.arg(cutoff)
  AND wd.id IS NULL;

-- name: LockEventOutboxRetentionBatchManual :many
SELECT event_id
FROM event_outbox
WHERE created_at < sqlc.arg(cutoff)
ORDER BY id
LIMIT 1000
FOR UPDATE;

-- name: DeleteWebhookDeliveriesForEventIDsManual :exec
DELETE FROM webhook_deliveries
WHERE event_id IN (sqlc.slice(event_ids));

-- name: DeleteEventOutboxForIDsManual :execresult
DELETE FROM event_outbox
WHERE event_id IN (sqlc.slice(event_ids));

-- name: MarkWebhookDeliveryDeliveredManual :execresult
UPDATE webhook_deliveries
SET status = 'delivered', lease_until = NULL, locked_by = NULL, last_error = NULL, updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND locked_by = sqlc.arg(locked_by)
  AND status = 'processing';

-- name: MarkWebhookDeliveryFailedManual :execresult
UPDATE webhook_deliveries
SET
  status = CASE WHEN attempt_count >= max_attempts THEN 'failed' ELSE 'pending' END,
  next_attempt_at = CASE WHEN attempt_count >= max_attempts THEN NULL ELSE DATE_ADD(NOW(), INTERVAL LEAST(900, POW(2, attempt_count) * 5) SECOND) END,
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

-- name: GetEventOutboxHighWaterForWorkspaceManual :one
SELECT COALESCE(MAX(id), 0) AS high_water_id
FROM event_outbox
WHERE workspace_id = sqlc.arg(workspace_id);

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
  AND workspace_id = sqlc.arg(workspace_id)
ORDER BY id ASC
LIMIT ?;
