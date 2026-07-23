package server

import (
	"context"
	"database/sql"
	"testing"

	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/providerregistry"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

type fixedProviderSecretResolver struct {
	secret store.ProviderSecret
	err    error
}

func (r fixedProviderSecretResolver) ResolvePreferred(context.Context, uint64, *uint64, string) (store.ProviderSecret, error) {
	return r.secret, r.err
}

type recordingProviderSecretResolver struct {
	secret          store.ProviderSecret
	requestedUserID *uint64
	calls           int
}

func (r *recordingProviderSecretResolver) ResolvePreferred(_ context.Context, _ uint64, userID *uint64, _ string) (store.ProviderSecret, error) {
	r.calls++
	if userID != nil {
		value := *userID
		r.requestedUserID = &value
	}
	return r.secret, nil
}

type countingProviderVault struct {
	reads int
	value string
}

func (v *countingProviderVault) Read(context.Context, string) (map[string]string, error) {
	v.reads++
	value := v.value
	if value == "" {
		value = "never-expose-this"
	}
	return map[string]string{"api_key": value}, nil
}

func TestProviderCredentialPreservesOpaqueVaultValue(t *testing.T) {
	previous := config.Get()
	config.Init(config.Runtime{Config: config.Config{Vault: config.VaultConfig{Paths: config.VaultPaths{
		ProviderSecrets: "scribe/test/provider-secrets/workspaces",
	}}}})
	t.Cleanup(func() { config.Init(previous) })

	const opaqueCredential = " provider-key-with-significant-whitespace "
	handler := &Handler{
		providerSecrets: fixedProviderSecretResolver{secret: store.ProviderSecret{
			ID:          93,
			WorkspaceID: 7,
			Provider:    "openai",
			Scope:       "workspace",
			VaultPath:   "scribe/test/provider-secrets/workspaces/7/openai/key-1",
		}},
		vault: &countingProviderVault{value: opaqueCredential},
	}

	ctx := handler.contextWithProviderSecret(context.Background(), 7, nil, "openai")
	if got := providerregistry.ContextCredential(ctx, "openai", "api_key"); got != opaqueCredential {
		t.Fatalf("provider credential = %q, want exact opaque Vault value", got)
	}
}

func TestProviderCredentialRejectsCrossWorkspaceVaultPathBeforeRead(t *testing.T) {
	previous := config.Get()
	config.Init(config.Runtime{Config: config.Config{Vault: config.VaultConfig{Paths: config.VaultPaths{
		ProviderSecrets: "scribe/test/provider-secrets/workspaces",
	}}}})
	t.Cleanup(func() { config.Init(previous) })

	vault := &countingProviderVault{}
	handler := &Handler{
		providerSecrets: fixedProviderSecretResolver{secret: store.ProviderSecret{
			ID:        91,
			Provider:  "openai",
			Scope:     "workspace",
			VaultPath: "scribe/test/provider-secrets/workspaces/8/openai/key-1",
		}},
		vault: vault,
	}
	ctx := handler.contextWithProviderSecret(context.Background(), 7, nil, "openai")
	if vault.reads != 0 {
		t.Fatalf("Vault reads = %d, want 0 for cross-workspace path", vault.reads)
	}
	if got := providerregistry.ContextCredential(ctx, "openai", "api_key"); got != "" {
		t.Fatal("cross-workspace provider credential reached the request context")
	}
}

func TestBackgroundProviderCredentialResolutionIsWorkspaceOnly(t *testing.T) {
	previous := config.Get()
	config.Init(config.Runtime{Config: config.Config{Vault: config.VaultConfig{Paths: config.VaultPaths{
		ProviderSecrets: "scribe/test/provider-secrets/workspaces",
	}}}})
	t.Cleanup(func() { config.Init(previous) })

	resolver := &recordingProviderSecretResolver{secret: store.ProviderSecret{
		ID:          92,
		WorkspaceID: 7,
		Provider:    "openai",
		Scope:       "workspace",
		VaultPath:   "scribe/test/provider-secrets/workspaces/7/openai/key-1",
	}}
	vault := &countingProviderVault{}
	handler := &Handler{providerSecrets: resolver, vault: vault}

	ambient := providerregistry.WithCredential(context.Background(), "openai", "api_key", "item-creator-key")
	ctx := handler.contextWithWorkspaceProviderSecret(ambient, 7, "openai")
	if resolver.calls != 1 {
		t.Fatalf("provider secret resolver calls = %d, want 1", resolver.calls)
	}
	if resolver.requestedUserID != nil {
		t.Fatalf("background provider secret lookup used user id %d", *resolver.requestedUserID)
	}
	if vault.reads != 1 {
		t.Fatalf("Vault reads = %d, want 1", vault.reads)
	}
	if got := providerregistry.ContextCredential(ctx, "openai", "api_key"); got != "never-expose-this" {
		t.Fatalf("background provider credential = %q, want workspace credential", got)
	}
}

func TestBackgroundProviderCredentialNeverRetainsAmbientUserSecret(t *testing.T) {
	previous := config.Get()
	config.Init(config.Runtime{
		Config: config.Config{Vault: config.VaultConfig{Paths: config.VaultPaths{
			ProviderSecrets: "scribe/test/provider-secrets/workspaces",
		}}},
		Secrets: config.Secrets{OpenAIAPIKey: "administrator-key"},
	})
	t.Cleanup(func() { config.Init(previous) })

	handler := &Handler{
		providerSecrets: fixedProviderSecretResolver{err: sql.ErrNoRows},
		vault:           &countingProviderVault{},
	}
	ambient := providerregistry.WithCredential(context.Background(), "openai", "api_key", "item-creator-key")
	ctx := handler.contextWithWorkspaceProviderSecret(ambient, 7, "openai")
	if got := providerregistry.ContextCredential(ctx, "openai", "api_key"); got != "" {
		t.Fatalf("background context retained ambient user credential %q", got)
	}
	descriptor, ok := providerregistry.New(config.Get().Config).Provider("openai")
	if !ok {
		t.Fatal("OpenAI descriptor is not installed")
	}
	if got := descriptor.Credential(ctx, "api_key"); got != "" {
		t.Fatalf("background context fell back to administrator credential %q", got)
	}
}
