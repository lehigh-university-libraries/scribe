-- name: InsertGroundTruth :exec
INSERT INTO annotation_ground_truth (
  workspace_id, item_image_id, revision, context_id, document_type, payload,
  original_page, image_url, transcript, marked_by_user_id
) VALUES (
  sqlc.arg(workspace_id), sqlc.arg(item_image_id), sqlc.arg(revision), sqlc.narg(context_id),
  sqlc.arg(document_type), sqlc.arg(payload), sqlc.arg(original_page), sqlc.arg(image_url),
  sqlc.arg(transcript), sqlc.narg(marked_by_user_id)
);

-- name: DeleteGroundTruth :exec
DELETE FROM annotation_ground_truth
WHERE workspace_id = sqlc.arg(workspace_id) AND item_image_id = sqlc.arg(item_image_id)
  AND revision = sqlc.arg(revision);

-- name: HasGroundTruth :one
SELECT EXISTS(SELECT 1 FROM annotation_ground_truth
WHERE workspace_id = sqlc.arg(workspace_id) AND item_image_id = sqlc.arg(item_image_id)
  AND revision = sqlc.arg(revision)) AS marked;

-- name: ListGroundTruthRevisions :many
SELECT gt.item_image_id, gt.revision, OCTET_LENGTH(gt.payload) + OCTET_LENGTH(gt.original_page) + OCTET_LENGTH(gt.transcript) AS payload_bytes
FROM annotation_ground_truth gt
WHERE gt.workspace_id = sqlc.arg(workspace_id)
  AND (sqlc.arg(context_id) = 0 OR gt.context_id = sqlc.arg(context_id))
  AND (sqlc.arg(item_id) = '' OR gt.item_image_id IN (
    SELECT ii.id FROM item_images ii WHERE ii.item_id = sqlc.arg(item_id) AND ii.workspace_id = sqlc.arg(workspace_id)))
ORDER BY gt.item_image_id, gt.revision
LIMIT 101;

-- name: GetGroundTruth :one
SELECT * FROM annotation_ground_truth
WHERE workspace_id = sqlc.arg(workspace_id) AND item_image_id = sqlc.arg(item_image_id)
  AND revision = sqlc.arg(revision);

-- name: SetSegmentationCorrection :exec
UPDATE ocr_runs run JOIN current_ocr_runs current_run ON current_run.session_id = run.session_id
SET run.segmentation_correction = sqlc.narg(segmentation_correction)
WHERE current_run.item_image_id = sqlc.arg(item_image_id)
  AND run.canonical_revision = sqlc.arg(revision);
