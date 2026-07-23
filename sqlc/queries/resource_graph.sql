-- The application owns relational lifecycle semantics. Every query below is
-- scoped by the authoritative workspace/item pair and optionally one image.
-- Passing item_image_id = 0 selects the complete item graph.

-- name: DeleteExternalRequestsForItemResourceGraph :exec
DELETE er
FROM external_requests er
WHERE er.workspace_id = sqlc.arg(workspace_id)
  AND (
    (
      sqlc.arg(item_image_id) = 0
      AND er.item_id = sqlc.narg(nullable_item_id)
    )
    OR EXISTS (
      SELECT 1
      FROM item_images target
      WHERE target.workspace_id = sqlc.arg(workspace_id)
        AND target.item_id = sqlc.arg(item_id)
        AND (sqlc.arg(item_image_id) = 0 OR target.id = sqlc.arg(item_image_id))
        AND target.id = er.item_image_id
    )
    OR EXISTS (
      SELECT 1
      FROM transcription_jobs tj
      JOIN item_images target ON target.id = tj.item_image_id
      WHERE target.workspace_id = sqlc.arg(workspace_id)
        AND target.item_id = sqlc.arg(item_id)
        AND (sqlc.arg(item_image_id) = 0 OR target.id = sqlc.arg(item_image_id))
        AND tj.workspace_id = sqlc.arg(workspace_id)
        AND tj.id = er.transcription_job_id
    )
    OR EXISTS (
      SELECT 1
      FROM ocr_runs run
      JOIN item_images target ON target.id = run.item_image_id
      WHERE target.workspace_id = sqlc.arg(workspace_id)
        AND target.item_id = sqlc.arg(item_id)
        AND (sqlc.arg(item_image_id) = 0 OR target.id = sqlc.arg(item_image_id))
        AND run.workspace_id = sqlc.arg(workspace_id)
        AND run.session_id = er.session_id
    )
  );

-- name: DeleteUploadBatchFilesForItemResourceGraph :exec
DELETE ubf
FROM upload_batch_files ubf
WHERE ubf.workspace_id = sqlc.arg(workspace_id)
  AND EXISTS (
    SELECT 1
    FROM upload_batches ub
    WHERE ub.workspace_id = sqlc.arg(workspace_id)
      AND ub.id = ubf.batch_id
      AND ub.item_id = sqlc.arg(item_id)
  );

-- name: DetachUploadBatchFilesForItemImageResourceGraph :exec
UPDATE upload_batch_files ubf
LEFT JOIN transcription_jobs tj
  ON tj.workspace_id = ubf.workspace_id
 AND tj.id = ubf.transcription_job_id
SET ubf.item_image_id = NULL,
    ubf.transcription_job_id = NULL,
    ubf.updated_at = GREATEST(DATE_ADD(ubf.updated_at, INTERVAL 1 SECOND), NOW())
WHERE ubf.workspace_id = sqlc.arg(workspace_id)
  AND EXISTS (
    SELECT 1
    FROM item_images target
    WHERE target.workspace_id = sqlc.arg(workspace_id)
      AND target.item_id = sqlc.arg(item_id)
      AND target.id = sqlc.arg(item_image_id)
      AND (target.id = ubf.item_image_id OR target.id = tj.item_image_id)
  );

-- name: DeleteTranscriptionAttemptsForItemResourceGraph :exec
DELETE attempt
FROM transcription_job_attempts attempt
JOIN transcription_jobs job ON job.id = attempt.job_id
JOIN item_images target ON target.id = job.item_image_id
WHERE target.workspace_id = sqlc.arg(workspace_id)
  AND target.item_id = sqlc.arg(item_id)
  AND (sqlc.arg(item_image_id) = 0 OR target.id = sqlc.arg(item_image_id))
  AND job.workspace_id = sqlc.arg(workspace_id);

-- name: DeleteTranscriptionJobsForItemResourceGraph :exec
DELETE job
FROM transcription_jobs job
JOIN item_images target ON target.id = job.item_image_id
WHERE target.workspace_id = sqlc.arg(workspace_id)
  AND target.item_id = sqlc.arg(item_id)
  AND (sqlc.arg(item_image_id) = 0 OR target.id = sqlc.arg(item_image_id))
  AND job.workspace_id = sqlc.arg(workspace_id);

-- name: DeleteProviderAuditsForItemResourceGraph :exec
DELETE audit
FROM provider_call_audits audit
JOIN item_images target ON target.id = audit.item_image_id
WHERE target.workspace_id = sqlc.arg(workspace_id)
  AND target.item_id = sqlc.arg(item_id)
  AND (sqlc.arg(item_image_id) = 0 OR target.id = sqlc.arg(item_image_id))
  AND audit.workspace_id = sqlc.arg(workspace_id);

-- name: DeleteCurrentOCRRunsForItemResourceGraph :exec
DELETE current_run
FROM current_ocr_runs current_run
JOIN item_images target ON target.id = current_run.item_image_id
WHERE target.workspace_id = sqlc.arg(workspace_id)
  AND target.item_id = sqlc.arg(item_id)
  AND (sqlc.arg(item_image_id) = 0 OR target.id = sqlc.arg(item_image_id));

-- name: DeleteOCRRunsForItemResourceGraph :exec
DELETE run
FROM ocr_runs run
JOIN item_images target ON target.id = run.item_image_id
WHERE target.workspace_id = sqlc.arg(workspace_id)
  AND target.item_id = sqlc.arg(item_id)
  AND (sqlc.arg(item_image_id) = 0 OR target.id = sqlc.arg(item_image_id))
  AND run.workspace_id = sqlc.arg(workspace_id);

-- name: DeleteAnnotationMirrorsForItemResourceGraph :exec
DELETE mirror
FROM annotation_mirror_outbox mirror
JOIN item_images target ON target.id = mirror.item_image_id
WHERE target.workspace_id = sqlc.arg(workspace_id)
  AND target.item_id = sqlc.arg(item_id)
  AND (sqlc.arg(item_image_id) = 0 OR target.id = sqlc.arg(item_image_id));

-- name: DeleteAnnotationMirrorTombstonesForItemResourceGraph :exec
DELETE tombstone
FROM annotation_mirror_tombstones tombstone
JOIN item_images target ON target.id = tombstone.item_image_id
WHERE target.workspace_id = sqlc.arg(workspace_id)
  AND target.item_id = sqlc.arg(item_id)
  AND (sqlc.arg(item_image_id) = 0 OR target.id = sqlc.arg(item_image_id));

-- name: DeletePublishedPagesForItemResourceGraph :exec
DELETE published
FROM published_annotation_pages published
JOIN item_images target ON target.id = published.item_image_id
WHERE target.workspace_id = sqlc.arg(workspace_id)
  AND target.item_id = sqlc.arg(item_id)
  AND (sqlc.arg(item_image_id) = 0 OR target.id = sqlc.arg(item_image_id))
  AND published.workspace_id = sqlc.arg(workspace_id);

-- name: DeleteAnnotationIndexForItemResourceGraph :exec
DELETE annotation
FROM annotations annotation
JOIN item_images target ON target.id = annotation.item_image_id
WHERE target.workspace_id = sqlc.arg(workspace_id)
  AND target.item_id = sqlc.arg(item_id)
  AND (sqlc.arg(item_image_id) = 0 OR target.id = sqlc.arg(item_image_id))
  AND annotation.workspace_id = sqlc.arg(workspace_id);

-- name: DeleteAnnotationPagesForItemResourceGraph :exec
DELETE page
FROM annotation_pages page
JOIN item_images target ON target.id = page.item_image_id
WHERE target.workspace_id = sqlc.arg(workspace_id)
  AND target.item_id = sqlc.arg(item_id)
  AND (sqlc.arg(item_image_id) = 0 OR target.id = sqlc.arg(item_image_id))
  AND page.workspace_id = sqlc.arg(workspace_id);

-- name: DeleteUploadBatchesForItemResourceGraph :exec
DELETE FROM upload_batches
WHERE workspace_id = sqlc.arg(workspace_id)
  AND item_id = sqlc.arg(item_id);

-- name: DeleteItemImagesForItemResourceGraph :exec
DELETE FROM item_images
WHERE workspace_id = sqlc.arg(workspace_id)
  AND item_id = sqlc.arg(item_id);
