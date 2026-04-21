-- name: GetItemForWorkspaceManual :one
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
WHERE id = sqlc.arg(id)
  AND workspace_id = sqlc.arg(workspace_id)
LIMIT 1;

-- name: DeleteItemForWorkspaceManual :execresult
DELETE FROM items
WHERE id = sqlc.arg(id)
  AND workspace_id = sqlc.arg(workspace_id);

-- name: ListItemImagesByWorkspaceManual :many
SELECT
  ii.id,
  ii.item_id,
  ii.sequence,
  ii.image_url,
  ii.canvas_uri,
  ii.label,
  ii.hocr_url,
  ii.created_at,
  ii.updated_at
FROM item_images ii
JOIN items i ON i.id = ii.item_id
WHERE i.workspace_id = sqlc.arg(workspace_id)
ORDER BY ii.item_id ASC, ii.sequence ASC;

-- name: GetItemImageForWorkspaceManual :one
SELECT
  ii.id,
  ii.item_id,
  ii.sequence,
  ii.image_url,
  ii.canvas_uri,
  ii.label,
  ii.hocr_url,
  ii.created_at,
  ii.updated_at
FROM item_images ii
JOIN items i ON i.id = ii.item_id
WHERE ii.id = sqlc.arg(id)
  AND i.workspace_id = sqlc.arg(workspace_id)
LIMIT 1;

-- name: GetItemImageByCanvasURIForWorkspaceManual :one
SELECT
  ii.id,
  ii.item_id,
  ii.sequence,
  ii.image_url,
  ii.canvas_uri,
  ii.label,
  ii.hocr_url,
  ii.created_at,
  ii.updated_at
FROM item_images ii
JOIN items i ON i.id = ii.item_id
WHERE ii.canvas_uri = sqlc.arg(canvas_uri)
  AND i.workspace_id = sqlc.arg(workspace_id)
LIMIT 1;

-- name: WorkspaceOwnsItemManual :one
SELECT EXISTS(
  SELECT 1
  FROM items
  WHERE id = sqlc.arg(item_id)
    AND workspace_id = sqlc.arg(workspace_id)
) AS owns_item;

-- name: WorkspaceOwnsItemImageManual :one
SELECT EXISTS(
  SELECT 1
  FROM item_images ii
  JOIN items i ON i.id = ii.item_id
  WHERE ii.id = sqlc.arg(item_image_id)
    AND i.workspace_id = sqlc.arg(workspace_id)
) AS owns_item_image;
