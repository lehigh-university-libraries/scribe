package server

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"

	"github.com/lehigh-university-libraries/scribe/internal/hocr"
)

func (h *Handler) contextWithProviderSecret(ctx context.Context, workspaceID uint64, userID *uint64, provider string) context.Context {
	if h == nil || h.providerSecrets == nil || h.vault == nil {
		return ctx
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" || provider == "ollama" || provider == "kraken" || provider == "tesseract" {
		return ctx
	}
	secret, err := h.providerSecrets.ResolvePreferred(ctx, workspaceID, userID, provider)
	if err != nil {
		if err != sql.ErrNoRows {
			slog.Warn("resolve provider secret failed", "workspace_id", workspaceID, "provider", provider, "error", err)
		}
		return ctx
	}
	payload, err := h.vault.Read(ctx, secret.VaultPath)
	if err != nil {
		slog.Warn("read provider secret failed", "provider_secret_id", secret.ID, "provider", secret.Provider, "error", err)
		return ctx
	}
	apiKey := strings.TrimSpace(payload["api_key"])
	if apiKey == "" {
		apiKey = strings.TrimSpace(payload["key"])
	}
	if apiKey == "" {
		return ctx
	}
	return hocr.WithProviderAPIKey(ctx, provider, apiKey)
}
