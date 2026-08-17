package config

import (
	"strings"
	"testing"
)

func TestLoadBrowserSessionConfigDoesNotRequireEntrypointSecrets(t *testing.T) {
	t.Setenv("SCRIBE_PAGE_TOKEN_SIGNING_KEY", "")
	t.Setenv("SCRIBE_PAGE_TOKEN_SIGNING_KEY_FILE", "/missing/unrelated/page-token-key")
	t.Setenv("PUBLIC_BASE_URL", "https://scribe.example")
	t.Setenv("VAULT_ADDRESS", "https://vault.example")
	t.Setenv("VAULT_WORKSPACE", "prod")
	t.Setenv("VAULT_SECRET_PREFIX", "scribe/prod")

	cfg, err := LoadBrowserSessionConfig()
	if err != nil {
		t.Fatalf("LoadBrowserSessionConfig returned error without PID1 secrets: %v", err)
	}
	if cfg.PublicBaseURL != "https://scribe.example" ||
		cfg.CookieName != "scribe_session" ||
		cfg.Vault.DatabasePath != "scribe/prod/database/app" {
		t.Fatalf("browser-session config = %+v", cfg)
	}

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "pagination.signing_key") {
		t.Fatalf("full Load error = %v, want missing page-token signing key", err)
	}
}

func TestParseBrowserSessionConfigExpandsOnlySelectedFields(t *testing.T) {
	t.Setenv("BROWSER_CONFIG_PUBLIC_BASE", "https://scribe.example")
	t.Setenv("BROWSER_CONFIG_COOKIE_DOMAIN", ".scribe.example")
	t.Setenv("BROWSER_CONFIG_DATABASE_PORT", "3307")
	t.Setenv("BROWSER_CONFIG_VAULT_ADDRESS", "https://vault.example")
	t.Setenv("BROWSER_CONFIG_VAULT_PATH", "scribe/prod/database/app")

	raw := []byte(`
public_base_url: "${BROWSER_CONFIG_PUBLIC_BASE}"
auth:
  cookie_name: scribe_browser
  cookie_domain: "${BROWSER_CONFIG_COOKIE_DOMAIN}"
database:
  dsn_template: "{{.User}}:{{.Password}}@tcp({{.Host}}:{{.Port}})/{{.Name}}"
  host: database.internal
  port: "${BROWSER_CONFIG_DATABASE_PORT}"
  name: scribe
  user: scribe_app
pagination:
  signing_key:
    unrelated: "${UNRELATED_FILE_SECRET}"
vault:
  address: "${BROWSER_CONFIG_VAULT_ADDRESS}"
  gcp_auth_role: scribe-app-prod
  workspace: prod
  kv_mount: secret
  paths:
    database: "${BROWSER_CONFIG_VAULT_PATH}"
    google_oauth:
      unrelated: true
`)
	cfg, err := parseBrowserSessionConfig(raw)
	if err != nil {
		t.Fatalf("parseBrowserSessionConfig returned error: %v", err)
	}
	if cfg.PublicBaseURL != "https://scribe.example" ||
		cfg.CookieName != "scribe_browser" ||
		cfg.CookieDomain != ".scribe.example" ||
		cfg.Database.Host != "database.internal" ||
		cfg.Database.Port != 3307 ||
		cfg.Database.Name != "scribe" ||
		cfg.Database.User != "scribe_app" ||
		cfg.Vault.Address != "https://vault.example" ||
		cfg.Vault.GCPAuthRole != "scribe-app-prod" ||
		cfg.Vault.DatabasePath != "scribe/prod/database/app" {
		t.Fatalf("parsed browser-session config = %+v", cfg)
	}
}

func TestParseBrowserSessionConfigValidatesItsExactBoundary(t *testing.T) {
	valid := `
public_base_url: https://scribe.example
auth:
  cookie_name: scribe_session
database:
  host: mariadb
  port: 3306
  name: scribe
  user: scribe
vault:
  address: https://vault.example
  workspace: prod
  paths:
    database: scribe/prod/database/app
`
	for _, test := range []struct {
		name    string
		raw     string
		wantErr string
	}{
		{name: "valid", raw: valid},
		{
			name:    "invalid origin",
			raw:     strings.Replace(valid, "https://scribe.example", "javascript:alert(1)", 1),
			wantErr: "public_base_url",
		},
		{
			name:    "cross-workspace database path",
			raw:     strings.Replace(valid, "scribe/prod/database/app", "scribe/dev/database/app", 1),
			wantErr: "does not match VAULT_WORKSPACE",
		},
		{
			name:    "noninteger database port",
			raw:     strings.Replace(valid, "port: 3306", "port: mysql", 1),
			wantErr: "database.port",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseBrowserSessionConfig([]byte(test.raw))
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("parseBrowserSessionConfig returned error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("parseBrowserSessionConfig error = %v, want %q", err, test.wantErr)
			}
		})
	}
}
