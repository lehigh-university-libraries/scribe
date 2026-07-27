-- name: GetItemForWorkspaceManual :one
SELECT
  id,
  user_id,
  workspace_id,
  name,
  source_type,
  source_url,
  source_manifest,
  COALESCE(metadata, JSON_OBJECT()) AS metadata,
  external_reference_id,
  caller_idempotency_key,
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

-- name: LockItemForCleanup :one
SELECT id
FROM items
WHERE id = sqlc.arg(id)
  AND workspace_id = sqlc.arg(workspace_id)
FOR UPDATE;

-- name: LockItemForUseManual :one
SELECT id
FROM items
WHERE id = sqlc.arg(id)
  AND workspace_id = sqlc.arg(workspace_id)
LOCK IN SHARE MODE;

-- name: ListItemImagesForCleanup :many
SELECT ii.id, ii.image_url, ii.storage_bytes, i.workspace_id
FROM item_images ii
JOIN items i ON i.id = ii.item_id
WHERE i.id = sqlc.arg(item_id)
  AND i.workspace_id = sqlc.arg(workspace_id)
ORDER BY ii.id ASC
FOR UPDATE;

-- name: DeleteItemImageForWorkspaceManual :execresult
DELETE ii
FROM item_images ii
JOIN items i ON i.id = ii.item_id
WHERE ii.id = sqlc.arg(id)
  AND i.workspace_id = sqlc.arg(workspace_id);

-- name: LockItemImageForCleanup :one
SELECT ii.id, ii.item_id, ii.image_url, ii.storage_bytes, i.workspace_id
FROM item_images ii
JOIN items i ON i.id = ii.item_id
WHERE ii.id = sqlc.arg(id)
  AND i.workspace_id = sqlc.arg(workspace_id)
FOR UPDATE;

-- name: LockItemImageForUseManual :one
SELECT item_id
FROM item_images
WHERE id = sqlc.arg(id)
  AND workspace_id = sqlc.arg(workspace_id)
LOCK IN SHARE MODE;

-- name: ListItemImagesByWorkspaceManual :many
SELECT
  ii.id,
  ii.item_id,
  ii.sequence,
  ii.image_url,
  ii.storage_bytes,
  ii.canvas_uri,
  ii.width,
  ii.height,
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
  ii.storage_bytes,
  ii.canvas_uri,
  ii.width,
  ii.height,
  ii.label,
  ii.hocr_url,
  ii.created_at,
  ii.updated_at
FROM item_images ii
JOIN items i ON i.id = ii.item_id
WHERE ii.id = sqlc.arg(id)
  AND i.workspace_id = sqlc.arg(workspace_id)
LIMIT 1;

-- name: LockItemImageDimensionsForWorkspaceManual :one
SELECT width, height
FROM item_images
WHERE id = sqlc.arg(id)
  AND workspace_id = sqlc.arg(workspace_id)
FOR UPDATE;

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

-- name: WorkspaceOwnsImageURLManual :one
SELECT EXISTS(
  SELECT 1
  FROM item_images ii
  JOIN items i
    ON i.id = ii.item_id
   AND i.workspace_id = ii.workspace_id
  JOIN workspaces w
    ON w.id = ii.workspace_id
  WHERE ii.image_url = sqlc.arg(image_url)
    AND ii.workspace_id = sqlc.arg(workspace_id)
  LIMIT 1
) AS owns_image_url;

-- name: UserCanReadImageURLManual :one
SELECT EXISTS(
  SELECT 1
  FROM item_images ii
  JOIN items i
    ON i.id = ii.item_id
   AND i.workspace_id = ii.workspace_id
  JOIN workspaces w
    ON w.id = ii.workspace_id
  JOIN workspace_members wm
    ON wm.workspace_id = w.id
  WHERE ii.image_url = sqlc.arg(image_url)
    AND wm.user_id = sqlc.arg(user_id)
  LIMIT 1
) AS can_read_image_url;
