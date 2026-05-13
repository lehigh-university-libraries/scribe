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
		case "/v1/secret/data/scribe/google_oauth":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"data": map[string]string{
						"client_id":     "client-id",
						"client_secret": "client-secret",
					},
				},
			})
		case "/v1/secret/data/scribe/openai", "/v1/secret/data/scribe/gemini":
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"errors": []string{}})
		case "/v1/secret/data/scribe/database/app":
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
				GoogleOAuth: "scribe/google_oauth",
				OpenAI:      "scribe/openai",
				Gemini:      "scribe/gemini",
				Database:    "scribe/database/app",
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
		case "/v1/secret/data/scribe/google_oauth":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"data": map[string]string{
						"client_id":     "client-id",
						"client_secret": "client-secret",
					},
				},
			})
		case "/v1/secret/data/scribe/openai", "/v1/secret/data/scribe/gemini", "/v1/secret/data/scribe/database/app":
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
				GoogleOAuth: "scribe/google_oauth",
				OpenAI:      "scribe/openai",
				Gemini:      "scribe/gemini",
				Database:    "scribe/database/app",
			},
		},
	}

	if _, err := LoadSecrets(context.Background(), cfg); err == nil {
		t.Fatal("expected error when required database secret is missing")
	}
}
