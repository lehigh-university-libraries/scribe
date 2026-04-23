package config

import (
	"os"
	"testing"
)

func TestExpandConfigEnvUsesDefaultsWhenUnset(t *testing.T) {
	t.Setenv("PUBLIC_BASE_URL", "")
	t.Setenv("VAULT_ADDRESS", "")

	raw := []byte(`public_base_url: "${PUBLIC_BASE_URL:-http://localhost:8080}"
vault:
  address: "${VAULT_ADDRESS:-}"
  gcp_auth_role: "${VAULT_GCP_AUTH_ROLE:-scribe-app}"
`)

	got := string(expandConfigEnv(raw))
	want := `public_base_url: "http://localhost:8080"
vault:
  address: ""
  gcp_auth_role: "scribe-app"
`

	if got != want {
		t.Fatalf("expandConfigEnv() = %q, want %q", got, want)
	}
}

func TestExpandConfigEnvUsesEnvironmentValues(t *testing.T) {
	t.Setenv("PUBLIC_BASE_URL", "https://scribe.example")
	t.Setenv("VAULT_ADDRESS", "https://vault.example")
	t.Setenv("VAULT_GCP_AUTH_ROLE", "scribe-app-prod")

	raw := []byte(`public_base_url: "${PUBLIC_BASE_URL:-http://localhost:8080}"
vault:
  address: "${VAULT_ADDRESS:-}"
  gcp_auth_role: "${VAULT_GCP_AUTH_ROLE:-scribe-app}"
`)

	got := string(expandConfigEnv(raw))
	want := `public_base_url: "https://scribe.example"
vault:
  address: "https://vault.example"
  gcp_auth_role: "scribe-app-prod"
`

	if got != want {
		t.Fatalf("expandConfigEnv() = %q, want %q", got, want)
	}
}

func TestExpandConfigEnvWithoutDefaultUsesEmptyString(t *testing.T) {
	name := "SCRIBE_CONFIG_TEST_UNSET"
	if err := os.Unsetenv(name); err != nil {
		t.Fatalf("Unsetenv(%q): %v", name, err)
	}

	got := string(expandConfigEnv([]byte(`value: "${SCRIBE_CONFIG_TEST_UNSET}"`)))
	if got != `value: ""` {
		t.Fatalf("expandConfigEnv() = %q, want %q", got, `value: ""`)
	}
}

func TestLoadModelEndpointMapEnv(t *testing.T) {
	t.Setenv("KRAKEN_MODEL_ENDPOINTS_JSON", `{
		"catmus-print-fondue-large.mlmodel": {
			"url": "https://kraken-catmus.example",
			"audience": "https://kraken-catmus.example"
		}
	}`)

	got, err := loadModelEndpointMapEnv("KRAKEN_MODEL_ENDPOINTS_JSON")
	if err != nil {
		t.Fatalf("loadModelEndpointMapEnv() error = %v", err)
	}
	if got == nil {
		t.Fatalf("loadModelEndpointMapEnv() returned nil")
	}
	endpoint, ok := got["catmus-print-fondue-large.mlmodel"]
	if !ok {
		t.Fatalf("expected catmus-print-fondue-large.mlmodel endpoint, got %v", got)
	}
	if endpoint.URL != "https://kraken-catmus.example" {
		t.Fatalf("endpoint.URL = %q", endpoint.URL)
	}
	if endpoint.Audience != "https://kraken-catmus.example" {
		t.Fatalf("endpoint.Audience = %q", endpoint.Audience)
	}
}

func TestLoadModelEndpointMapEnvRejectsInvalidJSON(t *testing.T) {
	t.Setenv("OLLAMA_MODEL_ENDPOINTS_JSON", `{not-json`)

	if _, err := loadModelEndpointMapEnv("OLLAMA_MODEL_ENDPOINTS_JSON"); err == nil {
		t.Fatal("expected error for invalid endpoint map json")
	}
}
