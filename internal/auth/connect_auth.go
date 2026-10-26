package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect/v2"
	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/providerregistry"
	"github.com/lehigh-university-libraries/scribe/internal/providersecret"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
	"github.com/lehigh-university-libraries/scribe/proto/scribe/v1/scribev1connect"
)

var _ scribev1connect.AuthServiceHandler = (*Manager)(nil)

func (m *Manager) GetAuthMe(ctx context.Context, _ *scribev1.GetAuthMeRequest) (*scribev1.GetAuthMeResponse, error) {
	principal, ok := PrincipalFromContext(ctx)
	if !ok {
		principal = m.anonymousPrincipal()
	}
	resp := &scribev1.GetAuthMeResponse{
		Authenticated: principal.Authenticated,
		AuthType:      principal.AuthType,
	}
	if !m.auth.PreviewAnonymous {
		resp.LoginUrl = "/auth/google"
		resp.LogoutUrl = "/logout"
	}
	if principal.Authenticated {
		resp.User = &scribev1.AuthUser{
			Id:                 principal.UserID,
			Email:              principal.Email,
			Name:               principal.Name,
			PictureUrl:         principal.PictureURL,
			IsAdmin:            principal.IsAdmin,
			DefaultWorkspaceId: principal.DefaultWorkspaceID,
		}
		resp.Workspace = &scribev1.AuthWorkspace{
			Id:   principal.WorkspaceID,
			Name: principal.WorkspaceName,
			Role: principal.WorkspaceRole,
		}
	}
	return resp, nil
}

func (m *Manager) ListAPIKeys(ctx context.Context, _ *scribev1.ListAPIKeysRequest) (*scribev1.ListAPIKeysResponse, error) {
	principal, err := m.sessionPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	keys, err := m.apiKeys.ListByWorkspace(ctx, principal.WorkspaceID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list api keys: %w", err).Error())
	}
	resp := &scribev1.ListAPIKeysResponse{ApiKeys: make([]*scribev1.APIKeyRecord, 0, len(keys))}
	for _, key := range keys {
		resp.ApiKeys = append(resp.ApiKeys, apiKeyToProto(key))
	}
	return resp, nil
}

func (m *Manager) CreateAPIKey(ctx context.Context, req *scribev1.CreateAPIKeyRequest) (*scribev1.CreateAPIKeyResponse, error) {
	principal, err := m.sessionPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	var expiresAt *time.Time
	if raw := strings.TrimSpace(req.GetExpiresAt()); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return nil, connect.Errorf(connect.CodeInvalidArgument, "invalid expires_at")
		}
		expiresAt = &parsed
	}
	scopes := req.GetScopes()
	if len(scopes) == 0 {
		// Role-only keys use the selected role's permissions; authorization still
		// intersects scopes with that role and the creator's current membership.
		scopes = []string{"*"}
	}
	apiKey, rawKey, err := m.apiKeys.Create(ctx, principal.WorkspaceID, principal.UserID, req.GetName(), req.GetRole(), scopes, expiresAt)
	if err != nil {
		if errors.Is(err, store.ErrAPIKeyLimit) {
			return nil, connect.NewError(connect.CodeResourceExhausted, err.Error()).WithCause(err)
		}
		return nil, connect.Errorf(connect.CodeInternal, "create api key persistence failed")
	}
	return &scribev1.CreateAPIKeyResponse{
		ApiKey: apiKeyToProto(apiKey),
		Key:    rawKey,
	}, nil
}

func (m *Manager) DeleteAPIKey(ctx context.Context, req *scribev1.DeleteAPIKeyRequest) (*scribev1.DeleteAPIKeyResponse, error) {
	principal, err := m.sessionPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := m.apiKeys.DeleteForWorkspace(ctx, req.GetKeyId(), principal.WorkspaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.Errorf(connect.CodeNotFound, "api key not found")
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("delete api key: %w", err).Error())
	}
	return &scribev1.DeleteAPIKeyResponse{}, nil
}

func (m *Manager) ListProviderSecrets(ctx context.Context, _ *scribev1.ListProviderSecretsRequest) (*scribev1.ListProviderSecretsResponse, error) {
	principal, err := m.sessionPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if m.providerSecrets == nil {
		return nil, connect.Errorf(connect.CodeUnavailable, "provider secret storage is not configured")
	}
	secrets, err := m.providerSecrets.ListVisible(ctx, principal.WorkspaceID, principal.UserID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list provider secrets: %w", err).Error())
	}
	resp := &scribev1.ListProviderSecretsResponse{ProviderSecrets: make([]*scribev1.ProviderSecretRecord, 0, len(secrets))}
	for _, secret := range secrets {
		resp.ProviderSecrets = append(resp.ProviderSecrets, providerSecretToProto(secret))
	}
	return resp, nil
}

func (m *Manager) CreateProviderSecret(ctx context.Context, req *scribev1.CreateProviderSecretRequest) (*scribev1.CreateProviderSecretResponse, error) {
	principal, err := m.sessionPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if m.providerSecrets == nil || m.vault == nil {
		return nil, connect.Errorf(connect.CodeUnavailable, "provider secret storage is not configured")
	}

	provider := strings.ToLower(strings.TrimSpace(req.GetProvider()))
	descriptor, supported := providerregistry.New(config.Get().Config).Provider(provider)
	if !supported || !descriptor.Credentials.Required() {
		return nil, connect.Errorf(connect.CodeInvalidArgument, "unsupported provider secret provider")
	}
	name := strings.TrimSpace(req.GetName())
	if name == "" {
		return nil, connect.Errorf(connect.CodeInvalidArgument, "name is required")
	}
	// Provider credentials are opaque: validate without trimming or otherwise
	// mutating the value that will be stored in Vault.
	apiKey := req.GetApiKey()
	apiKeyLength := utf8.RuneCountInString(apiKey)
	if strings.TrimSpace(apiKey) == "" || apiKeyLength < 8 || apiKeyLength > 8192 {
		return nil, connect.Errorf(connect.CodeInvalidArgument, "api_key must contain between 8 and 8192 characters")
	}

	scope := strings.ToLower(strings.TrimSpace(req.GetScope()))
	if scope == "" {
		scope = "workspace"
	}
	if scope != "user" && scope != "workspace" {
		return nil, connect.Errorf(connect.CodeInvalidArgument, "scope must be 'user' or 'workspace'")
	}
	if scope == "workspace" && !strings.EqualFold(principal.WorkspaceRole, "admin") && !principal.IsAdmin {
		return nil, connect.Errorf(connect.CodePermissionDenied, "workspace secrets require admin role")
	}

	pathSuffix, err := randomString(12)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("generate secret path: %w", err).Error())
	}
	vaultPath := fmt.Sprintf(
		"%s/%d/%s/%s-%s",
		m.providerSecretVaultPrefix(),
		principal.WorkspaceID,
		provider,
		store.Slugify(name),
		strings.ToLower(pathSuffix),
	)
	var userID *uint64
	workspaceID := principal.WorkspaceID
	if scope == "user" {
		userID = &principal.UserID
	}
	secret, err := m.providerSecrets.Create(ctx, store.ProviderSecret{
		UserID:      userID,
		WorkspaceID: workspaceID,
		Provider:    provider,
		Name:        name,
		VaultPath:   vaultPath,
		KeyHint:     secretKeyHint(apiKey),
		Scope:       scope,
	})
	if err != nil {
		if errors.Is(err, store.ErrProviderSecretLimit) {
			return nil, connect.NewError(connect.CodeResourceExhausted, err.Error()).WithCause(err)
		}
		return nil, connect.Errorf(connect.CodeInternal, "create provider secret persistence failed")
	}
	// Persist the non-secret locator before writing material. A process crash can
	// therefore leave only fail-closed metadata pointing at a missing value,
	// never an untracked live credential in Vault. The short pre-write window is
	// safe for concurrent readers: a missing Vault value yields no credential.
	if err := m.vault.Write(ctx, vaultPath, map[string]string{
		"api_key":  apiKey,
		"provider": provider,
		"name":     name,
	}); err != nil {
		m.scheduleProviderSecretCleanup(ctx, &secret)
		return nil, connect.Errorf(connect.CodeInternal, "write provider secret failed")
	}
	secret, err = m.providerSecrets.Activate(ctx, secret.ID, workspaceID)
	if err != nil {
		persisted, loadErr := m.providerSecrets.GetLifecycle(ctx, secret.ID, workspaceID)
		if loadErr == nil && persisted.LifecycleState == store.ProviderSecretActive {
			secret = persisted
			return &scribev1.CreateProviderSecretResponse{
				ProviderSecret: providerSecretToProto(secret),
			}, nil
		}
		m.scheduleProviderSecretCleanup(ctx, &secret)
		return nil, connect.Errorf(connect.CodeInternal, "activate provider secret failed")
	}
	return &scribev1.CreateProviderSecretResponse{
		ProviderSecret: providerSecretToProto(secret),
	}, nil
}

func (m *Manager) DeleteProviderSecret(ctx context.Context, req *scribev1.DeleteProviderSecretRequest) (*scribev1.DeleteProviderSecretResponse, error) {
	principal, err := m.sessionPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if m.providerSecrets == nil || m.vault == nil {
		return nil, connect.Errorf(connect.CodeUnavailable, "provider secret storage is not configured")
	}

	secret, err := m.providerSecrets.GetVisible(ctx, req.GetSecretId(), principal.WorkspaceID, principal.UserID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.Errorf(connect.CodeNotFound, "provider secret not found")
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("load provider secret: %w", err).Error())
	}
	if secret.Scope == "workspace" && !strings.EqualFold(principal.WorkspaceRole, "admin") && !principal.IsAdmin {
		return nil, connect.Errorf(connect.CodePermissionDenied, "workspace secrets require admin role")
	}
	if err := m.validateProviderSecretVaultPath(secret.VaultPath, principal.WorkspaceID); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error()).WithCause(err)
	}
	if err := m.providerSecrets.MarkActiveCleanup(ctx, secret.ID, secret.WorkspaceID); err != nil {
		return nil, connect.Errorf(connect.CodeInternal, "schedule provider secret deletion failed")
	}
	secret.LifecycleState = store.ProviderSecretCleanupPending
	if err := m.cleanupProviderSecret(ctx, secret); err != nil {
		return nil, connect.Errorf(connect.CodeUnavailable, "provider secret deletion is pending")
	}
	return &scribev1.DeleteProviderSecretResponse{}, nil
}

func (m *Manager) providerSecretVaultPrefix() string {
	if m != nil {
		return providersecret.VaultPrefix(m.providerSecretsVaultPrefix)
	}
	return providersecret.DefaultVaultPrefix
}

func (m *Manager) validateProviderSecretVaultPath(vaultPath string, workspaceID uint64) error {
	return providersecret.ValidateVaultPath(m.providerSecretVaultPrefix(), workspaceID, vaultPath)
}

func apiKeyToProto(apiKey store.APIKey) *scribev1.APIKeyRecord {
	record := &scribev1.APIKeyRecord{
		Id:              apiKey.ID,
		WorkspaceId:     apiKey.WorkspaceID,
		CreatedByUserId: apiKey.CreatedByUserID,
		Name:            apiKey.Name,
		KeyPrefix:       apiKey.KeyPrefix,
		Role:            apiKey.Role,
		Scopes:          append([]string(nil), apiKey.Scopes...),
		CreatedAt:       formatOptionalTime(apiKey.CreatedAt),
		UpdatedAt:       formatOptionalTime(apiKey.UpdatedAt),
	}
	if !apiKey.ExpiresAt.IsZero() {
		record.ExpiresAt = formatOptionalTime(apiKey.ExpiresAt)
	}
	return record
}

func providerSecretToProto(secret store.ProviderSecret) *scribev1.ProviderSecretRecord {
	record := &scribev1.ProviderSecretRecord{
		Id:        secret.ID,
		Provider:  secret.Provider,
		Name:      secret.Name,
		KeyHint:   secret.KeyHint,
		Scope:     secret.Scope,
		CreatedAt: formatOptionalTime(secret.CreatedAt),
		UpdatedAt: formatOptionalTime(secret.UpdatedAt),
	}
	if secret.UserID != nil {
		record.UserId = *secret.UserID
	}
	record.WorkspaceId = secret.WorkspaceID
	return record
}

func formatOptionalTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}
