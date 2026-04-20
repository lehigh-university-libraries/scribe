package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	db "github.com/lehigh-university-libraries/scribe/internal/db"
)

type ProviderSecret struct {
	ID          uint64    `json:"id"`
	UserID      *uint64   `json:"user_id,omitempty"`
	WorkspaceID *uint64   `json:"workspace_id,omitempty"`
	Provider    string    `json:"provider"`
	Name        string    `json:"name"`
	VaultPath   string    `json:"vault_path"`
	KeyHint     string    `json:"key_hint,omitempty"`
	Scope       string    `json:"scope"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type ProviderSecretStore struct {
	q *db.Queries
}

func NewProviderSecretStore(pool *sql.DB) *ProviderSecretStore {
	return &ProviderSecretStore{q: db.New(pool)}
}

func (s *ProviderSecretStore) Create(ctx context.Context, secret ProviderSecret) (ProviderSecret, error) {
	id, err := s.q.CreateProviderSecret(ctx, db.CreateProviderSecretParams{
		UserID:      secret.UserID,
		WorkspaceID: secret.WorkspaceID,
		Provider:    strings.ToLower(strings.TrimSpace(secret.Provider)),
		Name:        strings.TrimSpace(secret.Name),
		VaultPath:   strings.TrimSpace(secret.VaultPath),
		KeyHint:     strings.TrimSpace(secret.KeyHint),
	})
	if err != nil {
		return ProviderSecret{}, fmt.Errorf("create provider secret: %w", err)
	}
	if secret.WorkspaceID == nil {
		return ProviderSecret{}, fmt.Errorf("provider secret missing workspace id after insert")
	}
	visibleUserID := uint64(0)
	if secret.UserID != nil {
		visibleUserID = *secret.UserID
	}
	created, err := s.q.GetProviderSecretVisibleToUser(ctx, id, *secret.WorkspaceID, visibleUserID)
	if err != nil {
		return ProviderSecret{}, fmt.Errorf("reload provider secret: %w", err)
	}
	return rowToProviderSecret(created), nil
}

func (s *ProviderSecretStore) ListVisible(ctx context.Context, workspaceID, userID uint64) ([]ProviderSecret, error) {
	rows, err := s.q.ListProviderSecretsVisibleToUser(ctx, workspaceID, userID)
	if err != nil {
		return nil, fmt.Errorf("list provider secrets: %w", err)
	}
	out := make([]ProviderSecret, 0, len(rows))
	for _, row := range rows {
		out = append(out, rowToProviderSecret(row))
	}
	return out, nil
}

func (s *ProviderSecretStore) GetVisible(ctx context.Context, id, workspaceID, userID uint64) (ProviderSecret, error) {
	row, err := s.q.GetProviderSecretVisibleToUser(ctx, id, workspaceID, userID)
	if err != nil {
		return ProviderSecret{}, err
	}
	return rowToProviderSecret(row), nil
}

func (s *ProviderSecretStore) DeleteWorkspaceSecret(ctx context.Context, id, workspaceID uint64) error {
	return s.q.DeleteProviderSecret(ctx, id, workspaceID, nil)
}

func (s *ProviderSecretStore) DeleteUserSecret(ctx context.Context, id, workspaceID, userID uint64) error {
	return s.q.DeleteProviderSecret(ctx, id, workspaceID, &userID)
}

func (s *ProviderSecretStore) ResolvePreferred(ctx context.Context, workspaceID uint64, userID *uint64, provider string) (ProviderSecret, error) {
	row, err := s.q.FindPreferredProviderSecret(ctx, workspaceID, userID, provider)
	if err != nil {
		return ProviderSecret{}, err
	}
	return rowToProviderSecret(row), nil
}

func rowToProviderSecret(row db.ProviderSecret) ProviderSecret {
	secret := ProviderSecret{
		ID:        row.ID,
		Provider:  row.Provider,
		Name:      row.Name,
		VaultPath: row.VaultPath,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
		Scope:     "workspace",
	}
	if row.UserID.Valid {
		userID := uint64(row.UserID.Int64)
		secret.UserID = &userID
		secret.Scope = "user"
	}
	if row.WorkspaceID.Valid {
		workspaceID := uint64(row.WorkspaceID.Int64)
		secret.WorkspaceID = &workspaceID
	}
	if row.KeyHint.Valid {
		secret.KeyHint = row.KeyHint.String
	}
	return secret
}
