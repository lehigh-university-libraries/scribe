package providerregistry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lehigh-university-libraries/htr/pkg/providers"
	"github.com/lehigh-university-libraries/scribe/internal/config"
)

func TestOllamaClientUsesOnlyRegisteredModelRoute(t *testing.T) {
	var escaped atomic.Int32
	escape := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { escaped.Add(1) }))
	defer escape.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/trusted/api/generate" {
			t.Errorf("path = %q", request.URL.Path)
		}
		var payload struct {
			Model  string   `json:"model"`
			Prompt string   `json:"prompt"`
			Images []string `json:"images"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload.Model != "vision" || payload.Prompt != "transcribe" || len(payload.Images) != 1 {
			t.Errorf("payload = %#v", payload)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "vision-v2", "response": "café 世界"})
	}))
	defer server.Close()

	descriptor := testOllamaDescriptor(server.URL + "/trusted")
	descriptor.resolveEndpoint = func(model string) EndpointPolicy {
		if model == "vision" {
			return exactEndpoint(server.URL+"/trusted", "")
		}
		return exactEndpoint(escape.URL, "")
	}
	client, err := descriptor.NewClient("vision")
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Extract(context.Background(), providers.Request{
		Model: "vision", Prompt: "transcribe", Image: providers.Image{Data: []byte("image"), MediaType: "image/png"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "café 世界" || result.EffectiveModel != "vision-v2" {
		t.Fatalf("result = %#v", result)
	}
	if _, err := client.Extract(context.Background(), providers.Request{
		Model: "other", Prompt: "transcribe", Image: providers.Image{Data: []byte("image"), MediaType: "image/png"},
	}); err == nil {
		t.Fatal("model-bound client accepted a different model")
	}
	if _, err := descriptor.NewClient("unregistered"); err == nil {
		t.Fatal("unregistered model was accepted")
	}
	if escaped.Load() != 0 {
		t.Fatal("unregistered route received a request")
	}
}

func TestOllamaRegistrySnapshotsRegisteredRouting(t *testing.T) {
	cfg := config.Config{}
	cfg.LLM.Ollama.URL = "https://default.example"
	cfg.LLM.Ollama.Model = "vision"
	cfg.LLM.Ollama.Models = []string{"vision"}
	cfg.LLM.Ollama.ModelEndpoints = map[string]config.ModelEndpoint{"vision": {URL: "https://registered.example"}}
	registry := New(cfg)
	cfg.LLM.Ollama.ModelEndpoints["vision"] = config.ModelEndpoint{URL: "https://mutated.example"}
	runtimeConfig, err := registry.ProviderConfig("ollama", "vision", "prompt", 0)
	if err != nil {
		t.Fatal(err)
	}
	if runtimeConfig.BaseURL != "https://registered.example" {
		t.Fatalf("route changed after source mutation: %q", runtimeConfig.BaseURL)
	}
}

func TestOllamaAudienceMustMatchRegisteredEndpointOrigin(t *testing.T) {
	if _, err := validateOllamaAudience("https://service.example/path", "https://other.example"); err == nil {
		t.Fatal("off-origin audience was accepted")
	}
	if audience, err := validateOllamaAudience("https://service.example/path", "https://service.example"); err != nil || audience != "https://service.example" {
		t.Fatalf("audience = %q, %v", audience, err)
	}
	if _, err := validateOllamaAudience("http://service.example", "http://service.example"); err == nil || !strings.Contains(err.Error(), "provider request failed") {
		t.Fatalf("plaintext audience error = %v", err)
	}
	if _, err := validateOllamaAudience("https://service.example/path", "https://service.example/"); err == nil {
		t.Fatal("noncanonical trailing-slash audience was accepted")
	}
}

func testOllamaDescriptor(endpoint string) Provider {
	descriptor := newProvider(
		"ollama", "Ollama", []string{"vision"}, "vision", ExecutionAdapter,
		Capabilities{SystemPrompt: true, Temperature: true}, CredentialSchema{}, exactEndpoint(endpoint, ""),
		newOllamaClient, nil, nil,
	)
	descriptor.Limits.Timeout = 10 * time.Second
	descriptor.Limits.MaxResponseBytes = 8 << 10
	return descriptor
}
