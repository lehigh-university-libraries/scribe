-- name: UpsertOCRRun :exec
INSERT INTO ocr_runs (
  session_id, image_url, provider, model, original_hocr, original_text
) VALUES (?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
  image_url = VALUES(image_url),
  provider = VALUES(provider),
  model = VALUES(model),
  original_hocr = VALUES(original_hocr),
  original_text = VALUES(original_text);

-- name: GetOCRRun :one
SELECT
  session_id, image_url, provider, model, original_hocr, original_text,
  corrected_hocr, corrected_text, edit_count, levenshtein_distance,
  box_edit_count, boxes_added, boxes_deleted, box_change_score,
  created_at, updated_at
FROM ocr_runs
WHERE session_id = ?;

-- name: SaveOCREdits :exec
UPDATE ocr_runs
SET corrected_hocr = ?, corrected_text = ?, edit_count = ?, levenshtein_distance = ?,
    box_edit_count = ?, boxes_added = ?, boxes_deleted = ?, box_change_score = ?
WHERE session_id = ?;
