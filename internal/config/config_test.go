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
