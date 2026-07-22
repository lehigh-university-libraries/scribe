-- name: InsertProviderCallAudit :execresult
INSERT INTO provider_call_audits (
  workspace_id,
  session_id,
  item_image_id,
  context_id,
  context_scope_id,
  provider,
  model,
  operation,
  error_message,
  http_status,
  duration_ms,
  database_bytes
) SELECT
  CAST(sqlc.arg(audit_workspace_id) AS UNSIGNED),
  sqlc.narg(audit_session_id),
  sqlc.narg(audit_item_image_id),
  sqlc.narg(audit_context_id),
  c.scope_id,
  sqlc.arg(audit_provider),
  sqlc.arg(audit_model),
  sqlc.arg(audit_operation),
  sqlc.narg(audit_error_message),
  sqlc.narg(audit_http_status),
  sqlc.arg(audit_duration_ms),
  sqlc.arg(audit_database_bytes)
FROM (SELECT 1 AS seed) seed
LEFT JOIN contexts c
  ON c.id = sqlc.narg(audit_context_id)
 AND c.scope_id IN (0, CAST(sqlc.arg(audit_workspace_id) AS UNSIGNED))
WHERE (sqlc.narg(audit_context_id) IS NULL OR c.id IS NOT NULL)
  AND (
    sqlc.narg(audit_item_image_id) IS NULL
    OR EXISTS (
     SELECT 1
     FROM item_images ii
     WHERE ii.id = sqlc.narg(audit_item_image_id)
       AND ii.workspace_id = CAST(sqlc.arg(audit_workspace_id) AS UNSIGNED)
    )
  );

-- name: ListProviderCallAuditsByItem :many
SELECT
  a.id,
  a.workspace_id,
  COALESCE(a.session_id, '') AS session_id,
  a.item_image_id,
  a.context_id,
  a.provider,
  a.model,
  a.operation,
  COALESCE(a.error_message, '') AS error_message,
  a.http_status,
  a.duration_ms,
  a.created_at,
  i.id AS item_id,
  ii.sequence AS item_image_sequence,
  COALESCE(ii.label, '') AS item_image_label
FROM provider_call_audits a
JOIN item_images ii ON ii.id = a.item_image_id
JOIN items i ON i.id = ii.item_id
WHERE a.workspace_id = sqlc.arg(workspace_id)
  AND i.workspace_id = sqlc.arg(workspace_id)
  AND i.id = sqlc.arg(item_id)
ORDER BY a.created_at DESC, a.id DESC
LIMIT ?;

-- name: ListExpiredProviderAuditWorkspaces :many
SELECT DISTINCT workspace_id
FROM provider_call_audits
WHERE created_at < sqlc.arg(cutoff)
  AND workspace_id > sqlc.arg(after_workspace_id)
ORDER BY workspace_id
LIMIT 100;

-- name: LockExpiredProviderAuditBatch :many
SELECT id, database_bytes
FROM provider_call_audits
WHERE workspace_id = sqlc.arg(workspace_id)
  AND created_at < sqlc.arg(cutoff)
ORDER BY id
LIMIT ?
FOR UPDATE;

-- name: DeleteExpiredProviderAuditBatch :execresult
DELETE FROM provider_call_audits
WHERE workspace_id = sqlc.arg(workspace_id)
  AND created_at < sqlc.arg(cutoff)
ORDER BY id
LIMIT ?;
