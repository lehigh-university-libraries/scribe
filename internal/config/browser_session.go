package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// BrowserSessionConfig is the complete non-secret configuration required by
// the trusted browser-session mint helper.
type BrowserSessionConfig struct {
	PublicBaseURL string
	CookieName    string
	CookieDomain  string
	Database      DatabaseConfig
	Vault         BrowserSessionVaultConfig
}

// BrowserSessionVaultConfig contains only the Vault connection and exact
// database-secret path required by the browser-session helper.
type BrowserSessionVaultConfig struct {
	Address      string
	GCPAuthRole  string
	KVMount      string
	Workspace    string
	DatabasePath string
}

type browserSessionFileConfig struct {
	PublicBaseURL yaml.Node `yaml:"public_base_url"`
	Auth          struct {
		CookieName   yaml.Node `yaml:"cookie_name"`
		CookieDomain yaml.Node `yaml:"cookie_domain"`
	} `yaml:"auth"`
	Database struct {
		DSNTemplate yaml.Node `yaml:"dsn_template"`
		Host        yaml.Node `yaml:"host"`
		Port        yaml.Node `yaml:"port"`
		Name        yaml.Node `yaml:"name"`
		User        yaml.Node `yaml:"user"`
	} `yaml:"database"`
	Vault struct {
		Address     yaml.Node `yaml:"address"`
		GCPAuthRole yaml.Node `yaml:"gcp_auth_role"`
		KVMount     yaml.Node `yaml:"kv_mount"`
		Workspace   yaml.Node `yaml:"workspace"`
		Paths       struct {
			Database yaml.Node `yaml:"database"`
		} `yaml:"paths"`
	} `yaml:"vault"`
}

// LoadBrowserSessionConfig reads and validates only the configuration used by
// the trusted browser-session mint helper. Unrelated runtime configuration and
// secret-file environment variables are deliberately outside this boundary.
func LoadBrowserSessionConfig() (BrowserSessionConfig, error) {
	raw := embeddedDefaults
	if data, err := os.ReadFile(ConfigPath); err == nil {
		raw = data
	} else if !os.IsNotExist(err) {
		return BrowserSessionConfig{}, fmt.Errorf("read %s: %w", ConfigPath, err)
	}
	return parseBrowserSessionConfig(raw)
}

func parseBrowserSessionConfig(raw []byte) (BrowserSessionConfig, error) {
	var file browserSessionFileConfig
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return BrowserSessionConfig{}, fmt.Errorf("parse config: %w", err)
	}

	publicBaseURL, err := browserSessionConfigScalar("public_base_url", file.PublicBaseURL)
	if err != nil {
		return BrowserSessionConfig{}, err
	}
	cookieName, err := browserSessionConfigScalar("auth.cookie_name", file.Auth.CookieName)
	if err != nil {
		return BrowserSessionConfig{}, err
	}
	cookieDomain, err := browserSessionConfigScalar("auth.cookie_domain", file.Auth.CookieDomain)
	if err != nil {
		return BrowserSessionConfig{}, err
	}
	dsnTemplate, err := browserSessionConfigScalar("database.dsn_template", file.Database.DSNTemplate)
	if err != nil {
		return BrowserSessionConfig{}, err
	}
	databaseHost, err := browserSessionConfigScalar("database.host", file.Database.Host)
	if err != nil {
		return BrowserSessionConfig{}, err
	}
	databasePort, err := browserSessionConfigScalar("database.port", file.Database.Port)
	if err != nil {
		return BrowserSessionConfig{}, err
	}
	databaseName, err := browserSessionConfigScalar("database.name", file.Database.Name)
	if err != nil {
		return BrowserSessionConfig{}, err
	}
	databaseUser, err := browserSessionConfigScalar("database.user", file.Database.User)
	if err != nil {
		return BrowserSessionConfig{}, err
	}
	vaultAddress, err := browserSessionConfigScalar("vault.address", file.Vault.Address)
	if err != nil {
		return BrowserSessionConfig{}, err
	}
	vaultGCPAuthRole, err := browserSessionConfigScalar("vault.gcp_auth_role", file.Vault.GCPAuthRole)
	if err != nil {
		return BrowserSessionConfig{}, err
	}
	vaultKVMount, err := browserSessionConfigScalar("vault.kv_mount", file.Vault.KVMount)
	if err != nil {
		return BrowserSessionConfig{}, err
	}
	vaultWorkspace, err := browserSessionConfigScalar("vault.workspace", file.Vault.Workspace)
	if err != nil {
		return BrowserSessionConfig{}, err
	}
	vaultDatabasePath, err := browserSessionConfigScalar("vault.paths.database", file.Vault.Paths.Database)
	if err != nil {
		return BrowserSessionConfig{}, err
	}

	port := 0
	if databasePort != "" {
		parsedPort, parseErr := strconv.ParseInt(strings.TrimSpace(databasePort), 10, 32)
		if parseErr != nil {
			return BrowserSessionConfig{}, fmt.Errorf("database.port must be an integer")
		}
		port = int(parsedPort)
	}

	cfg := BrowserSessionConfig{
		PublicBaseURL: publicBaseURL,
		CookieName:    cookieName,
		CookieDomain:  cookieDomain,
		Database: DatabaseConfig{
			DSNTemplate: dsnTemplate,
			Host:        databaseHost,
			Port:        port,
			Name:        databaseName,
			User:        databaseUser,
		},
		Vault: BrowserSessionVaultConfig{
			Address:      vaultAddress,
			GCPAuthRole:  vaultGCPAuthRole,
			KVMount:      vaultKVMount,
			Workspace:    vaultWorkspace,
			DatabasePath: vaultDatabasePath,
		},
	}
	return normalizeBrowserSessionConfig(cfg)
}

func browserSessionConfigScalar(name string, node yaml.Node) (string, error) {
	if node.Kind == 0 {
		return "", nil
	}
	if node.Kind != yaml.ScalarNode {
		return "", fmt.Errorf("%s must be a scalar", name)
	}
	return string(expandConfigEnv([]byte(node.Value))), nil
}

func normalizeBrowserSessionConfig(cfg BrowserSessionConfig) (BrowserSessionConfig, error) {
	var err error
	cfg.PublicBaseURL, _, err = normalizePublicBaseURL(cfg.PublicBaseURL)
	if err != nil {
		return BrowserSessionConfig{}, err
	}
	if cfg.CookieName == "" {
		cfg.CookieName = "scribe_session"
	}

	cfg.Vault.Address = strings.TrimSpace(cfg.Vault.Address)
	if cfg.Vault.Address == "" {
		cfg.Vault.Address = strings.TrimSpace(os.Getenv("VAULT_ADDRESS"))
	}
	if cfg.Vault.Address == "" {
		cfg.Vault.Address = strings.TrimSpace(os.Getenv("VAULT_ADDR"))
	}
	if cfg.Vault.Address == "" {
		return BrowserSessionConfig{}, fmt.Errorf("vault.address is required")
	}
	cfg.Vault.GCPAuthRole = strings.TrimSpace(cfg.Vault.GCPAuthRole)
	if cfg.Vault.GCPAuthRole == "" {
		cfg.Vault.GCPAuthRole = strings.TrimSpace(os.Getenv("VAULT_GCP_AUTH_ROLE"))
	}
	if cfg.Vault.GCPAuthRole == "" {
		cfg.Vault.GCPAuthRole = "scribe-app"
	}
	if cfg.Vault.KVMount == "" {
		cfg.Vault.KVMount = "secret"
	}
	cfg.Vault.Workspace = strings.TrimSpace(cfg.Vault.Workspace)
	if cfg.Vault.Workspace == "" {
		cfg.Vault.Workspace = strings.TrimSpace(os.Getenv("VAULT_WORKSPACE"))
	}

	databasePath := strings.Trim(strings.TrimSpace(cfg.Vault.DatabasePath), "/")
	if databasePath == "" {
		return BrowserSessionConfig{}, fmt.Errorf("vault database path is required")
	}
	if cfg.Vault.Workspace != "" {
		expectedPath := "scribe/" + cfg.Vault.Workspace + "/database/app"
		if databasePath != expectedPath {
			return BrowserSessionConfig{}, fmt.Errorf(
				"vault path database=%q does not match VAULT_WORKSPACE %q",
				cfg.Vault.DatabasePath,
				cfg.Vault.Workspace,
			)
		}
	}
	return cfg, nil
}
