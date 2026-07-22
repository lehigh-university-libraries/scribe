package db

// Store query adapters in this file are the sole mapping boundary from
// domain-shaped identity values to sqlc-generated queries in identity.sql.

import (
	"context"
	"database/sql"
	"time"
)

type CreateUserParams struct {
	Name          string
	Email         string
	GoogleSubject string
	PictureURL    string
	IsAdmin       bool
}

func (q *Queries) CreateUser(ctx context.Context, arg CreateUserParams) (uint64, error) {
	res, err := q.CreateUserManual(ctx, CreateUserManualParams{
		Name:          arg.Name,
		Email:         nullString(arg.Email),
		GoogleSubject: nullString(arg.GoogleSubject),
		PictureUrl:    nullString(arg.PictureURL),
		IsAdmin:       arg.IsAdmin,
	})
	if err != nil {
		return 0, err
	}
	return lastInsertID(res)
}

func (q *Queries) GetUser(ctx context.Context, id uint64) (User, error) {
	row, err := q.GetUserManual(ctx, id)
	if err != nil {
		return User{}, err
	}
	return User{
		ID:            row.ID,
		Name:          row.Name,
		Email:         row.Email,
		GoogleSubject: row.GoogleSubject,
		PictureUrl:    row.PictureUrl,
		IsAdmin:       row.IsAdmin,
		LastLoginAt:   row.LastLoginAt,
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
	}, nil
}

func (q *Queries) GetUserByEmail(ctx context.Context, email string) (User, error) {
	row, err := q.GetUserByEmailManual(ctx, nullString(email))
	if err != nil {
		return User{}, err
	}
	return User{
		ID:            row.ID,
		Name:          row.Name,
		Email:         row.Email,
		GoogleSubject: row.GoogleSubject,
		PictureUrl:    row.PictureUrl,
		IsAdmin:       row.IsAdmin,
		LastLoginAt:   row.LastLoginAt,
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
	}, nil
}

func (q *Queries) GetUserByGoogleSubject(ctx context.Context, subject string) (User, error) {
	row, err := q.GetUserByGoogleSubjectManual(ctx, nullString(subject))
	if err != nil {
		return User{}, err
	}
	return User{
		ID:            row.ID,
		Name:          row.Name,
		Email:         row.Email,
		GoogleSubject: row.GoogleSubject,
		PictureUrl:    row.PictureUrl,
		IsAdmin:       row.IsAdmin,
		LastLoginAt:   row.LastLoginAt,
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
	}, nil
}

type UpdateUserAuthProfileParams struct {
	ID            uint64
	Name          string
	Email         string
	GoogleSubject string
	PictureURL    string
	IsAdmin       bool
}

func (q *Queries) UpdateUserAuthProfile(ctx context.Context, arg UpdateUserAuthProfileParams) error {
	return q.UpdateUserAuthProfileManual(ctx, UpdateUserAuthProfileManualParams{
		ID:            arg.ID,
		Name:          arg.Name,
		Email:         nullString(arg.Email),
		GoogleSubject: nullString(arg.GoogleSubject),
		PictureUrl:    nullString(arg.PictureURL),
		IsAdmin:       arg.IsAdmin,
	})
}

type CreateAuthSessionParams struct {
	TokenHash string
	UserID    uint64
	ExpiresAt time.Time
	UserAgent string
	IPAddress string
}

func (q *Queries) CreateAuthSession(ctx context.Context, arg CreateAuthSessionParams) error {
	return q.CreateAuthSessionManual(ctx, CreateAuthSessionManualParams{
		TokenHash: arg.TokenHash,
		UserID:    arg.UserID,
		ExpiresAt: arg.ExpiresAt,
		UserAgent: nullString(arg.UserAgent),
		IpAddress: nullString(arg.IPAddress),
	})
}

func (q *Queries) GetAuthSessionByTokenHash(ctx context.Context, tokenHash string) (AuthSession, error) {
	return q.GetAuthSessionByTokenHashManual(ctx, tokenHash)
}

func (q *Queries) DeleteAuthSessionByTokenHash(ctx context.Context, tokenHash string) error {
	return q.DeleteAuthSessionByTokenHashManual(ctx, tokenHash)
}

type CreateWorkspaceParams struct {
	OwnerUserID     *uint64
	Name            string
	Slug            string
	IsPersonal      bool
	CreatedByUserID *uint64
}

func (q *Queries) CreateWorkspace(ctx context.Context, arg CreateWorkspaceParams) (uint64, error) {
	ownerUserID, err := nullUint64(arg.OwnerUserID)
	if err != nil {
		return 0, err
	}
	createdByUserID, err := nullUint64(arg.CreatedByUserID)
	if err != nil {
		return 0, err
	}
	res, err := q.CreateWorkspaceManual(ctx, CreateWorkspaceManualParams{
		OwnerUserID:     ownerUserID,
		Name:            arg.Name,
		Slug:            arg.Slug,
		IsPersonal:      arg.IsPersonal,
		CreatedByUserID: createdByUserID,
	})
	if err != nil {
		return 0, err
	}
	return lastInsertID(res)
}

func (q *Queries) GetPersonalWorkspaceByUserID(ctx context.Context, userID uint64) (Workspace, error) {
	converted, err := uint64ToInt64(userID)
	if err != nil {
		return Workspace{}, err
	}
	return q.GetPersonalWorkspaceByUserIDManual(ctx, sql.NullInt64{Int64: converted, Valid: true})
}

type WorkspaceAccess struct {
	Workspace Workspace
	Role      string
}

func (q *Queries) CreateWorkspaceMember(ctx context.Context, workspaceID, userID uint64, role string) error {
	return q.CreateWorkspaceMemberManual(ctx, CreateWorkspaceMemberManualParams{
		WorkspaceID: workspaceID,
		UserID:      userID,
		Role:        WorkspaceMembersRole(role),
	})
}

func (q *Queries) GetWorkspaceAccess(ctx context.Context, workspaceID, userID uint64) (WorkspaceAccess, error) {
	row, err := q.GetWorkspaceAccessManual(ctx, GetWorkspaceAccessManualParams{
		WorkspaceID: workspaceID,
		UserID:      userID,
	})
	if err != nil {
		return WorkspaceAccess{}, err
	}
	return WorkspaceAccess{
		Workspace: row.Workspace,
		Role:      string(row.Role),
	}, nil
}

func (q *Queries) ListWorkspaceAccessByUser(ctx context.Context, userID uint64) ([]WorkspaceAccess, error) {
	rows, err := q.ListWorkspaceAccessByUserManual(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]WorkspaceAccess, 0, len(rows))
	for _, row := range rows {
		out = append(out, WorkspaceAccess{
			Workspace: row.Workspace,
			Role:      string(row.Role),
		})
	}
	return out, nil
}

// WorkspaceMemberDetail is the domain-shaped joined membership and user view.
// Keeping this mapping here prevents store code from depending on sqlc's
// query-specific generated row types.
type WorkspaceMemberDetail struct {
	WorkspaceID uint64
	User        User
	Role        string
	CreatedAt   time.Time
}

func (q *Queries) GetWorkspace(ctx context.Context, workspaceID uint64) (Workspace, error) {
	return q.GetWorkspaceManual(ctx, workspaceID)
}

func (q *Queries) UpdateWorkspaceName(ctx context.Context, workspaceID uint64, name string) (int64, error) {
	res, err := q.UpdateWorkspaceNameManual(ctx, UpdateWorkspaceNameManualParams{
		Name: name,
		ID:   workspaceID,
	})
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (q *Queries) ListWorkspaceMembers(ctx context.Context, workspaceID uint64) ([]WorkspaceMemberDetail, error) {
	rows, err := q.ListWorkspaceMembersManual(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]WorkspaceMemberDetail, 0, len(rows))
	for _, row := range rows {
		out = append(out, WorkspaceMemberDetail{
			WorkspaceID: row.WorkspaceID,
			User:        row.User,
			Role:        string(row.Role),
			CreatedAt:   row.CreatedAt,
		})
	}
	return out, nil
}

func (q *Queries) GetWorkspaceMember(ctx context.Context, workspaceID, userID uint64) (WorkspaceMemberDetail, error) {
	row, err := q.GetWorkspaceMemberManual(ctx, GetWorkspaceMemberManualParams{
		WorkspaceID: workspaceID,
		UserID:      userID,
	})
	if err != nil {
		return WorkspaceMemberDetail{}, err
	}
	return WorkspaceMemberDetail{
		WorkspaceID: row.WorkspaceID,
		User:        row.User,
		Role:        string(row.Role),
		CreatedAt:   row.CreatedAt,
	}, nil
}

func (q *Queries) AddWorkspaceMember(ctx context.Context, workspaceID, userID uint64, role string) error {
	return q.AddWorkspaceMemberManual(ctx, AddWorkspaceMemberManualParams{
		WorkspaceID: workspaceID,
		UserID:      userID,
		Role:        WorkspaceMembersRole(role),
	})
}

func (q *Queries) LockWorkspace(ctx context.Context, workspaceID uint64) (Workspace, error) {
	return q.LockWorkspaceManual(ctx, workspaceID)
}

func (q *Queries) LockWorkspaceMemberRole(ctx context.Context, workspaceID, userID uint64) (string, error) {
	role, err := q.LockWorkspaceMemberRoleManual(ctx, LockWorkspaceMemberRoleManualParams{
		WorkspaceID: workspaceID,
		UserID:      userID,
	})
	return string(role), err
}

func (q *Queries) CountWorkspaceAdmins(ctx context.Context, workspaceID uint64) (int64, error) {
	return q.CountWorkspaceAdminsManual(ctx, workspaceID)
}

func (q *Queries) UpdateWorkspaceMemberRole(ctx context.Context, workspaceID, userID uint64, role string) (int64, error) {
	res, err := q.UpdateWorkspaceMemberRoleManual(ctx, UpdateWorkspaceMemberRoleManualParams{
		Role:        WorkspaceMembersRole(role),
		WorkspaceID: workspaceID,
		UserID:      userID,
	})
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (q *Queries) DeleteWorkspaceMember(ctx context.Context, workspaceID, userID uint64) (int64, error) {
	res, err := q.DeleteWorkspaceMemberManual(ctx, DeleteWorkspaceMemberManualParams{
		WorkspaceID: workspaceID,
		UserID:      userID,
	})
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
