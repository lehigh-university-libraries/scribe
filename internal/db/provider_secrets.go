package db

import (
	"context"
	"database/sql"
	"strings"
)

type CreateProviderSecretParams struct {
	UserID      *uint64
	WorkspaceID *uint64
	Provider    string
	Name        string
	VaultPath   string
	KeyHint     string
}

func (q *Queries) CreateProviderSecret(ctx context.Context, arg CreateProviderSecretParams) (uint64, error) {
	userID, err := compatNullUint64(arg.UserID)
	if err != nil {
		return 0, err
	}
	workspaceID, err := compatNullUint64(arg.WorkspaceID)
	if err != nil {
		return 0, err
	}
	res, err := q.CreateProviderSecretManual(ctx, CreateProviderSecretManualParams{
		UserID:      userID,
		WorkspaceID: workspaceID,
		Provider:    strings.ToLower(strings.TrimSpace(arg.Provider)),
		Name:        arg.Name,
		VaultPath:   arg.VaultPath,
		KeyHint:     compatNullableString(arg.KeyHint),
	})
	if err != nil {
		return 0, err
	}
	return compatLastInsertID(res)
}

func (q *Queries) ListProviderSecretsVisibleToUser(ctx context.Context, workspaceID, userID uint64) ([]ProviderSecret, error) {
	workspaceIDParam, err := compatUint64ToInt64(workspaceID)
	if err != nil {
		return nil, err
	}
	userIDParam, err := compatUint64ToInt64(userID)
	if err != nil {
		return nil, err
	}
	return q.ListProviderSecretsVisibleToUserManual(ctx, ListProviderSecretsVisibleToUserManualParams{
		WorkspaceID: sql.NullInt64{Int64: workspaceIDParam, Valid: true},
		UserID:      sql.NullInt64{Int64: userIDParam, Valid: userID > 0},
	})
}

func (q *Queries) GetProviderSecretVisibleToUser(ctx context.Context, id, workspaceID, userID uint64) (ProviderSecret, error) {
	workspaceIDParam, err := compatUint64ToInt64(workspaceID)
	if err != nil {
		return ProviderSecret{}, err
	}
	userIDParam, err := compatUint64ToInt64(userID)
	if err != nil {
		return ProviderSecret{}, err
	}
	return q.GetProviderSecretVisibleToUserManual(ctx, GetProviderSecretVisibleToUserManualParams{
		ID:          id,
		WorkspaceID: sql.NullInt64{Int64: workspaceIDParam, Valid: true},
		UserID:      sql.NullInt64{Int64: userIDParam, Valid: userID > 0},
	})
}

func (q *Queries) DeleteProviderSecret(ctx context.Context, id, workspaceID uint64, userID *uint64) error {
	workspaceIDParam, err := compatUint64ToInt64(workspaceID)
	if err != nil {
		return err
	}
	if userID == nil {
		res, err := q.DeleteWorkspaceProviderSecretManual(ctx, DeleteWorkspaceProviderSecretManualParams{
			ID:          id,
			WorkspaceID: sql.NullInt64{Int64: workspaceIDParam, Valid: true},
		})
		return requireAffectedRow(res, err)
	}
	userIDParam, err := compatUint64ToInt64(*userID)
	if err != nil {
		return err
	}
	res, err := q.DeleteUserProviderSecretManual(ctx, DeleteUserProviderSecretManualParams{
		ID:          id,
		WorkspaceID: sql.NullInt64{Int64: workspaceIDParam, Valid: true},
		UserID:      sql.NullInt64{Int64: userIDParam, Valid: *userID > 0},
	})
	return requireAffectedRow(res, err)
}

func (q *Queries) FindPreferredProviderSecret(ctx context.Context, workspaceID uint64, userID *uint64, provider string) (ProviderSecret, error) {
	workspaceIDParam, err := compatUint64ToInt64(workspaceID)
	if err != nil {
		return ProviderSecret{}, err
	}
	var userIDParam sql.NullInt64
	if userID != nil && *userID > 0 {
		converted, err := compatUint64ToInt64(*userID)
		if err != nil {
			return ProviderSecret{}, err
		}
		userIDParam = sql.NullInt64{Int64: converted, Valid: true}
	}
	return q.FindPreferredProviderSecretManual(ctx, FindPreferredProviderSecretManualParams{
		WorkspaceID: sql.NullInt64{Int64: workspaceIDParam, Valid: true},
		Provider:    strings.ToLower(strings.TrimSpace(provider)),
		UserID:      userIDParam,
	})
}
