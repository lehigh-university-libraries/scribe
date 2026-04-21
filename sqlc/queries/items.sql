-- name: CreateItemManual :exec
INSERT INTO items (
  id,
  user_id,
  workspace_id,
  name,
  source_type,
  source_url,
  metadata
) VALUES (
  sqlc.arg(id),
  sqlc.arg(user_id),
  sqlc.arg(workspace_id),
  sqlc.arg(name),
  sqlc.arg(source_type),
  sqlc.narg(source_url),
  sqlc.narg(metadata)
);

-- name: GetItemManual :one
SELECT
  id,
  user_id,
  workspace_id,
  name,
  source_type,
  source_url,
  COALESCE(metadata, JSON_OBJECT()) AS metadata,
  created_at,
  updated_at
FROM items
WHERE id = sqlc.arg(id);

-- name: ListItemsManual :many
SELECT
  id,
  user_id,
  workspace_id,
  name,
  source_type,
  source_url,
  COALESCE(metadata, JSON_OBJECT()) AS metadata,
  created_at,
  updated_at
FROM items
WHERE workspace_id = sqlc.arg(workspace_id)
ORDER BY created_at DESC;

-- name: DeleteItemManual :exec
DELETE FROM items
WHERE id = sqlc.arg(id);

-- name: UpdateItemMetadataManual :exec
UPDATE items
SET metadata = sqlc.narg(metadata)
WHERE id = sqlc.arg(id);

-- name: CreateItemImageManual :execresult
INSERT INTO item_images (
  item_id,
  sequence,
  image_url,
  canvas_uri,
  label,
  hocr_url
) VALUES (
  sqlc.arg(item_id),
  sqlc.arg(sequence),
  sqlc.arg(image_url),
  sqlc.narg(canvas_uri),
  sqlc.narg(label),
  sqlc.narg(hocr_url)
)
ON DUPLICATE KEY UPDATE
  image_url = VALUES(image_url),
  canvas_uri = VALUES(canvas_uri),
  label = VALUES(label),
  hocr_url = VALUES(hocr_url);

-- name: GetItemImageManual :one
SELECT
  id,
  item_id,
  sequence,
  image_url,
  canvas_uri,
  label,
  hocr_url,
  created_at,
  updated_at
FROM item_images
WHERE id = sqlc.arg(id);

-- name: ListItemImagesManual :many
SELECT
  id,
  item_id,
  sequence,
  image_url,
  canvas_uri,
  label,
  hocr_url,
  created_at,
  updated_at
FROM item_images
WHERE item_id = sqlc.arg(item_id)
ORDER BY sequence ASC;

-- name: GetItemImageByCanvasURIManual :one
SELECT
  id,
  item_id,
  sequence,
  image_url,
  canvas_uri,
  label,
  hocr_url,
  created_at,
  updated_at
FROM item_images
WHERE canvas_uri = sqlc.arg(canvas_uri)
LIMIT 1;

-- name: UpdateItemImageCanvasURIManual :exec
UPDATE item_images
SET canvas_uri = sqlc.arg(canvas_uri)
WHERE id = sqlc.arg(id);
