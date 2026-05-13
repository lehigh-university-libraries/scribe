package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
	"github.com/lehigh-university-libraries/scribe/proto/scribe/v1/scribev1connect"
)

var _ scribev1connect.AuthServiceHandler = (*Manager)(nil)

func (m *Manager) GetAuthMe(ctx context.Context, _ *connect.Request[scribev1.AuthMeRequest]) (*connect.Response[scribev1.AuthMeResponse], error) {
	principal, ok := PrincipalFromContext(ctx)
	if !ok {
		principal = m.anonymousPrincipal()
	}
	resp := &scribev1.AuthMeResponse{
		Authenticated: principal.Authenticated,
		AuthType:      principal.AuthType,
		LoginUrl:      "/auth/google",
		LogoutUrl:     "/logout",
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
	return connect.NewResponse(resp), nil
}

func (m *Manager) ListAPIKeys(ctx context.Context, _ *connect.Request[scribev1.ListAPIKeysRequest]) (*connect.Response[scribev1.ListAPIKeysResponse], error) {
	principal, err := m.sessionPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	keys, err := m.apiKeys.ListByWorkspace(ctx, principal.WorkspaceID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list api keys: %w", err))
	}
	resp := &scribev1.ListAPIKeysResponse{ApiKeys: make([]*scribev1.APIKeyRecord, 0, len(keys))}
	for _, key := range keys {
		resp.ApiKeys = append(resp.ApiKeys, apiKeyToProto(key))
	}
	return connect.NewResponse(resp), nil
}

func (m *Manager) CreateAPIKey(ctx context.Context, req *connect.Request[scribev1.CreateAPIKeyRequest]) (*connect.Response[scribev1.CreateAPIKeyResponse], error) {
	principal, err := m.sessionPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	var expiresAt *time.Time
	if raw := strings.TrimSpace(req.Msg.GetExpiresAt()); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid expires_at"))
		}
		expiresAt = &parsed
	}
	apiKey, rawKey, err := m.apiKeys.Create(ctx, principal.WorkspaceID, principal.UserID, req.Msg.GetName(), req.Msg.GetRole(), req.Msg.GetScopes(), expiresAt)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("create api key: %w", err))
	}
	return connect.NewResponse(&scribev1.CreateAPIKeyResponse{
		ApiKey: apiKeyToProto(apiKey),
		Key:    rawKey,
	}), nil
}

func (m *Manager) DeleteAPIKey(ctx context.Context, req *connect.Request[scribev1.DeleteAPIKeyRequest]) (*connect.Response[scribev1.DeleteAPIKeyResponse], error) {
	principal, err := m.sessionPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := m.apiKeys.DeleteForWorkspace(ctx, req.Msg.GetKeyId(), principal.WorkspaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("api key not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("delete api key: %w", err))
	}
	return connect.NewResponse(&scribev1.DeleteAPIKeyResponse{}), nil
}

func (m *Manager) ListProviderSecrets(ctx context.Context, _ *connect.Request[scribev1.ListProviderSecretsRequest]) (*connect.Response[scribev1.ListProviderSecretsResponse], error) {
	principal, err := m.sessionPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if m.providerSecrets == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("provider secret storage is not configured"))
	}
	secrets, err := m.providerSecrets.ListVisible(ctx, principal.WorkspaceID, principal.UserID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list provider secrets: %w", err))
	}
	resp := &scribev1.ListProviderSecretsResponse{ProviderSecrets: make([]*scribev1.ProviderSecretRecord, 0, len(secrets))}
	for _, secret := range secrets {
		resp.ProviderSecrets = append(resp.ProviderSecrets, providerSecretToProto(secret))
	}
	return connect.NewResponse(resp), nil
}

func (m *Manager) CreateProviderSecret(ctx context.Context, req *connect.Request[scribev1.CreateProviderSecretRequest]) (*connect.Response[scribev1.CreateProviderSecretResponse], error) {
	principal, err := m.sessionPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if m.providerSecrets == nil || m.vault == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("provider secret storage is not configured"))
	}

	provider := strings.ToLower(strings.TrimSpace(req.Msg.GetProvider()))
	if provider != "gemini" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unsupported provider secret provider"))
	}
	name := strings.TrimSpace(req.Msg.GetName())
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("name is required"))
	}
	apiKey := strings.TrimSpace(req.Msg.GetApiKey())
	if apiKey == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("api_key is required"))
	}

	scope := strings.ToLower(strings.TrimSpace(req.Msg.GetScope()))
	if scope == "" {
		scope = "user"
	}
	if scope != "user" && scope != "workspace" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("scope must be 'user' or 'workspace'"))
	}
	if scope == "workspace" && !strings.EqualFold(principal.WorkspaceRole, "admin") && !principal.IsAdmin {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("workspace secrets require admin role"))
	}

	pathSuffix, err := randomString(12)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("generate secret path: %w", err))
	}
	vaultPath := fmt.Sprintf(
		"%s/%d/%s/%s-%s",
		m.providerSecretVaultPrefix(),
		principal.WorkspaceID,
		provider,
		store.Slugify(name),
		strings.ToLower(pathSuffix),
	)
	if err := m.vault.Write(ctx, vaultPath, map[string]string{
		"api_key":  apiKey,
		"provider": provider,
		"name":     name,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("write provider secret: %w", err))
	}

	var userID *uint64
	workspaceID := principal.WorkspaceID
	if scope == "user" {
		userID = &principal.UserID
	}
	secret, err := m.providerSecrets.Create(ctx, store.ProviderSecret{
		UserID:      userID,
		WorkspaceID: &workspaceID,
		Provider:    provider,
		Name:        name,
		VaultPath:   vaultPath,
		KeyHint:     secretKeyHint(apiKey),
		Scope:       scope,
	})
	if err != nil {
		_ = m.vault.Delete(ctx, vaultPath)
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("create provider secret: %w", err))
	}
	return connect.NewResponse(&scribev1.CreateProviderSecretResponse{
		ProviderSecret: providerSecretToProto(secret),
	}), nil
}

func (m *Manager) DeleteProviderSecret(ctx context.Context, req *connect.Request[scribev1.DeleteProviderSecretRequest]) (*connect.Response[scribev1.DeleteProviderSecretResponse], error) {
	principal, err := m.sessionPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if m.providerSecrets == nil || m.vault == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("provider secret storage is not configured"))
	}

	secret, err := m.providerSecrets.GetVisible(ctx, req.Msg.GetSecretId(), principal.WorkspaceID, principal.UserID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("provider secret not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("load provider secret: %w", err))
	}
	if secret.Scope == "workspace" && !strings.EqualFold(principal.WorkspaceRole, "admin") && !principal.IsAdmin {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("workspace secrets require admin role"))
	}

	if secret.Scope == "workspace" {
		err = m.providerSecrets.DeleteWorkspaceSecret(ctx, req.Msg.GetSecretId(), principal.WorkspaceID)
	} else {
		err = m.providerSecrets.DeleteUserSecret(ctx, req.Msg.GetSecretId(), principal.WorkspaceID, principal.UserID)
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("provider secret not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("delete provider secret: %w", err))
	}
	if err := m.validateProviderSecretVaultPath(secret.VaultPath, principal.WorkspaceID); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	_ = m.vault.Delete(ctx, secret.VaultPath)
	return connect.NewResponse(&scribev1.DeleteProviderSecretResponse{}), nil
}

func (m *Manager) providerSecretVaultPrefix() string {
	if m != nil && strings.TrimSpace(m.providerSecretsVaultPrefix) != "" {
		return strings.Trim(strings.TrimSpace(m.providerSecretsVaultPrefix), "/")
	}
	return "scribe/dev/provider-secrets/workspaces"
}

func (m *Manager) validateProviderSecretVaultPath(vaultPath string, workspaceID uint64) error {
	path := strings.Trim(strings.TrimSpace(vaultPath), "/")
	prefix := m.providerSecretVaultPrefix() + "/" + strconv.FormatUint(workspaceID, 10) + "/"
	if !strings.HasPrefix(path, prefix) {
		return fmt.Errorf("provider secret vault path is outside workspace scope")
	}
	if strings.Contains(path, "..") || strings.Contains(path, "//") {
		return fmt.Errorf("provider secret vault path is invalid")
	}
	return nil
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
	if !apiKey.LastUsedAt.IsZero() {
		record.LastUsedAt = formatOptionalTime(apiKey.LastUsedAt)
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
		VaultPath: secret.VaultPath,
		KeyHint:   secret.KeyHint,
		Scope:     secret.Scope,
		CreatedAt: formatOptionalTime(secret.CreatedAt),
		UpdatedAt: formatOptionalTime(secret.UpdatedAt),
	}
	if secret.UserID != nil {
		record.UserId = *secret.UserID
	}
	if secret.WorkspaceID != nil {
		record.WorkspaceId = *secret.WorkspaceID
	}
	return record
}

func formatOptionalTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}
