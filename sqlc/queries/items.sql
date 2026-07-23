-- name: CreateItemManual :exec
INSERT INTO items (
  id,
  user_id,
  workspace_id,
  name,
  source_type,
  source_url,
  source_manifest,
  metadata
) VALUES (
  sqlc.arg(id),
  sqlc.arg(user_id),
  sqlc.arg(workspace_id),
  sqlc.arg(name),
  sqlc.arg(source_type),
  sqlc.narg(source_url),
  sqlc.narg(source_manifest),
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
  source_manifest,
  COALESCE(metadata, JSON_OBJECT()) AS metadata,
  created_at,
  updated_at
FROM items
WHERE id = sqlc.arg(id);

-- name: ListItemSummariesPageManual :many
SELECT
  id,
  name,
  source_type,
  created_at,
  updated_at
FROM items
WHERE workspace_id = sqlc.arg(workspace_id)
  AND (
    name LIKE sqlc.arg(filter_pattern) ESCAPE '!'
    OR id LIKE sqlc.arg(filter_pattern) ESCAPE '!'
    OR CAST(source_type AS CHAR) LIKE CAST(sqlc.arg(filter_pattern) AS CHAR) ESCAPE '!'
  )
  AND (
    sqlc.narg(cursor_created_at) IS NULL
    OR created_at < sqlc.narg(cursor_created_at)
    OR (
      created_at = sqlc.narg(cursor_created_at)
      AND id < sqlc.arg(cursor_id)
    )
  )
ORDER BY created_at DESC, id DESC
LIMIT ?;

-- name: ListItemPreviewsForItemsPageManual :many
SELECT
  ranked.id,
  ranked.workspace_id,
  ranked.item_id,
  ranked.sequence,
  ranked.image_url,
  ranked.storage_bytes,
  ranked.canvas_uri,
  ranked.width,
  ranked.height,
  ranked.label,
  ranked.hocr_url,
  ranked.created_at,
  ranked.updated_at,
  ranked.image_count
FROM (
  SELECT
    ii.id,
    ii.workspace_id,
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
    ii.updated_at,
    CAST(COUNT(*) OVER (PARTITION BY ii.item_id) AS UNSIGNED) AS image_count,
    ROW_NUMBER() OVER (PARTITION BY ii.item_id ORDER BY ii.sequence ASC, ii.id ASC) AS image_rank
  FROM item_images ii
  JOIN (
    SELECT i.id
    FROM items i
    WHERE i.workspace_id = sqlc.arg(workspace_id)
      AND (
        i.name LIKE sqlc.arg(filter_pattern) ESCAPE '!'
        OR i.id LIKE sqlc.arg(filter_pattern) ESCAPE '!'
        OR CAST(i.source_type AS CHAR) LIKE CAST(sqlc.arg(filter_pattern) AS CHAR) ESCAPE '!'
      )
      AND (
        sqlc.narg(cursor_created_at) IS NULL
        OR i.created_at < sqlc.narg(cursor_created_at)
        OR (
          i.created_at = sqlc.narg(cursor_created_at)
          AND i.id < sqlc.arg(cursor_id)
        )
      )
    ORDER BY i.created_at DESC, i.id DESC
    LIMIT ?
  ) page ON page.id = ii.item_id
) ranked
WHERE ranked.image_rank = 1
ORDER BY ranked.item_id ASC;

-- name: UpdateItemMetadataManual :exec
UPDATE items
SET metadata = sqlc.narg(metadata)
WHERE id = sqlc.arg(id);

-- name: CreateItemImageManual :execresult
INSERT INTO item_images (
  workspace_id,
  item_id,
  sequence,
  image_url,
  storage_bytes,
  canvas_uri,
  width,
  height,
  label,
  hocr_url
) SELECT
  i.workspace_id,
  sqlc.arg(item_id),
  sqlc.arg(sequence),
  sqlc.arg(image_url),
  sqlc.arg(storage_bytes),
  sqlc.narg(canvas_uri),
  sqlc.narg(width),
  sqlc.narg(height),
  sqlc.narg(label),
  sqlc.narg(hocr_url)
FROM items i
WHERE i.id = sqlc.arg(item_id);

-- name: GetItemImageManual :one
SELECT
  id,
  workspace_id,
  item_id,
  sequence,
  image_url,
  storage_bytes,
  canvas_uri,
  width,
  height,
  label,
  hocr_url,
  created_at,
  updated_at
FROM item_images
WHERE id = sqlc.arg(id);

-- name: ListItemImagesManual :many
SELECT
  id,
  workspace_id,
  item_id,
  sequence,
  image_url,
  storage_bytes,
  canvas_uri,
  width,
  height,
  label,
  hocr_url,
  created_at,
  updated_at
FROM item_images
WHERE item_id = sqlc.arg(item_id)
ORDER BY sequence ASC;

-- name: CountItemImagesByURLManual :one
SELECT COUNT(*)
FROM item_images
WHERE image_url = sqlc.arg(image_url);

-- name: LockItemImageIDsByURL :many
SELECT id
FROM item_images
WHERE image_url = sqlc.arg(image_url)
ORDER BY id
FOR UPDATE;

-- name: UpdateItemImageCanvasURIManual :exec
UPDATE item_images
SET canvas_uri = sqlc.arg(canvas_uri)
WHERE id = sqlc.arg(id);

-- name: SetItemImageCanvasURIIfMissingManual :execrows
UPDATE item_images
SET canvas_uri = sqlc.arg(canvas_uri)
WHERE id = sqlc.arg(id)
  AND (canvas_uri IS NULL OR canvas_uri = '');
