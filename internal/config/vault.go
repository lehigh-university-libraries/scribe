package config

import (
	"context"
	"fmt"

	"github.com/lehigh-university-libraries/scribe/internal/secretmanager"
	"github.com/lehigh-university-libraries/scribe/internal/vaultkv"
)

type vaultSecretReader interface {
	Read(context.Context, string) (map[string]string, error)
}

// SecretClient is shared by bootstrap, credential resolution, and the durable
// cleanup ledger. Logical paths are storage locators, independent of backend.
type SecretClient interface {
	Read(context.Context, string) (map[string]string, error)
	Write(context.Context, string, map[string]string) error
	Delete(context.Context, string) error
}

func NewSecretClient(ctx context.Context, cfg Config) (SecretClient, error) {
	if cfg.SecretManagerProject != "" {
		return secretmanager.New(ctx, cfg.SecretManagerProject, cfg.SecretManagerPrefix)
	}
	if cfg.Vault.Address == "" {
		return nil, fmt.Errorf("secret backend is required")
	}
	return vaultkv.New(cfg.Vault.Address, cfg.Vault.Token, cfg.Vault.KVMount, cfg.Vault.GCPAuthRole), nil
}

// LoadSecrets eagerly fetches the secrets authorized for this runtime. An
// anonymous preview reads only its identity-scoped database bootstrap; ordinary
// deployments also read OAuth and optional provider credentials.
func LoadSecrets(ctx context.Context, cfg Config) (Secrets, error) {
	client, err := NewSecretClient(ctx, cfg)
	if err != nil {
		return Secrets{}, err
	}

	google := map[string]string{}
	openai := map[string]string{}
	gemini := map[string]string{}
	if !cfg.Auth.PreviewAnonymous {
		google, err = client.Read(ctx, cfg.Vault.Paths.GoogleOAuth)
		if err != nil {
			return Secrets{}, fmt.Errorf("read google_oauth secret: %w", err)
		}

		openai, err = client.Read(ctx, cfg.Vault.Paths.OpenAI)
		if err != nil {
			if !vaultkv.IsNotFound(err) && !secretmanager.IsNotFound(err) {
				return Secrets{}, fmt.Errorf("read openai secret: %w", err)
			}
			openai = map[string]string{}
		}

		gemini, err = client.Read(ctx, cfg.Vault.Paths.Gemini)
		if err != nil {
			if !vaultkv.IsNotFound(err) && !secretmanager.IsNotFound(err) {
				return Secrets{}, fmt.Errorf("read gemini secret: %w", err)
			}
			gemini = map[string]string{}
		}
	}
	databasePassword, err := readDatabasePassword(ctx, client, cfg.Vault.Paths.Database)
	if err != nil {
		return Secrets{}, err
	}

	return Secrets{
		GoogleOAuthClientID:     google["client_id"],
		GoogleOAuthClientSecret: google["client_secret"],
		OpenAIAPIKey:            openai["api_key"],
		GeminiAPIKey:            gemini["api_key"],
		DatabasePassword:        databasePassword,
	}, nil
}

func readDatabasePassword(ctx context.Context, client vaultSecretReader, path string) (string, error) {
	database, err := client.Read(ctx, path)
	if err != nil {
		return "", fmt.Errorf("read database secret: %w", err)
	}
	return database["password"], nil
}
