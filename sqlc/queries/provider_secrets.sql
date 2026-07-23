-- name: CreateProviderSecretManual :execresult
INSERT INTO provider_secrets (
  user_id,
  workspace_id,
  provider,
  name,
  vault_path,
  key_hint,
  lifecycle_state
) VALUES (
  sqlc.narg(user_id),
  sqlc.arg(workspace_id),
  sqlc.arg(provider),
  sqlc.arg(name),
  sqlc.arg(vault_path),
  sqlc.narg(key_hint),
  'pending_write'
);

-- name: GetProviderSecretLifecycleManual :one
SELECT
  id,
  user_id,
  workspace_id,
  provider,
  name,
  vault_path,
  key_hint,
  lifecycle_state,
  created_at,
  updated_at
FROM provider_secrets
WHERE id = sqlc.arg(id)
  AND workspace_id = sqlc.arg(workspace_id)
LIMIT 1;

-- name: ActivateProviderSecretManual :execresult
UPDATE provider_secrets
SET lifecycle_state = 'active', updated_at = CURRENT_TIMESTAMP(6)
WHERE id = sqlc.arg(id)
  AND workspace_id = sqlc.arg(workspace_id)
  AND lifecycle_state = 'pending_write';

-- name: MarkPendingProviderSecretCleanupManual :execresult
UPDATE provider_secrets
SET lifecycle_state = 'cleanup_pending', updated_at = CURRENT_TIMESTAMP(6)
WHERE id = sqlc.arg(id)
  AND workspace_id = sqlc.arg(workspace_id)
  AND lifecycle_state = 'pending_write';

-- name: MarkActiveProviderSecretCleanupManual :execresult
UPDATE provider_secrets
SET lifecycle_state = 'cleanup_pending', updated_at = CURRENT_TIMESTAMP(6)
WHERE id = sqlc.arg(id)
  AND workspace_id = sqlc.arg(workspace_id)
  AND lifecycle_state = 'active';

-- name: ListProviderSecretCleanupCandidatesManual :many
SELECT
  id,
  user_id,
  workspace_id,
  provider,
  name,
  vault_path,
  key_hint,
  lifecycle_state,
  created_at,
  updated_at
FROM provider_secrets
WHERE lifecycle_state = 'cleanup_pending'
   OR (lifecycle_state = 'pending_write' AND created_at < sqlc.arg(stale_before))
ORDER BY created_at ASC, id ASC
LIMIT ?;

-- name: DeleteInactiveProviderSecretManual :execresult
DELETE FROM provider_secrets
WHERE id = sqlc.arg(id)
  AND workspace_id = sqlc.arg(workspace_id)
  AND lifecycle_state <> 'active';

-- name: ListProviderSecretsVisibleToUserManual :many
SELECT
  id,
  user_id,
  workspace_id,
  provider,
  name,
  vault_path,
  key_hint,
  lifecycle_state,
  created_at,
  updated_at
FROM provider_secrets
WHERE workspace_id = sqlc.arg(workspace_id)
  AND lifecycle_state = 'active'
  AND (user_id IS NULL OR user_id = sqlc.arg(user_id))
ORDER BY provider ASC, name ASC, updated_at DESC
LIMIT 100;

-- name: CountProviderSecretsByWorkspaceManual :one
SELECT COUNT(*) FROM provider_secrets WHERE workspace_id = sqlc.arg(workspace_id);

-- name: GetProviderSecretVisibleToUserManual :one
SELECT
  id,
  user_id,
  workspace_id,
  provider,
  name,
  vault_path,
  key_hint,
  lifecycle_state,
  created_at,
  updated_at
FROM provider_secrets
WHERE id = sqlc.arg(id)
  AND workspace_id = sqlc.arg(workspace_id)
  AND lifecycle_state = 'active'
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
  lifecycle_state,
  created_at,
  updated_at
FROM provider_secrets
WHERE workspace_id = sqlc.arg(workspace_id)
  AND provider = sqlc.arg(provider)
  AND lifecycle_state = 'active'
  AND (
    user_id IS NULL
    OR user_id = sqlc.narg(user_id)
  )
ORDER BY
  CASE WHEN user_id = sqlc.narg(user_id) THEN 0 ELSE 1 END,
  updated_at DESC,
  id DESC
LIMIT 1;
