package config

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLoadSecretsAllowsMissingOptionalProviderSecrets(t *testing.T) {
	t.Setenv("VAULT_ADDRESS", "")
	t.Setenv("VAULT_TOKEN", "test-token")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/secret/data/scribe/dev/google_oauth":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"data": map[string]string{
						"client_id":     "client-id",
						"client_secret": "client-secret",
					},
				},
			})
		case "/v1/secret/data/scribe/dev/openai", "/v1/secret/data/scribe/dev/gemini":
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"errors": []string{}})
		case "/v1/secret/data/scribe/dev/database/app":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"data": map[string]string{
						"password": "db-password",
					},
				},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	cfg := Config{
		Vault: VaultConfig{
			Address:     srv.URL,
			KVMount:     "secret",
			GCPAuthRole: "scribe-app-prod",
			Token:       "test-token",
			Paths: VaultPaths{
				GoogleOAuth: "scribe/dev/google_oauth",
				OpenAI:      "scribe/dev/openai",
				Gemini:      "scribe/dev/gemini",
				Database:    "scribe/dev/database/app",
			},
		},
	}

	secrets, err := LoadSecrets(context.Background(), cfg)
	if err != nil {
		t.Fatalf("LoadSecrets returned error: %v", err)
	}
	if secrets.GoogleOAuthClientID != "client-id" || secrets.GoogleOAuthClientSecret != "client-secret" {
		t.Fatalf("unexpected google oauth secrets: %+v", secrets)
	}
	if secrets.OpenAIAPIKey != "" {
		t.Fatalf("OpenAIAPIKey = %q, want empty", secrets.OpenAIAPIKey)
	}
	if secrets.GeminiAPIKey != "" {
		t.Fatalf("GeminiAPIKey = %q, want empty", secrets.GeminiAPIKey)
	}
	if secrets.DatabasePassword != "db-password" {
		t.Fatalf("DatabasePassword = %q, want db-password", secrets.DatabasePassword)
	}
}

func TestLoadSecretsStillRequiresDatabaseSecret(t *testing.T) {
	t.Setenv("VAULT_ADDRESS", "")
	t.Setenv("VAULT_TOKEN", "test-token")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/secret/data/scribe/dev/google_oauth":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"data": map[string]string{
						"client_id":     "client-id",
						"client_secret": "client-secret",
					},
				},
			})
		case "/v1/secret/data/scribe/dev/openai", "/v1/secret/data/scribe/dev/gemini", "/v1/secret/data/scribe/dev/database/app":
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"errors": []string{}})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	cfg := Config{
		Vault: VaultConfig{
			Address:     srv.URL,
			KVMount:     "secret",
			GCPAuthRole: "scribe-app-prod",
			Token:       "test-token",
			Paths: VaultPaths{
				GoogleOAuth: "scribe/dev/google_oauth",
				OpenAI:      "scribe/dev/openai",
				Gemini:      "scribe/dev/gemini",
				Database:    "scribe/dev/database/app",
			},
		},
	}

	if _, err := LoadSecrets(context.Background(), cfg); err == nil {
		t.Fatal("expected error when required database secret is missing")
	}
}

func TestLoadSecretsPreviewModeRequiresOnlyItsDatabaseBootstrap(t *testing.T) {
	t.Setenv("VAULT_ADDRESS", "")
	t.Setenv("VAULT_TOKEN", "test-token")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/secret/data/scribe/previews/scribe-pr-75@example-project.iam.gserviceaccount.com/database/app":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"data": map[string]string{"password": "preview-db-password"}}})
		default:
			t.Fatalf("preview attempted unauthorized Vault path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	cfg := Config{
		Auth: AuthConfig{PreviewAnonymous: true},
		Vault: VaultConfig{
			Address: srv.URL, KVMount: "secret", GCPAuthRole: "scribe-app-pr-75", Token: "test-token",
			Paths: VaultPaths{
				GoogleOAuth: "scribe/previews/scribe-pr-75@example-project.iam.gserviceaccount.com/google_oauth",
				OpenAI:      "scribe/previews/scribe-pr-75@example-project.iam.gserviceaccount.com/openai",
				Gemini:      "scribe/previews/scribe-pr-75@example-project.iam.gserviceaccount.com/gemini",
				Database:    "scribe/previews/scribe-pr-75@example-project.iam.gserviceaccount.com/database/app",
			},
		},
	}
	secrets, err := LoadSecrets(context.Background(), cfg)
	if err != nil {
		t.Fatalf("LoadSecrets preview: %v", err)
	}
	if secrets.DatabasePassword != "preview-db-password" ||
		secrets.GoogleOAuthClientID != "" ||
		secrets.GoogleOAuthClientSecret != "" ||
		secrets.OpenAIAPIKey != "" ||
		secrets.GeminiAPIKey != "" {
		t.Fatalf("preview secrets = %+v", secrets)
	}
}
