-- name: SearchAnnotationsByCanvasManual :many
SELECT
  id,
  canvas_uri,
  payload,
  created_at,
  updated_at
FROM annotations
WHERE canvas_uri IN (sqlc.arg(canvas_uri), sqlc.arg(normalized_canvas_uri))
ORDER BY updated_at ASC;

-- name: GetAnnotationManual :one
SELECT
  id,
  canvas_uri,
  payload,
  created_at,
  updated_at
FROM annotations
WHERE id = sqlc.arg(id)
LIMIT 1;

-- name: UpsertAnnotationManual :exec
INSERT INTO annotations (id, canvas_uri, payload)
VALUES (sqlc.arg(id), sqlc.arg(canvas_uri), sqlc.arg(payload))
ON DUPLICATE KEY UPDATE
  canvas_uri = VALUES(canvas_uri),
  payload = VALUES(payload);

-- name: UpdateAnnotationManual :execresult
UPDATE annotations
SET
  canvas_uri = sqlc.arg(canvas_uri),
  payload = sqlc.arg(payload)
WHERE id = sqlc.arg(id);

-- name: DeleteAnnotationManual :exec
DELETE FROM annotations
WHERE id = sqlc.arg(id);

-- name: DeleteAnnotationsByCanvasManual :exec
DELETE FROM annotations
WHERE canvas_uri = sqlc.arg(canvas_uri);
