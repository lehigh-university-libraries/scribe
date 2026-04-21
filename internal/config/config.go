package config

import (
	_ "embed"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ConfigPath is the hardcoded location of the runtime config file inside the
// deployed container. The embedded defaults are used when the file is missing
// (e.g. `go test`, local development outside docker).
const ConfigPath = "/etc/scribe/config.yaml"

//go:embed defaults/config.yaml
var embeddedDefaults []byte

// Config mirrors the YAML file shape.
type Config struct {
	ListenAddr    string `yaml:"listen_addr"`
	PublicBaseURL string `yaml:"public_base_url"`

	Auth          AuthConfig          `yaml:"auth"`
	Database      DatabaseConfig      `yaml:"database"`
	LLM           LLMConfig           `yaml:"llm"`
	Transcription TranscriptionConfig `yaml:"transcription"`
	Cantaloupe    CantaloupeConfig    `yaml:"cantaloupe"`
	Annotation    AnnotationConfig    `yaml:"annotation"`
	Drupal        DrupalConfig        `yaml:"drupal"`
	Webhooks      WebhooksConfig      `yaml:"webhooks"`
	Vault         VaultConfig         `yaml:"vault"`

	// DatabaseDSN is resolved at load time from Vault + Database config.
	DatabaseDSN string `yaml:"-"`
}

type AuthConfig struct {
	CookieName         string        `yaml:"cookie_name"`
	CookieDomain       string        `yaml:"cookie_domain"`
	CookieSecure       string        `yaml:"cookie_secure"` // "auto" | "true" | "false"
	SessionTTL         time.Duration `yaml:"session_ttl"`
	GoogleCallbackPath string        `yaml:"google_callback_path"`
	AllowedDomains     []string      `yaml:"allowed_domains"`
	DeniedDomains      []string      `yaml:"denied_domains"`
	AdminEmails        []string      `yaml:"admin_emails"`
}

type DatabaseConfig struct {
	DSNTemplate string `yaml:"dsn_template"`
	Host        string `yaml:"host"`
	Port        int    `yaml:"port"`
	Name        string `yaml:"name"`
	User        string `yaml:"user"`
}

type LLMConfig struct {
	Provider                  string       `yaml:"provider"`
	DefaultSystemPrompt       string       `yaml:"default_system_prompt"`
	SegmentationModel         string       `yaml:"segmentation_model"`
	BatchSize                 int          `yaml:"batch_size"`
	LineTranscribeConcurrency int          `yaml:"line_transcribe_concurrency"`
	Ollama                    OllamaConfig `yaml:"ollama"`
	OpenAI                    OpenAIConfig `yaml:"openai"`
	Gemini                    GeminiConfig `yaml:"gemini"`
}

type OllamaConfig struct {
	URL      string   `yaml:"url"`
	Audience string   `yaml:"audience"`
	Model    string   `yaml:"model"`
	Models   []string `yaml:"models"`
}

type OpenAIConfig struct {
	Model  string   `yaml:"model"`
	Models []string `yaml:"models"`
}

type GeminiConfig struct {
	Model       string   `yaml:"model"`
	Models      []string `yaml:"models"`
	URLTemplate string   `yaml:"url_template"`
}

type TranscriptionConfig struct {
	JobWorkers int `yaml:"job_workers"`
}

type CantaloupeConfig struct {
	IIIFBase         string `yaml:"iiif_base"`
	IIIFInternalBase string `yaml:"iiif_internal_base"`
}

type AnnotationConfig struct {
	APIBase         string `yaml:"api_base"`
	APIInternalBase string `yaml:"api_internal_base"`
}

type DrupalConfig struct {
	HOCRURLTemplate string `yaml:"hocr_url_template"`
}

type WebhooksConfig struct {
	URLs []string `yaml:"urls"`
}

type VaultConfig struct {
	Address     string     `yaml:"address"`
	GCPAuthRole string     `yaml:"gcp_auth_role"`
	KVMount     string     `yaml:"kv_mount"`
	Paths       VaultPaths `yaml:"paths"`
	Token       string     `yaml:"-"` // optional local-dev fallback from env
}

type VaultPaths struct {
	GoogleOAuth string `yaml:"google_oauth"`
	OpenAI      string `yaml:"openai"`
	Gemini      string `yaml:"gemini"`
	Database    string `yaml:"database"`
}

// Secrets holds values loaded from Vault at startup. The fields are populated
// eagerly so request-path code can read them without blocking network calls.
type Secrets struct {
	GoogleOAuthClientID     string
	GoogleOAuthClientSecret string
	OpenAIAPIKey            string
	GeminiAPIKey            string
	DatabasePassword        string
	DatabaseRootPassword    string
}

// Load reads the YAML config from ConfigPath (falling back to the embedded
// defaults), overlays any local-only secret fallbacks from env, and returns the
// parsed config. It does NOT contact Vault — callers pair this with secrets.Load.
func Load() (Config, error) {
	raw := embeddedDefaults
	if data, err := os.ReadFile(ConfigPath); err == nil {
		raw = data
	} else if !os.IsNotExist(err) {
		return Config{}, fmt.Errorf("read %s: %w", ConfigPath, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}

	cfg.Vault.Address = strings.TrimSpace(cfg.Vault.Address)
	cfg.Vault.GCPAuthRole = strings.TrimSpace(cfg.Vault.GCPAuthRole)
	cfg.Vault.Token = strings.TrimSpace(os.Getenv("VAULT_TOKEN"))

	cfg.PublicBaseURL = strings.TrimRight(strings.TrimSpace(cfg.PublicBaseURL), "/")
	if cfg.Auth.CookieName == "" {
		cfg.Auth.CookieName = "scribe_session"
	}
	if cfg.Auth.SessionTTL <= 0 {
		cfg.Auth.SessionTTL = 24 * time.Hour
	}
	if cfg.Auth.GoogleCallbackPath == "" {
		cfg.Auth.GoogleCallbackPath = "/auth/callback/google"
	}
	if cfg.Vault.KVMount == "" {
		cfg.Vault.KVMount = "secret"
	}
	if cfg.Vault.GCPAuthRole == "" {
		cfg.Vault.GCPAuthRole = "scribe-app"
	}

	return cfg, nil
}

// GoogleCallbackURL returns the absolute callback URL constructed from
// PublicBaseURL + Auth.GoogleCallbackPath.
func (c Config) GoogleCallbackURL() string {
	if c.PublicBaseURL == "" {
		return ""
	}
	u, err := url.Parse(c.PublicBaseURL)
	if err != nil {
		return ""
	}
	u.Path = strings.TrimRight(u.Path, "/") + c.Auth.GoogleCallbackPath
	return u.String()
}

// CookieSecureResolved collapses the "auto" sentinel into a concrete bool.
func (c Config) CookieSecureResolved() bool {
	switch strings.ToLower(strings.TrimSpace(c.Auth.CookieSecure)) {
	case "true", "1", "yes":
		return true
	case "false", "0", "no":
		return false
	default:
		return strings.HasPrefix(c.PublicBaseURL, "https://")
	}
}

// BuildDSN renders the configured DSN template using the supplied password.
func (d DatabaseConfig) BuildDSN(password string) string {
	tpl := d.DSNTemplate
	if tpl == "" {
		tpl = "{{.User}}:{{.Password}}@tcp({{.Host}}:{{.Port}})/{{.Name}}?parseTime=true"
	}
	replacer := strings.NewReplacer(
		"{{.User}}", d.User,
		"{{.Password}}", password,
		"{{.Host}}", d.Host,
		"{{.Port}}", fmt.Sprintf("%d", d.Port),
		"{{.Name}}", d.Name,
	)
	return replacer.Replace(tpl)
}
