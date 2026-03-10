-- name: ListSessions :many
SELECT id, name, created_at, updated_at
FROM sessions
ORDER BY created_at DESC;

-- name: GetSession :one
SELECT id, name, created_at, updated_at
FROM sessions
WHERE id = ?;

-- name: CreateSession :exec
INSERT INTO sessions (id, name)
VALUES (?, ?);
