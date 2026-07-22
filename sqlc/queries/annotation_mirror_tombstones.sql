-- name: GetAnnotationMirrorTombstones :one
SELECT item_image_id, generation, annotation_ids, created_at, updated_at
FROM annotation_mirror_tombstones
WHERE item_image_id = sqlc.arg(item_image_id);

-- name: GetAnnotationMirrorTombstonesForUpdate :one
SELECT item_image_id, generation, annotation_ids, created_at, updated_at
FROM annotation_mirror_tombstones
WHERE item_image_id = sqlc.arg(item_image_id)
FOR UPDATE;

-- name: UpsertAnnotationMirrorTombstones :exec
INSERT INTO annotation_mirror_tombstones (
  item_image_id,
  annotation_ids
) VALUES (
  sqlc.arg(item_image_id),
  sqlc.arg(annotation_ids)
)
ON DUPLICATE KEY UPDATE
  generation = generation + 1,
  annotation_ids = VALUES(annotation_ids),
  updated_at = NOW();

-- name: DeleteAnnotationMirrorTombstones :exec
DELETE FROM annotation_mirror_tombstones
WHERE item_image_id = sqlc.arg(item_image_id);
