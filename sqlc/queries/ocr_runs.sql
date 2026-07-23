-- name: InsertOCRRunManual :execresult
INSERT INTO ocr_runs (
  session_id,
  workspace_id,
  item_image_id,
  context_id,
  context_scope_id,
  image_url,
  provider,
  model,
  original_hocr,
  original_text
) SELECT
  sqlc.arg(session_id),
  ii.workspace_id,
  sqlc.narg(item_image_id),
  sqlc.narg(context_id),
  c.scope_id,
  sqlc.arg(image_url),
  sqlc.arg(provider),
  sqlc.arg(model),
  sqlc.arg(original_hocr),
  sqlc.arg(original_text)
FROM item_images ii
LEFT JOIN contexts c
  ON c.id = sqlc.narg(context_id)
 AND (c.workspace_id IS NULL OR c.workspace_id = ii.workspace_id)
WHERE ii.id = sqlc.narg(item_image_id)
  AND (sqlc.narg(context_id) IS NULL OR c.id IS NOT NULL);

-- name: SetCurrentOCRRunManual :exec
INSERT INTO current_ocr_runs (item_image_id, session_id)
VALUES (sqlc.arg(item_image_id), sqlc.arg(session_id))
ON DUPLICATE KEY UPDATE
  session_id = VALUES(session_id),
  updated_at = NOW();

-- name: GetOCRRunManual :one
SELECT
  session_id,
  workspace_id,
  item_image_id,
  context_id,
  context_scope_id,
  image_url,
  provider,
  model,
  original_hocr,
  original_text,
  canonical_revision,
  levenshtein_distance,
  created_at,
  updated_at
FROM ocr_runs
WHERE session_id = sqlc.arg(session_id);

-- name: GetOCRRunByItemImageIDManual :one
SELECT
  run.session_id,
  run.workspace_id,
  run.item_image_id,
  run.context_id,
  run.context_scope_id,
  run.image_url,
  run.provider,
  run.model,
  run.original_hocr,
  run.original_text,
  run.canonical_revision,
  run.levenshtein_distance,
  run.created_at,
  run.updated_at
FROM current_ocr_runs current_run
JOIN ocr_runs run
  ON run.session_id = current_run.session_id
 AND run.item_image_id = current_run.item_image_id
WHERE current_run.item_image_id = sqlc.arg(item_image_id);
