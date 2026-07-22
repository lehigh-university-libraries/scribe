package db

// Store query adapters in this file are the sole mapping boundary from
// domain-shaped API-key values to sqlc-generated queries in api_keys.sql.

import (
	"context"
	"database/sql"
	"time"
)

type APIKey struct {
	ID              uint64
	WorkspaceID     uint64
	CreatedByUserID uint64
	Name            string
	KeyPrefix       string
	KeyHash         string
	Role            string
	Scopes          sql.NullString
	ExpiresAt       sql.NullTime
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type CreateAPIKeyParams struct {
	WorkspaceID     uint64
	CreatedByUserID uint64
	Name            string
	KeyPrefix       string
	KeyHash         string
	Role            string
	Scopes          string
	ExpiresAt       *time.Time
}

func (q *Queries) CreateAPIKey(ctx context.Context, arg CreateAPIKeyParams) (uint64, error) {
	res, err := q.CreateAPIKeyManual(ctx, CreateAPIKeyManualParams{
		WorkspaceID:     arg.WorkspaceID,
		CreatedByUserID: arg.CreatedByUserID,
		Name:            arg.Name,
		KeyPrefix:       arg.KeyPrefix,
		KeyHash:         arg.KeyHash,
		Role:            ApiKeysRole(arg.Role),
		Scopes:          rawJSON(arg.Scopes),
		ExpiresAt:       nullTime(arg.ExpiresAt),
	})
	if err != nil {
		return 0, err
	}
	return lastInsertID(res)
}

func (q *Queries) GetAPIKeyByHash(ctx context.Context, keyHash string) (APIKey, error) {
	row, err := q.GetAPIKeyByHashManual(ctx, keyHash)
	if err != nil {
		return APIKey{}, err
	}
	return APIKey{
		ID:              row.ID,
		WorkspaceID:     row.WorkspaceID,
		CreatedByUserID: row.CreatedByUserID,
		Name:            row.Name,
		KeyPrefix:       row.KeyPrefix,
		KeyHash:         row.KeyHash,
		Role:            string(row.Role),
		Scopes:          rawJSONToNullString(row.Scopes),
		ExpiresAt:       row.ExpiresAt,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	}, nil
}

func (q *Queries) GetAPIKey(ctx context.Context, id uint64) (APIKey, error) {
	row, err := q.GetAPIKeyManual(ctx, id)
	if err != nil {
		return APIKey{}, err
	}
	return APIKey{
		ID:              row.ID,
		WorkspaceID:     row.WorkspaceID,
		CreatedByUserID: row.CreatedByUserID,
		Name:            row.Name,
		KeyPrefix:       row.KeyPrefix,
		KeyHash:         row.KeyHash,
		Role:            string(row.Role),
		Scopes:          rawJSONToNullString(row.Scopes),
		ExpiresAt:       row.ExpiresAt,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	}, nil
}

func (q *Queries) ListAPIKeysByWorkspace(ctx context.Context, workspaceID uint64) ([]APIKey, error) {
	rows, err := q.ListAPIKeysByWorkspaceManual(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]APIKey, 0, len(rows))
	for _, row := range rows {
		out = append(out, APIKey{
			ID:              row.ID,
			WorkspaceID:     row.WorkspaceID,
			CreatedByUserID: row.CreatedByUserID,
			Name:            row.Name,
			KeyPrefix:       row.KeyPrefix,
			KeyHash:         row.KeyHash,
			Role:            string(row.Role),
			Scopes:          rawJSONToNullString(row.Scopes),
			ExpiresAt:       row.ExpiresAt,
			CreatedAt:       row.CreatedAt,
			UpdatedAt:       row.UpdatedAt,
		})
	}
	return out, nil
}

func (q *Queries) DeleteAPIKey(ctx context.Context, id uint64) error {
	return q.DeleteAPIKeyManual(ctx, id)
}

func (q *Queries) DeleteAPIKeyForWorkspace(ctx context.Context, id, workspaceID uint64) error {
	res, err := q.DeleteAPIKeyForWorkspaceManual(ctx, DeleteAPIKeyForWorkspaceManualParams{
		ID:          id,
		WorkspaceID: workspaceID,
	})
	return requireAffectedRow(res, err)
}
