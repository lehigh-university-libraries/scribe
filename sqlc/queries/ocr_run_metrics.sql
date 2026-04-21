-- name: GetContextOCRRunMetricsManual :one
SELECT
  COUNT(*) AS total_runs,
  CAST(COALESCE(SUM(CASE WHEN corrected_hocr IS NOT NULL AND corrected_hocr != '' THEN 1 ELSE 0 END), 0) AS SIGNED) AS corrected_runs,
  CAST(COALESCE(AVG(CASE WHEN corrected_hocr IS NOT NULL AND corrected_hocr != '' THEN levenshtein_distance END), 0) AS DOUBLE) AS avg_levenshtein_distance,
  CAST(COALESCE(AVG(CASE WHEN corrected_hocr IS NOT NULL AND corrected_hocr != '' THEN edit_count END), 0) AS DOUBLE) AS avg_edit_count,
  CAST(COALESCE(AVG(CASE WHEN corrected_hocr IS NOT NULL AND corrected_hocr != '' THEN box_change_score END), 0) AS DOUBLE) AS avg_box_change_score
FROM ocr_runs
WHERE context_id = sqlc.arg(context_id);
