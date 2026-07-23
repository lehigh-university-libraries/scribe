-- name: CreateTranscriptionJobManual :execresult
INSERT INTO transcription_jobs (
  workspace_id,
  item_image_id,
  context_id,
  context_scope_id,
  context_snapshot,
  input_revision,
  status
) SELECT
  i.workspace_id,
  sqlc.arg(item_image_id),
  sqlc.narg(context_id),
  c.scope_id,
  sqlc.arg(context_snapshot),
  ap.revision,
  'pending'
FROM item_images ii
JOIN items i ON i.id = ii.item_id
JOIN annotation_pages ap
  ON ap.item_image_id = ii.id
 AND ap.workspace_id = i.workspace_id
JOIN contexts c
  ON c.id = sqlc.narg(context_id)
 AND (c.workspace_id IS NULL OR c.workspace_id = i.workspace_id)
WHERE ii.id = sqlc.arg(item_image_id)
ON DUPLICATE KEY UPDATE id = LAST_INSERT_ID(transcription_jobs.id);

-- name: CreateUploadBatchTranscriptionJobManual :execresult
INSERT INTO transcription_jobs (
  workspace_id,
  item_image_id,
  context_id,
  context_scope_id,
  context_snapshot,
  input_revision,
  status
) SELECT
  ub.workspace_id,
  ii.id,
  ub.context_id,
  ub.context_scope_id,
  ub.context_snapshot,
  ap.revision,
  'pending'
FROM upload_batches ub
JOIN upload_batch_files ubf
  ON ubf.workspace_id = ub.workspace_id
 AND ubf.batch_id = ub.id
JOIN items i
  ON i.id = ub.item_id
 AND i.workspace_id = ub.workspace_id
JOIN item_images ii
  ON ii.item_id = i.id
 AND ii.workspace_id = i.workspace_id
 AND ii.sequence = ubf.sequence
JOIN annotation_pages ap
  ON ap.item_image_id = ii.id
 AND ap.workspace_id = ub.workspace_id
WHERE ub.workspace_id = sqlc.arg(workspace_id)
  AND ub.id = sqlc.arg(batch_id)
  AND ub.status = 'in_progress'
  AND ubf.sequence = sqlc.arg(sequence)
  AND ubf.status = 'processing'
  AND ubf.locked_by = sqlc.arg(locked_by)
  AND ubf.lease_until > NOW()
  AND ii.id = sqlc.arg(item_image_id)
ON DUPLICATE KEY UPDATE id = LAST_INSERT_ID(transcription_jobs.id);

-- name: LockTranscriptionJobWorkspaceByItemImageManual :one
-- Every code path that admits a pending transcription job takes this lock
-- before counting active jobs. The workspace row is the per-tenant admission
-- mutex; terminal job transitions do not need to update a separate counter.
SELECT w.id
FROM item_images ii
JOIN items i ON i.id = ii.item_id
JOIN workspaces w ON w.id = i.workspace_id
WHERE ii.id = sqlc.arg(item_image_id)
LIMIT 1
FOR UPDATE;

-- name: CountActiveTranscriptionJobsByWorkspaceManual :one
SELECT COUNT(*)
FROM transcription_jobs
WHERE workspace_id = sqlc.arg(workspace_id)
  AND status IN ('pending', 'running');

-- name: GetTranscriptionJobManual :one
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
LIMIT 1;

-- name: GetActiveTranscriptionJobByItemImageManual :one
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
WHERE active_item_image_id = sqlc.arg(item_image_id)
LIMIT 1;

-- name: LockActiveTranscriptionJobForUpdateManual :one
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
WHERE active_item_image_id = sqlc.arg(item_image_id)
LIMIT 1
FOR UPDATE;

-- name: LockTranscriptionJobForUpdateManual :one
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
LIMIT 1
FOR UPDATE;

-- name: LockTranscriptionJobForExternalRequestUseManual :one
SELECT item_image_id
FROM transcription_jobs
WHERE id = sqlc.arg(id)
  AND workspace_id = sqlc.arg(workspace_id)
LOCK IN SHARE MODE;

-- name: GetCanonicalRevisionForItemImageManual :one
SELECT revision
FROM annotation_pages
WHERE item_image_id = sqlc.arg(item_image_id)
LIMIT 1;

-- name: LockCanonicalRevisionForTranscriptionJobManual :one
SELECT revision
FROM item_images ii
JOIN items i ON i.id = ii.item_id
JOIN annotation_pages ap
  ON ap.item_image_id = ii.id
 AND ap.workspace_id = i.workspace_id
WHERE ii.id = sqlc.arg(item_image_id)
LIMIT 1
FOR UPDATE;

-- name: ListTranscriptionJobsByWorkspacePageManual :many
SELECT
  tj.id,
  tj.workspace_id,
  tj.item_image_id,
  tj.context_id,
  tj.context_scope_id,
  tj.input_revision,
  tj.status,
  tj.total_segments,
  tj.completed_segments,
  tj.failed_segments,
  tj.attempt_count,
  tj.max_attempts,
  tj.current_annotation_id,
  tj.error_message,
  tj.created_at,
  tj.updated_at
FROM transcription_jobs tj
WHERE tj.workspace_id = sqlc.arg(workspace_id)
  AND (
    sqlc.narg(cursor_created_at) IS NULL
    OR tj.created_at < sqlc.narg(cursor_created_at)
    OR (
      tj.created_at = sqlc.narg(cursor_created_at)
      AND tj.id < sqlc.arg(cursor_id)
    )
  )
ORDER BY tj.created_at DESC, tj.id DESC
LIMIT ?;

-- name: ListTranscriptionJobsByItemImagePageManual :many
SELECT
  tj.id,
  tj.workspace_id,
  tj.item_image_id,
  tj.context_id,
  tj.context_scope_id,
  tj.input_revision,
  tj.status,
  tj.total_segments,
  tj.completed_segments,
  tj.failed_segments,
  tj.attempt_count,
  tj.max_attempts,
  tj.current_annotation_id,
  tj.error_message,
  tj.created_at,
  tj.updated_at
FROM transcription_jobs tj
WHERE tj.workspace_id = sqlc.arg(workspace_id)
  AND tj.item_image_id = sqlc.arg(item_image_id)
  AND (
    sqlc.narg(cursor_created_at) IS NULL
    OR tj.created_at < sqlc.narg(cursor_created_at)
    OR (
      tj.created_at = sqlc.narg(cursor_created_at)
      AND tj.id < sqlc.arg(cursor_id)
    )
  )
ORDER BY tj.created_at DESC, tj.id DESC
LIMIT ?;

-- name: SupersedeTranscriptionJobByIDManual :execresult
UPDATE transcription_jobs
SET status = 'superseded',
    retry_after = NULL,
    error_message = 'superseded by a newer transcription request',
    current_annotation_id = NULL,
    current_annotation_json = NULL,
    last_result_annotation_json = NULL,
    lease_until = NULL,
    locked_by = NULL,
    updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND status IN ('pending', 'running');

-- name: SetTranscriptionJobTotalSegmentsManual :execresult
UPDATE transcription_jobs
SET
  total_segments = sqlc.arg(total_segments),
  updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND attempt_count = sqlc.arg(attempt_number)
  AND input_revision = sqlc.arg(input_revision)
  AND COALESCE(locked_by, '') = sqlc.arg(lease_token)
  AND status = 'running'
  AND lease_until IS NOT NULL
  AND lease_until > NOW();

-- name: UpdateTranscriptionJobProgressManual :execresult
UPDATE transcription_jobs
SET
  completed_segments = sqlc.arg(completed_segments),
  failed_segments = sqlc.arg(failed_segments),
  current_annotation_id = sqlc.narg(current_annotation_id),
  current_annotation_json = sqlc.narg(current_annotation_json),
  last_result_annotation_json = sqlc.narg(last_result_annotation_json),
  updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND attempt_count = sqlc.arg(attempt_number)
  AND input_revision = sqlc.arg(input_revision)
  AND COALESCE(locked_by, '') = sqlc.arg(lease_token)
  AND status = 'running'
  AND lease_until IS NOT NULL
  AND lease_until > NOW();

-- name: PermanentlyFailTranscriptionJobManual :execresult
UPDATE transcription_jobs
SET
  status = 'failed',
  retry_after = NULL,
  error_message = sqlc.arg(error_message),
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

-- name: CancelTranscriptionJobManual :execresult
UPDATE transcription_jobs
SET
  status = 'canceled',
  retry_after = NULL,
  error_message = 'canceled by user',
  current_annotation_id = NULL,
  current_annotation_json = NULL,
  last_result_annotation_json = NULL,
  lease_until = NULL,
  locked_by = NULL,
  updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND status IN ('pending', 'running');

-- name: WorkspaceOwnsTranscriptionJobManual :one
SELECT EXISTS(
  SELECT 1
  FROM transcription_jobs
  WHERE id = sqlc.arg(job_id)
    AND workspace_id = sqlc.arg(workspace_id)
) AS owns_job;

-- name: CountTerminalTranscriptionJobsForWorkspaceManual :one
SELECT COUNT(*)
FROM transcription_jobs
WHERE workspace_id = sqlc.arg(workspace_id)
  AND status IN ('completed', 'failed', 'canceled', 'superseded');

-- name: LockOldestTerminalTranscriptionJobsForWorkspaceManual :many
SELECT id
FROM transcription_jobs
WHERE workspace_id = sqlc.arg(workspace_id)
  AND status IN ('completed', 'failed', 'canceled', 'superseded')
ORDER BY updated_at ASC, id ASC
LIMIT ?
FOR UPDATE;

-- name: DetachUploadBatchFilesFromRetainedJobManual :exec
UPDATE upload_batch_files
SET transcription_job_id = NULL,
    updated_at = GREATEST(DATE_ADD(updated_at, INTERVAL 1 SECOND), NOW())
WHERE workspace_id = sqlc.arg(workspace_id)
  AND transcription_job_id = sqlc.arg(job_id);

-- name: DetachExternalRequestsFromRetainedJobManual :exec
UPDATE external_requests
SET transcription_job_id = NULL,
    updated_at = NOW()
WHERE workspace_id = sqlc.arg(workspace_id)
  AND transcription_job_id = sqlc.arg(job_id);

-- name: DeleteRetainedTranscriptionJobAttemptsManual :exec
DELETE FROM transcription_job_attempts
WHERE job_id = sqlc.arg(job_id);

-- name: DeleteRetainedTerminalTranscriptionJobManual :execresult
DELETE FROM transcription_jobs
WHERE id = sqlc.arg(job_id)
  AND workspace_id = sqlc.arg(workspace_id)
  AND status IN ('completed', 'failed', 'canceled', 'superseded');
