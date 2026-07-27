package config

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

const testPageTokenSigningKey = "test-page-token-signing-key-32-bytes-minimum"
const testTripletSourceReadToken = "test-triplet-source-token-32-bytes-minimum"
const testTripletPresentationWriteToken = "test-triplet-presentation-write-token-32-bytes-minimum"

func TestMain(m *testing.M) {
	if err := os.Setenv("SCRIBE_PAGE_TOKEN_SIGNING_KEY", testPageTokenSigningKey); err != nil {
		panic(err)
	}
	if err := os.Setenv("TRIPLET_SOURCE_READ_TOKEN", testTripletSourceReadToken); err != nil {
		panic(err)
	}
	if err := os.Setenv("TRIPLET_PRESENTATION_BASE", "https://scribe.test/presentation/v3"); err != nil {
		panic(err)
	}
	if err := os.Setenv("TRIPLET_PRESENTATION_INTERNAL_BASE", "http://triplet:8080/presentation/v3"); err != nil {
		panic(err)
	}
	if err := os.Setenv("TRIPLET_PRESENTATION_WRITE_TOKEN", testTripletPresentationWriteToken); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

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

func TestTrustedProxyCIDRsAreExplicitNormalizedAndBounded(t *testing.T) {
	t.Parallel()

	var scalar struct {
		Values CIDRList `yaml:"values"`
	}
	if err := yaml.Unmarshal([]byte("values: 172.30.0.2/32, 2001:db8::1/64, 172.30.0.2/32\n"), &scalar); err != nil {
		t.Fatalf("unmarshal scalar CIDRs: %v", err)
	}
	normalized, err := normalizeTrustedProxyCIDRs(scalar.Values)
	if err != nil {
		t.Fatalf("normalizeTrustedProxyCIDRs() error = %v", err)
	}
	want := CIDRList{"172.30.0.2/32", "2001:db8::/64"}
	if !reflect.DeepEqual(normalized, want) {
		t.Fatalf("normalized CIDRs = %#v; want %#v", normalized, want)
	}
	if !AddressInCIDRs("172.30.0.2:443", normalized) || !AddressInCIDRs("[2001:db8::99]:443", normalized) {
		t.Fatal("configured proxy address was not trusted")
	}
	for _, address := range []string{"172.30.0.3:443", "10.0.0.1:443", "127.0.0.1:443", "169.254.169.254:80"} {
		if AddressInCIDRs(address, normalized) {
			t.Fatalf("AddressInCIDRs(%q) = true; want false", address)
		}
	}

	if _, err := normalizeTrustedProxyCIDRs(CIDRList{"private-network"}); err == nil {
		t.Fatal("normalizeTrustedProxyCIDRs(invalid) succeeded")
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

func TestLoadModelEndpointMapEnvNormalizesModelKeyCasing(t *testing.T) {
	t.Setenv("OLLAMA_MODEL_ENDPOINTS_JSON", `{
		" Vision ": {
			"url": "https://vision.example",
			"audience": "https://vision.example"
		}
	}`)

	got, err := loadModelEndpointMapEnv("OLLAMA_MODEL_ENDPOINTS_JSON")
	if err != nil {
		t.Fatalf("loadModelEndpointMapEnv() error = %v", err)
	}
	if _, ok := got["vision"]; !ok {
		t.Fatalf("normalized endpoint key is missing: %v", got)
	}
	url, audience := (OllamaConfig{ModelEndpoints: got}).ResolveForModel("vIsIoN")
	if url != "https://vision.example" || audience != "https://vision.example" {
		t.Fatalf("ResolveForModel() = (%q, %q)", url, audience)
	}
}

func TestLoadModelEndpointMapEnvRejectsCaseInsensitiveDuplicates(t *testing.T) {
	t.Setenv("OLLAMA_MODEL_ENDPOINTS_JSON", `{
		"vision": {"url": "https://one.example"},
		"VISION": {"url": "https://two.example"}
	}`)

	_, err := loadModelEndpointMapEnv("OLLAMA_MODEL_ENDPOINTS_JSON")
	if err == nil || !strings.Contains(err.Error(), "case-insensitive duplicate model keys") {
		t.Fatalf("loadModelEndpointMapEnv() error = %v, want duplicate rejection", err)
	}
}

func TestLoadModelEndpointMapEnvRejectsInvalidJSON(t *testing.T) {
	t.Setenv("OLLAMA_MODEL_ENDPOINTS_JSON", `{not-json`)

	if _, err := loadModelEndpointMapEnv("OLLAMA_MODEL_ENDPOINTS_JSON"); err == nil {
		t.Fatal("expected error for invalid endpoint map json")
	}
}

func TestLoadStringListEnv(t *testing.T) {
	t.Setenv("OLLAMA_MODELS_JSON", `[" glm-ocr:bf16 ", "", "llava", "llava"]`)

	got, ok, err := loadStringListEnv("OLLAMA_MODELS_JSON")
	if err != nil {
		t.Fatalf("loadStringListEnv() error = %v", err)
	}
	if !ok {
		t.Fatal("loadStringListEnv() ok = false")
	}
	if strings.Join(got, ",") != "glm-ocr:bf16,llava" {
		t.Fatalf("loadStringListEnv() = %v", got)
	}
}

func TestLoadStringListEnvRejectsInvalidJSON(t *testing.T) {
	t.Setenv("SEGMENTATION_MODELS_JSON", `{"not":"a-list"}`)

	if _, _, err := loadStringListEnv("SEGMENTATION_MODELS_JSON"); err == nil {
		t.Fatal("expected error for invalid string list json")
	}
}

func TestLoadRejectsVaultWorkspacePathMismatch(t *testing.T) {
	t.Setenv("VAULT_WORKSPACE", "prod")
	t.Setenv("VAULT_SECRET_PREFIX", "scribe/dev")

	_, err := Load()
	if err == nil {
		t.Fatal("Load accepted Vault paths for the wrong workspace")
	}
	if !strings.Contains(err.Error(), "does not match VAULT_WORKSPACE") {
		t.Fatalf("Load error = %v, want workspace mismatch", err)
	}
}

func TestLoadAcceptsMatchingVaultWorkspacePaths(t *testing.T) {
	t.Setenv("VAULT_WORKSPACE", "prod")
	t.Setenv("VAULT_SECRET_PREFIX", "scribe/prod")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error for matching Vault workspace paths: %v", err)
	}
	if cfg.Vault.Workspace != "prod" {
		t.Fatalf("Vault.Workspace = %q, want prod", cfg.Vault.Workspace)
	}
}

func TestPreviewAnonymousModeIsBoundToMatchingPreviewOrigin(t *testing.T) {
	t.Setenv("AUTH_PREVIEW_ANONYMOUS", "true")
	t.Setenv("VAULT_WORKSPACE", "pr-75")
	t.Setenv("VAULT_SECRET_PREFIX", "scribe/previews/scribe-pr-75@example-project.iam.gserviceaccount.com")
	t.Setenv("PUBLIC_BASE_URL", "https://scribe-pr-75-123456.us-east5.run.app")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load matching preview mode: %v", err)
	}
	if !cfg.Auth.PreviewAnonymous {
		t.Fatal("preview anonymous mode was not enabled")
	}

	for _, test := range []struct {
		name      string
		workspace string
		baseURL   string
	}{
		{name: "production workspace", workspace: "prod", baseURL: "https://scribe-123456.us-east5.run.app"},
		{name: "mismatched preview", workspace: "pr-76", baseURL: "https://scribe-pr-75-123456.us-east5.run.app"},
		{name: "non Cloud Run origin", workspace: "pr-75", baseURL: "https://preview.example.org"},
		{name: "insecure origin", workspace: "pr-75", baseURL: "http://scribe-pr-75-123456.us-east5.run.app"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("VAULT_WORKSPACE", test.workspace)
			t.Setenv("VAULT_SECRET_PREFIX", "scribe/previews/scribe-"+test.workspace+"@example-project.iam.gserviceaccount.com")
			t.Setenv("PUBLIC_BASE_URL", test.baseURL)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), "auth.preview_anonymous") {
				t.Fatalf("Load error = %v, want preview binding rejection", err)
			}
		})
	}
}

func TestPreviewAnonymousModeRejectsAnotherServiceAccountNamespace(t *testing.T) {
	for _, prefix := range []string{
		"scribe/previews/scribe-pr-76@example-project.iam.gserviceaccount.com",
		"scribe/previews/scribe-pr-75@bad.iam.gserviceaccount.com",
		"scribe/pr-75",
	} {
		t.Run(prefix, func(t *testing.T) {
			t.Setenv("AUTH_PREVIEW_ANONYMOUS", "true")
			t.Setenv("VAULT_WORKSPACE", "pr-75")
			t.Setenv("VAULT_SECRET_PREFIX", prefix)
			t.Setenv("PUBLIC_BASE_URL", "https://scribe-pr-75-123456.us-east5.run.app")

			if _, err := Load(); err == nil || !strings.Contains(err.Error(), "does not match VAULT_WORKSPACE") {
				t.Fatalf("Load error = %v, want identity-scoped Vault path rejection", err)
			}
		})
	}
}

func TestLoadAppliesBoundedProcessingDefaults(t *testing.T) {
	t.Setenv("IIIF_MAX_MANIFEST_CANVASES", "")
	t.Setenv("IIIF_MAX_MANIFEST_IMPORT_BYTES", "")
	t.Setenv("TRANSCRIPTION_MAX_ACTIVE_JOBS_PER_WORKSPACE", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Processing.GlobalConcurrency != 4 || cfg.Processing.PerWorkspaceConcurrency != 2 || cfg.Processing.PerProviderConcurrency != 2 {
		t.Fatalf("processing concurrency = %+v, want global=4 workspace=2 provider=2", cfg.Processing)
	}
	if cfg.Processing.MaxPageEnrichmentLines != DefaultMaxPageEnrichmentLines {
		t.Fatalf("max page enrichment lines = %d, want %d", cfg.Processing.MaxPageEnrichmentLines, DefaultMaxPageEnrichmentLines)
	}
	if cfg.Processing.ExternalRequestRetention != 30*24*time.Hour {
		t.Fatalf("external request retention = %s, want 720h", cfg.Processing.ExternalRequestRetention)
	}
	if cfg.IIIF.MaxManifestCanvases != DefaultMaxManifestCanvases {
		t.Fatalf("IIIF manifest canvas limit = %d, want %d", cfg.IIIF.MaxManifestCanvases, DefaultMaxManifestCanvases)
	}
	if cfg.IIIF.MaxManifestImportBytes != DefaultMaxManifestImportBytes {
		t.Fatalf("IIIF manifest import byte limit = %d, want %d", cfg.IIIF.MaxManifestImportBytes, DefaultMaxManifestImportBytes)
	}
	if cfg.Transcription.MaxActiveJobsPerWorkspace != DefaultMaxActiveTranscriptionJobsPerWorkspace {
		t.Fatalf("active transcription job limit = %d, want %d", cfg.Transcription.MaxActiveJobsPerWorkspace, DefaultMaxActiveTranscriptionJobsPerWorkspace)
	}
	if cfg.Transcription.MaxSegmentsPerJob != DefaultMaxTranscriptionSegmentsPerJob {
		t.Fatalf("transcription segment limit = %d, want %d", cfg.Transcription.MaxSegmentsPerJob, DefaultMaxTranscriptionSegmentsPerJob)
	}
	if cfg.Transcription.JobWorkers != DefaultTranscriptionJobWorkers || cfg.Transcription.Queue.MaxOutstandingMessages != DefaultTranscriptionJobWorkers {
		t.Fatalf("transcription worker defaults = %+v", cfg.Transcription)
	}
	if cfg.LLM.BatchSize != DefaultLLMBatchSize || cfg.LLM.LineTranscribeConcurrency != DefaultLineTranscribeConcurrency {
		t.Fatalf("LLM concurrency defaults = %+v", cfg.LLM)
	}
	if cfg.Pagination.SigningKey != testPageTokenSigningKey {
		t.Fatalf("pagination signing key was not loaded from the environment")
	}
	if cfg.Storage.MaxBytesPerWorkspace != DefaultMaxStorageBytesPerWorkspace || cfg.Storage.MaxBytesTotal != DefaultMaxStorageBytesTotal ||
		cfg.Storage.MaxItemsPerWorkspace != DefaultMaxStorageItemsPerWorkspace || cfg.Storage.MaxItemsTotal != DefaultMaxStorageItemsTotal ||
		cfg.Storage.MaxImagesPerWorkspace != DefaultMaxStorageImagesPerWorkspace || cfg.Storage.MaxImagesTotal != DefaultMaxStorageImagesTotal ||
		cfg.Storage.ReservationTTL != DefaultStorageReservationTTL || cfg.Storage.NormalizationCacheMaxBytes != DefaultNormalizationCacheMaxBytes ||
		cfg.Storage.NormalizationCacheMaxAge != DefaultNormalizationCacheMaxAge {
		t.Fatalf("storage quota defaults = %+v", cfg.Storage)
	}
}

func TestNormalizeRuntimeConcurrencyIsBounded(t *testing.T) {
	t.Parallel()

	var defaults Config
	if err := normalizeRuntimeConcurrency(&defaults); err != nil {
		t.Fatalf("normalize defaults: %v", err)
	}
	if defaults.Transcription.JobWorkers != DefaultTranscriptionJobWorkers ||
		defaults.Transcription.Queue.MaxOutstandingMessages != DefaultTranscriptionJobWorkers ||
		defaults.LLM.BatchSize != DefaultLLMBatchSize ||
		defaults.LLM.LineTranscribeConcurrency != DefaultLineTranscribeConcurrency {
		t.Fatalf("normalized defaults = %+v", defaults)
	}

	valid := Config{
		LLM: LLMConfig{BatchSize: maxConfiguredLLMBatchSize, LineTranscribeConcurrency: maxConfiguredLineTranscribeConcurrency},
		Transcription: TranscriptionConfig{
			JobWorkers: maxConfiguredTranscriptionJobWorkers,
			Queue:      TranscriptionQueue{MaxOutstandingMessages: maxConfiguredQueueOutstandingMessages},
		},
	}
	if err := normalizeRuntimeConcurrency(&valid); err != nil {
		t.Fatalf("normalize maxima: %v", err)
	}

	for name, mutate := range map[string]func(*Config){
		"negative workers":     func(cfg *Config) { cfg.Transcription.JobWorkers = -1 },
		"too many workers":     func(cfg *Config) { cfg.Transcription.JobWorkers = maxConfiguredTranscriptionJobWorkers + 1 },
		"negative outstanding": func(cfg *Config) { cfg.Transcription.Queue.MaxOutstandingMessages = -1 },
		"too many outstanding": func(cfg *Config) {
			cfg.Transcription.Queue.MaxOutstandingMessages = maxConfiguredQueueOutstandingMessages + 1
		},
		"negative batch":            func(cfg *Config) { cfg.LLM.BatchSize = -1 },
		"oversized batch":           func(cfg *Config) { cfg.LLM.BatchSize = maxConfiguredLLMBatchSize + 1 },
		"negative line concurrency": func(cfg *Config) { cfg.LLM.LineTranscribeConcurrency = -1 },
		"line concurrency fanout":   func(cfg *Config) { cfg.LLM.LineTranscribeConcurrency = maxConfiguredLineTranscribeConcurrency + 1 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := Config{
				LLM: LLMConfig{BatchSize: 1, LineTranscribeConcurrency: 1},
				Transcription: TranscriptionConfig{
					JobWorkers: 1,
					Queue:      TranscriptionQueue{MaxOutstandingMessages: 1},
				},
			}
			mutate(&candidate)
			if err := normalizeRuntimeConcurrency(&candidate); err == nil {
				t.Fatal("expected bounded concurrency validation error")
			}
		})
	}
}

func TestLoadFailsClosedWithoutPageTokenSigningKey(t *testing.T) {
	t.Setenv("SCRIBE_PAGE_TOKEN_SIGNING_KEY", "")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "pagination.signing_key") {
		t.Fatalf("Load error = %v, want required pagination signing key", err)
	}
}

func TestNormalizePageTokenSigningKeyRejectsUnsafeValues(t *testing.T) {
	for _, value := range []string{"", "too-short", " " + testPageTokenSigningKey, strings.Repeat("x", 1025)} {
		if _, err := normalizePageTokenSigningKey(value); err == nil {
			t.Fatalf("normalizePageTokenSigningKey(%q) accepted an unsafe key", value)
		}
	}
	if got, err := normalizePageTokenSigningKey(testPageTokenSigningKey); err != nil || got != testPageTokenSigningKey {
		t.Fatalf("normalizePageTokenSigningKey(valid) = %q/%v", got, err)
	}
}

func TestNormalizeStorageConfigRejectsUnsafeLimits(t *testing.T) {
	t.Parallel()

	valid, err := normalizeStorageConfig(StorageConfig{})
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*StorageConfig){
		"workspace bytes below upload":  func(cfg *StorageConfig) { cfg.MaxBytesPerWorkspace = 99 << 20 },
		"global bytes below workspace":  func(cfg *StorageConfig) { cfg.MaxBytesTotal = cfg.MaxBytesPerWorkspace - 1 },
		"global items below workspace":  func(cfg *StorageConfig) { cfg.MaxItemsTotal = cfg.MaxItemsPerWorkspace - 1 },
		"global images below workspace": func(cfg *StorageConfig) { cfg.MaxImagesTotal = cfg.MaxImagesPerWorkspace - 1 },
		"short reservation":             func(cfg *StorageConfig) { cfg.ReservationTTL = time.Minute },
		"small normalization cache":     func(cfg *StorageConfig) { cfg.NormalizationCacheMaxBytes = 99 << 20 },
		"short normalization cache age": func(cfg *StorageConfig) { cfg.NormalizationCacheMaxAge = time.Minute },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if _, err := normalizeStorageConfig(candidate); err == nil {
				t.Fatal("expected storage quota validation error")
			}
		})
	}
}

func TestLoadValidatesActiveTranscriptionJobLimit(t *testing.T) {
	for _, test := range []struct {
		name      string
		value     string
		want      int
		wantError bool
	}{
		{name: "minimum", value: "1", want: 1},
		{name: "configured", value: "2500", want: 2500},
		{name: "maximum", value: "100000", want: 100000},
		{name: "negative", value: "-1", wantError: true},
		{name: "above maximum", value: "100001", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("TRANSCRIPTION_MAX_ACTIVE_JOBS_PER_WORKSPACE", test.value)
			cfg, err := Load()
			if (err != nil) != test.wantError {
				t.Fatalf("Load() error = %v, wantError %t", err, test.wantError)
			}
			if !test.wantError && cfg.Transcription.MaxActiveJobsPerWorkspace != test.want {
				t.Fatalf("active transcription job limit = %d, want %d", cfg.Transcription.MaxActiveJobsPerWorkspace, test.want)
			}
		})
	}
}

func TestLoadValidatesTranscriptionSegmentLimit(t *testing.T) {
	for _, test := range []struct {
		name      string
		value     string
		want      int
		wantError bool
	}{
		{name: "minimum", value: "1", want: 1},
		{name: "configured", value: "250", want: 250},
		{name: "maximum", value: "500", want: 500},
		{name: "negative", value: "-1", wantError: true},
		{name: "above maximum", value: "501", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("TRANSCRIPTION_MAX_SEGMENTS_PER_JOB", test.value)
			cfg, err := Load()
			if (err != nil) != test.wantError {
				t.Fatalf("Load() error = %v, wantError %t", err, test.wantError)
			}
			if !test.wantError && cfg.Transcription.MaxSegmentsPerJob != test.want {
				t.Fatalf("transcription segment limit = %d, want %d", cfg.Transcription.MaxSegmentsPerJob, test.want)
			}
		})
	}
}

func TestLoadRejectsInvalidManifestCanvasLimit(t *testing.T) {
	t.Setenv("IIIF_MAX_MANIFEST_CANVASES", "5001")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "iiif.max_manifest_canvases") {
		t.Fatalf("Load error = %v, want manifest canvas limit validation", err)
	}
}

func TestNormalizeMaxManifestCanvasesIsBounded(t *testing.T) {
	tests := []struct {
		name      string
		value     int
		want      int
		wantError bool
	}{
		{name: "default", value: 0, want: DefaultMaxManifestCanvases},
		{name: "minimum", value: 1, want: 1},
		{name: "configured", value: 750, want: 750},
		{name: "hard maximum", value: maxConfiguredManifestCanvases, want: maxConfiguredManifestCanvases},
		{name: "negative", value: -1, wantError: true},
		{name: "above hard maximum", value: maxConfiguredManifestCanvases + 1, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeMaxManifestCanvases(test.value)
			if (err != nil) != test.wantError {
				t.Fatalf("normalizeMaxManifestCanvases(%d) error = %v, wantError %t", test.value, err, test.wantError)
			}
			if !test.wantError && got != test.want {
				t.Fatalf("normalizeMaxManifestCanvases(%d) = %d, want %d", test.value, got, test.want)
			}
		})
	}
}

func TestNormalizeMaxManifestImportBytesIsBounded(t *testing.T) {
	tests := []struct {
		name      string
		value     uint64
		want      uint64
		wantError bool
	}{
		{name: "default", value: 0, want: DefaultMaxManifestImportBytes},
		{name: "minimum", value: 1, want: 1},
		{name: "configured", value: 8 << 20, want: 8 << 20},
		{name: "hard maximum", value: maxConfiguredManifestImportBytes, want: maxConfiguredManifestImportBytes},
		{name: "above hard maximum", value: maxConfiguredManifestImportBytes + 1, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeMaxManifestImportBytes(test.value)
			if (err != nil) != test.wantError {
				t.Fatalf("normalizeMaxManifestImportBytes(%d) error = %v, wantError %t", test.value, err, test.wantError)
			}
			if !test.wantError && got != test.want {
				t.Fatalf("normalizeMaxManifestImportBytes(%d) = %d, want %d", test.value, got, test.want)
			}
		})
	}
}

func TestLoadRejectsInvalidPublicBaseURL(t *testing.T) {
	t.Setenv("PUBLIC_BASE_URL", "javascript:alert(1)")

	_, err := Load()
	if err == nil {
		t.Fatal("Load accepted a non-HTTP public base URL")
	}
	if !strings.Contains(err.Error(), "public_base_url") {
		t.Fatalf("Load error = %v, want public_base_url validation", err)
	}
}

func TestLoadRejectsPublicBaseURLThatOverflowsCanonicalResourceIDs(t *testing.T) {
	t.Setenv("PUBLIC_BASE_URL", "https://example.org/"+strings.Repeat("a", 512))

	_, err := Load()
	if err == nil {
		t.Fatal("Load accepted a public base URL that overflows persisted IIIF IDs")
	}
	if !strings.Contains(err.Error(), "public_base_url") || !strings.Contains(err.Error(), "512-byte") {
		t.Fatalf("Load error = %v, want canonical IIIF identity bound", err)
	}
}

func TestValidateServiceEndpointRequiresHTTPSForAuthenticatedService(t *testing.T) {
	err := validateServiceEndpoint(
		"segmentation_service",
		"http://segmentor.example/v1",
		"https://segmentor.example",
	)
	if err == nil || !strings.Contains(err.Error(), "must use HTTPS") {
		t.Fatalf("validateServiceEndpoint error = %v, want HTTPS rejection", err)
	}
}

func TestValidateServiceEndpointRequiresExactAudienceOrigin(t *testing.T) {
	err := validateServiceEndpoint(
		"segmentation_service",
		"https://attacker.example/v1",
		"https://segmentor.example",
	)
	if err == nil || !strings.Contains(err.Error(), "same exact origin") {
		t.Fatalf("validateServiceEndpoint error = %v, want origin mismatch", err)
	}
}

func TestValidateServiceEndpointAcceptsExactAuthenticatedOrigin(t *testing.T) {
	err := validateServiceEndpoint(
		"segmentation_service",
		"https://segmentor.example/v1",
		"https://segmentor.example",
	)
	if err != nil {
		t.Fatalf("validateServiceEndpoint returned error: %v", err)
	}
}

func TestValidateServiceEndpointRejectsNoncanonicalAudienceSlash(t *testing.T) {
	err := validateServiceEndpoint(
		"segmentation_service",
		"https://segmentor.example/v1",
		"https://segmentor.example/",
	)
	if err == nil || !strings.Contains(err.Error(), "canonical HTTPS origin") {
		t.Fatalf("validateServiceEndpoint error = %v, want trailing-slash rejection", err)
	}
}

func TestValidateServiceEndpointAllowsUnauthenticatedComposeHTTP(t *testing.T) {
	if err := validateServiceEndpoint("segmentation_service", "http://segmentor:8080", ""); err != nil {
		t.Fatalf("validateServiceEndpoint returned error: %v", err)
	}
}

func TestValidateTripletSourceRequiresExactAuthenticatedUploadCollection(t *testing.T) {
	valid := IIIFConfig{
		InternalBase:    "http://triplet:8080/iiif/3",
		SourceBase:      "http://api:8080/static/uploads",
		SourceReadToken: testTripletSourceReadToken,
	}
	if err := validateTripletSource(valid); err != nil {
		t.Fatalf("validateTripletSource(valid): %v", err)
	}
	for _, invalid := range []IIIFConfig{
		{InternalBase: valid.InternalBase, SourceBase: valid.SourceBase},
		{InternalBase: valid.InternalBase, SourceBase: "http://api:8080/static/uploads/nested", SourceReadToken: valid.SourceReadToken},
		{InternalBase: valid.InternalBase, SourceBase: "http://api:8080/static/uploads?tenant=1", SourceReadToken: valid.SourceReadToken},
		{InternalBase: valid.InternalBase, SourceBase: valid.SourceBase, SourceReadToken: "short"},
		{InternalBase: valid.InternalBase, SourceBase: valid.SourceBase, SourceReadToken: " " + valid.SourceReadToken + " "},
	} {
		if err := validateTripletSource(invalid); err == nil {
			t.Fatalf("validateTripletSource(%+v) succeeded", invalid)
		}
	}
}

func TestValidateTripletPresentationRequiresCompleteStrongBoundary(t *testing.T) {
	valid := AnnotationConfig{
		TripletPresentationBase:         "https://scribe.example/presentation/v3",
		TripletPresentationInternalBase: "http://triplet:8080/presentation/v3",
		TripletPresentationWriteToken:   testTripletPresentationWriteToken,
	}
	if err := validateTripletPresentation(valid); err != nil {
		t.Fatalf("validateTripletPresentation(valid): %v", err)
	}
	loopback := valid
	loopback.TripletPresentationBase = "http://localhost/presentation/v3"
	if err := validateTripletPresentation(loopback); err != nil {
		t.Fatalf("validateTripletPresentation(loopback): %v", err)
	}

	invalid := []AnnotationConfig{
		{},
		{TripletPresentationBase: valid.TripletPresentationBase},
		{TripletPresentationBase: valid.TripletPresentationBase, TripletPresentationInternalBase: valid.TripletPresentationInternalBase},
		{TripletPresentationBase: "http://scribe.example/presentation/v3", TripletPresentationInternalBase: valid.TripletPresentationInternalBase, TripletPresentationWriteToken: valid.TripletPresentationWriteToken},
		{TripletPresentationBase: "https://scribe.example/presentation/v2", TripletPresentationInternalBase: valid.TripletPresentationInternalBase, TripletPresentationWriteToken: valid.TripletPresentationWriteToken},
		{TripletPresentationBase: valid.TripletPresentationBase, TripletPresentationInternalBase: "http://triplet:8080/presentation/v3/nested", TripletPresentationWriteToken: valid.TripletPresentationWriteToken},
		{TripletPresentationBase: valid.TripletPresentationBase + "?token=secret", TripletPresentationInternalBase: valid.TripletPresentationInternalBase, TripletPresentationWriteToken: valid.TripletPresentationWriteToken},
		{TripletPresentationBase: valid.TripletPresentationBase, TripletPresentationInternalBase: valid.TripletPresentationInternalBase, TripletPresentationWriteToken: "short"},
		{TripletPresentationBase: valid.TripletPresentationBase, TripletPresentationInternalBase: valid.TripletPresentationInternalBase, TripletPresentationWriteToken: " " + valid.TripletPresentationWriteToken},
		{TripletPresentationBase: valid.TripletPresentationBase, TripletPresentationInternalBase: valid.TripletPresentationInternalBase, TripletPresentationWriteToken: strings.Repeat("x", maxTripletPresentationWriteTokenBytes+1)},
	}
	for index, candidate := range invalid {
		if err := validateTripletPresentation(candidate); err == nil {
			t.Fatalf("invalid Triplet Presentation boundary %d succeeded: %+v", index, candidate)
		}
	}
}

func TestLoadFailsClosedWithoutTripletPresentationBoundary(t *testing.T) {
	t.Setenv("TRIPLET_PRESENTATION_BASE", "")
	t.Setenv("TRIPLET_PRESENTATION_INTERNAL_BASE", "")
	t.Setenv("TRIPLET_PRESENTATION_WRITE_TOKEN", "")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "triplet_presentation") || !strings.Contains(err.Error(), "configured together") {
		t.Fatalf("Load error = %v, want required Triplet Presentation boundary", err)
	}
}

func TestLoadRejectsMismatchedModelEndpointAudience(t *testing.T) {
	t.Setenv("OLLAMA_MODEL_ENDPOINTS_JSON", `{
		"model-a": {
			"url": "https://attacker.example",
			"audience": "https://ollama.example"
		}
	}`)

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "same exact origin") {
		t.Fatalf("Load error = %v, want model endpoint origin mismatch", err)
	}
}

func TestValidateServiceEndpointsRejectsCaseInsensitiveModelKeyDuplicates(t *testing.T) {
	cfg := Config{}
	cfg.LLM.Ollama.ModelEndpoints = map[string]ModelEndpoint{
		"vision": {URL: "https://one.example"},
		"VISION": {URL: "https://two.example"},
	}

	err := validateServiceEndpoints(cfg)
	if err == nil || !strings.Contains(err.Error(), "case-insensitive duplicate model keys") {
		t.Fatalf("validateServiceEndpoints() error = %v, want duplicate rejection", err)
	}
}

func TestNormalizeExternalJWTIssuersRequiresSecureCompleteConfig(t *testing.T) {
	t.Parallel()

	base := ExternalJWTIssuerConfig{
		Issuer:        "https://identity.example/",
		Audience:      "scribe",
		WorkspaceID:   42,
		ServiceUserID: 900,
	}
	normalized, err := normalizeExternalJWTIssuers([]ExternalJWTIssuerConfig{base})
	if err != nil {
		t.Fatal(err)
	}
	if got := normalized[0].Issuer; got != "https://identity.example" {
		t.Fatalf("issuer = %q", got)
	}
	if got := normalized[0].JWKSURL; got != "https://identity.example/oauth/discovery/keys" {
		t.Fatalf("jwks_url = %q", got)
	}

	for name, mutate := range map[string]func(*ExternalJWTIssuerConfig){
		"plaintext remote issuer": func(cfg *ExternalJWTIssuerConfig) { cfg.Issuer = "http://identity.example" },
		"plaintext remote jwks":   func(cfg *ExternalJWTIssuerConfig) { cfg.JWKSURL = "http://identity.example/keys" },
		"missing audience":        func(cfg *ExternalJWTIssuerConfig) { cfg.Audience = "" },
		"missing workspace":       func(cfg *ExternalJWTIssuerConfig) { cfg.WorkspaceID = 0 },
		"missing service user":    func(cfg *ExternalJWTIssuerConfig) { cfg.ServiceUserID = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := base
			mutate(&cfg)
			if _, err := normalizeExternalJWTIssuers([]ExternalJWTIssuerConfig{cfg}); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}

	loopback := base
	loopback.Issuer = "http://127.0.0.1:8080"
	loopback.JWKSURL = "http://localhost:8080/keys"
	if _, err := normalizeExternalJWTIssuers([]ExternalJWTIssuerConfig{loopback}); err != nil {
		t.Fatalf("loopback development issuer rejected: %v", err)
	}
}
