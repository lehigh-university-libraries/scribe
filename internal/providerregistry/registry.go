// Package providerregistry is the authoritative runtime boundary for OCR
// transcription providers and segmentation engines. It owns construction,
// defaults, capabilities, limits, credential shape, and trusted endpoint
// policy; callers only select registered identifiers and models.
package providerregistry

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lehigh-university-libraries/htr/pkg/providers"
	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/segmentor"
	"github.com/lehigh-university-libraries/scribe/internal/worddetection"
)

const (
	defaultProviderID        = "ollama"
	defaultOllamaModel       = "glm-ocr:bf16"
	defaultOpenAIModel       = "gpt-4o"
	defaultGeminiModel       = "gemini-3.5-flash"
	defaultKrakenModel       = "catmus-print-fondue-large.mlmodel"
	defaultProviderTimeout   = 2 * time.Minute
	defaultResponseByteLimit = int64(8 << 20)
)

// Model describes one selectable model or segmentation selection.
type Model struct {
	ID        string
	Label     string
	IsDefault bool
}

// Execution identifies the runtime implementation used by a transcription
// provider. Provider identifiers are deliberately mapped to execution modes
// only here.
type Execution string

const (
	// ExecutionAdapter uses an HTR byte-oriented provider client.
	ExecutionAdapter Execution = "adapter"
	// ExecutionTesseract uses Scribe's local/remote Tesseract path.
	ExecutionTesseract Execution = "tesseract"
)

// EndpointMode states how a provider endpoint is controlled.
type EndpointMode string

const (
	// EndpointLocal performs no provider network request.
	EndpointLocal EndpointMode = "local"
	// EndpointExactOrigin uses an administrator-configured exact origin/audience.
	EndpointExactOrigin EndpointMode = "server_exact_origin"
	// EndpointVendor uses a fixed vendor endpoint owned by the adapter.
	EndpointVendor EndpointMode = "server_fixed_vendor"
)

// EndpointPolicy contains trusted runtime routing. It must never be copied to
// a public API response because URLs and audiences are administrator-owned.
type EndpointPolicy struct {
	Mode        EndpointMode
	ServerOwned bool
	URL         string
	Audience    string
}

// CredentialField describes one accepted secret field, never its value.
type CredentialField struct {
	ID       string
	Label    string
	Required bool
	Secret   bool
}

// CredentialSchema describes the Vault material a provider accepts.
type CredentialSchema struct {
	Fields []CredentialField
}

// Required reports whether this provider needs stored credential material.
func (s CredentialSchema) Required() bool {
	for _, field := range s.Fields {
		if field.Required {
			return true
		}
	}
	return false
}

// Capabilities describes context options implemented by a provider.
type Capabilities struct {
	SystemPrompt bool
	Temperature  bool
}

// RetryPolicy defines bounded provider retry behavior.
type RetryPolicy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

// Limits are the provider-side cost and response bounds enforced by Scribe.
type Limits struct {
	Timeout          time.Duration
	MaxResponseBytes int64
	TemperatureMin   float64
	TemperatureMax   float64
	Retry            RetryPolicy
}

// Provider is an immutable installed transcription provider descriptor.
type Provider struct {
	ID           string
	Label        string
	Models       []Model
	Execution    Execution
	Capabilities Capabilities
	Credentials  CredentialSchema
	Limits       Limits
	Endpoint     EndpointPolicy

	factory            func(Provider, string) (providers.Client, error)
	resolveEndpoint    func(string) EndpointPolicy
	credentialFallback func(string) string
}

// DefaultModel returns the configured default model, if any.
func (p Provider) DefaultModel() string {
	for _, model := range p.Models {
		if model.IsDefault {
			return model.ID
		}
	}
	return ""
}

// NewClient constructs the HTR client for one registered model. Native Scribe
// providers intentionally return nil and are selected through Execution.
func (p Provider) NewClient(model string) (providers.Client, error) {
	registeredModel, ok := resolveModel(p.Models, model)
	if !ok {
		return nil, fmt.Errorf("provider model is not registered")
	}
	if p.factory == nil {
		return nil, nil
	}
	return p.factory(p, registeredModel.ID)
}

// Credential resolves one provider credential from the request context, then
// falls back to administrator-owned startup secrets. Credential values are
// never stored in a descriptor or catalog projection.
func (p Provider) Credential(ctx context.Context, fieldID string) string {
	if value := ContextCredential(ctx, p.ID, fieldID); value != "" {
		return value
	}
	if administratorCredentialFallbackDisabled(ctx) || p.credentialFallback == nil {
		return ""
	}
	return strings.TrimSpace(p.credentialFallback(strings.TrimSpace(fieldID)))
}

// EndpointForModel resolves trusted per-model routing without accepting a URL
// or audience from request or workspace data.
func (p Provider) EndpointForModel(model string) EndpointPolicy {
	if p.resolveEndpoint != nil {
		return p.resolveEndpoint(strings.TrimSpace(model))
	}
	return p.Endpoint
}

// ProviderCatalogEntry is the safe UI projection of a provider. Runtime
// endpoint routing, factories, retry timings, and credential field names are
// intentionally absent.
type ProviderCatalogEntry struct {
	ID                   string
	Label                string
	Models               []Model
	RequiresAPIKey       bool
	SupportsSystemPrompt bool
	SupportsTemperature  bool
}

// Catalog is the complete safe model-selection projection for clients.
type Catalog struct {
	TranscriptionProviders []ProviderCatalogEntry
	SegmentationModels     []Model
}

// SegmentationCapabilities describes an installed segmentation engine.
type SegmentationCapabilities struct {
	AutomaticSelection bool
	OutputGranularity  string
	RemoteCapable      bool
}

// Segmentor is an immutable segmentation-engine descriptor.
type Segmentor struct {
	ID           string
	Label        string
	ResultID     string
	Capabilities SegmentationCapabilities
	Limits       Limits
	Endpoint     EndpointPolicy

	factory func(string) Detector
}

// Detector is the runtime segmentation factory product. It returns the
// effective detector identifier as well as word geometry.
type Detector interface {
	DetectWords(context.Context, string) ([]worddetection.WordBox, string, error)
}

// Registry is the immutable runtime catalog derived from trusted server
// configuration.
type Registry struct {
	providers             map[string]Provider
	providerOrder         []string
	defaultProvider       string
	segmentors            map[string]Segmentor
	segmentationModels    []Model
	defaultSegmentation   string
	segmentationEndpoints config.ServiceEndpointConfig
}

// New builds a normalized registry from trusted server configuration.
func New(cfg config.Config) Registry {
	// Provider and segmentor routing are startup-owned security policies.
	// Snapshot reference-backed configuration so later mutation of the caller's
	// config cannot redirect a live shared service or introduce a data race.
	ollamaConfig := cfg.LLM.Ollama
	ollamaConfig.Models = append([]string(nil), cfg.LLM.Ollama.Models...)
	ollamaConfig.ModelEndpoints = snapshotModelEndpoints(cfg.LLM.Ollama.ModelEndpoints)
	krakenConfig := cfg.LLM.Kraken
	krakenConfig.Models = append([]string(nil), cfg.LLM.Kraken.Models...)
	krakenConfig.ModelEndpoints = snapshotModelEndpoints(cfg.LLM.Kraken.ModelEndpoints)
	segmentationConfig := cfg.Segmentation
	segmentationConfig.Models = append([]string(nil), cfg.Segmentation.Models...)
	segmentationConfig.ModelEndpoints = snapshotModelEndpoints(cfg.Segmentation.ModelEndpoints)
	providerDescriptors := []Provider{
		newProvider("tesseract", "Tesseract", []string{"tesseract"}, "tesseract", ExecutionTesseract, Capabilities{}, CredentialSchema{}, localEndpoint(), nil, nil, nil),
		newProvider("ollama", "Ollama", ollamaConfig.Models, valueOr(ollamaConfig.Model, defaultOllamaModel), ExecutionAdapter, Capabilities{SystemPrompt: true, Temperature: true}, CredentialSchema{}, exactEndpoint(ollamaConfig.URL, ollamaConfig.Audience), newOllamaClient, func(model string) EndpointPolicy {
			url, audience := ollamaConfig.ResolveForModel(model)
			if strings.TrimSpace(url) == "" {
				url = ollamaConfig.URL
			}
			if strings.TrimSpace(audience) == "" {
				audience = ollamaConfig.Audience
			}
			return exactEndpoint(url, audience)
		}, nil),
		newProvider("openai", "OpenAI", cfg.LLM.OpenAI.Models, valueOr(cfg.LLM.OpenAI.Model, defaultOpenAIModel), ExecutionAdapter, Capabilities{SystemPrompt: true, Temperature: true}, apiKeySchema(), EndpointPolicy{Mode: EndpointVendor, ServerOwned: true, URL: "https://api.openai.com/v1/chat/completions"}, newOpenAIClient, nil, func(field string) string {
			if field == "api_key" {
				return config.Get().Secrets.OpenAIAPIKey
			}
			return ""
		}),
		newProvider("gemini", "Google Gemini", cfg.LLM.Gemini.Models, valueOr(cfg.LLM.Gemini.Model, defaultGeminiModel), ExecutionAdapter, Capabilities{SystemPrompt: true, Temperature: true}, apiKeySchema(), EndpointPolicy{Mode: EndpointVendor, ServerOwned: true, URL: "https://generativelanguage.googleapis.com/v1beta"}, newGeminiClient, nil, func(field string) string {
			if field == "api_key" {
				return config.Get().Secrets.GeminiAPIKey
			}
			return ""
		}),
		newProvider("kraken", "Kraken", krakenConfig.Models, valueOr(krakenConfig.Model, defaultKrakenModel), ExecutionAdapter, Capabilities{}, CredentialSchema{}, exactEndpoint(krakenConfig.URL, krakenConfig.Audience), newKrakenClient, func(model string) EndpointPolicy {
			url, audience := krakenConfig.ResolveForModel(model)
			if strings.TrimSpace(url) == "" {
				url, audience = segmentationConfig.ResolveForModel(model)
			}
			if strings.TrimSpace(url) == "" {
				url = krakenConfig.URL
			}
			if strings.TrimSpace(url) == "" {
				url = segmentationConfig.URL
			}
			if strings.TrimSpace(audience) == "" {
				audience = krakenConfig.Audience
			}
			if strings.TrimSpace(audience) == "" {
				audience = segmentationConfig.Audience
			}
			return exactEndpoint(url, audience)
		}, nil),
	}

	providerMap := make(map[string]Provider, len(providerDescriptors))
	providerOrder := make([]string, 0, len(providerDescriptors))
	for _, descriptor := range providerDescriptors {
		if descriptor.ID == defaultProviderID {
			descriptor.Limits.Retry = RetryPolicy{MaxAttempts: 6, BaseDelay: time.Second, MaxDelay: 30 * time.Second}
		}
		providerMap[descriptor.ID] = descriptor
		providerOrder = append(providerOrder, descriptor.ID)
	}
	defaultProvider := normalizeID(cfg.LLM.Provider)
	if _, ok := providerMap[defaultProvider]; !ok {
		defaultProvider = defaultProviderID
	}

	segmentorMap := map[string]Segmentor{
		"auto": newSegmentor("auto", "Automatic", "", true, func(string) Detector { return autoDetector{} }),
		"tesseract": newSegmentor("tesseract", "Tesseract", "tesseract", false, func(string) Detector {
			return localDetector{provider: worddetection.NewTesseract(), resultID: "tesseract"}
		}),
		"scribe": newSegmentor("scribe", "Scribe", "custom", false, func(string) Detector { return localDetector{provider: worddetection.NewCustom(), resultID: "custom"} }),
		"kraken": newSegmentor("kraken", "Kraken", "kraken", false, func(model string) Detector {
			return localDetector{provider: worddetection.NewKraken(model), resultID: "kraken"}
		}),
	}
	defaultSegmentation := strings.TrimSpace(cfg.LLM.SegmentationModel)
	if defaultSegmentation == "" {
		defaultSegmentation = "auto"
	}
	selectionIDs := append([]string{"auto", "tesseract", "scribe", "kraken"}, segmentationConfig.Models...)
	segmentationModels := models(selectionIDs, defaultSegmentation)

	return Registry{
		providers:             providerMap,
		providerOrder:         providerOrder,
		defaultProvider:       defaultProvider,
		segmentors:            segmentorMap,
		segmentationModels:    segmentationModels,
		defaultSegmentation:   defaultSegmentation,
		segmentationEndpoints: segmentationConfig,
	}
}

func newKrakenClient(descriptor Provider, model string) (providers.Client, error) {
	endpoint, err := registeredProviderEndpoint(descriptor, model, EndpointExactOrigin)
	if err != nil {
		return nil, err
	}
	client, err := segmentor.NewClientForEndpoint(endpoint.URL, endpoint.Audience)
	return bindRegisteredModel(client, model, err)
}

func newProvider(id, label string, modelIDs []string, defaultModel string, execution Execution, capabilities Capabilities, credentials CredentialSchema, endpoint EndpointPolicy, factory func(Provider, string) (providers.Client, error), resolver func(string) EndpointPolicy, credentialFallback func(string) string) Provider {
	return Provider{
		ID:                 id,
		Label:              label,
		Models:             models(modelIDs, defaultModel),
		Execution:          execution,
		Capabilities:       capabilities,
		Credentials:        credentials,
		Limits:             Limits{Timeout: defaultProviderTimeout, MaxResponseBytes: defaultResponseByteLimit, TemperatureMin: 0, TemperatureMax: 2, Retry: RetryPolicy{MaxAttempts: 1}},
		Endpoint:           endpoint,
		factory:            factory,
		resolveEndpoint:    resolver,
		credentialFallback: credentialFallback,
	}
}

func newSegmentor(id, label, resultID string, automatic bool, factory func(string) Detector) Segmentor {
	return Segmentor{
		ID:       id,
		Label:    label,
		ResultID: resultID,
		Capabilities: SegmentationCapabilities{
			AutomaticSelection: automatic,
			OutputGranularity:  "word",
			RemoteCapable:      true,
		},
		Limits:   Limits{Timeout: defaultProviderTimeout, MaxResponseBytes: 16 << 20, Retry: RetryPolicy{MaxAttempts: 1}},
		Endpoint: EndpointPolicy{Mode: EndpointExactOrigin, ServerOwned: true},
		factory:  factory,
	}
}

func apiKeySchema() CredentialSchema {
	return CredentialSchema{Fields: []CredentialField{{ID: "api_key", Label: "API key", Required: true, Secret: true}}}
}

func localEndpoint() EndpointPolicy {
	return EndpointPolicy{Mode: EndpointLocal, ServerOwned: true}
}

func exactEndpoint(url, audience string) EndpointPolicy {
	return EndpointPolicy{Mode: EndpointExactOrigin, ServerOwned: true, URL: strings.TrimSpace(url), Audience: strings.TrimSpace(audience)}
}

func valueOr(value, fallback string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return fallback
}

func normalizeID(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func snapshotModelEndpoints(values map[string]config.ModelEndpoint) map[string]config.ModelEndpoint {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	snapshot := make(map[string]config.ModelEndpoint, len(values))
	for _, key := range keys {
		normalizedKey := normalizeID(key)
		if normalizedKey == "" {
			continue
		}
		if _, exists := snapshot[normalizedKey]; exists {
			// Load rejects case-insensitive duplicates. Keeping the first sorted
			// key makes direct construction in tests deterministic as well.
			continue
		}
		snapshot[normalizedKey] = values[key]
	}
	return snapshot
}

func models(values []string, defaultValue string) []Model {
	defaultValue = strings.TrimSpace(defaultValue)
	seen := make(map[string]struct{}, len(values)+1)
	normalized := make([]string, 0, len(values)+1)
	for _, value := range append(append([]string(nil), values...), defaultValue) {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, value)
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		if strings.EqualFold(normalized[i], defaultValue) {
			return true
		}
		if strings.EqualFold(normalized[j], defaultValue) {
			return false
		}
		return strings.ToLower(normalized[i]) < strings.ToLower(normalized[j])
	})
	out := make([]Model, 0, len(normalized))
	for _, value := range normalized {
		out = append(out, Model{ID: value, Label: value, IsDefault: strings.EqualFold(value, defaultValue)})
	}
	return out
}

// Catalog returns a defensive, endpoint-free client projection.
func (r Registry) Catalog() Catalog {
	catalog := Catalog{
		TranscriptionProviders: make([]ProviderCatalogEntry, 0, len(r.providerOrder)),
		SegmentationModels:     cloneModels(r.segmentationModels),
	}
	for _, id := range r.providerOrder {
		descriptor := r.providers[id]
		catalog.TranscriptionProviders = append(catalog.TranscriptionProviders, ProviderCatalogEntry{
			ID:                   descriptor.ID,
			Label:                descriptor.Label,
			Models:               cloneModels(descriptor.Models),
			RequiresAPIKey:       descriptor.Credentials.Required(),
			SupportsSystemPrompt: descriptor.Capabilities.SystemPrompt,
			SupportsTemperature:  descriptor.Capabilities.Temperature,
		})
	}
	return catalog
}

// Provider resolves a provider identifier case-insensitively.
func (r Registry) Provider(id string) (Provider, bool) {
	descriptor, ok := r.providers[normalizeID(id)]
	if ok {
		descriptor.Models = cloneModels(descriptor.Models)
		descriptor.Credentials.Fields = append([]CredentialField(nil), descriptor.Credentials.Fields...)
	}
	return descriptor, ok
}

// ResolveProvider applies the configured default to an empty selection.
func (r Registry) ResolveProvider(id string) (Provider, error) {
	if id = normalizeID(id); id == "" {
		id = r.defaultProvider
	}
	descriptor, ok := r.Provider(id)
	if !ok {
		return Provider{}, fmt.Errorf("unsupported transcription provider %q", strings.TrimSpace(id))
	}
	return descriptor, nil
}

// DefaultProvider returns the normalized configured provider identifier.
func (r Registry) DefaultProvider() string {
	return r.defaultProvider
}

// DefaultSegmentation returns the configured segmentation selection.
func (r Registry) DefaultSegmentation() string {
	return r.defaultSegmentation
}

// EffectiveModel returns a request model or the provider's default model.
func (r Registry) EffectiveModel(providerID, modelID string) (string, error) {
	descriptor, err := r.ResolveProvider(providerID)
	if err != nil {
		return "", err
	}
	if modelID = strings.TrimSpace(modelID); modelID != "" {
		canonicalModel, ok := resolveModel(descriptor.Models, modelID)
		if !ok {
			return "", fmt.Errorf("model %q is not configured for provider %q", modelID, descriptor.ID)
		}
		return canonicalModel.ID, nil
	}
	return descriptor.DefaultModel(), nil
}

// ProviderConfig builds the trusted htr adapter configuration for a model.
func (r Registry) ProviderConfig(providerID, model, prompt string, temperature float64) (providers.Config, error) {
	descriptor, err := r.ResolveProvider(providerID)
	if err != nil {
		return providers.Config{}, err
	}
	model, err = r.EffectiveModel(descriptor.ID, model)
	if err != nil {
		return providers.Config{}, err
	}
	endpoint := descriptor.EndpointForModel(model)
	return providers.Config{
		Provider:    descriptor.ID,
		Model:       model,
		Prompt:      prompt,
		Temperature: temperature,
		Timeout:     descriptor.Limits.Timeout,
		BaseURL:     endpoint.URL,
		Audience:    endpoint.Audience,
	}, nil
}

// ValidateSelection verifies that a context only references server-approved
// providers, models, and capabilities.
func (r Registry) ValidateSelection(providerID, modelID, systemPrompt string, temperature *float64) error {
	descriptor, ok := r.Provider(providerID)
	if !ok {
		return fmt.Errorf("unsupported transcription provider %q", strings.TrimSpace(providerID))
	}
	if _, err := r.EffectiveModel(descriptor.ID, modelID); err != nil {
		return err
	}
	if strings.TrimSpace(systemPrompt) != "" && !descriptor.Capabilities.SystemPrompt {
		return fmt.Errorf("provider %q does not support a system prompt", descriptor.ID)
	}
	if temperature != nil {
		if !descriptor.Capabilities.Temperature {
			return fmt.Errorf("provider %q does not support temperature", descriptor.ID)
		}
		if *temperature < descriptor.Limits.TemperatureMin || *temperature > descriptor.Limits.TemperatureMax {
			return fmt.Errorf("temperature must be between %v and %v", descriptor.Limits.TemperatureMin, descriptor.Limits.TemperatureMax)
		}
	}
	return nil
}

// ValidateSegmentation verifies that a segmentation selection is installed.
func (r Registry) ValidateSegmentation(modelID string) error {
	if !containsModel(r.segmentationModels, modelID) {
		return fmt.Errorf("unsupported segmentation model %q", strings.TrimSpace(modelID))
	}
	return nil
}

// ResolveSegmentor maps one approved selection to its descriptor and optional
// Kraken model suffix.
func (r Registry) ResolveSegmentor(selection string) (Segmentor, string, string, error) {
	selection = strings.TrimSpace(selection)
	if selection == "" {
		selection = r.defaultSegmentation
	}
	if !containsModel(r.segmentationModels, selection) {
		return Segmentor{}, "", "", fmt.Errorf("unsupported segmentation model %q", selection)
	}
	normalized := normalizeID(selection)
	kind := normalized
	model := ""
	if strings.HasPrefix(normalized, "kraken:") {
		kind = "kraken"
		model = strings.TrimSpace(selection[len("kraken:"):])
	}
	descriptor, ok := r.segmentors[kind]
	if !ok {
		// Administrator-registered model routes use the remote segmentor and
		// retain automatic local detection as an availability fallback.
		descriptor = r.segmentors["auto"]
	}
	descriptor.Endpoint = r.segmentationEndpoint(selection)
	return descriptor, selection, model, nil
}

// NewSegmentor constructs a detector using trusted endpoint routing and the
// descriptor's local fallback.
func (r Registry) NewSegmentor(selection string) (Detector, error) {
	descriptor, resolvedSelection, model, err := r.ResolveSegmentor(selection)
	if err != nil {
		return nil, err
	}
	local := descriptor.factory(model)
	if endpoint := descriptor.Endpoint; strings.TrimSpace(endpoint.URL) != "" {
		remote, err := segmentor.NewClientForEndpoint(endpoint.URL, endpoint.Audience)
		if err != nil {
			return nil, err
		}
		return remoteDetector{remote: remote, selection: resolvedSelection}, nil
	}
	return local, nil
}

func (r Registry) segmentationEndpoint(selection string) EndpointPolicy {
	url, audience := r.segmentationEndpoints.ResolveForModel(selection)
	if strings.TrimSpace(url) == "" {
		url = r.segmentationEndpoints.URL
	}
	if strings.TrimSpace(audience) == "" {
		audience = r.segmentationEndpoints.Audience
	}
	return exactEndpoint(url, audience)
}

func cloneModels(values []Model) []Model {
	return append([]Model(nil), values...)
}

func containsModel(values []Model, id string) bool {
	_, ok := resolveModel(values, id)
	return ok
}

func resolveModel(values []Model, id string) (Model, bool) {
	for _, model := range values {
		if strings.EqualFold(strings.TrimSpace(model.ID), strings.TrimSpace(id)) {
			return model, true
		}
	}
	return Model{}, false
}

type localDetector struct {
	provider worddetection.Provider
	resultID string
}

func (d localDetector) DetectWords(ctx context.Context, imagePath string) ([]worddetection.WordBox, string, error) {
	words, err := d.provider.DetectWords(ctx, imagePath)
	return words, d.resultID, err
}

type autoDetector struct{}

func (autoDetector) DetectWords(ctx context.Context, imagePath string) ([]worddetection.WordBox, string, error) {
	tesseractWords, tesseractErr := worddetection.NewTesseract().DetectWords(ctx, imagePath)
	customWords, customErr := worddetection.NewCustom().DetectWords(ctx, imagePath)
	if tesseractErr != nil && customErr != nil {
		return nil, "", fmt.Errorf("both detection methods failed - tesseract: %v, custom: %v", tesseractErr, customErr)
	}
	if tesseractErr != nil {
		return customWords, "custom", nil
	}
	if customErr != nil || len(tesseractWords) >= len(customWords) {
		return tesseractWords, "tesseract", nil
	}
	return customWords, "custom", nil
}

type remoteDetector struct {
	remote    *segmentor.Client
	selection string
}

func (d remoteDetector) DetectWords(ctx context.Context, imagePath string) ([]worddetection.WordBox, string, error) {
	if d.remote != nil && d.remote.Enabled() {
		words, provider, err := d.remote.DetectWords(ctx, imagePath, d.selection)
		return words, normalizeDetectionProvider(provider), err
	}
	return nil, "", fmt.Errorf("segmentation endpoint is not configured")
}

func normalizeDetectionProvider(provider string) string {
	normalized := normalizeID(provider)
	switch {
	case normalized == "scribe" || normalized == "custom":
		return "custom"
	case normalized == "kraken" || strings.HasPrefix(normalized, "kraken:"):
		return "kraken"
	case normalized == "tesseract":
		return "tesseract"
	default:
		return normalized
	}
}
