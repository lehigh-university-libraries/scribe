-- name: InsertUploadBatchManual :exec
INSERT INTO upload_batches (
  workspace_id,
  id,
  item_id,
  context_id,
  context_scope_id,
  context_snapshot,
  request_hash,
  creation_token,
  status
) SELECT
  sqlc.arg(workspace_id),
  sqlc.arg(id),
  sqlc.arg(item_id),
  sqlc.narg(context_id),
  c.scope_id,
  sqlc.arg(context_snapshot),
  sqlc.arg(request_hash),
  sqlc.arg(creation_token),
  'in_progress'
FROM items i
JOIN contexts c
  ON c.id = sqlc.narg(context_id)
 AND (c.workspace_id IS NULL OR c.workspace_id = i.workspace_id)
WHERE i.id = sqlc.arg(item_id)
  AND i.workspace_id = sqlc.arg(workspace_id)
ON DUPLICATE KEY UPDATE id = VALUES(id);

-- name: GetUploadBatchManual :one
SELECT
  workspace_id,
  id,
  item_id,
  context_id,
  context_scope_id,
  context_snapshot,
  request_hash,
  creation_token,
  status,
  created_at,
  updated_at
FROM upload_batches
WHERE workspace_id = sqlc.arg(workspace_id)
  AND id = sqlc.arg(id)
LIMIT 1;

-- name: LockUploadBatchManual :one
SELECT
  workspace_id,
  id,
  item_id,
  context_id,
  context_scope_id,
  context_snapshot,
  request_hash,
  creation_token,
  status,
  created_at,
  updated_at
FROM upload_batches
WHERE workspace_id = sqlc.arg(workspace_id)
  AND id = sqlc.arg(id)
FOR UPDATE;

-- name: InsertUploadBatchFileManual :execrows
INSERT INTO upload_batch_files (
  workspace_id,
  batch_id,
  sequence,
  filename,
  size,
  content_sha256
) SELECT
  ub.workspace_id,
  ub.id,
  sqlc.arg(sequence),
  sqlc.arg(filename),
  sqlc.arg(size),
  sqlc.arg(content_sha256)
FROM upload_batches ub
WHERE ub.workspace_id = sqlc.arg(workspace_id)
  AND ub.id = sqlc.arg(batch_id);

-- name: ListUploadBatchFilesManual :many
SELECT
  workspace_id,
  batch_id,
  sequence,
  filename,
  size,
  content_sha256,
  status,
  attempt_count,
  max_attempts,
  lease_until,
  locked_by,
  item_image_id,
  transcription_job_id,
  error_message,
  created_at,
  updated_at
FROM upload_batch_files
WHERE workspace_id = sqlc.arg(workspace_id)
  AND batch_id = sqlc.arg(batch_id)
ORDER BY sequence ASC;

-- name: LockUploadBatchFileManual :one
SELECT
  workspace_id,
  batch_id,
  sequence,
  filename,
  size,
  content_sha256,
  status,
  attempt_count,
  max_attempts,
  lease_until,
  locked_by,
  item_image_id,
  transcription_job_id,
  error_message,
  created_at,
  updated_at
FROM upload_batch_files
WHERE workspace_id = sqlc.arg(workspace_id)
  AND batch_id = sqlc.arg(batch_id)
  AND sequence = sqlc.arg(sequence)
FOR UPDATE;

-- name: ClaimUploadBatchFileManual :execrows
UPDATE upload_batch_files
SET
  status = 'processing',
  attempt_count = attempt_count + 1,
  lease_until = sqlc.arg(lease_until),
  locked_by = sqlc.arg(locked_by),
  error_message = NULL,
  updated_at = NOW()
WHERE workspace_id = sqlc.arg(workspace_id)
  AND batch_id = sqlc.arg(batch_id)
  AND sequence = sqlc.arg(sequence)
  AND attempt_count < max_attempts
  AND (
    status IN ('pending', 'failed')
    OR (status = 'processing' AND lease_until < NOW())
  );

-- name: RenewUploadBatchFileManual :execrows
UPDATE upload_batch_files ubf
SET
  -- Always advance the stored value so MySQL reports one affected row even
  -- when multiple checkpoints renew within the same DATETIME second.
  ubf.lease_until = GREATEST(DATE_ADD(ubf.lease_until, INTERVAL 1 SECOND), sqlc.arg(lease_until)),
  ubf.updated_at = NOW()
WHERE ubf.workspace_id = sqlc.arg(workspace_id)
  AND ubf.batch_id = sqlc.arg(batch_id)
  AND ubf.sequence = sqlc.arg(sequence)
  AND ubf.status = 'processing'
  AND ubf.locked_by = sqlc.arg(locked_by)
  AND ubf.lease_until > NOW()
  AND EXISTS (
    SELECT 1
    FROM upload_batches ub
    WHERE ub.workspace_id = sqlc.arg(workspace_id)
      AND ub.id = sqlc.arg(batch_id)
      AND ub.status = 'in_progress'
  );

-- name: FailUploadBatchFileManual :execrows
UPDATE upload_batch_files
SET
  status = 'failed',
  lease_until = NULL,
  locked_by = NULL,
  error_message = sqlc.narg(error_message),
  updated_at = NOW()
WHERE workspace_id = sqlc.arg(workspace_id)
  AND batch_id = sqlc.arg(batch_id)
  AND sequence = sqlc.arg(sequence)
  AND status = 'processing'
  AND locked_by = sqlc.arg(locked_by);

-- name: EnsureUploadBatchImageManual :execresult
INSERT INTO item_images (
  workspace_id,
  item_id,
  sequence,
  image_url,
  storage_bytes,
  width,
  height,
  label
)
SELECT
  ub.workspace_id,
  ub.item_id,
  ubf.sequence,
  sqlc.arg(image_url),
  sqlc.arg(storage_bytes),
  sqlc.arg(width),
  sqlc.arg(height),
  ubf.filename
FROM upload_batches ub
JOIN upload_batch_files ubf
  ON ubf.workspace_id = ub.workspace_id
 AND ubf.batch_id = ub.id
WHERE ub.workspace_id = sqlc.arg(workspace_id)
  AND ub.id = sqlc.arg(batch_id)
  AND ub.status = 'in_progress'
  AND ubf.sequence = sqlc.arg(sequence)
  AND ubf.status = 'processing'
  AND ubf.locked_by = sqlc.arg(locked_by)
  AND ubf.lease_until > NOW()
ON DUPLICATE KEY UPDATE id = LAST_INSERT_ID(item_images.id);

-- name: CompleteUploadBatchFileManual :execrows
UPDATE upload_batch_files ubf
SET
  ubf.status = 'completed',
  ubf.lease_until = NULL,
  ubf.locked_by = NULL,
  ubf.item_image_id = sqlc.arg(item_image_id),
  ubf.transcription_job_id = sqlc.arg(transcription_job_id),
  ubf.error_message = NULL,
  ubf.updated_at = NOW()
WHERE ubf.workspace_id = sqlc.arg(workspace_id)
  AND ubf.batch_id = sqlc.arg(batch_id)
  AND ubf.sequence = sqlc.arg(sequence)
  AND ubf.status = 'processing'
  AND ubf.locked_by = sqlc.arg(locked_by)
  AND ubf.lease_until > NOW()
  AND EXISTS (
    SELECT 1
    FROM upload_batches ub
    JOIN item_images ii
      ON ii.workspace_id = ub.workspace_id
     AND ii.item_id = ub.item_id
     AND ii.id = sqlc.narg(item_image_id)
    JOIN transcription_jobs tj
      ON tj.workspace_id = ub.workspace_id
     AND tj.item_image_id = ii.id
     AND tj.id = sqlc.narg(transcription_job_id)
    WHERE ub.workspace_id = sqlc.arg(workspace_id)
      AND ub.id = sqlc.arg(batch_id)
      AND ub.status = 'in_progress'
  );

-- name: LockUploadBatchCompletionImageManual :one
SELECT ii.id
FROM upload_batches ub
JOIN item_images ii
  ON ii.workspace_id = ub.workspace_id
 AND ii.item_id = ub.item_id
WHERE ub.workspace_id = sqlc.arg(workspace_id)
  AND ub.id = sqlc.arg(batch_id)
  AND ii.id = sqlc.arg(item_image_id)
FOR UPDATE;

-- name: LockUploadBatchCompletionJobManual :one
SELECT tj.id
FROM upload_batches ub
JOIN item_images ii
  ON ii.workspace_id = ub.workspace_id
 AND ii.item_id = ub.item_id
JOIN transcription_jobs tj
  ON tj.workspace_id = ub.workspace_id
 AND tj.item_image_id = ii.id
WHERE ub.workspace_id = sqlc.arg(workspace_id)
  AND ub.id = sqlc.arg(batch_id)
  AND ii.id = sqlc.arg(item_image_id)
  AND tj.id = sqlc.arg(transcription_job_id)
FOR UPDATE;

-- name: CompleteUploadBatchIfReadyManual :execrows
UPDATE upload_batches ub
SET
  ub.status = 'completed',
  ub.context_id = NULL,
  ub.context_scope_id = NULL,
  ub.updated_at = NOW()
WHERE ub.workspace_id = sqlc.arg(workspace_id)
  AND ub.id = sqlc.arg(batch_id)
  AND ub.status = 'in_progress'
  AND NOT EXISTS (
    SELECT 1
    FROM upload_batch_files ubf
    WHERE ubf.workspace_id = ub.workspace_id
      AND ubf.batch_id = ub.id
      AND ubf.status <> 'completed'
  );

-- name: CancelUploadBatchManual :execrows
UPDATE upload_batches
SET
  status = 'canceled',
  context_id = NULL,
  context_scope_id = NULL,
  updated_at = NOW()
WHERE workspace_id = sqlc.arg(workspace_id)
  AND id = sqlc.arg(id)
  AND status = 'in_progress';

-- name: CancelUploadBatchFilesManual :exec
UPDATE upload_batch_files
SET
  status = 'canceled',
  lease_until = NULL,
  locked_by = NULL,
  error_message = NULL,
  updated_at = NOW()
WHERE workspace_id = sqlc.arg(workspace_id)
  AND batch_id = sqlc.arg(batch_id)
  AND status IN ('pending', 'processing', 'failed');

-- name: LockUploadBatchFileImageForCleanupManual :one
SELECT ii.id, ii.item_id, ii.image_url, ii.storage_bytes
FROM item_images ii
JOIN upload_batches ub
  ON ub.item_id = ii.item_id
JOIN upload_batch_files ubf
  ON ubf.workspace_id = ub.workspace_id
 AND ubf.batch_id = ub.id
 AND ubf.sequence = ii.sequence
WHERE ub.workspace_id = sqlc.arg(workspace_id)
  AND ub.id = sqlc.arg(batch_id)
  AND ubf.sequence = sqlc.arg(sequence)
FOR UPDATE;

-- name: ListIncompleteUploadBatchImagesForCleanupManual :many
SELECT ii.id, ii.item_id, ii.image_url, ii.storage_bytes, ubf.sequence
FROM item_images ii
JOIN upload_batches ub
  ON ub.item_id = ii.item_id
JOIN upload_batch_files ubf
  ON ubf.workspace_id = ub.workspace_id
 AND ubf.batch_id = ub.id
 AND ubf.sequence = ii.sequence
WHERE ub.workspace_id = sqlc.arg(workspace_id)
  AND ub.id = sqlc.arg(batch_id)
  AND ubf.status IN ('pending', 'processing', 'failed', 'canceled')
FOR UPDATE;

-- name: ClearUploadBatchFileResourcesManual :execrows
UPDATE upload_batch_files
SET item_image_id = NULL,
    transcription_job_id = NULL,
    updated_at = GREATEST(DATE_ADD(updated_at, INTERVAL 1 SECOND), NOW())
WHERE workspace_id = sqlc.arg(workspace_id)
  AND batch_id = sqlc.arg(batch_id)
  AND sequence = sqlc.arg(sequence);

-- name: CancelUploadBatchJobsManual :exec
UPDATE transcription_jobs tj
JOIN upload_batch_files ubf
  ON ubf.transcription_job_id = tj.id
LEFT JOIN transcription_job_attempts tja
  ON tja.job_id = tj.id
 AND tja.attempt_number = tj.attempt_count
 AND tja.input_revision = tj.input_revision
 AND tja.lease_token = tj.locked_by
 AND tja.outcome = 'running'
SET
  tja.outcome = 'canceled',
  tja.safe_error_message = 'canceled with upload batch',
  tja.finished_at = NOW(6),
  tj.status = 'canceled',
  tj.lease_until = NULL,
  tj.locked_by = NULL,
  tj.error_message = 'canceled with upload batch',
  tj.last_result_annotation_json = NULL,
  tj.updated_at = NOW()
WHERE ubf.workspace_id = sqlc.arg(workspace_id)
  AND ubf.batch_id = sqlc.arg(batch_id)
  AND tj.status IN ('pending', 'running');
