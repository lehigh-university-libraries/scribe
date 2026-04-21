-- name: CreateTranscriptionJobManual :execresult
INSERT INTO transcription_jobs (
  item_image_id,
  context_id,
  status
) VALUES (
  sqlc.arg(item_image_id),
  sqlc.narg(context_id),
  'pending'
);

-- name: GetTranscriptionJobManual :one
SELECT
  id,
  item_image_id,
  context_id,
  status,
  total_segments,
  completed_segments,
  failed_segments,
  current_annotation_id,
  current_annotation_json,
  last_result_annotation_json,
  error_message,
  created_at,
  updated_at
FROM transcription_jobs
WHERE id = sqlc.arg(id)
LIMIT 1;

-- name: ListTranscriptionJobsByItemImageManual :many
SELECT
  id,
  item_image_id,
  context_id,
  status,
  total_segments,
  completed_segments,
  failed_segments,
  current_annotation_id,
  current_annotation_json,
  last_result_annotation_json,
  error_message,
  created_at,
  updated_at
FROM transcription_jobs
WHERE item_image_id = sqlc.arg(item_image_id)
ORDER BY created_at DESC;

-- name: ListTranscriptionJobsByWorkspaceManual :many
SELECT
  tj.id,
  tj.item_image_id,
  tj.context_id,
  tj.status,
  tj.total_segments,
  tj.completed_segments,
  tj.failed_segments,
  tj.current_annotation_id,
  tj.current_annotation_json,
  tj.last_result_annotation_json,
  tj.error_message,
  tj.created_at,
  tj.updated_at
FROM transcription_jobs tj
JOIN item_images ii ON ii.id = tj.item_image_id
JOIN items i ON i.id = ii.item_id
WHERE i.workspace_id = sqlc.arg(workspace_id)
ORDER BY tj.created_at DESC;

-- name: ClaimNextPendingTranscriptionJobManual :one
SELECT
  id,
  item_image_id,
  context_id,
  status,
  total_segments,
  completed_segments,
  failed_segments,
  current_annotation_id,
  current_annotation_json,
  last_result_annotation_json,
  error_message,
  created_at,
  updated_at
FROM transcription_jobs
WHERE status = 'pending'
ORDER BY created_at ASC
LIMIT 1
FOR UPDATE;

-- name: MarkTranscriptionJobRunningManual :exec
UPDATE transcription_jobs
SET
  status = 'running',
  updated_at = NOW()
WHERE id = sqlc.arg(id);

-- name: SetTranscriptionJobTotalSegmentsManual :exec
UPDATE transcription_jobs
SET
  total_segments = sqlc.arg(total_segments),
  updated_at = NOW()
WHERE id = sqlc.arg(id);

-- name: UpdateTranscriptionJobProgressManual :exec
UPDATE transcription_jobs
SET
  completed_segments = sqlc.arg(completed_segments),
  failed_segments = sqlc.arg(failed_segments),
  current_annotation_id = sqlc.narg(current_annotation_id),
  current_annotation_json = sqlc.narg(current_annotation_json),
  last_result_annotation_json = sqlc.narg(last_result_annotation_json),
  updated_at = NOW()
WHERE id = sqlc.arg(id);

-- name: CompleteTranscriptionJobManual :exec
UPDATE transcription_jobs
SET
  status = 'completed',
  current_annotation_id = NULL,
  current_annotation_json = NULL,
  updated_at = NOW()
WHERE id = sqlc.arg(id);

-- name: FailTranscriptionJobManual :exec
UPDATE transcription_jobs
SET
  status = 'failed',
  error_message = sqlc.arg(error_message),
  current_annotation_id = NULL,
  current_annotation_json = NULL,
  updated_at = NOW()
WHERE id = sqlc.arg(id);

-- name: WorkspaceOwnsTranscriptionJobManual :one
SELECT EXISTS(
  SELECT 1
  FROM transcription_jobs tj
  JOIN item_images ii ON ii.id = tj.item_image_id
  JOIN items i ON i.id = ii.item_id
  WHERE tj.id = sqlc.arg(job_id)
    AND i.workspace_id = sqlc.arg(workspace_id)
) AS owns_job;
