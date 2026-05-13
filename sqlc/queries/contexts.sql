-- name: CreateContextManual :execresult
INSERT INTO contexts (
  user_id,
  workspace_id,
  name,
  description,
  is_default,
  segmentation_model,
  image_preprocessors,
  transcription_provider,
  transcription_model,
  transcription_base_url,
  transcription_audience,
  temperature,
  system_prompt,
  post_processing_steps
) VALUES (
  sqlc.narg(user_id),
  sqlc.narg(workspace_id),
  sqlc.arg(name),
  sqlc.narg(description),
  sqlc.arg(is_default),
  sqlc.arg(segmentation_model),
  sqlc.narg(image_preprocessors),
  sqlc.arg(transcription_provider),
  sqlc.arg(transcription_model),
  sqlc.narg(transcription_base_url),
  sqlc.narg(transcription_audience),
  sqlc.narg(temperature),
  sqlc.narg(system_prompt),
  sqlc.narg(post_processing_steps)
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
  COALESCE(image_preprocessors, JSON_ARRAY()) AS image_preprocessors,
  transcription_provider,
  transcription_model,
  transcription_base_url,
  transcription_audience,
  temperature,
  system_prompt,
  COALESCE(post_processing_steps, JSON_ARRAY()) AS post_processing_steps,
  created_at,
  updated_at
FROM contexts
WHERE id = sqlc.arg(id);

-- name: GetDefaultContextManual :one
SELECT
  id,
  user_id,
  workspace_id,
  name,
  description,
  is_default,
  segmentation_model,
  COALESCE(image_preprocessors, JSON_ARRAY()) AS image_preprocessors,
  transcription_provider,
  transcription_model,
  transcription_base_url,
  transcription_audience,
  temperature,
  system_prompt,
  COALESCE(post_processing_steps, JSON_ARRAY()) AS post_processing_steps,
  created_at,
  updated_at
FROM contexts
WHERE is_default = TRUE
  AND user_id IS NULL
LIMIT 1;

-- name: ListContextsManual :many
SELECT
  id,
  user_id,
  workspace_id,
  name,
  description,
  is_default,
  segmentation_model,
  COALESCE(image_preprocessors, JSON_ARRAY()) AS image_preprocessors,
  transcription_provider,
  transcription_model,
  transcription_base_url,
  transcription_audience,
  temperature,
  system_prompt,
  COALESCE(post_processing_steps, JSON_ARRAY()) AS post_processing_steps,
  created_at,
  updated_at
FROM contexts
WHERE sqlc.arg(system_only) = FALSE OR user_id IS NULL
ORDER BY is_default DESC, name ASC;

-- name: ListContextsForWorkspaceManual :many
SELECT
  id,
  user_id,
  workspace_id,
  name,
  description,
  is_default,
  segmentation_model,
  COALESCE(image_preprocessors, JSON_ARRAY()) AS image_preprocessors,
  transcription_provider,
  transcription_model,
  transcription_base_url,
  transcription_audience,
  temperature,
  system_prompt,
  COALESCE(post_processing_steps, JSON_ARRAY()) AS post_processing_steps,
  created_at,
  updated_at
FROM contexts
WHERE workspace_id IS NULL
   OR (sqlc.arg(system_only) = FALSE AND workspace_id = sqlc.arg(workspace_id))
ORDER BY is_default DESC, name ASC;

-- name: UpdateContextManual :exec
UPDATE contexts
SET
  name = sqlc.arg(name),
  description = sqlc.narg(description),
  is_default = sqlc.arg(is_default),
  segmentation_model = sqlc.arg(segmentation_model),
  image_preprocessors = sqlc.narg(image_preprocessors),
  transcription_provider = sqlc.arg(transcription_provider),
  transcription_model = sqlc.arg(transcription_model),
  transcription_base_url = sqlc.narg(transcription_base_url),
  transcription_audience = sqlc.narg(transcription_audience),
  temperature = sqlc.narg(temperature),
  system_prompt = sqlc.narg(system_prompt),
  post_processing_steps = sqlc.narg(post_processing_steps)
WHERE id = sqlc.arg(id);

-- name: DeleteContextManual :exec
DELETE FROM contexts
WHERE id = sqlc.arg(id);

-- name: DeleteContextForWorkspaceManual :execresult
DELETE FROM contexts
WHERE id = sqlc.arg(id)
  AND workspace_id = sqlc.arg(workspace_id);

-- name: HasDefaultContextManual :one
SELECT COUNT(*) > 0
FROM contexts
WHERE is_default = TRUE
  AND user_id IS NULL;

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

-- name: ListSelectionRulesManual :many
SELECT
  id,
  context_id,
  priority,
  conditions,
  created_at
FROM context_selection_rules
WHERE sqlc.arg(context_id) = 0
   OR context_id = sqlc.arg(context_id)
ORDER BY priority DESC, id ASC;

-- name: ListSelectionRulesForWorkspaceManual :many
SELECT r.id, r.context_id, r.priority, r.conditions, r.created_at
FROM context_selection_rules r
JOIN contexts c ON c.id = r.context_id
WHERE (c.workspace_id IS NULL OR c.workspace_id = sqlc.arg(workspace_id))
  AND (sqlc.arg(context_id) = 0 OR r.context_id = sqlc.arg(context_id))
ORDER BY r.priority DESC, r.id ASC;

-- name: DeleteSelectionRuleManual :exec
DELETE FROM context_selection_rules
WHERE id = sqlc.arg(id);

-- name: DeleteSelectionRuleForWorkspaceManual :execresult
DELETE r
FROM context_selection_rules r
JOIN contexts c ON c.id = r.context_id
WHERE r.id = sqlc.arg(id)
  AND c.workspace_id = sqlc.arg(workspace_id);

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
