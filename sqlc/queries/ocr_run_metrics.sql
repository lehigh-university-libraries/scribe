-- name: GetContextOCRRunMetricsManual :one
SELECT
  COUNT(*) AS total_runs,
  CAST(COALESCE(SUM(has_correction), 0) AS SIGNED) AS corrected_runs,
  CAST(COALESCE(AVG(CASE WHEN has_correction = 1 THEN levenshtein_distance END), 0) AS DOUBLE) AS avg_levenshtein_distance,
  CAST(COALESCE(AVG(CASE WHEN has_correction = 1 THEN edit_count END), 0) AS DOUBLE) AS avg_edit_count,
  CAST(COALESCE(AVG(CASE WHEN has_correction = 1 THEN box_change_score END), 0) AS DOUBLE) AS avg_box_change_score
FROM (
  SELECT
    MAX(CASE WHEN corrected_hocr IS NOT NULL AND corrected_hocr != '' THEN 1 ELSE 0 END) AS has_correction,
    MAX(CASE WHEN corrected_hocr IS NOT NULL AND corrected_hocr != '' THEN levenshtein_distance ELSE 0 END) AS levenshtein_distance,
    MAX(CASE WHEN corrected_hocr IS NOT NULL AND corrected_hocr != '' THEN edit_count ELSE 0 END) AS edit_count,
    MAX(CASE WHEN corrected_hocr IS NOT NULL AND corrected_hocr != '' THEN box_change_score ELSE 0 END) AS box_change_score
  FROM ocr_runs
  WHERE context_id = sqlc.arg(context_id)
  GROUP BY CASE
    WHEN item_image_id IS NOT NULL THEN CONCAT('img:', CAST(item_image_id AS CHAR))
    ELSE CONCAT('sess:', session_id)
  END
) deduped_runs;
