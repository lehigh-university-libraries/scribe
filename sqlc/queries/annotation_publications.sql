-- name: LockAnnotationPageForPublication :one
SELECT
  workspace_id,
  item_image_id,
  page_id,
  canvas_uri,
  payload,
  revision,
  updated_by_user_id,
  created_at,
  updated_at
FROM annotation_pages
WHERE workspace_id = sqlc.arg(workspace_id)
  AND item_image_id = sqlc.arg(item_image_id)
LIMIT 1
FOR UPDATE;

-- name: GetPublishedAnnotationPageForUpdate :one
SELECT
  workspace_id,
  item_image_id,
  page_id,
  canvas_uri,
  payload,
  published_revision,
  published_by_user_id,
  published_at,
  created_at,
  updated_at
FROM published_annotation_pages
WHERE workspace_id = sqlc.arg(workspace_id)
  AND item_image_id = sqlc.arg(item_image_id)
LIMIT 1
FOR UPDATE;

-- name: GetPublishedAnnotationPage :one
SELECT
  workspace_id,
  item_image_id,
  page_id,
  canvas_uri,
  payload,
  published_revision,
  published_by_user_id,
  published_at,
  created_at,
  updated_at
FROM published_annotation_pages
WHERE item_image_id = sqlc.arg(item_image_id)
LIMIT 1;

-- name: ListPublishedItemAnnotationManifestReferences :many
SELECT
  pap.workspace_id,
  pap.item_image_id,
  pap.page_id,
  pap.canvas_uri,
  pap.published_revision,
  pap.published_at
FROM items i
JOIN item_images ii
  ON ii.item_id = i.id
JOIN published_annotation_pages pap
  ON pap.workspace_id = i.workspace_id
 AND pap.item_image_id = ii.id
WHERE i.id = sqlc.arg(item_id)
ORDER BY ii.sequence ASC, ii.id ASC;

-- name: PublishedImageURLExists :one
SELECT EXISTS(
  SELECT 1
  FROM item_images ii
  JOIN items i
    ON i.id = ii.item_id
   AND i.workspace_id = ii.workspace_id
  JOIN workspaces w
    ON w.id = ii.workspace_id
  JOIN published_annotation_pages pap
    ON pap.item_image_id = ii.id
   AND pap.workspace_id = ii.workspace_id
  WHERE ii.image_url = sqlc.arg(image_url)
) AS is_published;

-- name: InsertPublishedAnnotationPage :exec
INSERT INTO published_annotation_pages (
  workspace_id,
  item_image_id,
  page_id,
  canvas_uri,
  payload,
  published_revision,
  published_by_user_id,
  published_at
) VALUES (
  sqlc.arg(workspace_id),
  sqlc.arg(item_image_id),
  sqlc.arg(page_id),
  sqlc.arg(canvas_uri),
  sqlc.arg(payload),
  sqlc.arg(published_revision),
  sqlc.narg(published_by_user_id),
  sqlc.arg(published_at)
);

-- name: UpdatePublishedAnnotationPage :execrows
UPDATE published_annotation_pages
SET
  page_id = sqlc.arg(page_id),
  canvas_uri = sqlc.arg(canvas_uri),
  payload = sqlc.arg(payload),
  published_revision = sqlc.arg(published_revision),
  published_by_user_id = sqlc.narg(published_by_user_id),
  published_at = sqlc.arg(published_at),
  updated_at = CURRENT_TIMESTAMP(6)
WHERE workspace_id = sqlc.arg(workspace_id)
  AND item_image_id = sqlc.arg(item_image_id);
