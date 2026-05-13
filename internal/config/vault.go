package config

import (
	"context"
	"fmt"

	"github.com/lehigh-university-libraries/scribe/internal/vaultkv"
)

// LoadSecrets eagerly fetches all application secrets from Vault. It blocks
// until every required secret path has been read; failures abort startup.
func LoadSecrets(ctx context.Context, cfg Config) (Secrets, error) {
	if cfg.Vault.Address == "" {
		return Secrets{}, fmt.Errorf("vault.address is required")
	}
	client := vaultkv.New(cfg.Vault.Address, cfg.Vault.Token, cfg.Vault.KVMount, cfg.Vault.GCPAuthRole)

	google, err := client.Read(ctx, cfg.Vault.Paths.GoogleOAuth)
	if err != nil {
		return Secrets{}, fmt.Errorf("read google_oauth secret: %w", err)
	}
	openai, err := client.Read(ctx, cfg.Vault.Paths.OpenAI)
	if err != nil {
		if !vaultkv.IsNotFound(err) {
			return Secrets{}, fmt.Errorf("read openai secret: %w", err)
		}
		openai = map[string]string{}
	}
	gemini, err := client.Read(ctx, cfg.Vault.Paths.Gemini)
	if err != nil {
		if !vaultkv.IsNotFound(err) {
			return Secrets{}, fmt.Errorf("read gemini secret: %w", err)
		}
		gemini = map[string]string{}
	}
	db, err := client.Read(ctx, cfg.Vault.Paths.Database)
	if err != nil {
		return Secrets{}, fmt.Errorf("read database secret: %w", err)
	}

	return Secrets{
		GoogleOAuthClientID:     google["client_id"],
		GoogleOAuthClientSecret: google["client_secret"],
		OpenAIAPIKey:            openai["api_key"],
		GeminiAPIKey:            gemini["api_key"],
		DatabasePassword:        db["password"],
	}, nil
}
