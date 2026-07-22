-- name: SelectExternalRequestForUpdateManual :one
SELECT
  id,
  workspace_id,
  source,
  idempotency_key,
  request_hash,
  status,
  item_id,
  item_image_id,
  transcription_job_id,
  session_id,
  attempt_count,
  max_attempts,
  lease_until,
  locked_by,
  error_message
FROM external_requests
WHERE workspace_id = sqlc.arg(workspace_id)
  AND source = sqlc.arg(source)
  AND idempotency_key = sqlc.arg(idempotency_key)
FOR UPDATE;

-- name: GetExternalRequestManual :one
SELECT *
FROM external_requests
WHERE workspace_id = sqlc.arg(workspace_id)
  AND source = sqlc.arg(source)
  AND idempotency_key = sqlc.arg(idempotency_key);

-- name: InsertExternalRequestManual :execresult
INSERT INTO external_requests (
  workspace_id,
  source,
  idempotency_key,
  request_hash,
  status,
  item_image_id,
  event_header,
  attempt_count,
  lease_until,
  locked_by
) SELECT
  w.id,
  sqlc.arg(source),
  sqlc.arg(idempotency_key),
  sqlc.arg(request_hash),
  'in_progress',
  sqlc.narg(item_image_id),
  sqlc.narg(event_header),
  1,
  sqlc.arg(lease_until),
  sqlc.arg(locked_by)
FROM workspaces w
LEFT JOIN item_images ii
  ON ii.workspace_id = w.id
 AND ii.id = sqlc.narg(item_image_id)
WHERE w.id = sqlc.arg(workspace_id)
  AND (sqlc.narg(item_image_id) IS NULL OR ii.id IS NOT NULL)
ON DUPLICATE KEY UPDATE id = LAST_INSERT_ID(external_requests.id);

-- name: ReclaimExternalRequestManual :exec
UPDATE external_requests
SET
  status = 'in_progress',
  event_header = sqlc.narg(event_header),
  item_id = NULL,
  item_image_id = COALESCE(sqlc.narg(item_image_id), item_image_id),
  transcription_job_id = NULL,
  session_id = NULL,
  error_message = NULL,
  attempt_count = attempt_count + 1,
  lease_until = sqlc.arg(lease_until),
  locked_by = sqlc.arg(locked_by),
  updated_at = NOW()
WHERE id = sqlc.arg(id);

-- name: CompleteExternalRequestManual :execresult
UPDATE external_requests
SET
  status = 'completed',
  item_id = sqlc.narg(item_id),
  item_image_id = sqlc.narg(item_image_id),
  transcription_job_id = sqlc.narg(transcription_job_id),
  session_id = sqlc.narg(session_id),
  error_message = NULL,
  lease_until = NULL,
  locked_by = NULL,
  updated_at = NOW()
WHERE external_requests.workspace_id = sqlc.arg(workspace_id)
  AND external_requests.source = sqlc.arg(source)
	AND external_requests.idempotency_key = sqlc.arg(idempotency_key)
	AND external_requests.status = 'in_progress'
	AND external_requests.locked_by = sqlc.arg(locked_by)
  AND (
    sqlc.narg(item_id) IS NULL
    OR EXISTS (
      SELECT 1 FROM items i
      WHERE i.workspace_id = sqlc.arg(workspace_id)
        AND i.id = sqlc.narg(item_id)
    )
  )
  AND (
    sqlc.narg(item_image_id) IS NULL
    OR EXISTS (
      SELECT 1 FROM item_images ii
      WHERE ii.workspace_id = sqlc.arg(workspace_id)
        AND ii.id = sqlc.narg(item_image_id)
        AND (sqlc.narg(item_id) IS NULL OR ii.item_id = sqlc.narg(item_id))
    )
  )
  AND (
    sqlc.narg(transcription_job_id) IS NULL
    OR EXISTS (
      SELECT 1 FROM transcription_jobs tj
      WHERE tj.workspace_id = sqlc.arg(workspace_id)
        AND tj.id = sqlc.narg(transcription_job_id)
        AND (sqlc.narg(item_image_id) IS NULL OR tj.item_image_id = sqlc.narg(item_image_id))
    )
  )
  AND (
    sqlc.narg(session_id) IS NULL
    OR EXISTS (
      SELECT 1 FROM ocr_runs run
      WHERE run.workspace_id = sqlc.arg(workspace_id)
        AND run.session_id = sqlc.narg(session_id)
        AND (sqlc.narg(item_image_id) IS NULL OR run.item_image_id = sqlc.narg(item_image_id))
    )
  )
  AND (
    sqlc.narg(item_id) IS NULL
    OR sqlc.narg(transcription_job_id) IS NULL
    OR EXISTS (
      SELECT 1
      FROM transcription_jobs item_job
      JOIN item_images item_job_image ON item_job_image.id = item_job.item_image_id
      WHERE item_job.workspace_id = sqlc.arg(workspace_id)
        AND item_job.id = sqlc.narg(transcription_job_id)
        AND item_job_image.workspace_id = sqlc.arg(workspace_id)
        AND item_job_image.item_id = sqlc.narg(item_id)
    )
  )
  AND (
    sqlc.narg(item_id) IS NULL
    OR sqlc.narg(session_id) IS NULL
    OR EXISTS (
      SELECT 1
      FROM ocr_runs item_run
      JOIN item_images item_run_image ON item_run_image.id = item_run.item_image_id
      WHERE item_run.workspace_id = sqlc.arg(workspace_id)
        AND item_run.session_id = sqlc.narg(session_id)
        AND item_run_image.workspace_id = sqlc.arg(workspace_id)
        AND item_run_image.item_id = sqlc.narg(item_id)
    )
  )
  AND (
    sqlc.narg(transcription_job_id) IS NULL
    OR sqlc.narg(session_id) IS NULL
    OR EXISTS (
      SELECT 1
      FROM transcription_jobs paired_job
      JOIN ocr_runs paired_run
        ON paired_run.workspace_id = paired_job.workspace_id
       AND paired_run.item_image_id = paired_job.item_image_id
      WHERE paired_job.workspace_id = sqlc.arg(workspace_id)
        AND paired_job.id = sqlc.narg(transcription_job_id)
        AND paired_run.session_id = sqlc.narg(session_id)
    )
  );

-- name: DeleteRetainableExternalRequestsManual :execresult
DELETE FROM external_requests
WHERE updated_at < sqlc.arg(cutoff)
  AND (
    status IN ('completed', 'failed')
    OR (
      status = 'in_progress'
      AND (lease_until IS NULL OR lease_until < NOW())
    )
  )
ORDER BY id
LIMIT 1000;

-- name: FailExternalRequestManual :execresult
UPDATE external_requests
SET
  status = 'failed',
  error_message = sqlc.narg(error_message),
  lease_until = NULL,
  locked_by = NULL,
  updated_at = NOW()
WHERE workspace_id = sqlc.arg(workspace_id)
  AND source = sqlc.arg(source)
	AND idempotency_key = sqlc.arg(idempotency_key)
	AND status = 'in_progress'
	AND locked_by = sqlc.arg(locked_by);
