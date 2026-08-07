package providerregistry

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/config"
)

func TestRegistryOwnsProviderDefaultsFactoriesAndCapabilities(t *testing.T) {
	cfg := config.Config{}
	cfg.LLM.Ollama.URL = "http://localhost:11434"
	registry := New(cfg)

	ollamaDescriptor, err := registry.ResolveProvider("")
	if err != nil {
		t.Fatalf("ResolveProvider() error = %v", err)
	}
	if ollamaDescriptor.ID != "ollama" || ollamaDescriptor.Label != "Ollama" {
		t.Fatalf("default provider = %#v", ollamaDescriptor)
	}
	if ollamaDescriptor.DefaultModel() != defaultOllamaModel {
		t.Fatalf("default model = %q", ollamaDescriptor.DefaultModel())
	}
	if client, clientErr := ollamaDescriptor.NewClient(ollamaDescriptor.DefaultModel()); clientErr != nil || client == nil || client.Name() != "ollama" {
		t.Fatalf("Ollama client = %#v, %v", client, clientErr)
	}
	if ollamaDescriptor.Limits.Timeout != 2*time.Minute || ollamaDescriptor.Limits.Retry.MaxAttempts != 6 {
		t.Fatalf("Ollama limits = %#v", ollamaDescriptor.Limits)
	}

	openAI, ok := registry.Provider("OPENAI")
	if !ok || !openAI.Credentials.Required() || !openAI.Capabilities.SystemPrompt || !openAI.Capabilities.Temperature {
		t.Fatalf("OpenAI descriptor = %#v", openAI)
	}
	if client, clientErr := openAI.NewClient(openAI.DefaultModel()); clientErr != nil || client == nil || client.Name() != "openai" {
		t.Fatalf("OpenAI client = %#v, %v", client, clientErr)
	}

	gemini, ok := registry.Provider("gemini")
	if !ok || gemini.DefaultModel() != defaultGeminiModel {
		t.Fatalf("Gemini descriptor = %#v", gemini)
	}
	for _, model := range gemini.Models {
		if model.ID == "gemini-2.0-flash" || model.ID == "gemini-1.5-pro" {
			t.Fatalf("Gemini descriptor advertises retired model %q", model.ID)
		}
	}

	kraken, ok := registry.Provider("kraken")
	if !ok || kraken.Execution != ExecutionAdapter {
		t.Fatalf("Kraken descriptor = %#v", kraken)
	}
}

func TestProviderCredentialPrefersRequestContextOverAdministratorFallback(t *testing.T) {
	config.Init(config.Runtime{Secrets: config.Secrets{OpenAIAPIKey: "administrator-key"}})
	t.Cleanup(func() { config.Init(config.Runtime{}) })
	descriptor, ok := New(config.Config{}).Provider("openai")
	if !ok {
		t.Fatal("OpenAI descriptor is not installed")
	}
	if got := descriptor.Credential(context.Background(), openAIAPIKeyField); got != "administrator-key" {
		t.Fatalf("fallback credential = %q", got)
	}
	ctx := WithCredential(context.Background(), "openai", openAIAPIKeyField, "workspace-key")
	if got := descriptor.Credential(ctx, openAIAPIKeyField); got != "workspace-key" {
		t.Fatalf("request credential = %q", got)
	}
}

func TestRequestCredentialsRemainOpaque(t *testing.T) {
	t.Parallel()

	const credential = " provider-key-with-significant-whitespace "
	ctx := WithCredential(context.Background(), "openai", openAIAPIKeyField, credential)
	if got := ContextCredential(ctx, "openai", openAIAPIKeyField); got != credential {
		t.Fatalf("ContextCredential() = %q, want exact opaque value", got)
	}
	values := ContextCredentialValues(ctx)
	if len(values) != 1 || values[0] != credential {
		t.Fatalf("ContextCredentialValues() = %#v, want exact opaque value", values)
	}
}

func TestWithoutCredentialsShadowsAmbientRequestSecrets(t *testing.T) {
	ambient := WithCredential(context.Background(), "openai", "api_key", "creator-key")
	ambient = WithCredential(ambient, "gemini", "api_key", "unrelated-key")
	stripped := WithoutCredentials(ambient)

	if got := ContextCredential(stripped, "openai", "api_key"); got != "" {
		t.Fatalf("stripped OpenAI credential = %q", got)
	}
	if got := ContextCredential(stripped, "gemini", "api_key"); got != "" {
		t.Fatalf("stripped Gemini credential = %q", got)
	}
	if got := ContextCredential(ambient, "openai", "api_key"); got != "creator-key" {
		t.Fatalf("ambient context was mutated: %q", got)
	}
}

func TestWithoutAdministratorCredentialFallbackRequiresExplicitCredential(t *testing.T) {
	config.Init(config.Runtime{Secrets: config.Secrets{OpenAIAPIKey: "administrator-key"}})
	t.Cleanup(func() { config.Init(config.Runtime{}) })
	descriptor, ok := New(config.Config{}).Provider("openai")
	if !ok {
		t.Fatal("OpenAI descriptor is not installed")
	}

	bounded := WithoutAdministratorCredentialFallback(context.Background())
	if got := descriptor.Credential(bounded, openAIAPIKeyField); got != "" {
		t.Fatalf("bounded credential fell back to administrator key %q", got)
	}
	bounded = WithCredential(bounded, "openai", openAIAPIKeyField, "workspace-key")
	if got := descriptor.Credential(bounded, openAIAPIKeyField); got != "workspace-key" {
		t.Fatalf("bounded explicit credential = %q", got)
	}
}

func TestValidateSelectionUsesDescriptorCapabilitiesAndModels(t *testing.T) {
	cfg := config.Config{}
	cfg.LLM.OpenAI.Model = "gpt-test"
	cfg.LLM.OpenAI.Models = []string{"gpt-test"}
	registry := New(cfg)
	temperature := 0.3
	if err := registry.ValidateSelection("openai", "gpt-test", "read this hand", &temperature); err != nil {
		t.Fatalf("ValidateSelection() error = %v", err)
	}
	if err := registry.ValidateSelection("openai", "unknown", "", nil); err == nil {
		t.Fatal("ValidateSelection() accepted an unconfigured model")
	}
	if err := registry.ValidateSelection("kraken", "", "custom prompt", nil); err == nil {
		t.Fatal("ValidateSelection() accepted an unsupported provider capability")
	}
}

func TestGemini3DoesNotAdvertiseOrAcceptDeprecatedTemperature(t *testing.T) {
	cfg := config.Config{}
	cfg.LLM.Gemini.Model = "gemini-3.5-flash"
	cfg.LLM.Gemini.Models = []string{"gemini-3.5-flash", "gemini-2.5-flash"}
	registry := New(cfg)
	temperature := 0.3

	if err := registry.ValidateSelection("gemini", "gemini-3.5-flash", "", &temperature); err == nil {
		t.Fatal("ValidateSelection() accepted temperature for Gemini 3.5")
	}
	if err := registry.ValidateSelection("gemini", "gemini-2.5-flash", "", &temperature); err != nil {
		t.Fatalf("ValidateSelection() rejected legacy Gemini temperature: %v", err)
	}
	normalized, err := registry.NormalizeExecutionSelection("gemini", "gemini-3.5-flash", "", &temperature)
	if err != nil || normalized != nil {
		t.Fatalf("NormalizeExecutionSelection(Gemini 3.5) = %v, %v; want nil temperature", normalized, err)
	}
	legacyNormalized, err := registry.NormalizeExecutionSelection("gemini", "gemini-2.5-flash", "", &temperature)
	if err != nil || legacyNormalized == nil || *legacyNormalized != temperature || legacyNormalized == &temperature {
		t.Fatalf("NormalizeExecutionSelection(Gemini 2.5) = %v, %v; want detached %v", legacyNormalized, err, temperature)
	}
	if _, err := registry.ProviderConfig("gemini", "gemini-3.5-flash", "", temperature); err == nil {
		t.Fatal("ProviderConfig(Gemini 3.5) accepted a non-normalized execution temperature")
	}
	providerConfig, err := registry.ProviderConfig("gemini", "gemini-3.5-flash", "", 0)
	if err != nil || providerConfig.Temperature != 0 {
		t.Fatalf("ProviderConfig(Gemini 3.5 default sampling) = %v, %v", providerConfig.Temperature, err)
	}
	invalidTemperature := 3.0
	if _, err := registry.NormalizeExecutionSelection("gemini", "gemini-3.5-flash", "", &invalidTemperature); err == nil {
		t.Fatal("NormalizeExecutionSelection() accepted an out-of-range legacy temperature")
	}
	notANumber := math.NaN()
	if _, err := registry.NormalizeExecutionSelection("gemini", "gemini-3.5-flash", "", &notANumber); err == nil {
		t.Fatal("NormalizeExecutionSelection() accepted a non-finite legacy temperature")
	}
	if _, err := registry.NormalizeExecutionSelection("gemini", "unknown", "", &temperature); err == nil {
		t.Fatal("NormalizeExecutionSelection() accepted an unregistered legacy model")
	}

	for _, provider := range registry.Catalog().TranscriptionProviders {
		if provider.ID != "gemini" {
			continue
		}
		for _, model := range provider.Models {
			switch model.ID {
			case "gemini-3.5-flash":
				if model.SupportsTemperature {
					t.Fatal("Gemini 3.5 catalog model advertised deprecated temperature support")
				}
			case "gemini-2.5-flash":
				if !model.SupportsTemperature {
					t.Fatal("Gemini 2.5 catalog model omitted temperature support")
				}
			}
		}
	}
}

func TestEffectiveModelReturnsRegisteredCanonicalIDForMixedCaseSelections(t *testing.T) {
	cfg := config.Config{}
	cfg.LLM.Ollama.Model = "VISION"
	cfg.LLM.Ollama.Models = []string{"Vision", "Text"}
	cfg.LLM.Ollama.URL = "https://default.example"
	cfg.LLM.Ollama.ModelEndpoints = map[string]config.ModelEndpoint{
		"VISION": {URL: "https://vision.example", Audience: "https://vision.example"},
	}
	registry := New(cfg)

	model, err := registry.EffectiveModel("OLLAMA", "vIsIoN")
	if err != nil {
		t.Fatalf("EffectiveModel() error = %v", err)
	}
	if model != "Vision" {
		t.Fatalf("EffectiveModel() = %q, want registered ID %q", model, "Vision")
	}
	defaultModel, err := registry.EffectiveModel("ollama", "")
	if err != nil {
		t.Fatalf("EffectiveModel(default) error = %v", err)
	}
	if defaultModel != "Vision" {
		t.Fatalf("EffectiveModel(default) = %q, want registered ID %q", defaultModel, "Vision")
	}

	providerConfig, err := registry.ProviderConfig("oLlAmA", "vision", "", 0)
	if err != nil {
		t.Fatalf("ProviderConfig() error = %v", err)
	}
	if providerConfig.Model != "Vision" {
		t.Fatalf("ProviderConfig().Model = %q, want registered ID %q", providerConfig.Model, "Vision")
	}
	if providerConfig.BaseURL != "https://vision.example" || providerConfig.Audience != "https://vision.example" {
		t.Fatalf("ProviderConfig() routing = %#v", providerConfig)
	}
	descriptor, ok := registry.Provider("ollama")
	if !ok {
		t.Fatal("Ollama descriptor is not installed")
	}
	client, err := descriptor.NewClient(" vIsIoN ")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	bound, ok := client.(registeredModelClient)
	if !ok || bound.model != "Vision" {
		t.Fatalf("NewClient() model binding = %#v, want canonical %q", client, "Vision")
	}
}

func TestCatalogProjectionDoesNotExposeTrustedRouting(t *testing.T) {
	cfg := config.Config{}
	cfg.LLM.Ollama.URL = "https://private.example"
	cfg.LLM.Ollama.Audience = "https://private.example"
	cfg.LLM.Ollama.Model = "vision"
	cfg.LLM.Ollama.Models = []string{"vision"}
	registry := New(cfg)

	runtimeDescriptor, ok := registry.Provider("ollama")
	if !ok || runtimeDescriptor.Endpoint.URL != "https://private.example" {
		t.Fatalf("runtime descriptor = %#v", runtimeDescriptor)
	}
	encoded, err := json.Marshal(registry.Catalog())
	if err != nil {
		t.Fatalf("Marshal(Catalog()) error = %v", err)
	}
	if strings.Contains(string(encoded), "private.example") {
		t.Fatalf("public catalog leaked trusted endpoint: %s", encoded)
	}
	if strings.Contains(string(encoded), "api_key") {
		t.Fatalf("public catalog leaked credential field names: %s", encoded)
	}

	catalog := registry.Catalog()
	catalog.TranscriptionProviders[0].Models[0].ID = "mutated"
	catalog.SegmentationModels[0].ID = "mutated"
	fresh := registry.Catalog()
	if fresh.TranscriptionProviders[0].Models[0].ID == "mutated" || fresh.SegmentationModels[0].ID == "mutated" {
		t.Fatal("Catalog() returned mutable registry storage")
	}
}

func TestProviderConfigUsesExactServerOwnedModelRoute(t *testing.T) {
	cfg := config.Config{}
	cfg.LLM.Ollama.Model = "vision"
	cfg.LLM.Ollama.Models = []string{"vision"}
	cfg.LLM.Ollama.URL = "https://default.example"
	cfg.LLM.Ollama.Audience = "https://default.example"
	cfg.LLM.Ollama.ModelEndpoints = map[string]config.ModelEndpoint{
		"vision": {URL: "https://vision.example", Audience: "https://vision.example"},
	}
	registry := New(cfg)
	providerConfig, err := registry.ProviderConfig("ollama", "vision", "prompt", 0.2)
	if err != nil {
		t.Fatalf("ProviderConfig() error = %v", err)
	}
	if providerConfig.BaseURL != "https://vision.example" || providerConfig.Audience != "https://vision.example" {
		t.Fatalf("ProviderConfig() routing = %#v", providerConfig)
	}
	if providerConfig.Timeout != 2*time.Minute {
		t.Fatalf("ProviderConfig() timeout = %v", providerConfig.Timeout)
	}
}

func TestKrakenRegistrySnapshotsRegisteredRouting(t *testing.T) {
	cfg := config.Config{}
	cfg.LLM.Kraken.URL = "https://kraken-default.example"
	cfg.LLM.Kraken.Audience = "https://kraken-default.example"
	cfg.LLM.Kraken.Model = "transcription-v1"
	cfg.LLM.Kraken.Models = []string{"transcription-v1", "shared-v1"}
	cfg.LLM.Kraken.ModelEndpoints = map[string]config.ModelEndpoint{
		"transcription-v1": {URL: "https://kraken-model.example", Audience: "https://kraken-model.example"},
	}
	cfg.Segmentation.ModelEndpoints = map[string]config.ModelEndpoint{
		"shared-v1": {URL: "https://shared-model.example", Audience: "https://shared-model.example"},
	}
	registry := New(cfg)

	cfg.LLM.Kraken.Models[0] = "mutated-transcription"
	cfg.LLM.Kraken.ModelEndpoints["transcription-v1"] = config.ModelEndpoint{URL: "https://mutated-kraken.example"}
	cfg.Segmentation.ModelEndpoints["shared-v1"] = config.ModelEndpoint{URL: "https://mutated-shared.example"}

	tests := []struct {
		model        string
		wantURL      string
		wantAudience string
	}{
		{
			model:        "transcription-v1",
			wantURL:      "https://kraken-model.example",
			wantAudience: "https://kraken-model.example",
		},
		{
			model:        "shared-v1",
			wantURL:      "https://shared-model.example",
			wantAudience: "https://shared-model.example",
		},
	}
	for _, test := range tests {
		t.Run(test.model, func(t *testing.T) {
			runtimeConfig, err := registry.ProviderConfig("kraken", test.model, "", 0)
			if err != nil {
				t.Fatalf("ProviderConfig: %v", err)
			}
			if runtimeConfig.BaseURL != test.wantURL || runtimeConfig.Audience != test.wantAudience {
				t.Fatalf("registered route changed after source mutation: %#v", runtimeConfig)
			}
		})
	}
}

func TestRemoteSegmentationRegistrySnapshotsRegisteredRouting(t *testing.T) {
	cfg := config.Config{}
	cfg.Segmentation.Models = []string{"layout-v1"}
	cfg.Segmentation.ModelEndpoints = map[string]config.ModelEndpoint{
		"layout-v1": {URL: "https://segmentor.example", Audience: "https://segmentor.example"},
	}
	registry := New(cfg)

	cfg.Segmentation.Models[0] = "mutated-layout"
	cfg.Segmentation.ModelEndpoints["layout-v1"] = config.ModelEndpoint{URL: "https://mutated-segmentor.example"}
	descriptor, selection, _, err := registry.ResolveSegmentor("layout-v1")
	if err != nil {
		t.Fatalf("ResolveSegmentor: %v", err)
	}
	if selection != "layout-v1" {
		t.Fatalf("selection = %q", selection)
	}
	if descriptor.Endpoint.URL != "https://segmentor.example" || descriptor.Endpoint.Audience != "https://segmentor.example" {
		t.Fatalf("registered route changed after source mutation: %#v", descriptor.Endpoint)
	}
}

func TestRegistryRoutingSnapshotsAreSafeDuringConcurrentSourceMutation(t *testing.T) {
	cfg := config.Config{}
	cfg.LLM.Kraken.Model = "transcription-v1"
	cfg.LLM.Kraken.Models = []string{"transcription-v1"}
	cfg.LLM.Kraken.ModelEndpoints = map[string]config.ModelEndpoint{
		"transcription-v1": {URL: "https://kraken-model.example", Audience: "https://kraken-model.example"},
	}
	cfg.Segmentation.Models = []string{"layout-v1"}
	cfg.Segmentation.ModelEndpoints = map[string]config.ModelEndpoint{
		"layout-v1": {URL: "https://segmentor.example", Audience: "https://segmentor.example"},
	}
	registry := New(cfg)

	const iterations = 2_000
	start := make(chan struct{})
	failures := make(chan error, 1)
	report := func(err error) {
		select {
		case failures <- err:
		default:
		}
	}
	var workers sync.WaitGroup
	workers.Add(3)
	go func() {
		defer workers.Done()
		<-start
		for i := 0; i < iterations; i++ {
			cfg.LLM.Kraken.ModelEndpoints["transcription-v1"] = config.ModelEndpoint{URL: fmt.Sprintf("https://mutated-kraken-%d.example", i)}
			cfg.Segmentation.ModelEndpoints["layout-v1"] = config.ModelEndpoint{URL: fmt.Sprintf("https://mutated-segmentor-%d.example", i)}
		}
	}()
	go func() {
		defer workers.Done()
		<-start
		for i := 0; i < iterations; i++ {
			runtimeConfig, err := registry.ProviderConfig("kraken", "transcription-v1", "", 0)
			if err != nil {
				report(fmt.Errorf("provider config: %w", err))
				return
			}
			if runtimeConfig.BaseURL != "https://kraken-model.example" || runtimeConfig.Audience != "https://kraken-model.example" {
				report(fmt.Errorf("kraken route changed to %#v", runtimeConfig))
				return
			}
		}
	}()
	go func() {
		defer workers.Done()
		<-start
		for i := 0; i < iterations; i++ {
			descriptor, _, _, err := registry.ResolveSegmentor("layout-v1")
			if err != nil {
				report(fmt.Errorf("resolve segmentor: %w", err))
				return
			}
			if descriptor.Endpoint.URL != "https://segmentor.example" || descriptor.Endpoint.Audience != "https://segmentor.example" {
				report(fmt.Errorf("segmentation route changed to %#v", descriptor.Endpoint))
				return
			}
		}
	}()
	close(start)
	workers.Wait()
	close(failures)
	if err := <-failures; err != nil {
		t.Fatal(err)
	}
}

func TestResolveSegmentorPreservesApprovedKrakenModelAndRejectsUnknown(t *testing.T) {
	cfg := config.Config{}
	cfg.LLM.SegmentationModel = "kraken:Models/Latin.mlmodel"
	cfg.Segmentation.Models = []string{"kraken:Models/Latin.mlmodel"}
	registry := New(cfg)

	descriptor, selection, model, err := registry.ResolveSegmentor("")
	if err != nil {
		t.Fatalf("ResolveSegmentor() error = %v", err)
	}
	if descriptor.ID != "kraken" || selection != "kraken:Models/Latin.mlmodel" || model != "Models/Latin.mlmodel" {
		t.Fatalf("ResolveSegmentor() = (%#v, %q, %q)", descriptor, selection, model)
	}
	if _, _, _, err := registry.ResolveSegmentor("not-installed"); err == nil {
		t.Fatal("ResolveSegmentor() silently accepted an unknown selection")
	}
}

func TestResolveSegmentorReturnsCanonicalRegisteredRouteID(t *testing.T) {
	cfg := config.Config{}
	cfg.Segmentation.Models = []string{"Layout-Lines-V2"}
	registry := New(cfg)

	descriptor, selection, model, err := registry.ResolveSegmentor("layout-lines-v2")
	if err != nil {
		t.Fatalf("ResolveSegmentor() error = %v", err)
	}
	if descriptor.ID != "auto" || selection != "Layout-Lines-V2" || model != "" {
		t.Fatalf("ResolveSegmentor() = (%#v, %q, %q)", descriptor, selection, model)
	}
}
