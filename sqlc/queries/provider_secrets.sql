-- name: CreateProviderSecretManual :execresult
INSERT INTO provider_secrets (
  user_id,
  workspace_id,
  provider,
  name,
  vault_path,
  key_hint
) VALUES (
  sqlc.narg(user_id),
  sqlc.narg(workspace_id),
  sqlc.arg(provider),
  sqlc.arg(name),
  sqlc.arg(vault_path),
  sqlc.narg(key_hint)
);

-- name: ListProviderSecretsVisibleToUserManual :many
SELECT
  id,
  user_id,
  workspace_id,
  provider,
  name,
  vault_path,
  key_hint,
  created_at,
  updated_at
FROM provider_secrets
WHERE workspace_id = sqlc.arg(workspace_id)
  AND (user_id IS NULL OR user_id = sqlc.arg(user_id))
ORDER BY provider ASC, name ASC, updated_at DESC;

-- name: GetProviderSecretVisibleToUserManual :one
SELECT
  id,
  user_id,
  workspace_id,
  provider,
  name,
  vault_path,
  key_hint,
  created_at,
  updated_at
FROM provider_secrets
WHERE id = sqlc.arg(id)
  AND workspace_id = sqlc.arg(workspace_id)
  AND (user_id IS NULL OR user_id = sqlc.arg(user_id))
LIMIT 1;

-- name: FindPreferredProviderSecretManual :one
SELECT
  id,
  user_id,
  workspace_id,
  provider,
  name,
  vault_path,
  key_hint,
  created_at,
  updated_at
FROM provider_secrets
WHERE workspace_id = sqlc.arg(workspace_id)
  AND provider = sqlc.arg(provider)
  AND (
    user_id IS NULL
    OR user_id = sqlc.narg(user_id)
  )
ORDER BY
  CASE WHEN user_id = sqlc.narg(user_id) THEN 0 ELSE 1 END,
  updated_at DESC,
  id DESC
LIMIT 1;

-- name: DeleteWorkspaceProviderSecretManual :execresult
DELETE FROM provider_secrets
WHERE id = sqlc.arg(id)
  AND workspace_id = sqlc.arg(workspace_id)
  AND user_id IS NULL;

-- name: DeleteUserProviderSecretManual :execresult
DELETE FROM provider_secrets
WHERE id = sqlc.arg(id)
  AND workspace_id = sqlc.arg(workspace_id)
  AND user_id = sqlc.arg(user_id);
