-- name: CreateUserManual :execresult
INSERT INTO users (
  name,
  email,
  google_subject,
  picture_url,
  is_admin,
  last_login_at
) VALUES (
  sqlc.arg(name),
  sqlc.narg(email),
  sqlc.narg(google_subject),
  sqlc.narg(picture_url),
  sqlc.arg(is_admin),
  NOW()
);

-- name: GetUserManual :one
SELECT
  id,
  name,
  email,
  google_subject,
  picture_url,
  is_admin,
  last_login_at,
  created_at,
  updated_at
FROM users
WHERE id = sqlc.arg(id);

-- name: GetUserByEmailManual :one
SELECT
  id,
  name,
  email,
  google_subject,
  picture_url,
  is_admin,
  last_login_at,
  created_at,
  updated_at
FROM users
WHERE email = sqlc.arg(email)
LIMIT 1;

-- name: GetUserByGoogleSubjectManual :one
SELECT
  id,
  name,
  email,
  google_subject,
  picture_url,
  is_admin,
  last_login_at,
  created_at,
  updated_at
FROM users
WHERE google_subject = sqlc.arg(google_subject)
LIMIT 1;

-- name: UpdateUserAuthProfileManual :exec
UPDATE users
SET
  name = sqlc.arg(name),
  email = sqlc.narg(email),
  google_subject = sqlc.narg(google_subject),
  picture_url = sqlc.narg(picture_url),
  is_admin = sqlc.arg(is_admin),
  last_login_at = NOW()
WHERE id = sqlc.arg(id);

-- name: CreateAuthSessionManual :exec
INSERT INTO auth_sessions (
  token_hash,
  user_id,
  expires_at,
  user_agent,
  ip_address,
  last_seen_at
) VALUES (
  sqlc.arg(token_hash),
  sqlc.arg(user_id),
  sqlc.arg(expires_at),
  sqlc.narg(user_agent),
  sqlc.narg(ip_address),
  NOW()
);

-- name: GetAuthSessionByTokenHashManual :one
SELECT
  id,
  token_hash,
  user_id,
  expires_at,
  user_agent,
  ip_address,
  last_seen_at,
  created_at
FROM auth_sessions
WHERE token_hash = sqlc.arg(token_hash)
LIMIT 1;

-- name: DeleteAuthSessionByTokenHashManual :exec
DELETE FROM auth_sessions
WHERE token_hash = sqlc.arg(token_hash);

-- name: TouchAuthSessionManual :exec
UPDATE auth_sessions
SET last_seen_at = NOW()
WHERE token_hash = sqlc.arg(token_hash);

-- name: CreateWorkspaceManual :execresult
INSERT INTO workspaces (
  organization_id,
  owner_user_id,
  name,
  slug,
  is_personal,
  created_by_user_id
) VALUES (
  sqlc.narg(organization_id),
  sqlc.narg(owner_user_id),
  sqlc.arg(name),
  sqlc.arg(slug),
  sqlc.arg(is_personal),
  sqlc.narg(created_by_user_id)
);

-- name: GetPersonalWorkspaceByUserIDManual :one
SELECT
  id,
  organization_id,
  owner_user_id,
  name,
  slug,
  is_personal,
  created_by_user_id,
  created_at,
  updated_at
FROM workspaces
WHERE owner_user_id = sqlc.arg(user_id)
  AND is_personal = TRUE
LIMIT 1;

-- name: CreateWorkspaceMemberManual :exec
INSERT IGNORE INTO workspace_members (
  workspace_id,
  user_id,
  role
) VALUES (
  sqlc.arg(workspace_id),
  sqlc.arg(user_id),
  sqlc.arg(role)
);

-- name: GetWorkspaceAccessManual :one
SELECT sqlc.embed(w), wm.role
FROM workspaces w
JOIN workspace_members wm ON wm.workspace_id = w.id
WHERE w.id = sqlc.arg(workspace_id)
  AND wm.user_id = sqlc.arg(user_id)
LIMIT 1;

-- name: ListWorkspaceAccessByUserManual :many
SELECT sqlc.embed(w), wm.role
FROM workspaces w
JOIN workspace_members wm ON wm.workspace_id = w.id
WHERE wm.user_id = sqlc.arg(user_id)
ORDER BY w.is_personal DESC, w.name ASC;
