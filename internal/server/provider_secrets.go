package server

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"

	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/providerregistry"
	"github.com/lehigh-university-libraries/scribe/internal/providersecret"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

type providerSecretResolver interface {
	ResolvePreferred(context.Context, uint64, *uint64, string) (store.ProviderSecret, error)
}

type providerSecretVault interface {
	Read(context.Context, string) (map[string]string, error)
}

func (h *Handler) contextWithProviderSecret(ctx context.Context, workspaceID uint64, userID *uint64, provider string) context.Context {
	if h == nil || h.providerSecrets == nil || h.vault == nil {
		return ctx
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	descriptor, ok := providerregistry.New(config.Get().Config).Provider(provider)
	if !ok || !descriptor.Credentials.Required() {
		return ctx
	}
	if providerregistry.ContextCredential(ctx, provider, "api_key") != "" {
		return ctx
	}
	secret, err := h.providerSecrets.ResolvePreferred(ctx, workspaceID, userID, provider)
	if err != nil {
		if err != sql.ErrNoRows {
			// Vault and database errors can contain secret paths and backend
			// topology. Keep operational context while logging only the type.
			slog.Warn("resolve provider secret failed", "workspace_id", workspaceID, "provider", provider, "error_type", fmt.Sprintf("%T", err))
		}
		return ctx
	}
	if err := providersecret.ValidateVaultPath(config.Get().Config.Vault.Paths.ProviderSecrets, workspaceID, secret.VaultPath); err != nil {
		slog.Warn("provider secret path rejected", "workspace_id", workspaceID, "provider_secret_id", secret.ID, "provider", provider)
		return ctx
	}
	payload, err := h.vault.Read(ctx, secret.VaultPath)
	if err != nil {
		slog.Warn("read provider secret failed", "provider_secret_id", secret.ID, "provider", secret.Provider, "error_type", fmt.Sprintf("%T", err))
		return ctx
	}
	// Provider credentials are opaque. Whitespace is used only to detect a
	// missing value; the exact Vault value must reach the provider unchanged.
	apiKey := payload["api_key"]
	if strings.TrimSpace(apiKey) == "" {
		apiKey = payload["key"]
	}
	if strings.TrimSpace(apiKey) == "" {
		return ctx
	}
	return providerregistry.WithCredential(ctx, provider, "api_key", apiKey)
}

// contextWithWorkspaceProviderSecret is the credential boundary for durable
// background work. A queued job is workspace-owned, not user-owned, and must
// never infer a private credential from the user who created the item.
func (h *Handler) contextWithWorkspaceProviderSecret(ctx context.Context, workspaceID uint64, provider string) context.Context {
	ctx = providerregistry.WithoutCredentials(ctx)
	ctx = providerregistry.WithoutAdministratorCredentialFallback(ctx)
	return h.contextWithProviderSecret(ctx, workspaceID, nil, provider)
}
