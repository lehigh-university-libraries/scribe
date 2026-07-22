-- name: GetContextOCRRunMetricsManual :one
SELECT
  COUNT(*) AS total_runs,
  CAST(COALESCE(SUM(current_run.canonical_revision IS NOT NULL), 0) AS SIGNED) AS corrected_runs,
  CAST(COALESCE(AVG(CASE WHEN current_run.canonical_revision IS NOT NULL THEN current_run.levenshtein_distance END), 0) AS DOUBLE) AS avg_levenshtein_distance
FROM current_ocr_runs current_pointer
JOIN ocr_runs current_run
  ON current_run.session_id = current_pointer.session_id
 AND current_run.item_image_id = current_pointer.item_image_id
JOIN item_images current_image ON current_image.id = current_run.item_image_id
JOIN items current_item ON current_item.id = current_image.item_id
WHERE current_run.context_id = sqlc.arg(context_id)
  AND current_item.workspace_id = sqlc.arg(workspace_id);
