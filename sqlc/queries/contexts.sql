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
  image_preprocessors,
  transcription_provider,
  transcription_model,
  temperature,
  system_prompt,
  post_processing_steps,
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
  image_preprocessors,
  transcription_provider,
  transcription_model,
  temperature,
  system_prompt,
  post_processing_steps,
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
  image_preprocessors,
  transcription_provider,
  transcription_model,
  temperature,
  system_prompt,
  post_processing_steps,
  created_at,
  updated_at
FROM contexts
WHERE NOT sqlc.arg(system_only) OR user_id IS NULL
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
  temperature = sqlc.narg(temperature),
  system_prompt = sqlc.narg(system_prompt),
  post_processing_steps = sqlc.narg(post_processing_steps)
WHERE id = sqlc.arg(id);

-- name: DeleteContextManual :exec
DELETE FROM contexts
WHERE id = sqlc.arg(id);

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

-- name: DeleteSelectionRuleManual :exec
DELETE FROM context_selection_rules
WHERE id = sqlc.arg(id);
