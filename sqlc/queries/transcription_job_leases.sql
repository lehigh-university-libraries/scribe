-- name: ClaimNextLeasedTranscriptionJobManual :one
SELECT
  id,
  workspace_id,
  item_image_id,
  context_id,
  context_scope_id,
  context_snapshot,
  input_revision,
  status,
  active_item_image_id,
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
    AND attempt_count < max_attempts
  ) OR (
    status = 'running'
    AND lease_until IS NOT NULL
    AND lease_until < NOW()
  )
ORDER BY created_at ASC
LIMIT 1
FOR UPDATE SKIP LOCKED;

-- name: FailExpiredTranscriptionJobManual :execresult
UPDATE transcription_jobs
SET status = 'failed',
    retry_after = NULL,
    error_message = 'worker lease expired after maximum attempts',
    current_annotation_id = NULL,
    current_annotation_json = NULL,
    last_result_annotation_json = NULL,
    lease_until = NULL,
    locked_by = NULL,
    updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND status = 'running'
  AND lease_until IS NOT NULL
  AND lease_until < NOW()
  AND attempt_count = sqlc.arg(attempt_number)
  AND input_revision = sqlc.arg(input_revision)
  AND COALESCE(locked_by, '') = sqlc.arg(lease_token)
  AND attempt_count >= max_attempts;

-- name: ClaimNextLeasedTranscriptionJobOlderThanManual :one
SELECT
  id,
  workspace_id,
  item_image_id,
  context_id,
  context_scope_id,
  context_snapshot,
  input_revision,
  status,
  active_item_image_id,
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
      AND attempt_count < max_attempts
    ) OR (
      status = 'running'
      AND lease_until IS NOT NULL
      AND lease_until < NOW()
    )
  )
ORDER BY created_at ASC
LIMIT 1
FOR UPDATE SKIP LOCKED;

-- name: ClaimLeasedTranscriptionJobByIDManual :one
SELECT
  id,
  workspace_id,
  item_image_id,
  context_id,
  context_scope_id,
  context_snapshot,
  input_revision,
  status,
  active_item_image_id,
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
      AND attempt_count < max_attempts
    ) OR (
      status = 'running'
      AND lease_until IS NOT NULL
      AND lease_until < NOW()
    )
  )
LIMIT 1
FOR UPDATE SKIP LOCKED;

-- name: LockActiveTranscriptionJobLeaseManual :one
SELECT id
FROM transcription_jobs
WHERE id = sqlc.arg(id)
  AND attempt_count = sqlc.arg(attempt_number)
  AND input_revision = sqlc.arg(input_revision)
  AND COALESCE(locked_by, '') = sqlc.arg(lease_token)
  AND status = 'running'
  AND lease_until IS NOT NULL
  AND lease_until > NOW()
FOR UPDATE;

-- name: MarkTranscriptionJobLeasedManual :execresult
UPDATE transcription_jobs
SET
  status = 'running',
  attempt_count = attempt_count + 1,
  total_segments = 0,
  completed_segments = 0,
  failed_segments = 0,
  current_annotation_id = NULL,
  current_annotation_json = NULL,
  last_result_annotation_json = NULL,
  lease_until = sqlc.arg(lease_until),
  locked_by = sqlc.arg(lease_token),
  retry_after = NULL,
  error_message = NULL,
  updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND attempt_count = sqlc.arg(previous_attempt_count)
  AND input_revision = sqlc.arg(input_revision)
  AND (locked_by <=> sqlc.narg(previous_lease_token))
  AND (
    (
      status = 'pending'
      AND (retry_after IS NULL OR retry_after <= NOW())
      AND attempt_count < max_attempts
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
  last_result_annotation_json = NULL,
  lease_until = NULL,
  locked_by = NULL,
  updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND attempt_count = sqlc.arg(attempt_number)
  AND input_revision = sqlc.arg(input_revision)
  AND COALESCE(locked_by, '') = sqlc.arg(lease_token)
  AND status = 'running'
  AND lease_until IS NOT NULL
  AND lease_until > NOW();

-- name: DeferTranscriptionJobLeaseManual :execresult
UPDATE transcription_jobs
SET
  status = 'pending',
  retry_after = sqlc.arg(retry_after),
  error_message = sqlc.narg(error_message),
  current_annotation_id = NULL,
  current_annotation_json = NULL,
  last_result_annotation_json = NULL,
  lease_until = NULL,
  locked_by = NULL,
  updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND attempt_count = sqlc.arg(attempt_number)
  AND input_revision = sqlc.arg(input_revision)
  AND COALESCE(locked_by, '') = sqlc.arg(lease_token)
  AND status = 'running'
  AND lease_until IS NOT NULL
  AND lease_until > NOW();

-- name: ExtendTranscriptionJobLeaseManual :execresult
UPDATE transcription_jobs
SET
  lease_until = sqlc.arg(lease_until),
  updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND attempt_count = sqlc.arg(attempt_number)
  AND input_revision = sqlc.arg(input_revision)
  AND COALESCE(locked_by, '') = sqlc.arg(lease_token)
  AND status = 'running'
  AND lease_until IS NOT NULL
  AND lease_until > NOW();

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
  AND attempt_count = sqlc.arg(attempt_number)
  AND input_revision = sqlc.arg(input_revision)
  AND COALESCE(locked_by, '') = sqlc.arg(lease_token)
  AND status = 'running'
  AND lease_until IS NOT NULL
  AND lease_until > NOW();
