package config

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
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

var configEnvPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:-([^}]*))?\}`)

// Config mirrors the YAML file shape.
type Config struct {
	ListenAddr    string `yaml:"listen_addr"`
	PublicBaseURL string `yaml:"public_base_url"`

	Auth          AuthConfig            `yaml:"auth"`
	CORS          CORSConfig            `yaml:"cors"`
	Database      DatabaseConfig        `yaml:"database"`
	LLM           LLMConfig             `yaml:"llm"`
	Transcription TranscriptionConfig   `yaml:"transcription"`
	IIIF          IIIFConfig            `yaml:"iiif"`
	Segmentation  ServiceEndpointConfig `yaml:"segmentation_service"`
	ImageService  ServiceEndpointConfig `yaml:"image_service"`
	Annotation    AnnotationConfig      `yaml:"annotation"`
	Drupal        DrupalConfig          `yaml:"drupal"`
	Webhooks      WebhooksConfig        `yaml:"webhooks"`
	Vault         VaultConfig           `yaml:"vault"`

	// DatabaseDSN is resolved at load time from Vault + Database config.
	DatabaseDSN string `yaml:"-"`
}

type AuthConfig struct {
	CookieName         string                    `yaml:"cookie_name"`
	CookieDomain       string                    `yaml:"cookie_domain"`
	SessionTTL         time.Duration             `yaml:"session_ttl"`
	GoogleCallbackPath string                    `yaml:"google_callback_path"`
	AllowedDomains     []string                  `yaml:"allowed_domains"`
	DeniedDomains      []string                  `yaml:"denied_domains"`
	AdminEmails        []string                  `yaml:"admin_emails"`
	ExternalJWTIssuers []ExternalJWTIssuerConfig `yaml:"external_jwt_issuers"`
}

type ExternalJWTIssuerConfig struct {
	Issuer        string                   `yaml:"issuer"`
	Audience      string                   `yaml:"audience"`
	JWKSURL       string                   `yaml:"jwks_url"`
	WorkspaceID   uint64                   `yaml:"workspace_id"`
	RequiredRoles []string                 `yaml:"required_roles"`
	Role          string                   `yaml:"role"`
	Scopes        []string                 `yaml:"scopes"`
	RoleMappings  []ExternalJWTRoleMapping `yaml:"role_mappings"`
}

type ExternalJWTRoleMapping struct {
	Roles  []string `yaml:"roles"`
	Role   string   `yaml:"role"`
	Scopes []string `yaml:"scopes"`
}

type CORSConfig struct {
	AllowedOrigins []string `yaml:"allowed_origins"`
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
	Kraken                    KrakenConfig `yaml:"kraken"`
	OpenAI                    OpenAIConfig `yaml:"openai"`
	Gemini                    GeminiConfig `yaml:"gemini"`
}

type OllamaConfig struct {
	URL            string                   `yaml:"url"`
	Audience       string                   `yaml:"audience"`
	Model          string                   `yaml:"model"`
	Models         []string                 `yaml:"models"`
	ModelEndpoints map[string]ModelEndpoint `yaml:"-"`
}

type ModelEndpoint struct {
	URL      string `json:"url"`
	Audience string `json:"audience"`
}

type OpenAIConfig struct {
	Model  string   `yaml:"model"`
	Models []string `yaml:"models"`
}

type KrakenConfig struct {
	URL            string                   `yaml:"url"`
	Audience       string                   `yaml:"audience"`
	Model          string                   `yaml:"model"`
	Models         []string                 `yaml:"models"`
	ModelEndpoints map[string]ModelEndpoint `yaml:"-"`
}

type GeminiConfig struct {
	Model       string   `yaml:"model"`
	Models      []string `yaml:"models"`
	URLTemplate string   `yaml:"url_template"`
}

type TranscriptionConfig struct {
	JobWorkers int                `yaml:"job_workers"`
	Queue      TranscriptionQueue `yaml:"queue"`
}

type TranscriptionQueue struct {
	Backend                string        `yaml:"backend"`
	ProjectID              string        `yaml:"project_id"`
	TopicID                string        `yaml:"topic_id"`
	SubscriptionID         string        `yaml:"subscription_id"`
	MaxOutstandingMessages int           `yaml:"max_outstanding_messages"`
	MaxExtension           time.Duration `yaml:"max_extension"`
	RecoveryPollInterval   time.Duration `yaml:"recovery_poll_interval"`
	RecoveryMinAge         time.Duration `yaml:"recovery_min_age"`
}

type IIIFConfig struct {
	Base         string `yaml:"base"`
	InternalBase string `yaml:"internal_base"`
}

type ServiceEndpointConfig struct {
	URL            string                   `yaml:"url"`
	Audience       string                   `yaml:"audience"`
	Models         []string                 `yaml:"models"`
	ModelEndpoints map[string]ModelEndpoint `yaml:"-"`
}

type AnnotationConfig struct {
	APIBase                         string `yaml:"api_base"`
	APIInternalBase                 string `yaml:"api_internal_base"`
	TripletPresentationBase         string `yaml:"triplet_presentation_base"`
	TripletPresentationInternalBase string `yaml:"triplet_presentation_internal_base"`
	TripletPresentationWriteToken   string `yaml:"triplet_presentation_write_token"`
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
	Workspace   string     `yaml:"workspace"`
	Paths       VaultPaths `yaml:"paths"`
	Token       string     `yaml:"-"` // optional local-dev fallback from env
}

type VaultPaths struct {
	GoogleOAuth     string `yaml:"google_oauth"`
	OpenAI          string `yaml:"openai"`
	Gemini          string `yaml:"gemini"`
	Database        string `yaml:"database"`
	ProviderSecrets string `yaml:"provider_secrets"`
}

// Secrets holds values loaded from Vault at startup. The fields are populated
// eagerly so request-path code can read them without blocking network calls.
type Secrets struct {
	GoogleOAuthClientID     string
	GoogleOAuthClientSecret string
	OpenAIAPIKey            string
	GeminiAPIKey            string
	DatabasePassword        string
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
	raw = expandConfigEnv(raw)

	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}

	cfg.Vault.Address = strings.TrimSpace(cfg.Vault.Address)
	if cfg.Vault.Address == "" {
		cfg.Vault.Address = strings.TrimSpace(os.Getenv("VAULT_ADDRESS"))
	}
	if cfg.Vault.Address == "" {
		cfg.Vault.Address = strings.TrimSpace(os.Getenv("VAULT_ADDR"))
	}
	cfg.Vault.GCPAuthRole = strings.TrimSpace(cfg.Vault.GCPAuthRole)
	if cfg.Vault.GCPAuthRole == "" {
		cfg.Vault.GCPAuthRole = strings.TrimSpace(os.Getenv("VAULT_GCP_AUTH_ROLE"))
	}
	cfg.Vault.Workspace = strings.TrimSpace(cfg.Vault.Workspace)
	if cfg.Vault.Workspace == "" {
		cfg.Vault.Workspace = strings.TrimSpace(os.Getenv("VAULT_WORKSPACE"))
	}
	cfg.Vault.Token = strings.TrimSpace(os.Getenv("VAULT_TOKEN"))
	var err error
	cfg.LLM.Ollama.URL = strings.TrimSpace(cfg.LLM.Ollama.URL)
	cfg.LLM.Ollama.Audience = strings.TrimSpace(cfg.LLM.Ollama.Audience)
	cfg.LLM.Ollama.ModelEndpoints, err = loadModelEndpointMapEnv("OLLAMA_MODEL_ENDPOINTS_JSON")
	if err != nil {
		return Config{}, err
	}
	if models, ok, err := loadStringListEnv("OLLAMA_MODELS_JSON"); err != nil {
		return Config{}, err
	} else if ok {
		cfg.LLM.Ollama.Models = models
	}
	cfg.Segmentation.URL = strings.TrimSpace(cfg.Segmentation.URL)
	cfg.Segmentation.Audience = strings.TrimSpace(cfg.Segmentation.Audience)
	cfg.Segmentation.ModelEndpoints, err = loadModelEndpointMapEnv("SEGMENTATION_MODEL_ENDPOINTS_JSON")
	if err != nil {
		return Config{}, err
	}
	if models, ok, err := loadStringListEnv("SEGMENTATION_MODELS_JSON"); err != nil {
		return Config{}, err
	} else if ok {
		cfg.Segmentation.Models = models
	}
	cfg.ImageService.URL = strings.TrimSpace(cfg.ImageService.URL)
	cfg.ImageService.Audience = strings.TrimSpace(cfg.ImageService.Audience)
	cfg.Annotation.TripletPresentationBase = strings.TrimRight(strings.TrimSpace(cfg.Annotation.TripletPresentationBase), "/")
	cfg.Annotation.TripletPresentationInternalBase = strings.TrimRight(strings.TrimSpace(cfg.Annotation.TripletPresentationInternalBase), "/")
	cfg.Annotation.TripletPresentationWriteToken = strings.TrimSpace(cfg.Annotation.TripletPresentationWriteToken)
	cfg.LLM.Kraken.URL = strings.TrimSpace(cfg.LLM.Kraken.URL)
	cfg.LLM.Kraken.Audience = strings.TrimSpace(cfg.LLM.Kraken.Audience)
	cfg.LLM.Kraken.Model = strings.TrimSpace(cfg.LLM.Kraken.Model)
	cfg.LLM.Kraken.ModelEndpoints, err = loadModelEndpointMapEnv("KRAKEN_MODEL_ENDPOINTS_JSON")
	if err != nil {
		return Config{}, err
	}
	if models, ok, err := loadStringListEnv("KRAKEN_MODELS_JSON"); err != nil {
		return Config{}, err
	} else if ok {
		cfg.LLM.Kraken.Models = models
	}
	if cfg.LLM.Kraken.URL == "" {
		cfg.LLM.Kraken.URL = cfg.Segmentation.URL
	}
	if cfg.LLM.Kraken.Audience == "" {
		cfg.LLM.Kraken.Audience = cfg.Segmentation.Audience
	}
	if cfg.LLM.Kraken.Model == "" {
		cfg.LLM.Kraken.Model = "catmus-print-fondue-large.mlmodel"
	}

	cfg.PublicBaseURL = strings.TrimRight(strings.TrimSpace(cfg.PublicBaseURL), "/")
	for _, origin := range cfg.CORS.AllowedOrigins {
		if strings.TrimSpace(origin) == "*" {
			return Config{}, fmt.Errorf("cors.allowed_origins must not contain wildcard '*' when credentials are enabled")
		}
	}
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
	if cfg.Vault.Workspace != "" {
		expectedPrefix := "scribe/" + strings.Trim(cfg.Vault.Workspace, "/") + "/"
		for name, path := range map[string]string{
			"google_oauth":     cfg.Vault.Paths.GoogleOAuth,
			"openai":           cfg.Vault.Paths.OpenAI,
			"gemini":           cfg.Vault.Paths.Gemini,
			"database":         cfg.Vault.Paths.Database,
			"provider_secrets": cfg.Vault.Paths.ProviderSecrets,
		} {
			normalized := strings.Trim(strings.TrimSpace(path), "/") + "/"
			if !strings.HasPrefix(normalized, expectedPrefix) {
				return Config{}, fmt.Errorf("vault path %s=%q does not match VAULT_WORKSPACE %q", name, path, cfg.Vault.Workspace)
			}
		}
	}

	return cfg, nil
}

func expandConfigEnv(raw []byte) []byte {
	expanded := configEnvPattern.ReplaceAllStringFunc(string(raw), func(expr string) string {
		match := configEnvPattern.FindStringSubmatch(expr)
		if len(match) < 2 {
			return expr
		}
		name := match[1]
		value, ok := os.LookupEnv(name)
		if ok && value != "" {
			return value
		}
		if len(match) >= 3 && match[2] != "" {
			return match[3]
		}
		if ok {
			return value
		}
		return ""
	})
	return []byte(expanded)
}

func loadModelEndpointMapEnv(name string) (map[string]ModelEndpoint, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return nil, nil
	}

	var parsed map[string]ModelEndpoint
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("parse %s: %w", name, err)
	}

	normalized := make(map[string]ModelEndpoint, len(parsed))
	for key, endpoint := range parsed {
		trimmedKey := strings.TrimSpace(key)
		if trimmedKey == "" {
			continue
		}
		endpoint.URL = strings.TrimSpace(endpoint.URL)
		endpoint.Audience = strings.TrimSpace(endpoint.Audience)
		if endpoint.URL == "" {
			continue
		}
		normalized[trimmedKey] = endpoint
	}
	if len(normalized) == 0 {
		return nil, nil
	}
	return normalized, nil
}

func loadStringListEnv(name string) ([]string, bool, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return nil, false, nil
	}

	var parsed []string
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, true, fmt.Errorf("parse %s: %w", name, err)
	}

	seen := make(map[string]struct{}, len(parsed))
	values := make([]string, 0, len(parsed))
	for _, value := range parsed {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	return values, true, nil
}

func resolveModelEndpoint(endpoints map[string]ModelEndpoint, key string) (string, string) {
	trimmedKey := strings.TrimSpace(key)
	if trimmedKey == "" || len(endpoints) == 0 {
		return "", ""
	}
	if endpoint, ok := endpoints[trimmedKey]; ok {
		return endpoint.URL, endpoint.Audience
	}
	for candidate, endpoint := range endpoints {
		if strings.EqualFold(strings.TrimSpace(candidate), trimmedKey) {
			return endpoint.URL, endpoint.Audience
		}
	}
	return "", ""
}

func (c ServiceEndpointConfig) ResolveForModel(model string) (string, string) {
	return resolveModelEndpoint(c.ModelEndpoints, model)
}

func (c KrakenConfig) ResolveForModel(model string) (string, string) {
	return resolveModelEndpoint(c.ModelEndpoints, model)
}

func (c OllamaConfig) ResolveForModel(model string) (string, string) {
	return resolveModelEndpoint(c.ModelEndpoints, model)
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
