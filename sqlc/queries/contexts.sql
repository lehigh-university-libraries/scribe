-- name: CreateContextManual :execresult
INSERT INTO contexts (
  user_id,
  workspace_id,
  name,
  description,
  is_default,
  segmentation_model,
  transcription_provider,
  transcription_model,
  temperature,
  system_prompt
) VALUES (
  sqlc.narg(user_id),
  sqlc.narg(workspace_id),
  sqlc.arg(name),
  sqlc.narg(description),
  sqlc.arg(is_default),
  sqlc.arg(segmentation_model),
  sqlc.arg(transcription_provider),
  sqlc.arg(transcription_model),
  sqlc.narg(temperature),
  sqlc.narg(system_prompt)
);

-- name: GetContextManual :one
SELECT
  id,
  user_id,
  workspace_id,
  name,
  description,
  is_default,
  segmentation_model,
  transcription_provider,
  transcription_model,
  temperature,
  system_prompt,
  scope_id,
  default_scope_id,
  created_at,
  updated_at
FROM contexts
WHERE id = sqlc.arg(id);

-- name: LockContextForUseManual :one
SELECT
  id,
  user_id,
  workspace_id,
  name,
  description,
  is_default,
  segmentation_model,
  transcription_provider,
  transcription_model,
  temperature,
  system_prompt,
  scope_id,
  default_scope_id,
  created_at,
  updated_at
FROM contexts
WHERE id = sqlc.arg(context_id)
  AND scope_id IN (0, sqlc.arg(workspace_id))
LIMIT 1
LOCK IN SHARE MODE;

-- name: LockContextByIDForUseManual :one
SELECT
  id,
  user_id,
  workspace_id,
  name,
  description,
  is_default,
  segmentation_model,
  transcription_provider,
  transcription_model,
  temperature,
  system_prompt,
  scope_id,
  default_scope_id,
  created_at,
  updated_at
FROM contexts
WHERE id = sqlc.arg(context_id)
LIMIT 1
LOCK IN SHARE MODE;

-- name: LockContextForDeleteManual :one
SELECT id, workspace_id
FROM contexts
WHERE id = sqlc.arg(id)
FOR UPDATE;

-- name: LockContextForWorkspaceDeleteManual :one
SELECT id
FROM contexts
WHERE id = sqlc.arg(id)
  AND workspace_id = sqlc.arg(workspace_id)
FOR UPDATE;

-- name: GetDefaultContextManual :one
SELECT
  id,
  user_id,
  workspace_id,
  name,
  description,
  is_default,
  segmentation_model,
  transcription_provider,
  transcription_model,
  temperature,
  system_prompt,
  scope_id,
  default_scope_id,
  created_at,
  updated_at
FROM contexts
WHERE is_default = TRUE
  AND workspace_id IS NULL
LIMIT 1;

-- name: GetDefaultContextForWorkspaceManual :one
SELECT
  id,
  user_id,
  workspace_id,
  name,
  description,
  is_default,
  segmentation_model,
  transcription_provider,
  transcription_model,
  temperature,
  system_prompt,
  scope_id,
  default_scope_id,
  created_at,
  updated_at
FROM contexts
WHERE is_default = TRUE
  AND (workspace_id = sqlc.arg(workspace_id) OR workspace_id IS NULL)
ORDER BY workspace_id IS NULL ASC
LIMIT 1;

-- name: ClearDefaultContextsForScopeManual :exec
UPDATE contexts
SET is_default = FALSE
WHERE is_default = TRUE
  AND workspace_id <=> sqlc.narg(workspace_id)
  AND id <> sqlc.arg(except_id);

-- name: GetSystemContextByNameManual :one
SELECT
  id,
  user_id,
  workspace_id,
  name,
  description,
  is_default,
  segmentation_model,
  transcription_provider,
  transcription_model,
  temperature,
  system_prompt,
  scope_id,
  default_scope_id,
  created_at,
  updated_at
FROM contexts
WHERE workspace_id IS NULL
  AND name = sqlc.arg(name)
LIMIT 1;

-- name: ListContextsPageForWorkspaceManual :many
SELECT
  id,
  user_id,
  workspace_id,
  name,
  description,
  is_default,
  segmentation_model,
  transcription_provider,
  transcription_model,
  temperature,
  system_prompt,
  scope_id,
  default_scope_id,
  created_at,
  updated_at
FROM contexts
WHERE (
    workspace_id IS NULL
    OR (sqlc.arg(system_only) = FALSE AND workspace_id = sqlc.arg(workspace_id))
  )
  AND (
    sqlc.arg(cursor_id) = 0
    OR is_default < sqlc.arg(cursor_is_default)
    OR (
      is_default = sqlc.arg(cursor_is_default)
      AND (workspace_id IS NULL) < sqlc.arg(cursor_is_system)
    )
    OR (
      is_default = sqlc.arg(cursor_is_default)
      AND (workspace_id IS NULL) = sqlc.arg(cursor_is_system)
      AND id > sqlc.arg(cursor_id)
    )
  )
ORDER BY is_default DESC, (workspace_id IS NULL) DESC, id ASC
LIMIT ?;

-- name: UpdateContextManual :execresult
UPDATE contexts
SET
  name = sqlc.arg(name),
  description = sqlc.narg(description),
  is_default = sqlc.arg(is_default),
  segmentation_model = sqlc.arg(segmentation_model),
  transcription_provider = sqlc.arg(transcription_provider),
  transcription_model = sqlc.arg(transcription_model),
  temperature = sqlc.narg(temperature),
  system_prompt = sqlc.narg(system_prompt)
WHERE id = sqlc.arg(id);

-- name: DeleteContextManual :execresult
DELETE FROM contexts
WHERE id = sqlc.arg(id);

-- name: DeleteContextForWorkspaceManual :execresult
DELETE FROM contexts
WHERE id = sqlc.arg(id)
  AND workspace_id = sqlc.arg(workspace_id);

-- name: ClearOCRRunContextLinksManual :exec
UPDATE ocr_runs
SET context_id = NULL,
    context_scope_id = NULL
WHERE context_id = sqlc.arg(context_id);

-- name: ClearTranscriptionJobContextLinksManual :exec
UPDATE transcription_jobs
SET context_id = NULL,
    context_scope_id = NULL
WHERE context_id = sqlc.arg(context_id);

-- name: ClearUploadBatchContextLinksManual :exec
UPDATE upload_batches
SET context_id = NULL,
    context_scope_id = NULL
WHERE context_id = sqlc.arg(context_id);

-- name: ClearProviderAuditContextLinksManual :exec
UPDATE provider_call_audits
SET context_id = NULL,
    context_scope_id = NULL
WHERE context_id = sqlc.arg(context_id);

-- name: DeleteSelectionRulesForContextManual :exec
DELETE FROM context_selection_rules
WHERE context_id = sqlc.arg(context_id);

-- name: HasDefaultContextManual :one
SELECT COUNT(*) > 0
FROM contexts
WHERE is_default = TRUE
  AND workspace_id IS NULL;

-- name: CreateSelectionRuleManual :execresult
INSERT INTO context_selection_rules (
  context_id,
  priority,
  conditions
) VALUES (
  sqlc.arg(context_id),
  sqlc.arg(priority),
  sqlc.arg(conditions)
);

-- name: GetSelectionRuleByIDManual :one
SELECT
  id,
  context_id,
  priority,
  conditions,
  created_at
FROM context_selection_rules
WHERE id = sqlc.arg(id)
LIMIT 1;

-- name: GetSelectionRuleForWorkspaceManual :one
SELECT r.id, r.context_id, r.priority, r.conditions, r.created_at
FROM context_selection_rules r
JOIN contexts c ON c.id = r.context_id
WHERE r.id = sqlc.arg(id)
  AND c.workspace_id = sqlc.arg(workspace_id)
LIMIT 1;

-- name: ListSelectionRulesPageForWorkspaceManual :many
SELECT r.id, r.context_id, r.priority, r.conditions, r.created_at
FROM context_selection_rules r
JOIN contexts c ON c.id = r.context_id
WHERE (c.workspace_id IS NULL OR c.workspace_id = sqlc.arg(workspace_id))
  AND (sqlc.arg(context_id) = 0 OR r.context_id = sqlc.arg(context_id))
  AND (
    sqlc.arg(cursor_id) = 0
    OR r.priority < sqlc.arg(cursor_priority)
    OR (r.priority = sqlc.arg(cursor_priority) AND r.id > sqlc.arg(cursor_id))
  )
ORDER BY r.priority DESC, r.id ASC
LIMIT ?;

-- name: ListSelectionRulesForResolutionManual :many
SELECT r.id, r.context_id, r.priority, r.conditions, r.created_at
FROM context_selection_rules r
JOIN contexts c ON c.id = r.context_id
WHERE c.workspace_id IS NULL
ORDER BY r.priority DESC, r.id ASC
LIMIT ?;

-- name: ListSelectionRulesForWorkspaceResolutionManual :many
SELECT r.id, r.context_id, r.priority, r.conditions, r.created_at
FROM context_selection_rules r
JOIN contexts c ON c.id = r.context_id
WHERE c.workspace_id IS NULL OR c.workspace_id = sqlc.arg(workspace_id)
ORDER BY r.priority DESC, r.id ASC
LIMIT ?;

-- name: CountSelectionRulesForWorkspaceManual :one
SELECT COUNT(*)
FROM context_selection_rules r
JOIN contexts c ON c.id = r.context_id
WHERE c.workspace_id IS NULL OR c.workspace_id = sqlc.arg(workspace_id);

-- name: LockWorkspaceForSelectionRuleAdmissionManual :one
SELECT id
FROM workspaces
WHERE id = sqlc.arg(workspace_id)
FOR UPDATE;

-- name: DeleteSelectionRuleManual :exec
DELETE FROM context_selection_rules
WHERE id = sqlc.arg(id);

-- name: DeleteSelectionRuleForWorkspaceManual :execresult
DELETE r
FROM context_selection_rules r
JOIN contexts c ON c.id = r.context_id
WHERE r.id = sqlc.arg(id)
  AND c.workspace_id = sqlc.arg(workspace_id);

-- name: WorkspaceOwnsSelectionRuleManual :one
SELECT EXISTS(
  SELECT 1
  FROM context_selection_rules r
  JOIN contexts c ON c.id = r.context_id
  WHERE r.id = sqlc.arg(rule_id)
    AND c.workspace_id = sqlc.arg(workspace_id)
) AS owns_rule;

-- name: WorkspaceCanReadContextManual :one
SELECT EXISTS(
  SELECT 1
  FROM contexts
  WHERE id = sqlc.arg(context_id)
    AND (workspace_id IS NULL OR workspace_id = sqlc.arg(workspace_id))
) AS can_read;

-- name: WorkspaceCanWriteContextManual :one
SELECT EXISTS(
  SELECT 1
  FROM contexts
  WHERE id = sqlc.arg(context_id)
    AND workspace_id = sqlc.arg(workspace_id)
) AS can_write;
