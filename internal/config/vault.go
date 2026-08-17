package config

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/lehigh-university-libraries/scribe/internal/vaultkv"
)

type vaultSecretReader interface {
	Read(context.Context, string) (map[string]string, error)
}

// LoadSecrets eagerly fetches the secrets authorized for this runtime. An
// anonymous preview reads only its identity-scoped database bootstrap; ordinary
// deployments also read OAuth and optional provider credentials.
func LoadSecrets(ctx context.Context, cfg Config) (Secrets, error) {
	client, err := newVaultSecretReader(
		cfg.Vault.Address,
		cfg.Vault.Token,
		cfg.Vault.KVMount,
		cfg.Vault.GCPAuthRole,
	)
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
			if !vaultkv.IsNotFound(err) {
				return Secrets{}, fmt.Errorf("read openai secret: %w", err)
			}
			openai = map[string]string{}
		}

		gemini, err = client.Read(ctx, cfg.Vault.Paths.Gemini)
		if err != nil {
			if !vaultkv.IsNotFound(err) {
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

// LoadDatabasePassword fetches only the database bootstrap secret. It is for
// narrow trusted-host helpers that need database-backed stores but must not
// request the OAuth or provider credentials loaded by LoadSecrets.
func LoadDatabasePassword(ctx context.Context, cfg BrowserSessionVaultConfig) (string, error) {
	client, err := newVaultSecretReader(
		cfg.Address,
		strings.TrimSpace(os.Getenv("VAULT_TOKEN")),
		cfg.KVMount,
		cfg.GCPAuthRole,
	)
	if err != nil {
		return "", err
	}
	return readDatabasePassword(ctx, client, cfg.DatabasePath)
}

func newVaultSecretReader(address, token, kvMount, gcpAuthRole string) (vaultSecretReader, error) {
	if address == "" {
		return nil, fmt.Errorf("vault.address is required")
	}
	return vaultkv.New(address, token, kvMount, gcpAuthRole), nil
}

func readDatabasePassword(ctx context.Context, client vaultSecretReader, path string) (string, error) {
	database, err := client.Read(ctx, path)
	if err != nil {
		return "", fmt.Errorf("read database secret: %w", err)
	}
	return database["password"], nil
}
