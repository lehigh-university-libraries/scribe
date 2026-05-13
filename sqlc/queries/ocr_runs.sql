-- name: UpsertOCRRunManual :exec
INSERT INTO ocr_runs (
  session_id,
  item_image_id,
  context_id,
  image_url,
  provider,
  model,
  original_hocr,
  original_text
) VALUES (
  sqlc.arg(session_id),
  sqlc.narg(item_image_id),
  sqlc.narg(context_id),
  sqlc.arg(image_url),
  sqlc.arg(provider),
  sqlc.arg(model),
  sqlc.arg(original_hocr),
  sqlc.arg(original_text)
)
ON DUPLICATE KEY UPDATE
  item_image_id = COALESCE(VALUES(item_image_id), item_image_id),
  context_id = COALESCE(VALUES(context_id), context_id),
  image_url = VALUES(image_url),
  provider = VALUES(provider),
  model = VALUES(model),
  original_hocr = VALUES(original_hocr),
  original_text = VALUES(original_text);

-- name: GetOCRRunManual :one
SELECT
  session_id,
  item_image_id,
  context_id,
  image_url,
  provider,
  model,
  original_hocr,
  original_text,
  corrected_hocr,
  corrected_text,
  edit_count,
  levenshtein_distance,
  box_edit_count,
  boxes_added,
  boxes_deleted,
  box_change_score,
  created_at,
  updated_at
FROM ocr_runs
WHERE session_id = sqlc.arg(session_id);

-- name: GetOCRRunByItemImageIDManual :one
SELECT
  session_id,
  item_image_id,
  context_id,
  image_url,
  provider,
  model,
  original_hocr,
  original_text,
  corrected_hocr,
  corrected_text,
  edit_count,
  levenshtein_distance,
  box_edit_count,
  boxes_added,
  boxes_deleted,
  box_change_score,
  created_at,
  updated_at
FROM ocr_runs
WHERE item_image_id = sqlc.arg(item_image_id);

-- name: SaveOCREditsManual :execresult
UPDATE ocr_runs
SET
  corrected_hocr = sqlc.arg(corrected_hocr),
  corrected_text = sqlc.arg(corrected_text),
  edit_count = sqlc.arg(edit_count),
  levenshtein_distance = sqlc.arg(levenshtein_distance),
  box_edit_count = sqlc.arg(box_edit_count),
  boxes_added = sqlc.arg(boxes_added),
  boxes_deleted = sqlc.arg(boxes_deleted),
  box_change_score = sqlc.arg(box_change_score)
WHERE session_id = sqlc.arg(session_id);
