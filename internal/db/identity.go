package db

// Compatibility wrappers in this file preserve the older store-facing API while
// delegating SQL execution to sqlc-generated queries in identity.sql.

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
		Email:         compatNullString(arg.Email),
		GoogleSubject: compatNullString(arg.GoogleSubject),
		PictureUrl:    compatNullString(arg.PictureURL),
		IsAdmin:       arg.IsAdmin,
	})
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	return uint64(id), err
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
	row, err := q.GetUserByEmailManual(ctx, compatNullString(email))
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
	row, err := q.GetUserByGoogleSubjectManual(ctx, compatNullString(subject))
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
		Email:         compatNullString(arg.Email),
		GoogleSubject: compatNullString(arg.GoogleSubject),
		PictureUrl:    compatNullString(arg.PictureURL),
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
		UserAgent: compatNullString(arg.UserAgent),
		IpAddress: compatNullString(arg.IPAddress),
	})
}

func (q *Queries) GetAuthSessionByTokenHash(ctx context.Context, tokenHash string) (AuthSession, error) {
	return q.GetAuthSessionByTokenHashManual(ctx, tokenHash)
}

func (q *Queries) DeleteAuthSessionByTokenHash(ctx context.Context, tokenHash string) error {
	return q.DeleteAuthSessionByTokenHashManual(ctx, tokenHash)
}

func (q *Queries) TouchAuthSession(ctx context.Context, tokenHash string) error {
	return q.TouchAuthSessionManual(ctx, tokenHash)
}

type CreateWorkspaceParams struct {
	OrganizationID  *uint64
	OwnerUserID     *uint64
	Name            string
	Slug            string
	IsPersonal      bool
	CreatedByUserID *uint64
}

func (q *Queries) CreateWorkspace(ctx context.Context, arg CreateWorkspaceParams) (uint64, error) {
	res, err := q.CreateWorkspaceManual(ctx, CreateWorkspaceManualParams{
		OrganizationID:  compatNullUint64(arg.OrganizationID),
		OwnerUserID:     compatNullUint64(arg.OwnerUserID),
		Name:            arg.Name,
		Slug:            arg.Slug,
		IsPersonal:      arg.IsPersonal,
		CreatedByUserID: compatNullUint64(arg.CreatedByUserID),
	})
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	return uint64(id), err
}

func (q *Queries) GetPersonalWorkspaceByUserID(ctx context.Context, userID uint64) (Workspace, error) {
	return q.GetPersonalWorkspaceByUserIDManual(ctx, sql.NullInt64{Int64: int64(userID), Valid: true})
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
