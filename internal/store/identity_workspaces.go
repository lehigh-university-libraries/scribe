package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	db "github.com/lehigh-university-libraries/scribe/internal/db"
)

var (
	ErrInvalidWorkspaceRole       = errors.New("invalid workspace role")
	ErrPersonalWorkspaceImmutable = errors.New("personal workspace cannot be modified")
	ErrLastWorkspaceAdmin         = errors.New("workspace must retain at least one admin")
	ErrWorkspaceMemberExists      = errors.New("workspace member already exists")
	ErrWorkspaceUserNotFound      = errors.New("workspace user not found")
)

type WorkspaceMember struct {
	WorkspaceID uint64    `json:"workspace_id"`
	User        User      `json:"user"`
	Role        string    `json:"role"`
	CreatedAt   time.Time `json:"created_at"`
}

func normalizeWorkspaceMemberRole(raw string) (string, error) {
	role := strings.ToLower(strings.TrimSpace(raw))
	switch role {
	case "admin", "write", "create", "read":
		return role, nil
	default:
		return "", ErrInvalidWorkspaceRole
	}
}

func (s *IdentityStore) GetWorkspace(ctx context.Context, workspaceID uint64) (Workspace, error) {
	row := s.pool.QueryRowContext(ctx, `
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
WHERE id = ?
LIMIT 1
`, workspaceID)

	var workspaceRow db.Workspace
	if err := row.Scan(
		&workspaceRow.ID,
		&workspaceRow.OrganizationID,
		&workspaceRow.OwnerUserID,
		&workspaceRow.Name,
		&workspaceRow.Slug,
		&workspaceRow.IsPersonal,
		&workspaceRow.CreatedByUserID,
		&workspaceRow.CreatedAt,
		&workspaceRow.UpdatedAt,
	); err != nil {
		return Workspace{}, fmt.Errorf("get workspace: %w", err)
	}
	return rowToWorkspace(workspaceRow), nil
}

func (s *IdentityStore) CreateWorkspaceForUser(ctx context.Context, userID uint64, name string) (WorkspaceAccess, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return WorkspaceAccess{}, fmt.Errorf("workspace name is required")
	}

	baseSlug := Slugify(name)
	for attempt := 0; attempt < 20; attempt++ {
		slug := baseSlug
		if attempt > 0 {
			slug = fmt.Sprintf("%s-%d", baseSlug, attempt+1)
		}

		tx, err := s.pool.BeginTx(ctx, nil)
		if err != nil {
			return WorkspaceAccess{}, fmt.Errorf("begin workspace transaction: %w", err)
		}

		res, err := tx.ExecContext(ctx, `
INSERT INTO workspaces (
  owner_user_id,
  name,
  slug,
  is_personal,
  created_by_user_id
) VALUES (?, ?, ?, FALSE, ?)
`, userID, name, slug, userID)
		if err != nil {
			_ = tx.Rollback()
			if isDuplicateEntryError(err) {
				continue
			}
			return WorkspaceAccess{}, fmt.Errorf("create workspace: %w", err)
		}

		workspaceID, err := res.LastInsertId()
		if err != nil {
			_ = tx.Rollback()
			return WorkspaceAccess{}, fmt.Errorf("read workspace id: %w", err)
		}

		if _, err := tx.ExecContext(ctx, `
INSERT INTO workspace_members (
  workspace_id,
  user_id,
  role
) VALUES (?, ?, 'admin')
`, workspaceID, userID); err != nil {
			_ = tx.Rollback()
			return WorkspaceAccess{}, fmt.Errorf("create workspace membership: %w", err)
		}

		if err := tx.Commit(); err != nil {
			return WorkspaceAccess{}, fmt.Errorf("commit workspace creation: %w", err)
		}
		createdWorkspaceID, ok := uint64FromNonNegativeInt64(workspaceID)
		if !ok {
			return WorkspaceAccess{}, fmt.Errorf("create workspace returned negative id %d", workspaceID)
		}
		return s.GetWorkspaceAccess(ctx, userID, createdWorkspaceID)
	}

	return WorkspaceAccess{}, fmt.Errorf("create workspace: unable to allocate unique slug")
}

func (s *IdentityStore) UpdateWorkspaceName(ctx context.Context, workspaceID uint64, name string) (Workspace, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Workspace{}, fmt.Errorf("workspace name is required")
	}

	workspace, err := s.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return Workspace{}, err
	}
	if workspace.IsPersonal {
		return Workspace{}, ErrPersonalWorkspaceImmutable
	}

	res, err := s.pool.ExecContext(ctx, `
UPDATE workspaces
SET name = ?
WHERE id = ?
`, name, workspaceID)
	if err != nil {
		return Workspace{}, fmt.Errorf("update workspace name: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return Workspace{}, fmt.Errorf("workspace rows affected: %w", err)
	}
	if rows == 0 {
		return Workspace{}, sql.ErrNoRows
	}
	return s.GetWorkspace(ctx, workspaceID)
}

func (s *IdentityStore) ListWorkspaceMembers(ctx context.Context, workspaceID uint64) ([]WorkspaceMember, error) {
	rows, err := s.pool.QueryContext(ctx, `
SELECT
  wm.workspace_id,
  wm.role,
  wm.created_at,
  u.id,
  u.name,
  u.email,
  u.google_subject,
  u.picture_url,
  u.is_admin,
  u.last_login_at,
  u.created_at,
  u.updated_at
FROM workspace_members wm
JOIN users u ON u.id = wm.user_id
WHERE wm.workspace_id = ?
ORDER BY FIELD(wm.role, 'admin', 'write', 'create', 'read'), LOWER(u.name), LOWER(COALESCE(u.email, ''))
`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list workspace members: %w", err)
	}
	defer rows.Close()

	out := make([]WorkspaceMember, 0)
	for rows.Next() {
		member, err := scanWorkspaceMember(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, member)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate workspace members: %w", err)
	}
	return out, nil
}

func (s *IdentityStore) GetWorkspaceMember(ctx context.Context, workspaceID, userID uint64) (WorkspaceMember, error) {
	rows, err := s.pool.QueryContext(ctx, `
SELECT
  wm.workspace_id,
  wm.role,
  wm.created_at,
  u.id,
  u.name,
  u.email,
  u.google_subject,
  u.picture_url,
  u.is_admin,
  u.last_login_at,
  u.created_at,
  u.updated_at
FROM workspace_members wm
JOIN users u ON u.id = wm.user_id
WHERE wm.workspace_id = ?
  AND wm.user_id = ?
LIMIT 1
`, workspaceID, userID)
	if err != nil {
		return WorkspaceMember{}, fmt.Errorf("get workspace member: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return WorkspaceMember{}, sql.ErrNoRows
	}
	member, err := scanWorkspaceMember(rows)
	if err != nil {
		return WorkspaceMember{}, err
	}
	return member, nil
}

func (s *IdentityStore) AddWorkspaceMemberByEmail(ctx context.Context, workspaceID uint64, email, role string) (WorkspaceMember, error) {
	role, err := normalizeWorkspaceMemberRole(role)
	if err != nil {
		return WorkspaceMember{}, err
	}

	workspace, err := s.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return WorkspaceMember{}, err
	}
	if workspace.IsPersonal {
		return WorkspaceMember{}, ErrPersonalWorkspaceImmutable
	}

	user, err := s.q.GetUserByEmail(ctx, strings.ToLower(strings.TrimSpace(email)))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return WorkspaceMember{}, ErrWorkspaceUserNotFound
		}
		return WorkspaceMember{}, fmt.Errorf("lookup member by email: %w", err)
	}

	_, err = s.pool.ExecContext(ctx, `
INSERT INTO workspace_members (
  workspace_id,
  user_id,
  role
) VALUES (?, ?, ?)
`, workspaceID, user.ID, role)
	if err != nil {
		if isDuplicateEntryError(err) {
			return WorkspaceMember{}, ErrWorkspaceMemberExists
		}
		return WorkspaceMember{}, fmt.Errorf("add workspace member: %w", err)
	}
	return s.GetWorkspaceMember(ctx, workspaceID, user.ID)
}

func (s *IdentityStore) UpdateWorkspaceMemberRole(ctx context.Context, workspaceID, userID uint64, role string) (WorkspaceMember, error) {
	role, err := normalizeWorkspaceMemberRole(role)
	if err != nil {
		return WorkspaceMember{}, err
	}

	workspace, err := s.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return WorkspaceMember{}, err
	}
	if workspace.IsPersonal {
		return WorkspaceMember{}, ErrPersonalWorkspaceImmutable
	}

	member, err := s.GetWorkspaceMember(ctx, workspaceID, userID)
	if err != nil {
		return WorkspaceMember{}, err
	}
	if member.Role == "admin" && role != "admin" {
		adminCount, err := s.countWorkspaceAdmins(ctx, workspaceID)
		if err != nil {
			return WorkspaceMember{}, err
		}
		if adminCount <= 1 {
			return WorkspaceMember{}, ErrLastWorkspaceAdmin
		}
	}

	res, err := s.pool.ExecContext(ctx, `
UPDATE workspace_members
SET role = ?
WHERE workspace_id = ?
  AND user_id = ?
`, role, workspaceID, userID)
	if err != nil {
		return WorkspaceMember{}, fmt.Errorf("update workspace member: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return WorkspaceMember{}, fmt.Errorf("workspace member rows affected: %w", err)
	}
	if rows == 0 {
		return WorkspaceMember{}, sql.ErrNoRows
	}
	return s.GetWorkspaceMember(ctx, workspaceID, userID)
}

func (s *IdentityStore) RemoveWorkspaceMember(ctx context.Context, workspaceID, userID uint64) error {
	workspace, err := s.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return err
	}
	if workspace.IsPersonal {
		return ErrPersonalWorkspaceImmutable
	}

	member, err := s.GetWorkspaceMember(ctx, workspaceID, userID)
	if err != nil {
		return err
	}
	if member.Role == "admin" {
		adminCount, err := s.countWorkspaceAdmins(ctx, workspaceID)
		if err != nil {
			return err
		}
		if adminCount <= 1 {
			return ErrLastWorkspaceAdmin
		}
	}

	res, err := s.pool.ExecContext(ctx, `
DELETE FROM workspace_members
WHERE workspace_id = ?
  AND user_id = ?
`, workspaceID, userID)
	if err != nil {
		return fmt.Errorf("remove workspace member: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("workspace member rows affected: %w", err)
	}
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *IdentityStore) countWorkspaceAdmins(ctx context.Context, workspaceID uint64) (int, error) {
	row := s.pool.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM workspace_members
WHERE workspace_id = ?
  AND role = 'admin'
`, workspaceID)
	var count int
	if err := row.Scan(&count); err != nil {
		return 0, fmt.Errorf("count workspace admins: %w", err)
	}
	return count, nil
}

func scanWorkspaceMember(scanner interface {
	Scan(dest ...any) error
}) (WorkspaceMember, error) {
	var (
		member        WorkspaceMember
		email         sql.NullString
		googleSubject sql.NullString
		pictureURL    sql.NullString
		lastLoginAt   sql.NullTime
		userCreatedAt time.Time
		userUpdatedAt time.Time
	)

	if err := scanner.Scan(
		&member.WorkspaceID,
		&member.Role,
		&member.CreatedAt,
		&member.User.ID,
		&member.User.Name,
		&email,
		&googleSubject,
		&pictureURL,
		&member.User.IsAdmin,
		&lastLoginAt,
		&userCreatedAt,
		&userUpdatedAt,
	); err != nil {
		return WorkspaceMember{}, fmt.Errorf("scan workspace member: %w", err)
	}

	member.User.CreatedAt = userCreatedAt
	member.User.UpdatedAt = userUpdatedAt
	if email.Valid {
		member.User.Email = email.String
	}
	if googleSubject.Valid {
		member.User.GoogleSubject = googleSubject.String
	}
	if pictureURL.Valid {
		member.User.PictureURL = pictureURL.String
	}
	if lastLoginAt.Valid {
		member.User.LastLoginAt = lastLoginAt.Time
	}
	return member, nil
}

func isDuplicateEntryError(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}
