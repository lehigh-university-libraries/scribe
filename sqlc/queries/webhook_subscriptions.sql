-- name: CountWebhookSubscriptionsForWorkspaceManual :one
SELECT COUNT(*)
FROM webhook_subscriptions
WHERE workspace_id = sqlc.arg(workspace_id);

-- name: CreateWebhookSubscriptionManual :execresult
INSERT INTO webhook_subscriptions (
  workspace_id,
  target_url,
  target_hash,
  signing_secret
) SELECT
  w.id,
  sqlc.arg(target_url),
  sqlc.arg(target_hash),
  sqlc.arg(signing_secret)
FROM workspaces w
WHERE w.id = sqlc.arg(workspace_id);

-- name: GetWebhookSubscriptionManual :one
SELECT id, workspace_id, target_url, created_at, updated_at
FROM webhook_subscriptions
WHERE id = sqlc.arg(id)
  AND workspace_id = sqlc.arg(workspace_id)
LIMIT 1;

-- name: ListWebhookSubscriptionsManual :many
SELECT id, workspace_id, target_url, created_at, updated_at
FROM webhook_subscriptions
WHERE workspace_id = sqlc.arg(workspace_id)
ORDER BY created_at ASC, id ASC;

-- name: LockWebhookSubscriptionManual :one
SELECT id
FROM webhook_subscriptions
WHERE id = sqlc.arg(id)
  AND workspace_id = sqlc.arg(workspace_id)
FOR UPDATE;

-- name: DeleteWebhookDeliveriesForSubscriptionManual :exec
DELETE FROM webhook_deliveries
WHERE subscription_id = sqlc.arg(subscription_id);

-- name: DeleteWebhookSubscriptionManual :execresult
DELETE FROM webhook_subscriptions
WHERE id = sqlc.arg(id)
  AND workspace_id = sqlc.arg(workspace_id);
