-- name: UpsertAnnotationMirrorOutbox :exec
INSERT INTO annotation_mirror_outbox (
  item_image_id,
  revision,
  payload,
  status,
  attempt_count,
  next_attempt_at,
  lease_until,
  locked_by,
  last_error
) VALUES (
  sqlc.arg(item_image_id),
  sqlc.arg(revision),
  sqlc.arg(payload),
  'pending',
  0,
  NOW(),
  NULL,
  NULL,
  NULL
)
ON DUPLICATE KEY UPDATE
  payload = IF(VALUES(revision) > revision, VALUES(payload), payload),
  status = IF(VALUES(revision) > revision, 'pending', status),
  attempt_count = IF(VALUES(revision) > revision, 0, attempt_count),
  -- A superseded PUT may still be in flight. Keep the replacement revision
  -- behind the prior lease horizon so the remote writes remain serialized;
  -- revision fencing alone can protect this row but cannot undo a late PUT.
  next_attempt_at = IF(
    VALUES(revision) > revision,
    GREATEST(NOW(), COALESCE(lease_until, NOW())),
    next_attempt_at
  ),
  lease_until = IF(VALUES(revision) > revision, NULL, lease_until),
  locked_by = IF(VALUES(revision) > revision, NULL, locked_by),
  last_error = IF(VALUES(revision) > revision, NULL, last_error),
  revision = IF(VALUES(revision) > revision, VALUES(revision), revision);

-- name: FailExhaustedAnnotationMirrors :exec
UPDATE annotation_mirror_outbox
SET status = 'failed',
    lease_until = NULL,
    locked_by = NULL,
    last_error = COALESCE(last_error, 'delivery lease expired after maximum attempts'),
    updated_at = NOW()
WHERE status = 'processing'
  AND lease_until < NOW()
  AND attempt_count >= max_attempts;

-- name: SelectAnnotationMirrorForUpdate :one
SELECT
  item_image_id,
  revision,
  payload,
  status,
  attempt_count,
  max_attempts,
  next_attempt_at,
  lease_until,
  locked_by,
  last_error,
  created_at,
  updated_at
FROM annotation_mirror_outbox
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

-- name: MarkAnnotationMirrorProcessing :execresult
UPDATE annotation_mirror_outbox
SET status = 'processing',
    attempt_count = attempt_count + 1,
    lease_until = sqlc.arg(lease_until),
    locked_by = sqlc.arg(locked_by),
    updated_at = NOW()
WHERE item_image_id = sqlc.arg(item_image_id)
  AND revision = sqlc.arg(revision)
  AND (
    (status = 'pending' AND next_attempt_at <= NOW())
    OR (status = 'processing' AND lease_until < NOW() AND attempt_count < max_attempts)
  );

-- name: CompleteAnnotationMirror :execresult
DELETE FROM annotation_mirror_outbox
WHERE item_image_id = sqlc.arg(item_image_id)
  AND revision = sqlc.arg(revision)
  AND status = 'processing'
  AND locked_by = sqlc.arg(locked_by);

-- name: RetryAnnotationMirror :execresult
UPDATE annotation_mirror_outbox
SET status = IF(attempt_count >= max_attempts, 'failed', 'pending'),
    next_attempt_at = sqlc.arg(next_attempt_at),
    lease_until = NULL,
    locked_by = NULL,
    last_error = sqlc.arg(last_error),
    updated_at = NOW()
WHERE item_image_id = sqlc.arg(item_image_id)
  AND revision = sqlc.arg(revision)
  AND status = 'processing'
  AND locked_by = sqlc.arg(locked_by);
