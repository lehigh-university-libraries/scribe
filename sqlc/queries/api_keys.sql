-- name: CreateAPIKeyManual :execresult
INSERT INTO api_keys (
  workspace_id,
  created_by_user_id,
  name,
  key_prefix,
  key_hash,
  role,
  scopes,
  expires_at
) VALUES (
  sqlc.arg(workspace_id),
  sqlc.arg(created_by_user_id),
  sqlc.arg(name),
  sqlc.arg(key_prefix),
  sqlc.arg(key_hash),
  sqlc.arg(role),
  sqlc.narg(scopes),
  sqlc.narg(expires_at)
);

-- name: GetAPIKeyByHashManual :one
SELECT
  id,
  workspace_id,
  created_by_user_id,
  name,
  key_prefix,
  key_hash,
  role,
  scopes,
  expires_at,
  created_at,
  updated_at
FROM api_keys
WHERE key_hash = sqlc.arg(key_hash)
LIMIT 1;

-- name: GetAPIKeyManual :one
SELECT
  id,
  workspace_id,
  created_by_user_id,
  name,
  key_prefix,
  key_hash,
  role,
  scopes,
  expires_at,
  created_at,
  updated_at
FROM api_keys
WHERE id = sqlc.arg(id)
LIMIT 1;

-- name: ListAPIKeysByWorkspaceManual :many
SELECT
  id,
  workspace_id,
  created_by_user_id,
  name,
  key_prefix,
  key_hash,
  role,
  scopes,
  expires_at,
  created_at,
  updated_at
FROM api_keys
WHERE workspace_id = sqlc.arg(workspace_id)
ORDER BY created_at DESC
LIMIT 100;

-- name: CountAPIKeysByWorkspaceManual :one
SELECT COUNT(*) FROM api_keys WHERE workspace_id = sqlc.arg(workspace_id);

-- name: DeleteAPIKeyManual :exec
DELETE FROM api_keys
WHERE id = sqlc.arg(id);

-- name: DeleteAPIKeyForWorkspaceManual :execresult
DELETE FROM api_keys
WHERE id = sqlc.arg(id)
  AND workspace_id = sqlc.arg(workspace_id);
