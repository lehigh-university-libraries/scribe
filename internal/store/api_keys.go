package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	db "github.com/lehigh-university-libraries/scribe/internal/db"
)

type APIKey struct {
	ID              uint64    `json:"id"`
	WorkspaceID     uint64    `json:"workspace_id"`
	CreatedByUserID uint64    `json:"created_by_user_id"`
	Name            string    `json:"name"`
	KeyPrefix       string    `json:"key_prefix"`
	Role            string    `json:"role"`
	Scopes          []string  `json:"scopes,omitempty"`
	LastUsedAt      time.Time `json:"last_used_at,omitempty"`
	ExpiresAt       time.Time `json:"expires_at,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type APIKeyStore struct {
	q *db.Queries
}

func NewAPIKeyStore(pool *sql.DB) *APIKeyStore {
	return &APIKeyStore{q: db.New(pool)}
}

func (s *APIKeyStore) Create(ctx context.Context, workspaceID, createdByUserID uint64, name, role string, scopes []string, expiresAt *time.Time) (APIKey, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return APIKey{}, "", fmt.Errorf("api key name is required")
	}
	role = normalizeWorkspaceRole(role)
	if role == "" {
		role = "read"
	}
	rawToken, err := generateAPIKeyToken()
	if err != nil {
		return APIKey{}, "", err
	}
	keyPrefix := apiKeyPrefix(rawToken)
	id, err := s.q.CreateAPIKey(ctx, db.CreateAPIKeyParams{
		WorkspaceID:     workspaceID,
		CreatedByUserID: createdByUserID,
		Name:            name,
		KeyPrefix:       keyPrefix,
		KeyHash:         hashAPIKey(rawToken),
		Role:            role,
		Scopes:          marshalScopes(scopes),
		ExpiresAt:       expiresAt,
	})
	if err != nil {
		return APIKey{}, "", fmt.Errorf("create api key: %w", err)
	}
	apiKey, err := s.Get(ctx, id)
	if err != nil {
		return APIKey{}, "", err
	}
	return apiKey, rawToken, nil
}

func (s *APIKeyStore) Get(ctx context.Context, id uint64) (APIKey, error) {
	row, err := s.q.GetAPIKey(ctx, id)
	if err != nil {
		return APIKey{}, fmt.Errorf("get api key: %w", err)
	}
	return rowToAPIKey(row), nil
}

func (s *APIKeyStore) GetByToken(ctx context.Context, rawToken string) (APIKey, error) {
	row, err := s.q.GetAPIKeyByHash(ctx, hashAPIKey(rawToken))
	if err != nil {
		return APIKey{}, err
	}
	apiKey := rowToAPIKey(row)
	if !apiKey.ExpiresAt.IsZero() && time.Now().UTC().After(apiKey.ExpiresAt) {
		return APIKey{}, sql.ErrNoRows
	}
	_ = s.q.TouchAPIKey(ctx, apiKey.ID)
	return apiKey, nil
}

func (s *APIKeyStore) ListByWorkspace(ctx context.Context, workspaceID uint64) ([]APIKey, error) {
	rows, err := s.q.ListAPIKeysByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	out := make([]APIKey, 0, len(rows))
	for _, row := range rows {
		out = append(out, rowToAPIKey(row))
	}
	return out, nil
}

func (s *APIKeyStore) DeleteForWorkspace(ctx context.Context, id, workspaceID uint64) error {
	return s.q.DeleteAPIKeyForWorkspace(ctx, id, workspaceID)
}

func rowToAPIKey(row db.APIKey) APIKey {
	apiKey := APIKey{
		ID:              row.ID,
		WorkspaceID:     row.WorkspaceID,
		CreatedByUserID: row.CreatedByUserID,
		Name:            row.Name,
		KeyPrefix:       row.KeyPrefix,
		Role:            row.Role,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	}
	if row.Scopes.Valid && strings.TrimSpace(row.Scopes.String) != "" {
		_ = json.Unmarshal([]byte(row.Scopes.String), &apiKey.Scopes)
	}
	if row.LastUsedAt.Valid {
		apiKey.LastUsedAt = row.LastUsedAt.Time
	}
	if row.ExpiresAt.Valid {
		apiKey.ExpiresAt = row.ExpiresAt.Time
	}
	return apiKey
}

func marshalScopes(scopes []string) string {
	if len(scopes) == 0 {
		return ""
	}
	normalized := make([]string, 0, len(scopes))
	seen := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			continue
		}
		if _, ok := seen[scope]; ok {
			continue
		}
		seen[scope] = struct{}{}
		normalized = append(normalized, scope)
	}
	if len(normalized) == 0 {
		return ""
	}
	body, err := json.Marshal(normalized)
	if err != nil {
		return ""
	}
	return string(body)
}

func hashAPIKey(rawToken string) string {
	sum := sha256.Sum256([]byte(rawToken))
	return hex.EncodeToString(sum[:])
}

func generateAPIKeyToken() (string, error) {
	buf := make([]byte, 48)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate api key: %w", err)
	}
	return "scribe_" + base64.RawURLEncoding.EncodeToString(buf)[:48], nil
}

func apiKeyPrefix(rawToken string) string {
	if len(rawToken) <= 16 {
		return rawToken
	}
	return rawToken[:16]
}

func normalizeWorkspaceRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "admin", "write", "create", "read":
		return strings.ToLower(strings.TrimSpace(role))
	default:
		return ""
	}
}
