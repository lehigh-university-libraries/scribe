package db

import (
	"context"
	"database/sql"
	"strings"
)

type CreateProviderSecretParams struct {
	UserID      *uint64
	WorkspaceID uint64
	Provider    string
	Name        string
	VaultPath   string
	KeyHint     string
}

func (q *Queries) CreateProviderSecret(ctx context.Context, arg CreateProviderSecretParams) (uint64, error) {
	userID, err := nullUint64(arg.UserID)
	if err != nil {
		return 0, err
	}
	res, err := q.CreateProviderSecretManual(ctx, CreateProviderSecretManualParams{
		UserID:      userID,
		WorkspaceID: arg.WorkspaceID,
		Provider:    strings.ToLower(strings.TrimSpace(arg.Provider)),
		Name:        arg.Name,
		VaultPath:   arg.VaultPath,
		KeyHint:     nullableString(arg.KeyHint),
	})
	if err != nil {
		return 0, err
	}
	return lastInsertID(res)
}

func (q *Queries) ListProviderSecretsVisibleToUser(ctx context.Context, workspaceID, userID uint64) ([]ProviderSecret, error) {
	userIDParam, err := uint64ToInt64(userID)
	if err != nil {
		return nil, err
	}
	return q.ListProviderSecretsVisibleToUserManual(ctx, ListProviderSecretsVisibleToUserManualParams{
		WorkspaceID: workspaceID,
		UserID:      sql.NullInt64{Int64: userIDParam, Valid: userID > 0},
	})
}

func (q *Queries) GetProviderSecretVisibleToUser(ctx context.Context, id, workspaceID, userID uint64) (ProviderSecret, error) {
	userIDParam, err := uint64ToInt64(userID)
	if err != nil {
		return ProviderSecret{}, err
	}
	return q.GetProviderSecretVisibleToUserManual(ctx, GetProviderSecretVisibleToUserManualParams{
		ID:          id,
		WorkspaceID: workspaceID,
		UserID:      sql.NullInt64{Int64: userIDParam, Valid: userID > 0},
	})
}

func (q *Queries) FindPreferredProviderSecret(ctx context.Context, workspaceID uint64, userID *uint64, provider string) (ProviderSecret, error) {
	var userIDParam sql.NullInt64
	if userID != nil && *userID > 0 {
		converted, err := uint64ToInt64(*userID)
		if err != nil {
			return ProviderSecret{}, err
		}
		userIDParam = sql.NullInt64{Int64: converted, Valid: true}
	}
	return q.FindPreferredProviderSecretManual(ctx, FindPreferredProviderSecretManualParams{
		WorkspaceID: workspaceID,
		Provider:    strings.ToLower(strings.TrimSpace(provider)),
		UserID:      userIDParam,
	})
}
