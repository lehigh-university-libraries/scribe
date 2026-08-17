-- name: UpsertResourceCleanup :exec
INSERT INTO resource_cleanup_outbox (
  kind,
  resource_key,
  workspace_id,
  storage_bytes,
  next_attempt_at,
  status,
  attempt_count,
  lease_until,
  locked_by,
  last_error
) VALUES (
  sqlc.arg(kind),
  sqlc.arg(resource_key),
  sqlc.arg(workspace_id),
  sqlc.arg(storage_bytes),
  sqlc.arg(next_attempt_at),
  'pending',
  0,
  NULL,
  NULL,
  NULL
)
ON DUPLICATE KEY UPDATE
  workspace_id = IF(VALUES(workspace_id) > 0, VALUES(workspace_id), workspace_id),
  storage_bytes = GREATEST(storage_bytes, VALUES(storage_bytes)),
  generation = generation + 1,
  status = 'pending',
  attempt_count = 0,
  -- A superseded remote operation can still be in flight after its generation
  -- is fenced in this row. Do not let the replacement run until that bounded
  -- operation's lease horizon has passed.
  next_attempt_at = GREATEST(VALUES(next_attempt_at), COALESCE(lease_until, NOW())),
  lease_until = NULL,
  locked_by = NULL,
  last_error = NULL,
  updated_at = NOW();

-- name: LockResourceCleanupByKindKey :one
SELECT
  id,
  kind,
  resource_key,
  workspace_id,
  storage_bytes,
  generation,
  delete_fenced_at,
  status,
  attempt_count,
  max_attempts,
  next_attempt_at,
  lease_until,
  locked_by,
  last_error,
  created_at,
  updated_at
FROM resource_cleanup_outbox
WHERE kind = sqlc.arg(kind)
  AND resource_key = sqlc.arg(resource_key)
FOR UPDATE;

-- name: ResizeStagedUploadCleanup :exec
UPDATE resource_cleanup_outbox
SET workspace_id = sqlc.arg(workspace_id),
    storage_bytes = GREATEST(storage_bytes, sqlc.arg(storage_bytes)),
    next_attempt_at = GREATEST(next_attempt_at, sqlc.arg(next_attempt_at)),
    updated_at = NOW()
WHERE kind = 'upload_blob'
  AND resource_key = sqlc.arg(resource_key);

-- name: FenceUploadResourceCleanup :execresult
UPDATE resource_cleanup_outbox
SET delete_fenced_at = COALESCE(delete_fenced_at, NOW()),
    updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND generation = sqlc.arg(generation)
  AND kind = 'upload_blob'
  AND status = 'processing'
  AND locked_by = sqlc.arg(locked_by);

-- name: DeleteResourceCleanupByKindKey :execresult
DELETE FROM resource_cleanup_outbox
WHERE kind = sqlc.arg(kind)
  AND resource_key = sqlc.arg(resource_key);

-- name: FailExhaustedResourceCleanups :exec
UPDATE resource_cleanup_outbox
SET status = IF(kind = 'upload_blob', 'pending', 'failed'),
    next_attempt_at = IF(kind = 'upload_blob', DATE_ADD(NOW(), INTERVAL 1 HOUR), next_attempt_at),
    lease_until = NULL,
    locked_by = NULL,
    last_error = COALESCE(
      last_error,
      IF(
        kind = 'upload_blob',
        'cleanup lease expired; another retry is scheduled',
        'cleanup lease expired after maximum attempts'
      )
    ),
    updated_at = NOW()
WHERE status = 'processing'
  AND lease_until < NOW()
  AND attempt_count >= max_attempts;

-- name: SelectResourceCleanupForUpdate :one
SELECT
  id,
  kind,
  resource_key,
  workspace_id,
  storage_bytes,
  generation,
  delete_fenced_at,
  status,
  attempt_count,
  max_attempts,
  next_attempt_at,
  lease_until,
  locked_by,
  last_error,
  created_at,
  updated_at
FROM resource_cleanup_outbox
WHERE (
    status = 'pending'
    AND next_attempt_at <= NOW()
  ) OR (
    status = 'processing'
    AND lease_until < NOW()
    AND attempt_count < max_attempts
  )
ORDER BY next_attempt_at ASC, updated_at ASC
LIMIT 1
FOR UPDATE SKIP LOCKED;

-- name: MarkResourceCleanupProcessing :execresult
UPDATE resource_cleanup_outbox
SET status = 'processing',
    attempt_count = attempt_count + 1,
    lease_until = sqlc.arg(lease_until),
    locked_by = sqlc.arg(locked_by),
    updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND generation = sqlc.arg(generation)
  AND (
    (status = 'pending' AND next_attempt_at <= NOW())
    OR (status = 'processing' AND lease_until < NOW() AND attempt_count < max_attempts)
  );

-- name: CompleteResourceCleanup :execresult
DELETE FROM resource_cleanup_outbox
WHERE id = sqlc.arg(id)
  AND generation = sqlc.arg(generation)
  AND status = 'processing'
  AND locked_by = sqlc.arg(locked_by)
  AND (kind <> 'upload_blob' OR delete_fenced_at IS NULL);

-- name: CompleteFencedUploadResourceCleanup :execresult
DELETE FROM resource_cleanup_outbox
WHERE id = sqlc.arg(id)
  AND generation = sqlc.arg(generation)
  AND kind = 'upload_blob'
  AND delete_fenced_at IS NOT NULL
  AND status = 'processing'
  AND locked_by = sqlc.arg(locked_by);

-- name: RetryResourceCleanup :execresult
UPDATE resource_cleanup_outbox
SET status = IF(kind = 'upload_blob', 'pending', IF(attempt_count >= max_attempts, 'failed', 'pending')),
    next_attempt_at = sqlc.arg(next_attempt_at),
    lease_until = NULL,
    locked_by = NULL,
    last_error = sqlc.arg(last_error),
    updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND generation = sqlc.arg(generation)
  AND status = 'processing'
  AND locked_by = sqlc.arg(locked_by);
