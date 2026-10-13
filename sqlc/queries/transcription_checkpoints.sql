-- name: GetTranscriptionCheckpoint :one
SELECT c.payload FROM transcription_jobs j
LEFT JOIN transcription_job_checkpoints c ON c.job_id = j.id AND c.line_id = ''
WHERE j.id = sqlc.arg(id)
  AND j.status = 'running'
  AND j.attempt_count = sqlc.arg(attempt_number)
  AND j.input_revision = sqlc.arg(input_revision)
  AND j.locked_by = sqlc.arg(lease_token)
  AND j.lease_until > NOW();

-- name: ListTranscriptionLineCheckpoints :many
SELECT line_id, payload FROM transcription_job_checkpoints
WHERE job_id = ? AND line_id <> '';

-- name: SaveTranscriptionCheckpoint :exec
INSERT INTO transcription_job_checkpoints (job_id, line_id, payload)
VALUES (?, ?, ?)
ON DUPLICATE KEY UPDATE payload = VALUES(payload);

-- name: DeleteTranscriptionCheckpoints :exec
DELETE FROM transcription_job_checkpoints WHERE job_id = ?;

-- name: DeleteCheckpointsForItemResourceGraph :exec
DELETE c FROM transcription_job_checkpoints c
JOIN transcription_jobs j ON j.id = c.job_id
JOIN item_images i ON i.id = j.item_image_id
WHERE j.workspace_id = sqlc.arg(workspace_id)
  AND i.item_id = sqlc.arg(item_id)
  AND (sqlc.arg(item_image_id) = 0 OR i.id = sqlc.arg(item_image_id));

-- name: LockCheckpointJobsForItemResourceGraph :many
SELECT j.id FROM transcription_jobs j
JOIN item_images i ON i.id = j.item_image_id
WHERE j.workspace_id = sqlc.arg(workspace_id)
  AND i.item_id = sqlc.arg(item_id)
  AND (sqlc.arg(item_image_id) = 0 OR i.id = sqlc.arg(item_image_id))
ORDER BY j.id
FOR UPDATE;

-- name: DeleteCheckpointsForUploadBatch :exec
DELETE c FROM transcription_job_checkpoints c
JOIN upload_batch_files ubf ON ubf.transcription_job_id = c.job_id
WHERE ubf.workspace_id = sqlc.arg(workspace_id)
  AND ubf.batch_id = sqlc.arg(batch_id);
