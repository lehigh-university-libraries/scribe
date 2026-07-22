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
	ErrWorkspaceAccessLimit       = errors.New("user workspace limit reached")
	ErrWorkspaceMemberLimit       = errors.New("workspace member limit reached")
)

const (
	MaxWorkspaceAccessPerUser = 50
	MaxWorkspaceMembers       = 100
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
	row, err := s.q.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return Workspace{}, fmt.Errorf("get workspace: %w", err)
	}
	return rowToWorkspace(row), nil
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
		queries := s.q.WithTx(tx)
		if _, err := queries.LockUserForIdentityAdmissionManual(ctx, userID); err != nil {
			_ = tx.Rollback()
			return WorkspaceAccess{}, fmt.Errorf("lock workspace creator: %w", err)
		}
		accessCount, err := queries.CountWorkspaceAccessByUserManual(ctx, userID)
		if err != nil {
			_ = tx.Rollback()
			return WorkspaceAccess{}, fmt.Errorf("count user workspaces: %w", err)
		}
		if accessCount >= MaxWorkspaceAccessPerUser {
			_ = tx.Rollback()
			return WorkspaceAccess{}, ErrWorkspaceAccessLimit
		}
		ownerUserID := userID
		createdByUserID := userID
		workspaceID, err := queries.CreateWorkspace(ctx, db.CreateWorkspaceParams{
			OwnerUserID:     &ownerUserID,
			Name:            name,
			Slug:            slug,
			IsPersonal:      false,
			CreatedByUserID: &createdByUserID,
		})
		if err != nil {
			_ = tx.Rollback()
			if isDuplicateEntryError(err) {
				continue
			}
			return WorkspaceAccess{}, fmt.Errorf("create workspace: %w", err)
		}
		if err := queries.EnsureStorageQuotaUsage(ctx, workspaceID); err != nil {
			_ = tx.Rollback()
			return WorkspaceAccess{}, fmt.Errorf("create workspace quota row: %w", err)
		}

		if err := queries.CreateWorkspaceMember(ctx, workspaceID, userID, "admin"); err != nil {
			_ = tx.Rollback()
			return WorkspaceAccess{}, fmt.Errorf("create workspace membership: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return WorkspaceAccess{}, fmt.Errorf("commit workspace creation: %w", err)
		}
		return s.GetWorkspaceAccess(ctx, userID, workspaceID)
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

	rows, err := s.q.UpdateWorkspaceName(ctx, workspaceID, name)
	if err != nil {
		return Workspace{}, fmt.Errorf("update workspace name: %w", err)
	}
	if rows == 0 {
		return Workspace{}, sql.ErrNoRows
	}
	return s.GetWorkspace(ctx, workspaceID)
}

func (s *IdentityStore) ListWorkspaceMembers(ctx context.Context, workspaceID uint64) ([]WorkspaceMember, error) {
	rows, err := s.q.ListWorkspaceMembers(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list workspace members: %w", err)
	}
	out := make([]WorkspaceMember, 0, len(rows))
	for _, row := range rows {
		out = append(out, rowToWorkspaceMember(row))
	}
	return out, nil
}

func (s *IdentityStore) GetWorkspaceMember(ctx context.Context, workspaceID, userID uint64) (WorkspaceMember, error) {
	row, err := s.q.GetWorkspaceMember(ctx, workspaceID, userID)
	if err != nil {
		return WorkspaceMember{}, fmt.Errorf("get workspace member: %w", err)
	}
	return rowToWorkspaceMember(row), nil
}

func (s *IdentityStore) AddWorkspaceMemberByEmail(ctx context.Context, workspaceID uint64, email, role string) (WorkspaceMember, error) {
	role, err := normalizeWorkspaceMemberRole(role)
	if err != nil {
		return WorkspaceMember{}, err
	}

	user, err := s.q.GetUserByEmail(ctx, strings.ToLower(strings.TrimSpace(email)))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return WorkspaceMember{}, ErrWorkspaceUserNotFound
		}
		return WorkspaceMember{}, fmt.Errorf("lookup member by email: %w", err)
	}

	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return WorkspaceMember{}, fmt.Errorf("add workspace member: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.q.WithTx(tx)
	workspace, err := queries.LockWorkspace(ctx, workspaceID)
	if err != nil {
		return WorkspaceMember{}, err
	}
	if workspace.IsPersonal {
		return WorkspaceMember{}, ErrPersonalWorkspaceImmutable
	}
	if _, err := queries.LockUserForIdentityAdmissionManual(ctx, user.ID); err != nil {
		return WorkspaceMember{}, fmt.Errorf("lock workspace member user: %w", err)
	}
	memberCount, err := queries.CountWorkspaceMembersManual(ctx, workspaceID)
	if err != nil {
		return WorkspaceMember{}, fmt.Errorf("count workspace members: %w", err)
	}
	if memberCount >= MaxWorkspaceMembers {
		return WorkspaceMember{}, ErrWorkspaceMemberLimit
	}
	accessCount, err := queries.CountWorkspaceAccessByUserManual(ctx, user.ID)
	if err != nil {
		return WorkspaceMember{}, fmt.Errorf("count user workspaces: %w", err)
	}
	if accessCount >= MaxWorkspaceAccessPerUser {
		return WorkspaceMember{}, ErrWorkspaceAccessLimit
	}
	if err := queries.AddWorkspaceMember(ctx, workspaceID, user.ID, role); err != nil {
		if isDuplicateEntryError(err) {
			return WorkspaceMember{}, ErrWorkspaceMemberExists
		}
		return WorkspaceMember{}, fmt.Errorf("add workspace member: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return WorkspaceMember{}, fmt.Errorf("add workspace member: commit: %w", err)
	}
	return s.GetWorkspaceMember(ctx, workspaceID, user.ID)
}

func (s *IdentityStore) UpdateWorkspaceMemberRole(ctx context.Context, workspaceID, userID uint64, role string) (WorkspaceMember, error) {
	role, err := normalizeWorkspaceMemberRole(role)
	if err != nil {
		return WorkspaceMember{}, err
	}

	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return WorkspaceMember{}, fmt.Errorf("update workspace member: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.q.WithTx(tx)
	workspace, err := queries.LockWorkspace(ctx, workspaceID)
	if err != nil {
		return WorkspaceMember{}, fmt.Errorf("lock workspace: %w", err)
	}
	if workspace.IsPersonal {
		return WorkspaceMember{}, ErrPersonalWorkspaceImmutable
	}
	currentRole, err := queries.LockWorkspaceMemberRole(ctx, workspaceID, userID)
	if err != nil {
		return WorkspaceMember{}, fmt.Errorf("lock workspace member: %w", err)
	}
	if currentRole == "admin" && role != "admin" {
		adminCount, countErr := queries.CountWorkspaceAdmins(ctx, workspaceID)
		if countErr != nil {
			return WorkspaceMember{}, fmt.Errorf("count workspace admins: %w", countErr)
		}
		if adminCount <= 1 {
			return WorkspaceMember{}, ErrLastWorkspaceAdmin
		}
	}

	rows, err := queries.UpdateWorkspaceMemberRole(ctx, workspaceID, userID, role)
	if err != nil {
		return WorkspaceMember{}, fmt.Errorf("update workspace member: %w", err)
	}
	if rows == 0 && currentRole != role {
		return WorkspaceMember{}, sql.ErrNoRows
	}
	if err := tx.Commit(); err != nil {
		return WorkspaceMember{}, fmt.Errorf("update workspace member: commit: %w", err)
	}
	return s.GetWorkspaceMember(ctx, workspaceID, userID)
}

func (s *IdentityStore) RemoveWorkspaceMember(ctx context.Context, workspaceID, userID uint64) error {
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("remove workspace member: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.q.WithTx(tx)
	workspace, err := queries.LockWorkspace(ctx, workspaceID)
	if err != nil {
		return fmt.Errorf("lock workspace: %w", err)
	}
	if workspace.IsPersonal {
		return ErrPersonalWorkspaceImmutable
	}
	currentRole, err := queries.LockWorkspaceMemberRole(ctx, workspaceID, userID)
	if err != nil {
		return fmt.Errorf("lock workspace member: %w", err)
	}
	if currentRole == "admin" {
		adminCount, countErr := queries.CountWorkspaceAdmins(ctx, workspaceID)
		if countErr != nil {
			return fmt.Errorf("count workspace admins: %w", countErr)
		}
		if adminCount <= 1 {
			return ErrLastWorkspaceAdmin
		}
	}

	rows, err := queries.DeleteWorkspaceMember(ctx, workspaceID, userID)
	if err != nil {
		return fmt.Errorf("remove workspace member: %w", err)
	}
	if rows == 0 {
		return sql.ErrNoRows
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("remove workspace member: commit: %w", err)
	}
	return nil
}

func rowToWorkspaceMember(row db.WorkspaceMemberDetail) WorkspaceMember {
	return WorkspaceMember{
		WorkspaceID: row.WorkspaceID,
		User:        rowToUser(row.User),
		Role:        row.Role,
		CreatedAt:   row.CreatedAt,
	}
}

func isDuplicateEntryError(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}
