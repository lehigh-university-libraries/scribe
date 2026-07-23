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

-- name: LockUserByGoogleSubjectForIdentityManual :one
SELECT sqlc.embed(u)
FROM users u
WHERE u.google_subject = sqlc.arg(google_subject)
LIMIT 1
FOR UPDATE;

-- name: LockUserByEmailForIdentityManual :one
SELECT sqlc.embed(u)
FROM users u
WHERE u.email = sqlc.arg(email)
LIMIT 1
FOR UPDATE;

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
  ip_address
) VALUES (
  sqlc.arg(token_hash),
  sqlc.arg(user_id),
  sqlc.arg(expires_at),
  sqlc.narg(user_agent),
  sqlc.narg(ip_address)
);

-- name: GetAuthSessionByTokenHashManual :one
SELECT
  id,
  token_hash,
  user_id,
  expires_at,
  user_agent,
  ip_address,
  created_at
FROM auth_sessions
WHERE token_hash = sqlc.arg(token_hash)
LIMIT 1;

-- name: DeleteAuthSessionByTokenHashManual :exec
DELETE FROM auth_sessions
WHERE token_hash = sqlc.arg(token_hash);

-- name: LockUserForIdentityAdmissionManual :one
SELECT id FROM users WHERE id = sqlc.arg(user_id) FOR UPDATE;

-- Name-locking is connection scoped and intentionally wraps the entire
-- identity transaction. The caller hashes subject/email values before use so
-- lock names never expose identity data through MariaDB diagnostics.
-- name: AcquireIdentityConvergenceLockManual :one
SELECT GET_LOCK(sqlc.arg(lock_name), 30);

-- name: ReleaseIdentityConvergenceLockManual :one
SELECT RELEASE_LOCK(sqlc.arg(lock_name));

-- name: DeleteExpiredAuthSessionsForUserManual :exec
DELETE FROM auth_sessions
WHERE user_id = sqlc.arg(user_id) AND expires_at <= NOW();

-- name: CountAuthSessionsForUserManual :one
SELECT COUNT(*) FROM auth_sessions WHERE user_id = sqlc.arg(user_id);

-- name: DeleteOldestAuthSessionForUserManual :execresult
DELETE FROM auth_sessions
WHERE user_id = sqlc.arg(user_id)
ORDER BY created_at ASC, id ASC
LIMIT 1;

-- name: DeleteExpiredAuthSessionsBatchManual :execresult
DELETE FROM auth_sessions
WHERE expires_at <= sqlc.arg(cutoff)
ORDER BY expires_at ASC, id ASC
LIMIT 1000;

-- name: CreateWorkspaceManual :execresult
INSERT INTO workspaces (
  owner_user_id,
  name,
  slug,
  is_personal,
  created_by_user_id
) VALUES (
  sqlc.narg(owner_user_id),
  sqlc.arg(name),
  sqlc.arg(slug),
  sqlc.arg(is_personal),
  sqlc.narg(created_by_user_id)
);

-- name: GetPersonalWorkspaceByUserIDManual :one
SELECT
  id,
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
ORDER BY w.is_personal DESC, w.name ASC
LIMIT 50;

-- name: CountWorkspaceAccessByUserManual :one
SELECT COUNT(*) FROM workspace_members WHERE user_id = sqlc.arg(user_id);

-- name: GetWorkspaceManual :one
SELECT
  id,
  owner_user_id,
  name,
  slug,
  is_personal,
  created_by_user_id,
  created_at,
  updated_at
FROM workspaces
WHERE id = sqlc.arg(id)
LIMIT 1;

-- name: LockWorkspaceForUseManual :one
SELECT id
FROM workspaces
WHERE id = sqlc.arg(id)
LOCK IN SHARE MODE;

-- name: UpdateWorkspaceNameManual :execresult
UPDATE workspaces
SET name = sqlc.arg(name)
WHERE id = sqlc.arg(id);

-- name: ListWorkspaceMembersManual :many
SELECT
  wm.workspace_id,
  wm.role,
  wm.created_at,
  sqlc.embed(u)
FROM workspace_members wm
JOIN users u ON u.id = wm.user_id
WHERE wm.workspace_id = sqlc.arg(workspace_id)
ORDER BY FIELD(wm.role, 'admin', 'write', 'create', 'read'), LOWER(u.name), LOWER(COALESCE(u.email, ''))
LIMIT 100;

-- name: CountWorkspaceMembersManual :one
SELECT COUNT(*) FROM workspace_members WHERE workspace_id = sqlc.arg(workspace_id);

-- name: GetWorkspaceMemberManual :one
SELECT
  wm.workspace_id,
  wm.role,
  wm.created_at,
  sqlc.embed(u)
FROM workspace_members wm
JOIN users u ON u.id = wm.user_id
WHERE wm.workspace_id = sqlc.arg(workspace_id)
  AND wm.user_id = sqlc.arg(user_id)
LIMIT 1;

-- name: AddWorkspaceMemberManual :exec
INSERT INTO workspace_members (
  workspace_id,
  user_id,
  role
) VALUES (
  sqlc.arg(workspace_id),
  sqlc.arg(user_id),
  sqlc.arg(role)
);

-- name: LockWorkspaceManual :one
SELECT
  id,
  owner_user_id,
  name,
  slug,
  is_personal,
  created_by_user_id,
  created_at,
  updated_at
FROM workspaces
WHERE id = sqlc.arg(id)
FOR UPDATE;

-- name: LockWorkspaceMemberRoleManual :one
SELECT role
FROM workspace_members
WHERE workspace_id = sqlc.arg(workspace_id)
  AND user_id = sqlc.arg(user_id)
FOR UPDATE;

-- name: CountWorkspaceAdminsManual :one
SELECT COUNT(*)
FROM workspace_members
WHERE workspace_id = sqlc.arg(workspace_id)
  AND role = 'admin';

-- name: UpdateWorkspaceMemberRoleManual :execresult
UPDATE workspace_members
SET role = sqlc.arg(role)
WHERE workspace_id = sqlc.arg(workspace_id)
  AND user_id = sqlc.arg(user_id);

-- name: DeleteWorkspaceMemberManual :execresult
DELETE FROM workspace_members
WHERE workspace_id = sqlc.arg(workspace_id)
  AND user_id = sqlc.arg(user_id);
