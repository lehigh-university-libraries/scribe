-- name: GetAnnotationPageManual :one
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
WHERE annotation_pages.workspace_id = sqlc.arg(workspace_id)
  AND annotation_pages.item_image_id = sqlc.arg(item_image_id)
  AND EXISTS (
    SELECT 1
    FROM item_images ii
    JOIN items i ON i.id = ii.item_id
    WHERE ii.id = annotation_pages.item_image_id
      AND i.workspace_id = annotation_pages.workspace_id
  )
LIMIT 1;

-- name: ListItemAnnotationRevisionsManual :many
SELECT
  ap.item_image_id,
  ap.revision,
  OCTET_LENGTH(ap.payload) AS payload_bytes
FROM items i
JOIN item_images ii
  ON ii.item_id = i.id
JOIN annotation_pages ap
  ON ap.workspace_id = i.workspace_id
 AND ap.item_image_id = ii.id
WHERE i.workspace_id = sqlc.arg(workspace_id)
  AND i.id = sqlc.arg(item_id)
ORDER BY ii.sequence ASC, ii.id ASC;

-- name: ListItemAnnotationManifestReferencesManual :many
SELECT
  ap.workspace_id,
  ap.item_image_id,
  ap.page_id,
  ap.canvas_uri,
  ap.revision,
  ap.updated_at
FROM items i
JOIN item_images ii
  ON ii.item_id = i.id
JOIN annotation_pages ap
  ON ap.workspace_id = i.workspace_id
 AND ap.item_image_id = ii.id
WHERE i.workspace_id = sqlc.arg(workspace_id)
  AND i.id = sqlc.arg(item_id)
ORDER BY ii.sequence ASC, ii.id ASC;

-- name: ListItemAnnotationPagesManual :many
SELECT
  ap.workspace_id,
  ap.item_image_id,
  ap.page_id,
  ap.canvas_uri,
  ap.payload,
  ap.revision,
  ap.updated_by_user_id,
  ap.created_at,
  ap.updated_at
FROM items i
JOIN item_images ii
  ON ii.item_id = i.id
JOIN annotation_pages ap
  ON ap.workspace_id = i.workspace_id
 AND ap.item_image_id = ii.id
WHERE i.workspace_id = sqlc.arg(workspace_id)
  AND i.id = sqlc.arg(item_id)
  AND (
    SELECT COALESCE(SUM(OCTET_LENGTH(ap_budget.payload)), 0)
    FROM item_images ii_budget
    JOIN annotation_pages ap_budget
      ON ap_budget.item_image_id = ii_budget.id
     AND ap_budget.workspace_id = i.workspace_id
    WHERE ii_budget.item_id = i.id
  ) <= sqlc.arg(max_source_bytes)
ORDER BY ii.sequence ASC, ii.id ASC;

-- name: AnnotationPageResourceExistsManual :one
SELECT EXISTS(
  SELECT 1
  FROM item_images ii
  JOIN items i ON i.id = ii.item_id
  WHERE ii.id = sqlc.arg(item_image_id)
    AND i.workspace_id = sqlc.arg(workspace_id)
) AS resource_exists;

-- name: CreateAnnotationPageManual :execresult
INSERT INTO annotation_pages (
  workspace_id,
  item_image_id,
  page_id,
  canvas_uri,
  payload,
  revision,
  updated_by_user_id
) SELECT
  sqlc.arg(workspace_id),
  sqlc.arg(item_image_id),
  sqlc.arg(page_id),
  sqlc.arg(canvas_uri),
  sqlc.arg(payload),
  1,
  sqlc.narg(updated_by_user_id)
FROM item_images ii
JOIN items i ON i.id = ii.item_id
WHERE ii.id = sqlc.arg(item_image_id)
  AND i.workspace_id = sqlc.arg(workspace_id);

-- name: UpdateAnnotationPageCASManual :execresult
UPDATE annotation_pages
SET
  page_id = sqlc.arg(page_id),
  canvas_uri = sqlc.arg(canvas_uri),
  payload = sqlc.arg(payload),
  revision = revision + 1,
  updated_by_user_id = sqlc.narg(updated_by_user_id)
WHERE annotation_pages.workspace_id = sqlc.arg(workspace_id)
  AND annotation_pages.item_image_id = sqlc.arg(item_image_id)
  AND annotation_pages.revision = sqlc.arg(expected_revision)
  AND EXISTS (
    SELECT 1
    FROM item_images ii
    JOIN items i ON i.id = ii.item_id
    WHERE ii.id = annotation_pages.item_image_id
      AND i.workspace_id = annotation_pages.workspace_id
  );

-- name: SaveCanonicalOCRCorrectionMetricManual :execresult
UPDATE ocr_runs run
JOIN current_ocr_runs current_run
  ON current_run.session_id = run.session_id
 AND current_run.item_image_id = run.item_image_id
SET
  run.canonical_revision = sqlc.arg(canonical_revision),
  run.levenshtein_distance = sqlc.arg(levenshtein_distance)
WHERE current_run.item_image_id = sqlc.arg(item_image_id);

-- name: DeleteAnnotationIndexForPageManual :exec
DELETE FROM annotations
WHERE workspace_id = sqlc.arg(workspace_id)
  AND item_image_id = sqlc.arg(item_image_id);

-- name: CreateAnnotationIndexEntryManual :exec
INSERT INTO annotations (
  workspace_id,
  item_image_id,
  id,
  canvas_uri,
  text_granularity,
  position,
  payload
) VALUES (
  sqlc.arg(workspace_id),
  sqlc.arg(item_image_id),
  sqlc.arg(id),
  sqlc.arg(canvas_uri),
  sqlc.narg(text_granularity),
  sqlc.arg(position),
  sqlc.arg(payload)
);

-- name: SearchAnnotationIndexManual :many
SELECT
  workspace_id,
  item_image_id,
  id,
  canvas_uri,
  text_granularity,
  position,
  payload,
  created_at,
  updated_at
FROM annotations
WHERE workspace_id = sqlc.arg(workspace_id)
  AND item_image_id = sqlc.arg(item_image_id)
ORDER BY position ASC;

-- name: GetAnnotationIndexEntryManual :one
SELECT
  workspace_id,
  item_image_id,
  id,
  canvas_uri,
  text_granularity,
  position,
  payload,
  created_at,
  updated_at
FROM annotations
WHERE workspace_id = sqlc.arg(workspace_id)
  AND id = sqlc.arg(id)
LIMIT 1;
