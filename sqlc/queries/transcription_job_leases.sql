-- name: ClaimNextLeasedTranscriptionJobManual :one
SELECT
  id,
  item_image_id,
  context_id,
  status,
  total_segments,
  completed_segments,
  failed_segments,
  attempt_count,
  max_attempts,
  retry_after,
  lease_until,
  locked_by,
  current_annotation_id,
  current_annotation_json,
  last_result_annotation_json,
  error_message,
  created_at,
  updated_at
FROM transcription_jobs
WHERE (
    status = 'pending'
    AND (retry_after IS NULL OR retry_after <= NOW())
  ) OR (
    status = 'running'
    AND lease_until IS NOT NULL
    AND lease_until < NOW()
    AND attempt_count < max_attempts
  )
ORDER BY created_at ASC
LIMIT 1
FOR UPDATE SKIP LOCKED;

-- name: ClaimNextLeasedTranscriptionJobOlderThanManual :one
SELECT
  id,
  item_image_id,
  context_id,
  status,
  total_segments,
  completed_segments,
  failed_segments,
  attempt_count,
  max_attempts,
  retry_after,
  lease_until,
  locked_by,
  current_annotation_id,
  current_annotation_json,
  last_result_annotation_json,
  error_message,
  created_at,
  updated_at
FROM transcription_jobs
WHERE created_at < sqlc.arg(cutoff)
  AND (
    (
      status = 'pending'
      AND (retry_after IS NULL OR retry_after <= NOW())
    ) OR (
      status = 'running'
      AND lease_until IS NOT NULL
      AND lease_until < NOW()
      AND attempt_count < max_attempts
    )
  )
ORDER BY created_at ASC
LIMIT 1
FOR UPDATE SKIP LOCKED;

-- name: ClaimLeasedTranscriptionJobByIDManual :one
SELECT
  id,
  item_image_id,
  context_id,
  status,
  total_segments,
  completed_segments,
  failed_segments,
  attempt_count,
  max_attempts,
  retry_after,
  lease_until,
  locked_by,
  current_annotation_id,
  current_annotation_json,
  last_result_annotation_json,
  error_message,
  created_at,
  updated_at
FROM transcription_jobs
WHERE id = sqlc.arg(id)
  AND (
    (
      status = 'pending'
      AND (retry_after IS NULL OR retry_after <= NOW())
    ) OR (
      status = 'running'
      AND lease_until IS NOT NULL
      AND lease_until < NOW()
      AND attempt_count < max_attempts
    )
  )
LIMIT 1
FOR UPDATE SKIP LOCKED;

-- name: MarkTranscriptionJobLeasedManual :execresult
UPDATE transcription_jobs
SET
  status = 'running',
  attempt_count = attempt_count + 1,
  lease_until = sqlc.arg(lease_until),
  locked_by = sqlc.arg(locked_by),
  updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND (
    (
      status = 'pending'
      AND (retry_after IS NULL OR retry_after <= NOW())
    ) OR (
      status = 'running'
      AND lease_until IS NOT NULL
      AND lease_until < NOW()
      AND attempt_count < max_attempts
    )
  );

-- name: CompleteTranscriptionJobLeasedManual :execresult
UPDATE transcription_jobs
SET
  status = 'completed',
  current_annotation_id = NULL,
  current_annotation_json = NULL,
  lease_until = NULL,
  locked_by = NULL,
  updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND locked_by = sqlc.arg(locked_by)
  AND status = 'running';

-- name: DeferTranscriptionJobLeaseManual :execresult
UPDATE transcription_jobs
SET
  status = 'pending',
  retry_after = sqlc.arg(retry_after),
  error_message = sqlc.narg(error_message),
  current_annotation_id = NULL,
  current_annotation_json = NULL,
  lease_until = NULL,
  locked_by = NULL,
  updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND locked_by = sqlc.arg(locked_by)
  AND status = 'running';

-- name: ExtendTranscriptionJobLeaseManual :execresult
UPDATE transcription_jobs
SET
  lease_until = sqlc.arg(lease_until),
  updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND locked_by = sqlc.arg(locked_by)
  AND status = 'running';

-- name: RetryOrFailTranscriptionJobManual :execresult
UPDATE transcription_jobs
SET
  status = CASE WHEN attempt_count < max_attempts THEN 'pending' ELSE 'failed' END,
  retry_after = CASE WHEN attempt_count < max_attempts THEN DATE_ADD(NOW(), INTERVAL LEAST(300, POW(2, attempt_count) * 10) SECOND) ELSE NULL END,
  error_message = sqlc.narg(error_message),
  current_annotation_id = NULL,
  current_annotation_json = NULL,
  lease_until = NULL,
  locked_by = NULL,
  updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND locked_by = sqlc.arg(locked_by)
  AND status = 'running';
