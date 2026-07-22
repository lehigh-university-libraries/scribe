-- name: InsertTranscriptionJobAttemptManual :execresult
INSERT INTO transcription_job_attempts (
  job_id,
  attempt_number,
  context_snapshot,
  input_revision,
  lease_owner,
  lease_token,
  outcome,
  started_at
)
SELECT
  tj.id,
  tj.attempt_count,
  tj.context_snapshot,
  tj.input_revision,
  sqlc.arg(lease_owner),
  sqlc.arg(lease_token),
  'running',
  NOW(6)
FROM transcription_jobs tj
WHERE tj.id = sqlc.arg(job_id)
  AND tj.status = 'running'
  AND tj.attempt_count = sqlc.arg(attempt_number)
  AND tj.input_revision = sqlc.arg(input_revision)
  AND COALESCE(tj.locked_by, '') = sqlc.arg(job_lease_token);

-- name: FinishTranscriptionJobAttemptManual :execresult
UPDATE transcription_job_attempts
SET outcome = sqlc.arg(outcome),
    safe_error_message = sqlc.narg(safe_error_message),
    result_revision = sqlc.narg(result_revision),
    finished_at = NOW(6)
WHERE job_id = sqlc.arg(job_id)
  AND attempt_number = sqlc.arg(attempt_number)
  AND input_revision = sqlc.arg(input_revision)
  AND lease_token = sqlc.arg(lease_token)
  AND outcome = 'running';

-- name: GetTranscriptionJobAttemptManual :one
SELECT
  job_id,
  attempt_number,
  context_snapshot,
  input_revision,
  lease_owner,
  lease_token,
  outcome,
  safe_error_message,
  result_revision,
  started_at,
  finished_at
FROM transcription_job_attempts
WHERE job_id = sqlc.arg(job_id)
  AND attempt_number = sqlc.arg(attempt_number)
LIMIT 1;

-- name: ListTranscriptionJobAttemptsManual :many
SELECT
  job_id,
  attempt_number,
  context_snapshot,
  input_revision,
  lease_owner,
  lease_token,
  outcome,
  safe_error_message,
  result_revision,
  started_at,
  finished_at
FROM transcription_job_attempts
WHERE job_id = sqlc.arg(job_id)
ORDER BY attempt_number ASC;
