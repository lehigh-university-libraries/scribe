-- name: ListSessionsManual :many
SELECT id, name, created_at, updated_at
FROM sessions
ORDER BY created_at DESC;

-- name: GetSessionManual :one
SELECT id, name, created_at, updated_at
FROM sessions
WHERE id = sqlc.arg(id);

-- name: CreateSessionManual :exec
INSERT INTO sessions (id, name)
VALUES (sqlc.arg(id), sqlc.arg(name));
