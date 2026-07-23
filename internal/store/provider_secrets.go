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
	ID             uint64    `json:"id"`
	UserID         *uint64   `json:"user_id,omitempty"`
	WorkspaceID    uint64    `json:"workspace_id"`
	Provider       string    `json:"provider"`
	Name           string    `json:"name"`
	VaultPath      string    `json:"vault_path"`
	KeyHint        string    `json:"key_hint,omitempty"`
	Scope          string    `json:"scope"`
	LifecycleState string    `json:"-"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

const (
	ProviderSecretPendingWrite   = "pending_write"
	ProviderSecretActive         = "active"
	ProviderSecretCleanupPending = "cleanup_pending"
)

type ProviderSecretStore struct {
	pool *sql.DB
	q    *db.Queries
}

const MaxProviderSecretsPerWorkspace = 100

var ErrProviderSecretLimit = fmt.Errorf("workspace provider secret limit reached")

func NewProviderSecretStore(pool *sql.DB) *ProviderSecretStore {
	return &ProviderSecretStore{pool: pool, q: db.New(pool)}
}

func (s *ProviderSecretStore) Create(ctx context.Context, secret ProviderSecret) (ProviderSecret, error) {
	if secret.WorkspaceID == 0 {
		return ProviderSecret{}, fmt.Errorf("provider secret workspace id is required")
	}
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return ProviderSecret{}, fmt.Errorf("create provider secret: begin admission transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.q.WithTx(tx)
	if _, err := queries.LockWorkspaceManual(ctx, secret.WorkspaceID); err != nil {
		return ProviderSecret{}, fmt.Errorf("create provider secret: lock workspace: %w", err)
	}
	if secret.UserID != nil {
		if *secret.UserID == 0 {
			return ProviderSecret{}, fmt.Errorf("create provider secret: valid user is required")
		}
		if _, err := queries.LockWorkspaceMemberRole(ctx, secret.WorkspaceID, *secret.UserID); err != nil {
			return ProviderSecret{}, fmt.Errorf("create provider secret: lock user membership: %w", err)
		}
	}
	count, err := queries.CountProviderSecretsByWorkspaceManual(ctx, secret.WorkspaceID)
	if err != nil {
		return ProviderSecret{}, fmt.Errorf("create provider secret: count workspace secrets: %w", err)
	}
	if count >= MaxProviderSecretsPerWorkspace {
		return ProviderSecret{}, ErrProviderSecretLimit
	}
	id, err := queries.CreateProviderSecret(ctx, db.CreateProviderSecretParams{
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
	visibleUserID := uint64(0)
	if secret.UserID != nil {
		visibleUserID = *secret.UserID
	}
	row, err := queries.GetProviderSecretLifecycleManual(ctx, db.GetProviderSecretLifecycleManualParams{ID: id, WorkspaceID: secret.WorkspaceID})
	if err != nil {
		return ProviderSecret{}, fmt.Errorf("reload provider secret: %w", err)
	}
	loaded := rowToProviderSecret(row)
	if loaded.LifecycleState != ProviderSecretPendingWrite || (loaded.UserID == nil) != (visibleUserID == 0) {
		return ProviderSecret{}, fmt.Errorf("provider secret was created with invalid lifecycle ownership")
	}
	if err := tx.Commit(); err != nil {
		return ProviderSecret{}, fmt.Errorf("create provider secret: commit admission: %w", err)
	}
	return loaded, nil
}

func (s *ProviderSecretStore) GetLifecycle(ctx context.Context, id, workspaceID uint64) (ProviderSecret, error) {
	row, err := s.q.GetProviderSecretLifecycleManual(ctx, db.GetProviderSecretLifecycleManualParams{ID: id, WorkspaceID: workspaceID})
	if err != nil {
		return ProviderSecret{}, err
	}
	return rowToProviderSecret(row), nil
}

func (s *ProviderSecretStore) Activate(ctx context.Context, id, workspaceID uint64) (ProviderSecret, error) {
	result, err := s.q.ActivateProviderSecretManual(ctx, db.ActivateProviderSecretManualParams{ID: id, WorkspaceID: workspaceID})
	if err != nil {
		return ProviderSecret{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return ProviderSecret{}, err
	}
	if affected != 1 {
		return ProviderSecret{}, sql.ErrNoRows
	}
	return s.GetLifecycle(ctx, id, workspaceID)
}

func (s *ProviderSecretStore) MarkPendingCleanup(ctx context.Context, id, workspaceID uint64) error {
	result, err := s.q.MarkPendingProviderSecretCleanupManual(ctx, db.MarkPendingProviderSecretCleanupManualParams{ID: id, WorkspaceID: workspaceID})
	return requireSingleProviderSecretTransition(result, err)
}

func (s *ProviderSecretStore) MarkActiveCleanup(ctx context.Context, id, workspaceID uint64) error {
	result, err := s.q.MarkActiveProviderSecretCleanupManual(ctx, db.MarkActiveProviderSecretCleanupManualParams{ID: id, WorkspaceID: workspaceID})
	return requireSingleProviderSecretTransition(result, err)
}

func requireSingleProviderSecretTransition(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *ProviderSecretStore) ListCleanupCandidates(ctx context.Context, staleBefore time.Time, limit int) ([]ProviderSecret, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := s.q.ListProviderSecretCleanupCandidatesManual(ctx, db.ListProviderSecretCleanupCandidatesManualParams{
		StaleBefore: staleBefore,
		Limit:       int32(limit),
	})
	if err != nil {
		return nil, err
	}
	secrets := make([]ProviderSecret, 0, len(rows))
	for _, row := range rows {
		secrets = append(secrets, rowToProviderSecret(row))
	}
	return secrets, nil
}

func (s *ProviderSecretStore) DeleteInactive(ctx context.Context, id, workspaceID uint64) error {
	result, err := s.q.DeleteInactiveProviderSecretManual(ctx, db.DeleteInactiveProviderSecretManualParams{ID: id, WorkspaceID: workspaceID})
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
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

func (s *ProviderSecretStore) ResolvePreferred(ctx context.Context, workspaceID uint64, userID *uint64, provider string) (ProviderSecret, error) {
	row, err := s.q.FindPreferredProviderSecret(ctx, workspaceID, userID, provider)
	if err != nil {
		return ProviderSecret{}, err
	}
	return rowToProviderSecret(row), nil
}

func rowToProviderSecret(row db.ProviderSecret) ProviderSecret {
	secret := ProviderSecret{
		ID:             row.ID,
		WorkspaceID:    row.WorkspaceID,
		Provider:       row.Provider,
		Name:           row.Name,
		VaultPath:      row.VaultPath,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
		Scope:          "workspace",
		LifecycleState: string(row.LifecycleState),
	}
	if userID, ok := uint64PtrFromNullInt64(row.UserID); ok {
		secret.UserID = userID
		secret.Scope = "user"
	}
	if row.KeyHint.Valid {
		secret.KeyHint = row.KeyHint.String
	}
	return secret
}
