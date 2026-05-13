-- name: SelectExternalRequestForUpdateManual :one
SELECT
  id,
  workspace_id,
  source,
  idempotency_key,
  status,
  item_id,
  item_image_id,
  transcription_job_id,
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

-- name: InsertExternalRequestManual :execresult
INSERT INTO external_requests (
  workspace_id,
  source,
  idempotency_key,
  status,
  event_header,
  attempt_count,
  lease_until,
  locked_by
) VALUES (
  sqlc.arg(workspace_id),
  sqlc.arg(source),
  sqlc.arg(idempotency_key),
  'in_progress',
  sqlc.narg(event_header),
  1,
  sqlc.arg(lease_until),
  sqlc.arg(locked_by)
);

-- name: ReclaimExternalRequestManual :exec
UPDATE external_requests
SET
  status = 'in_progress',
  event_header = sqlc.narg(event_header),
  error_message = NULL,
  attempt_count = attempt_count + 1,
  lease_until = sqlc.arg(lease_until),
  locked_by = sqlc.arg(locked_by),
  updated_at = NOW()
WHERE id = sqlc.arg(id);

-- name: CompleteExternalRequestManual :exec
UPDATE external_requests
SET
  status = 'completed',
  item_id = sqlc.narg(item_id),
  item_image_id = sqlc.narg(item_image_id),
  transcription_job_id = sqlc.narg(transcription_job_id),
  error_message = NULL,
  lease_until = NULL,
  locked_by = NULL,
  updated_at = NOW()
WHERE workspace_id = sqlc.arg(workspace_id)
  AND source = sqlc.arg(source)
  AND idempotency_key = sqlc.arg(idempotency_key);

-- name: FailExternalRequestManual :exec
UPDATE external_requests
SET
  status = 'failed',
  error_message = sqlc.narg(error_message),
  lease_until = NULL,
  locked_by = NULL,
  updated_at = NOW()
WHERE workspace_id = sqlc.arg(workspace_id)
  AND source = sqlc.arg(source)
  AND idempotency_key = sqlc.arg(idempotency_key);
