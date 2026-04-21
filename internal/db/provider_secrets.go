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
	res, err := q.db.ExecContext(ctx, `
INSERT INTO provider_secrets (
  user_id,
  workspace_id,
  provider,
  name,
  vault_path,
  key_hint
) VALUES (?, ?, ?, ?, ?, ?)
`,
		compatNullUint64(arg.UserID),
		compatNullUint64(arg.WorkspaceID),
		strings.ToLower(strings.TrimSpace(arg.Provider)),
		arg.Name,
		arg.VaultPath,
		compatNullableString(arg.KeyHint),
	)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	return uint64(id), err
}

func (q *Queries) ListProviderSecretsVisibleToUser(ctx context.Context, workspaceID, userID uint64) ([]ProviderSecret, error) {
	rows, err := q.db.QueryContext(ctx, `
SELECT
  id,
  user_id,
  workspace_id,
  provider,
  name,
  vault_path,
  key_hint,
  created_at,
  updated_at
FROM provider_secrets
WHERE workspace_id = ?
  AND (user_id IS NULL OR user_id = ?)
ORDER BY provider ASC, name ASC, updated_at DESC
`, workspaceID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]ProviderSecret, 0)
	for rows.Next() {
		var row ProviderSecret
		if err := rows.Scan(
			&row.ID,
			&row.UserID,
			&row.WorkspaceID,
			&row.Provider,
			&row.Name,
			&row.VaultPath,
			&row.KeyHint,
			&row.CreatedAt,
			&row.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (q *Queries) GetProviderSecretVisibleToUser(ctx context.Context, id, workspaceID, userID uint64) (ProviderSecret, error) {
	var row ProviderSecret
	err := q.db.QueryRowContext(ctx, `
SELECT
  id,
  user_id,
  workspace_id,
  provider,
  name,
  vault_path,
  key_hint,
  created_at,
  updated_at
FROM provider_secrets
WHERE id = ?
  AND workspace_id = ?
  AND (user_id IS NULL OR user_id = ?)
LIMIT 1
`, id, workspaceID, userID).Scan(
		&row.ID,
		&row.UserID,
		&row.WorkspaceID,
		&row.Provider,
		&row.Name,
		&row.VaultPath,
		&row.KeyHint,
		&row.CreatedAt,
		&row.UpdatedAt,
	)
	return row, err
}

func (q *Queries) DeleteProviderSecret(ctx context.Context, id, workspaceID uint64, userID *uint64) error {
	args := []any{id, workspaceID}
	query := `
DELETE FROM provider_secrets
WHERE id = ?
  AND workspace_id = ?`
	if userID == nil {
		query += " AND user_id IS NULL"
	} else {
		query += " AND user_id = ?"
		args = append(args, *userID)
	}

	res, err := q.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (q *Queries) FindPreferredProviderSecret(ctx context.Context, workspaceID uint64, userID *uint64, provider string) (ProviderSecret, error) {
	var row ProviderSecret
	provider = strings.ToLower(strings.TrimSpace(provider))
	if userID != nil && *userID > 0 {
		err := q.db.QueryRowContext(ctx, `
SELECT
  id,
  user_id,
  workspace_id,
  provider,
  name,
  vault_path,
  key_hint,
  created_at,
  updated_at
FROM provider_secrets
WHERE workspace_id = ?
  AND user_id = ?
  AND provider = ?
ORDER BY updated_at DESC, id DESC
LIMIT 1
`, workspaceID, *userID, provider).Scan(
			&row.ID,
			&row.UserID,
			&row.WorkspaceID,
			&row.Provider,
			&row.Name,
			&row.VaultPath,
			&row.KeyHint,
			&row.CreatedAt,
			&row.UpdatedAt,
		)
		if err == nil {
			return row, nil
		}
		if err != sql.ErrNoRows {
			return ProviderSecret{}, err
		}
	}

	err := q.db.QueryRowContext(ctx, `
SELECT
  id,
  user_id,
  workspace_id,
  provider,
  name,
  vault_path,
  key_hint,
  created_at,
  updated_at
FROM provider_secrets
WHERE workspace_id = ?
  AND user_id IS NULL
  AND provider = ?
ORDER BY updated_at DESC, id DESC
LIMIT 1
`, workspaceID, provider).Scan(
		&row.ID,
		&row.UserID,
		&row.WorkspaceID,
		&row.Provider,
		&row.Name,
		&row.VaultPath,
		&row.KeyHint,
		&row.CreatedAt,
		&row.UpdatedAt,
	)
	return row, err
}
