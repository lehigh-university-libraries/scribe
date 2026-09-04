package config

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"gopkg.in/yaml.v3"
)

// ConfigPath is the hardcoded location of the runtime config file inside the
// deployed container. The embedded defaults are used when the file is missing
// (e.g. `go test`, local development outside docker).
const ConfigPath = "/etc/scribe/config.yaml"

// DefaultMaxManifestCanvases is the safe request fan-out used when the IIIF
// manifest limit is omitted from configuration.
const DefaultMaxManifestCanvases = 500

// DefaultMaxManifestImportBytes bounds the aggregate remote hOCR content read
// while importing one manifest. The hard ceiling deliberately matches the
// default so an operator cannot turn a single request into unbounded fan-out.
const DefaultMaxManifestImportBytes uint64 = 64 << 20

const (
	maxConfiguredManifestCanvases                        = 5000
	maxConfiguredManifestImportBytes              uint64 = 64 << 20
	DefaultMaxActiveTranscriptionJobsPerWorkspace        = 1000
	maxConfiguredActiveTranscriptionJobs                 = 100000
	DefaultMaxTranscriptionSegmentsPerJob                = 50
	maxConfiguredTranscriptionSegmentsPerJob             = 500
	DefaultTranscriptionJobWorkers                       = 3
	maxConfiguredTranscriptionJobWorkers                 = 32
	DefaultLLMBatchSize                                  = 10
	maxConfiguredLLMBatchSize                            = 100
	DefaultLineTranscribeConcurrency                     = 5
	maxConfiguredLineTranscribeConcurrency               = 32
	maxConfiguredQueueOutstandingMessages                = 128
	DefaultMaxStorageBytesPerWorkspace            uint64 = 5 << 30
	DefaultMaxStorageBytesTotal                   uint64 = 30 << 30
	DefaultMaxStorageItemsPerWorkspace            uint64 = 5000
	DefaultMaxStorageItemsTotal                   uint64 = 50000
	DefaultMaxStorageImagesPerWorkspace           uint64 = 10000
	DefaultMaxStorageImagesTotal                  uint64 = 100000
	DefaultStorageReservationTTL                         = 6 * time.Hour
	DefaultNormalizationCacheMaxBytes             uint64 = 2 << 30
	DefaultNormalizationCacheMaxAge                      = 7 * 24 * time.Hour
	DefaultMaxPageEnrichmentLines                        = 50
	maxConfiguredPageEnrichmentLines                     = 500
	DefaultTelemetryMetricExportInterval                 = 60 * time.Second
	DefaultTelemetryQueuePollInterval                    = 30 * time.Second
	DefaultTelemetryExportTimeout                        = 10 * time.Second
	DefaultTelemetryTraceSampleRatio                     = 0.05
	minTelemetryInterval                                 = 10 * time.Second
	maxTelemetryInterval                                 = 5 * time.Minute
	minTelemetryExportTimeout                            = time.Second
	maxTelemetryExportTimeout                            = 30 * time.Second
	maxConfiguredStorageBytes                     uint64 = 10 << 40
	maxConfiguredStorageObjects                   uint64 = 10_000_000
	tripletPresentationPath                              = "/presentation/v3"
	minTripletPresentationWriteTokenBytes                = 32
	maxTripletPresentationWriteTokenBytes                = 1024
)

//go:embed defaults/config.yaml
var embeddedDefaults []byte

var configEnvPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:-([^}]*))?\}`)
var proxyHostnameLabelPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)

// Config mirrors the YAML file shape.
type Config struct {
	ListenAddr    string `yaml:"listen_addr"`
	PublicBaseURL string `yaml:"public_base_url"`

	Server        ServerConfig          `yaml:"server"`
	Auth          AuthConfig            `yaml:"auth"`
	Pagination    PaginationConfig      `yaml:"pagination"`
	CORS          CORSConfig            `yaml:"cors"`
	Database      DatabaseConfig        `yaml:"database"`
	LLM           LLMConfig             `yaml:"llm"`
	Transcription TranscriptionConfig   `yaml:"transcription"`
	IIIF          IIIFConfig            `yaml:"iiif"`
	Segmentation  ServiceEndpointConfig `yaml:"segmentation_service"`
	Annotation    AnnotationConfig      `yaml:"annotation"`
	Processing    ProcessingConfig      `yaml:"processing"`
	Storage       StorageConfig         `yaml:"storage"`
	Audit         AuditConfig           `yaml:"audit"`
	Observability ObservabilityConfig   `yaml:"observability"`
	Vault         VaultConfig           `yaml:"vault"`

	// DatabaseDSN is resolved at load time from Vault + Database config.
	DatabaseDSN string `yaml:"-"`
}

// ServerConfig controls trust at the HTTP transport boundary. Forwarding
// headers are accepted only from these explicitly configured networks; an
// empty list is intentionally fail-closed.
type ServerConfig struct {
	TrustedProxyCIDRs CIDRList `yaml:"trusted_proxy_cidrs"`
	TrustedProxyHosts HostList `yaml:"trusted_proxy_hosts"`
}

// CIDRList accepts either a YAML sequence or a comma-separated scalar. The
// scalar form keeps environment-variable expansion convenient in deployed
// configuration without weakening validation.
type CIDRList []string

// HostList accepts a YAML sequence or comma-separated scalar. Hostnames are
// resolved only for direct-peer proxy authentication; forwarded client values
// are never resolved as names.
type HostList []string

func (values *CIDRList) UnmarshalYAML(node *yaml.Node) error {
	if node == nil {
		*values = nil
		return nil
	}

	var raw []string
	switch node.Kind {
	case yaml.ScalarNode:
		raw = strings.Split(node.Value, ",")
	case yaml.SequenceNode:
		if err := node.Decode(&raw); err != nil {
			return fmt.Errorf("trusted proxy CIDRs must be strings: %w", err)
		}
	default:
		return fmt.Errorf("trusted proxy CIDRs must be a sequence or comma-separated string")
	}

	*values = raw
	return nil
}

func (values *HostList) UnmarshalYAML(node *yaml.Node) error {
	if node == nil {
		*values = nil
		return nil
	}

	var raw []string
	switch node.Kind {
	case yaml.ScalarNode:
		raw = strings.Split(node.Value, ",")
	case yaml.SequenceNode:
		if err := node.Decode(&raw); err != nil {
			return fmt.Errorf("trusted proxy hosts must be strings: %w", err)
		}
	default:
		return fmt.Errorf("trusted proxy hosts must be a sequence or comma-separated string")
	}

	*values = raw
	return nil
}

type AuthConfig struct {
	CookieName         string                    `yaml:"cookie_name"`
	CookieDomain       string                    `yaml:"cookie_domain"`
	SessionTTL         time.Duration             `yaml:"session_ttl"`
	GoogleCallbackPath string                    `yaml:"google_callback_path"`
	PreviewAnonymous   bool                      `yaml:"preview_anonymous"`
	AllowedDomains     []string                  `yaml:"allowed_domains"`
	DeniedDomains      []string                  `yaml:"denied_domains"`
	AdminEmails        []string                  `yaml:"admin_emails"`
	ExternalJWTIssuers []ExternalJWTIssuerConfig `yaml:"external_jwt_issuers"`
}

// PaginationConfig contains secret material used to authenticate opaque
// continuation tokens across every API replica.
type PaginationConfig struct {
	SigningKey string `yaml:"signing_key"`
}

type ExternalJWTIssuerConfig struct {
	Issuer        string                   `yaml:"issuer"`
	Audience      string                   `yaml:"audience"`
	JWKSURL       string                   `yaml:"jwks_url"`
	WorkspaceID   uint64                   `yaml:"workspace_id"`
	ServiceUserID uint64                   `yaml:"service_user_id"`
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
	Model  string   `yaml:"model"`
	Models []string `yaml:"models"`
}

type TranscriptionConfig struct {
	JobWorkers                int                `yaml:"job_workers"`
	MaxActiveJobsPerWorkspace int                `yaml:"max_active_jobs_per_workspace"`
	MaxSegmentsPerJob         int                `yaml:"max_segments_per_job"`
	Queue                     TranscriptionQueue `yaml:"queue"`
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
	Base                   string `yaml:"base"`
	InternalBase           string `yaml:"internal_base"`
	SourceBase             string `yaml:"source_base"`
	SourceReadToken        string `yaml:"source_read_token"`
	MaxManifestCanvases    int    `yaml:"max_manifest_canvases"`
	MaxManifestImportBytes uint64 `yaml:"max_manifest_import_bytes"`
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

type ProcessingConfig struct {
	GlobalConcurrency        int           `yaml:"global_concurrency"`
	PerWorkspaceConcurrency  int           `yaml:"per_workspace_concurrency"`
	PerProviderConcurrency   int           `yaml:"per_provider_concurrency"`
	MaxPageEnrichmentLines   int           `yaml:"max_page_enrichment_lines"`
	ExternalRequestRetention time.Duration `yaml:"external_request_retention"`
}

type StorageConfig struct {
	MaxBytesPerWorkspace       uint64        `yaml:"max_bytes_per_workspace"`
	MaxBytesTotal              uint64        `yaml:"max_bytes_total"`
	MaxItemsPerWorkspace       uint64        `yaml:"max_items_per_workspace"`
	MaxItemsTotal              uint64        `yaml:"max_items_total"`
	MaxImagesPerWorkspace      uint64        `yaml:"max_images_per_workspace"`
	MaxImagesTotal             uint64        `yaml:"max_images_total"`
	ReservationTTL             time.Duration `yaml:"reservation_ttl"`
	NormalizationCacheMaxBytes uint64        `yaml:"normalization_cache_max_bytes"`
	NormalizationCacheMaxAge   time.Duration `yaml:"normalization_cache_max_age"`
}

type AuditConfig struct {
	ProviderCallRetention time.Duration `yaml:"provider_call_retention"`
}

// ObservabilityConfig controls the bounded OpenTelemetry export pipeline.
// It contains no credentials or arbitrary endpoints: the Google exporter uses
// Application Default Credentials and fixed Google APIs.
type ObservabilityConfig struct {
	Exporter              string        `yaml:"exporter"`
	GoogleProjectID       string        `yaml:"google_project_id"`
	DeploymentEnvironment string        `yaml:"deployment_environment"`
	MetricExportInterval  time.Duration `yaml:"metric_export_interval"`
	QueuePollInterval     time.Duration `yaml:"queue_poll_interval"`
	ExportTimeout         time.Duration `yaml:"export_timeout"`
	TraceSampleRatio      float64       `yaml:"trace_sample_ratio"`
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
	cfg.IIIF.Base = strings.TrimRight(strings.TrimSpace(cfg.IIIF.Base), "/")
	cfg.IIIF.InternalBase = strings.TrimRight(strings.TrimSpace(cfg.IIIF.InternalBase), "/")
	cfg.IIIF.SourceBase = strings.TrimRight(strings.TrimSpace(cfg.IIIF.SourceBase), "/")
	cfg.Annotation.TripletPresentationBase = strings.TrimRight(strings.TrimSpace(cfg.Annotation.TripletPresentationBase), "/")
	cfg.Annotation.TripletPresentationInternalBase = strings.TrimRight(strings.TrimSpace(cfg.Annotation.TripletPresentationInternalBase), "/")
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
		cfg.LLM.Kraken.Model = "catmus-medieval-1.6.0.mlmodel"
	}

	cfg.Server.TrustedProxyCIDRs, err = normalizeTrustedProxyCIDRs(cfg.Server.TrustedProxyCIDRs)
	if err != nil {
		return Config{}, err
	}
	cfg.Server.TrustedProxyHosts, err = normalizeTrustedProxyHosts(cfg.Server.TrustedProxyHosts)
	if err != nil {
		return Config{}, err
	}
	var publicBase *url.URL
	cfg.PublicBaseURL, publicBase, err = normalizePublicBaseURL(cfg.PublicBaseURL)
	if err != nil {
		return Config{}, err
	}
	cfg.Pagination.SigningKey, err = normalizePageTokenSigningKey(cfg.Pagination.SigningKey)
	if err != nil {
		return Config{}, err
	}
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
	if cfg.Auth.PreviewAnonymous {
		workspace := strings.Trim(strings.TrimSpace(cfg.Vault.Workspace), "/")
		expectedHostPrefix := "scribe-" + workspace + "-"
		if !regexp.MustCompile(`^pr-[0-9]+$`).MatchString(workspace) ||
			publicBase.Scheme != "https" ||
			!strings.HasPrefix(publicBase.Hostname(), expectedHostPrefix) ||
			!strings.HasSuffix(publicBase.Hostname(), ".run.app") {
			return Config{}, fmt.Errorf("auth.preview_anonymous is allowed only for the matching HTTPS pr-N run.app deployment")
		}
	}
	if cfg.Audit.ProviderCallRetention <= 0 {
		cfg.Audit.ProviderCallRetention = 30 * 24 * time.Hour
	}
	cfg.Observability, err = normalizeObservabilityConfig(cfg.Observability)
	if err != nil {
		return Config{}, err
	}
	if err := normalizeRuntimeConcurrency(&cfg); err != nil {
		return Config{}, err
	}
	if cfg.Transcription.MaxActiveJobsPerWorkspace == 0 {
		cfg.Transcription.MaxActiveJobsPerWorkspace = DefaultMaxActiveTranscriptionJobsPerWorkspace
	}
	if cfg.Transcription.MaxActiveJobsPerWorkspace < 1 || cfg.Transcription.MaxActiveJobsPerWorkspace > maxConfiguredActiveTranscriptionJobs {
		return Config{}, fmt.Errorf("transcription.max_active_jobs_per_workspace must be between 1 and %d", maxConfiguredActiveTranscriptionJobs)
	}
	if cfg.Transcription.MaxSegmentsPerJob == 0 {
		cfg.Transcription.MaxSegmentsPerJob = DefaultMaxTranscriptionSegmentsPerJob
	}
	if cfg.Transcription.MaxSegmentsPerJob < 1 || cfg.Transcription.MaxSegmentsPerJob > maxConfiguredTranscriptionSegmentsPerJob {
		return Config{}, fmt.Errorf("transcription.max_segments_per_job must be between 1 and %d", maxConfiguredTranscriptionSegmentsPerJob)
	}
	if cfg.Processing.GlobalConcurrency == 0 {
		cfg.Processing.GlobalConcurrency = 4
	}
	if cfg.Processing.PerWorkspaceConcurrency == 0 {
		cfg.Processing.PerWorkspaceConcurrency = 2
	}
	if cfg.Processing.PerProviderConcurrency == 0 {
		cfg.Processing.PerProviderConcurrency = 2
	}
	if cfg.Processing.MaxPageEnrichmentLines == 0 {
		cfg.Processing.MaxPageEnrichmentLines = DefaultMaxPageEnrichmentLines
	}
	if cfg.Processing.ExternalRequestRetention <= 0 {
		cfg.Processing.ExternalRequestRetention = 30 * 24 * time.Hour
	}
	cfg.IIIF.MaxManifestCanvases, err = normalizeMaxManifestCanvases(cfg.IIIF.MaxManifestCanvases)
	if err != nil {
		return Config{}, err
	}
	cfg.IIIF.MaxManifestImportBytes, err = normalizeMaxManifestImportBytes(cfg.IIIF.MaxManifestImportBytes)
	if err != nil {
		return Config{}, err
	}
	cfg.Storage, err = normalizeStorageConfig(cfg.Storage)
	if err != nil {
		return Config{}, err
	}
	if cfg.Processing.GlobalConcurrency < 1 || cfg.Processing.GlobalConcurrency > 32 {
		return Config{}, fmt.Errorf("processing.global_concurrency must be between 1 and 32")
	}
	if cfg.Processing.PerWorkspaceConcurrency < 1 || cfg.Processing.PerWorkspaceConcurrency > cfg.Processing.GlobalConcurrency {
		return Config{}, fmt.Errorf("processing.per_workspace_concurrency must be between 1 and processing.global_concurrency")
	}
	if cfg.Processing.PerProviderConcurrency < 1 || cfg.Processing.PerProviderConcurrency > cfg.Processing.GlobalConcurrency {
		return Config{}, fmt.Errorf("processing.per_provider_concurrency must be between 1 and processing.global_concurrency")
	}
	if cfg.Processing.MaxPageEnrichmentLines < 1 || cfg.Processing.MaxPageEnrichmentLines > maxConfiguredPageEnrichmentLines {
		return Config{}, fmt.Errorf("processing.max_page_enrichment_lines must be between 1 and %d", maxConfiguredPageEnrichmentLines)
	}
	if cfg.Vault.KVMount == "" {
		cfg.Vault.KVMount = "secret"
	}
	if cfg.Vault.GCPAuthRole == "" {
		cfg.Vault.GCPAuthRole = "scribe-app"
	}
	if cfg.Vault.Workspace != "" {
		expectedPrefix, err := expectedVaultPathPrefix(cfg)
		if err != nil {
			return Config{}, err
		}
		for name, path := range map[string]struct {
			value  string
			suffix string
		}{
			"google_oauth":     {value: cfg.Vault.Paths.GoogleOAuth, suffix: "google_oauth"},
			"openai":           {value: cfg.Vault.Paths.OpenAI, suffix: "openai"},
			"gemini":           {value: cfg.Vault.Paths.Gemini, suffix: "gemini"},
			"database":         {value: cfg.Vault.Paths.Database, suffix: "database/app"},
			"provider_secrets": {value: cfg.Vault.Paths.ProviderSecrets, suffix: "provider-secrets/workspaces"},
		} {
			normalized := strings.Trim(strings.TrimSpace(path.value), "/")
			if normalized != expectedPrefix+"/"+path.suffix {
				return Config{}, fmt.Errorf("vault path %s=%q does not match VAULT_WORKSPACE %q", name, path.value, cfg.Vault.Workspace)
			}
		}
	}
	cfg.Auth.ExternalJWTIssuers, err = normalizeExternalJWTIssuers(cfg.Auth.ExternalJWTIssuers)
	if err != nil {
		return Config{}, err
	}
	if err := validateServiceEndpoints(cfg); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func normalizePublicBaseURL(raw string) (string, *url.URL, error) {
	normalized := strings.TrimRight(strings.TrimSpace(raw), "/")
	publicBase, err := url.Parse(normalized)
	if err != nil || publicBase.Host == "" || (publicBase.Scheme != "http" && publicBase.Scheme != "https") || publicBase.User != nil || publicBase.RawQuery != "" || publicBase.Fragment != "" {
		return "", nil, fmt.Errorf("public_base_url must be an absolute HTTP(S) URL without credentials, query, or fragment")
	}
	if err := iiif.ValidatePublicBaseURL(normalized); err != nil {
		return "", nil, fmt.Errorf("public_base_url cannot produce persisted IIIF resource IDs: %w", err)
	}
	return normalized, publicBase, nil
}

func normalizeObservabilityConfig(value ObservabilityConfig) (ObservabilityConfig, error) {
	value.Exporter = strings.ToLower(strings.TrimSpace(value.Exporter))
	if value.Exporter == "" {
		value.Exporter = "none"
	}
	if value.Exporter != "none" && value.Exporter != "google" {
		return ObservabilityConfig{}, fmt.Errorf("observability.exporter must be one of none or google")
	}
	value.GoogleProjectID = strings.TrimSpace(value.GoogleProjectID)
	if value.GoogleProjectID != "" && !regexp.MustCompile(`^([a-z0-9][a-z0-9.-]*:)?[a-z][a-z0-9-]{4,28}[a-z0-9]$`).MatchString(value.GoogleProjectID) {
		return ObservabilityConfig{}, fmt.Errorf("observability.google_project_id must be empty or a valid Google Cloud project ID")
	}
	if value.Exporter == "google" && value.GoogleProjectID == "" {
		return ObservabilityConfig{}, fmt.Errorf("observability.google_project_id is required for the Google exporter")
	}
	value.DeploymentEnvironment = strings.ToLower(strings.TrimSpace(value.DeploymentEnvironment))
	if value.DeploymentEnvironment == "" {
		value.DeploymentEnvironment = "local"
	}
	if value.DeploymentEnvironment != "local" && value.DeploymentEnvironment != "dev" &&
		value.DeploymentEnvironment != "prod" && value.DeploymentEnvironment != "preview" {
		return ObservabilityConfig{}, fmt.Errorf("observability.deployment_environment must be one of local, dev, prod, or preview")
	}
	if value.MetricExportInterval == 0 {
		value.MetricExportInterval = DefaultTelemetryMetricExportInterval
	}
	if value.MetricExportInterval < minTelemetryInterval || value.MetricExportInterval > maxTelemetryInterval {
		return ObservabilityConfig{}, fmt.Errorf("observability.metric_export_interval must be between %s and %s", minTelemetryInterval, maxTelemetryInterval)
	}
	if value.QueuePollInterval == 0 {
		value.QueuePollInterval = DefaultTelemetryQueuePollInterval
	}
	if value.QueuePollInterval < minTelemetryInterval || value.QueuePollInterval > maxTelemetryInterval {
		return ObservabilityConfig{}, fmt.Errorf("observability.queue_poll_interval must be between %s and %s", minTelemetryInterval, maxTelemetryInterval)
	}
	if value.ExportTimeout == 0 {
		value.ExportTimeout = DefaultTelemetryExportTimeout
	}
	if value.ExportTimeout < minTelemetryExportTimeout || value.ExportTimeout > maxTelemetryExportTimeout {
		return ObservabilityConfig{}, fmt.Errorf("observability.export_timeout must be between %s and %s", minTelemetryExportTimeout, maxTelemetryExportTimeout)
	}
	if math.IsNaN(value.TraceSampleRatio) || math.IsInf(value.TraceSampleRatio, 0) ||
		value.TraceSampleRatio < 0 || value.TraceSampleRatio > 1 {
		return ObservabilityConfig{}, fmt.Errorf("observability.trace_sample_ratio must be between 0 and 1")
	}
	return value, nil
}

func expectedVaultPathPrefix(cfg Config) (string, error) {
	workspace := strings.Trim(strings.TrimSpace(cfg.Vault.Workspace), "/")
	if !cfg.Auth.PreviewAnonymous {
		return "scribe/" + workspace, nil
	}

	databasePath := strings.Trim(strings.TrimSpace(cfg.Vault.Paths.Database), "/")
	const databaseSuffix = "/database/app"
	if !strings.HasSuffix(databasePath, databaseSuffix) {
		return "", fmt.Errorf("vault database path %q does not match VAULT_WORKSPACE %q", cfg.Vault.Paths.Database, cfg.Vault.Workspace)
	}
	prefix := strings.TrimSuffix(databasePath, databaseSuffix)
	previewIdentityPrefix := regexp.MustCompile(
		`^scribe/previews/scribe-` + regexp.QuoteMeta(workspace) +
			`@[a-z][a-z0-9-]{4,28}[a-z0-9]\.iam\.gserviceaccount\.com$`,
	)
	if !previewIdentityPrefix.MatchString(prefix) {
		return "", fmt.Errorf("vault database path %q does not match VAULT_WORKSPACE %q identity scope", cfg.Vault.Paths.Database, cfg.Vault.Workspace)
	}
	return prefix, nil
}

func normalizeRuntimeConcurrency(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("runtime concurrency configuration is required")
	}
	if cfg.Transcription.JobWorkers == 0 {
		cfg.Transcription.JobWorkers = DefaultTranscriptionJobWorkers
	}
	if cfg.Transcription.JobWorkers < 1 || cfg.Transcription.JobWorkers > maxConfiguredTranscriptionJobWorkers {
		return fmt.Errorf("transcription.job_workers must be between 1 and %d", maxConfiguredTranscriptionJobWorkers)
	}
	if cfg.Transcription.Queue.MaxOutstandingMessages == 0 {
		cfg.Transcription.Queue.MaxOutstandingMessages = cfg.Transcription.JobWorkers
	}
	if cfg.Transcription.Queue.MaxOutstandingMessages < 1 || cfg.Transcription.Queue.MaxOutstandingMessages > maxConfiguredQueueOutstandingMessages {
		return fmt.Errorf("transcription.queue.max_outstanding_messages must be between 1 and %d", maxConfiguredQueueOutstandingMessages)
	}
	if cfg.LLM.BatchSize == 0 {
		cfg.LLM.BatchSize = DefaultLLMBatchSize
	}
	if cfg.LLM.BatchSize < 1 || cfg.LLM.BatchSize > maxConfiguredLLMBatchSize {
		return fmt.Errorf("llm.batch_size must be between 1 and %d", maxConfiguredLLMBatchSize)
	}
	if cfg.LLM.LineTranscribeConcurrency == 0 {
		cfg.LLM.LineTranscribeConcurrency = DefaultLineTranscribeConcurrency
	}
	if cfg.LLM.LineTranscribeConcurrency < 1 || cfg.LLM.LineTranscribeConcurrency > maxConfiguredLineTranscribeConcurrency {
		return fmt.Errorf("llm.line_transcribe_concurrency must be between 1 and %d", maxConfiguredLineTranscribeConcurrency)
	}
	return nil
}

func normalizeTrustedProxyCIDRs(values CIDRList) (CIDRList, error) {
	normalized := make(CIDRList, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		cidr := strings.TrimSpace(raw)
		if cidr == "" {
			continue
		}
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			return nil, fmt.Errorf("server.trusted_proxy_cidrs contains invalid CIDR %q: %w", cidr, err)
		}
		canonical := network.String()
		if _, duplicate := seen[canonical]; duplicate {
			continue
		}
		seen[canonical] = struct{}{}
		normalized = append(normalized, canonical)
	}
	return normalized, nil
}

func normalizeTrustedProxyHosts(values HostList) (HostList, error) {
	if len(values) > 8 {
		return nil, fmt.Errorf("server.trusted_proxy_hosts must contain at most 8 hostnames")
	}
	normalized := make(HostList, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		host := strings.ToLower(strings.TrimSpace(raw))
		if host == "" {
			continue
		}
		if len(host) > 253 || net.ParseIP(host) != nil {
			return nil, fmt.Errorf("server.trusted_proxy_hosts contains invalid DNS hostname %q", raw)
		}
		for _, label := range strings.Split(host, ".") {
			if len(label) == 0 || len(label) > 63 || !proxyHostnameLabelPattern.MatchString(label) {
				return nil, fmt.Errorf("server.trusted_proxy_hosts contains invalid DNS hostname %q", raw)
			}
		}
		if _, duplicate := seen[host]; duplicate {
			continue
		}
		seen[host] = struct{}{}
		normalized = append(normalized, host)
	}
	return normalized, nil
}

// AddressInCIDRs reports whether an address belongs to an explicitly trusted
// network. address may be an IP literal or a host:port RemoteAddr value.
func AddressInCIDRs(address string, cidrs CIDRList) bool {
	host := strings.TrimSpace(address)
	if splitHost, _, err := net.SplitHostPort(host); err == nil {
		host = splitHost
	}
	host = strings.Trim(strings.TrimSpace(host), "[]")
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, raw := range cidrs {
		_, network, err := net.ParseCIDR(raw)
		if err == nil && network.Contains(ip) {
			return true
		}
	}
	return false
}

func normalizePageTokenSigningKey(raw string) (string, error) {
	key := strings.TrimSpace(raw)
	if key != raw || len(key) < 32 || len(key) > 1024 {
		return "", fmt.Errorf("pagination.signing_key must contain between 32 and 1024 non-whitespace bytes")
	}
	return key, nil
}

func normalizeExternalJWTIssuers(issuers []ExternalJWTIssuerConfig) ([]ExternalJWTIssuerConfig, error) {
	normalized := make([]ExternalJWTIssuerConfig, 0, len(issuers))
	seen := make(map[string]struct{}, len(issuers))
	for index, issuer := range issuers {
		name := fmt.Sprintf("auth.external_jwt_issuers[%d]", index)
		parsedIssuer, err := parseSecureIdentityURL(name+".issuer", issuer.Issuer)
		if err != nil {
			return nil, err
		}
		issuer.Issuer = strings.TrimRight(parsedIssuer.String(), "/")
		issuer.Audience = strings.TrimSpace(issuer.Audience)
		if issuer.Audience == "" || strings.ContainsAny(issuer.Audience, "\r\n\t") {
			return nil, fmt.Errorf("%s.audience is required and must not contain control whitespace", name)
		}
		if issuer.WorkspaceID == 0 {
			return nil, fmt.Errorf("%s.workspace_id must be positive", name)
		}
		if issuer.ServiceUserID == 0 {
			return nil, fmt.Errorf("%s.service_user_id must be positive", name)
		}
		if strings.TrimSpace(issuer.JWKSURL) == "" {
			issuer.JWKSURL = issuer.Issuer + "/oauth/discovery/keys"
		}
		parsedJWKS, err := parseSecureIdentityURL(name+".jwks_url", issuer.JWKSURL)
		if err != nil {
			return nil, err
		}
		issuer.JWKSURL = parsedJWKS.String()
		key := strings.ToLower(issuer.Issuer)
		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("%s.issuer duplicates another configured issuer", name)
		}
		seen[key] = struct{}{}
		normalized = append(normalized, issuer)
	}
	return normalized, nil
}

func parseSecureIdentityURL(name, raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.Opaque != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("%s must be an absolute URL without credentials, query, or fragment", name)
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	isLoopback := host == "localhost" || host == "127.0.0.1" || host == "::1"
	if !strings.EqualFold(parsed.Scheme, "https") && (!strings.EqualFold(parsed.Scheme, "http") || !isLoopback) {
		return nil, fmt.Errorf("%s must use HTTPS except for a loopback development endpoint", name)
	}
	return parsed, nil
}

func normalizeMaxManifestCanvases(value int) (int, error) {
	if value == 0 {
		return DefaultMaxManifestCanvases, nil
	}
	if value < 1 || value > maxConfiguredManifestCanvases {
		return 0, fmt.Errorf("iiif.max_manifest_canvases must be between 1 and %d", maxConfiguredManifestCanvases)
	}
	return value, nil
}

func normalizeMaxManifestImportBytes(value uint64) (uint64, error) {
	if value == 0 {
		return DefaultMaxManifestImportBytes, nil
	}
	if value > maxConfiguredManifestImportBytes {
		return 0, fmt.Errorf("iiif.max_manifest_import_bytes must be between 1 and %d", maxConfiguredManifestImportBytes)
	}
	return value, nil
}

func normalizeStorageConfig(value StorageConfig) (StorageConfig, error) {
	if value.MaxBytesPerWorkspace == 0 {
		value.MaxBytesPerWorkspace = DefaultMaxStorageBytesPerWorkspace
	}
	if value.MaxBytesTotal == 0 {
		value.MaxBytesTotal = DefaultMaxStorageBytesTotal
	}
	if value.MaxItemsPerWorkspace == 0 {
		value.MaxItemsPerWorkspace = DefaultMaxStorageItemsPerWorkspace
	}
	if value.MaxItemsTotal == 0 {
		value.MaxItemsTotal = DefaultMaxStorageItemsTotal
	}
	if value.MaxImagesPerWorkspace == 0 {
		value.MaxImagesPerWorkspace = DefaultMaxStorageImagesPerWorkspace
	}
	if value.MaxImagesTotal == 0 {
		value.MaxImagesTotal = DefaultMaxStorageImagesTotal
	}
	if value.ReservationTTL == 0 {
		value.ReservationTTL = DefaultStorageReservationTTL
	}
	if value.NormalizationCacheMaxBytes == 0 {
		value.NormalizationCacheMaxBytes = DefaultNormalizationCacheMaxBytes
	}
	if value.NormalizationCacheMaxAge == 0 {
		value.NormalizationCacheMaxAge = DefaultNormalizationCacheMaxAge
	}
	if value.MaxBytesPerWorkspace < 100<<20 || value.MaxBytesPerWorkspace > maxConfiguredStorageBytes {
		return StorageConfig{}, fmt.Errorf("storage.max_bytes_per_workspace must be between 100 MiB and 10 TiB")
	}
	if value.MaxBytesTotal < value.MaxBytesPerWorkspace || value.MaxBytesTotal > maxConfiguredStorageBytes {
		return StorageConfig{}, fmt.Errorf("storage.max_bytes_total must be at least max_bytes_per_workspace and no more than 10 TiB")
	}
	if value.MaxItemsPerWorkspace < 1 || value.MaxItemsPerWorkspace > maxConfiguredStorageObjects {
		return StorageConfig{}, fmt.Errorf("storage.max_items_per_workspace must be between 1 and %d", maxConfiguredStorageObjects)
	}
	if value.MaxItemsTotal < value.MaxItemsPerWorkspace || value.MaxItemsTotal > maxConfiguredStorageObjects {
		return StorageConfig{}, fmt.Errorf("storage.max_items_total must be at least max_items_per_workspace and no more than %d", maxConfiguredStorageObjects)
	}
	if value.MaxImagesPerWorkspace < 1 || value.MaxImagesPerWorkspace > maxConfiguredStorageObjects {
		return StorageConfig{}, fmt.Errorf("storage.max_images_per_workspace must be between 1 and %d", maxConfiguredStorageObjects)
	}
	if value.MaxImagesTotal < value.MaxImagesPerWorkspace || value.MaxImagesTotal > maxConfiguredStorageObjects {
		return StorageConfig{}, fmt.Errorf("storage.max_images_total must be at least max_images_per_workspace and no more than %d", maxConfiguredStorageObjects)
	}
	if value.ReservationTTL < 5*time.Minute || value.ReservationTTL > 24*time.Hour {
		return StorageConfig{}, fmt.Errorf("storage.reservation_ttl must be between 5m and 24h")
	}
	if value.NormalizationCacheMaxBytes < 100<<20 || value.NormalizationCacheMaxBytes > maxConfiguredStorageBytes {
		return StorageConfig{}, fmt.Errorf("storage.normalization_cache_max_bytes must be between 100 MiB and 10 TiB")
	}
	if value.NormalizationCacheMaxAge < time.Hour || value.NormalizationCacheMaxAge > 365*24*time.Hour {
		return StorageConfig{}, fmt.Errorf("storage.normalization_cache_max_age must be between 1h and 8760h")
	}
	return value, nil
}

func validateServiceEndpoints(cfg Config) error {
	endpoints := []struct {
		name     string
		url      string
		audience string
	}{
		{name: "llm.ollama", url: cfg.LLM.Ollama.URL, audience: cfg.LLM.Ollama.Audience},
		{name: "llm.kraken", url: cfg.LLM.Kraken.URL, audience: cfg.LLM.Kraken.Audience},
		{name: "segmentation_service", url: cfg.Segmentation.URL, audience: cfg.Segmentation.Audience},
	}
	for _, endpoint := range endpoints {
		if err := validateServiceEndpoint(endpoint.name, endpoint.url, endpoint.audience); err != nil {
			return err
		}
	}
	modelEndpointSets := []struct {
		name      string
		endpoints map[string]ModelEndpoint
	}{
		{name: "llm.ollama.model_endpoints", endpoints: cfg.LLM.Ollama.ModelEndpoints},
		{name: "llm.kraken.model_endpoints", endpoints: cfg.LLM.Kraken.ModelEndpoints},
		{name: "segmentation_service.model_endpoints", endpoints: cfg.Segmentation.ModelEndpoints},
	}
	for _, set := range modelEndpointSets {
		keys := make([]string, 0, len(set.endpoints))
		for key := range set.endpoints {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		seen := make(map[string]string, len(keys))
		for _, key := range keys {
			normalizedKey := normalizeModelEndpointKey(key)
			if previous, duplicate := seen[normalizedKey]; duplicate {
				return fmt.Errorf("%s contains case-insensitive duplicate model keys %q and %q", set.name, previous, key)
			}
			seen[normalizedKey] = key
			endpoint := set.endpoints[key]
			if err := validateServiceEndpoint(set.name+"["+key+"]", endpoint.URL, endpoint.Audience); err != nil {
				return err
			}
		}
	}
	if err := validateAbsoluteServiceURL("iiif.internal_base", cfg.IIIF.InternalBase); err != nil {
		return err
	}
	if err := validateTripletSource(cfg.IIIF); err != nil {
		return err
	}
	if err := validateTripletPresentation(cfg.Annotation); err != nil {
		return err
	}
	return nil
}

func validateTripletPresentation(cfg AnnotationConfig) error {
	publicBase := strings.TrimSpace(cfg.TripletPresentationBase)
	internalBase := strings.TrimSpace(cfg.TripletPresentationInternalBase)
	token := strings.TrimSpace(cfg.TripletPresentationWriteToken)
	if publicBase == "" || internalBase == "" || token == "" {
		return fmt.Errorf("annotation.triplet_presentation_base, annotation.triplet_presentation_internal_base, and annotation.triplet_presentation_write_token must be configured together")
	}
	if token != cfg.TripletPresentationWriteToken || len(token) < minTripletPresentationWriteTokenBytes || len(token) > maxTripletPresentationWriteTokenBytes {
		return fmt.Errorf("annotation.triplet_presentation_write_token must contain between %d and %d non-whitespace bytes", minTripletPresentationWriteTokenBytes, maxTripletPresentationWriteTokenBytes)
	}
	publicURL, err := parseAbsoluteServiceURL("annotation.triplet_presentation_base", publicBase)
	if err != nil {
		return err
	}
	internalURL, err := parseAbsoluteServiceURL("annotation.triplet_presentation_internal_base", internalBase)
	if err != nil {
		return err
	}
	for name, parsed := range map[string]*url.URL{
		"annotation.triplet_presentation_base":          publicURL,
		"annotation.triplet_presentation_internal_base": internalURL,
	} {
		if parsed.Path != tripletPresentationPath || parsed.EscapedPath() != tripletPresentationPath {
			return fmt.Errorf("%s path must be exactly %s", name, tripletPresentationPath)
		}
	}
	publicHost := strings.ToLower(publicURL.Hostname())
	publicLoopback := publicHost == "localhost" || publicHost == "127.0.0.1" || publicHost == "::1"
	if !strings.EqualFold(publicURL.Scheme, "https") && !publicLoopback {
		return fmt.Errorf("annotation.triplet_presentation_base must use HTTPS except on loopback")
	}
	return nil
}

func validateTripletSource(cfg IIIFConfig) error {
	internalBase := strings.TrimSpace(cfg.InternalBase)
	sourceBase := strings.TrimSpace(cfg.SourceBase)
	token := strings.TrimSpace(cfg.SourceReadToken)
	if internalBase == "" && sourceBase == "" && token == "" {
		return nil
	}
	if internalBase == "" || sourceBase == "" || token == "" {
		return fmt.Errorf("iiif.internal_base, iiif.source_base, and iiif.source_read_token must be configured together")
	}
	if len(token) < 32 || len(token) > 1024 || token != cfg.SourceReadToken {
		return fmt.Errorf("iiif.source_read_token must contain between 32 and 1024 non-whitespace bytes")
	}
	parsed, err := parseAbsoluteServiceURL("iiif.source_base", sourceBase)
	if err != nil {
		return err
	}
	if parsed.Path != "/static/uploads" || parsed.EscapedPath() != "/static/uploads" {
		return fmt.Errorf("iiif.source_base path must be exactly /static/uploads")
	}
	return nil
}

func validateServiceEndpoint(name, rawURL, rawAudience string) error {
	rawURL = strings.TrimSpace(rawURL)
	rawAudience = strings.TrimSpace(rawAudience)
	if rawURL == "" {
		if rawAudience != "" {
			return fmt.Errorf("%s.audience requires %s.url", name, name)
		}
		return nil
	}
	endpoint, err := parseAbsoluteServiceURL(name+".url", rawURL)
	if err != nil {
		return err
	}
	if rawAudience == "" {
		return nil
	}
	audience, err := parseAbsoluteServiceURL(name+".audience", rawAudience)
	if err != nil {
		return err
	}
	if !strings.EqualFold(audience.Scheme, "https") || audience.Path != "" {
		return fmt.Errorf("%s.audience must be a canonical HTTPS origin without a trailing slash or path", name)
	}
	if !strings.EqualFold(endpoint.Scheme, "https") {
		return fmt.Errorf("%s.url must use HTTPS when an audience is configured", name)
	}
	if !strings.EqualFold(endpoint.Scheme, audience.Scheme) || !strings.EqualFold(endpoint.Host, audience.Host) {
		return fmt.Errorf("%s.url and %s.audience must use the same exact origin", name, name)
	}
	return nil
}

func validateAbsoluteServiceURL(name, raw string) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	_, err := parseAbsoluteServiceURL(name, raw)
	return err
}

func parseAbsoluteServiceURL(name, raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || (!strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https")) || parsed.Opaque != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("%s must be an absolute HTTP(S) URL without credentials, query, or fragment", name)
	}
	return parsed, nil
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

	keys := make([]string, 0, len(parsed))
	for key := range parsed {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	normalized := make(map[string]ModelEndpoint, len(parsed))
	originalKeys := make(map[string]string, len(parsed))
	for _, key := range keys {
		normalizedKey := normalizeModelEndpointKey(key)
		if normalizedKey == "" {
			continue
		}
		if previous, duplicate := originalKeys[normalizedKey]; duplicate {
			return nil, fmt.Errorf("%s contains case-insensitive duplicate model keys %q and %q", name, previous, key)
		}
		originalKeys[normalizedKey] = key

		endpoint := parsed[key]
		endpoint.URL = strings.TrimSpace(endpoint.URL)
		endpoint.Audience = strings.TrimSpace(endpoint.Audience)
		if endpoint.URL == "" {
			continue
		}
		normalized[normalizedKey] = endpoint
	}
	if len(normalized) == 0 {
		return nil, nil
	}
	return normalized, nil
}

func normalizeModelEndpointKey(key string) string {
	return strings.ToLower(strings.TrimSpace(key))
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
	normalizedKey := normalizeModelEndpointKey(key)
	if normalizedKey == "" || len(endpoints) == 0 {
		return "", ""
	}
	if endpoint, ok := endpoints[normalizedKey]; ok {
		return endpoint.URL, endpoint.Audience
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
