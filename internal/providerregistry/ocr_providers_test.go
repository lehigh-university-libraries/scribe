package providerregistry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lehigh-university-libraries/htr/pkg/providers"
	"github.com/lehigh-university-libraries/scribe/internal/config"
)

func TestConfiguredOCRProvidersUseApprovedModelsAndWorkspaceKeys(t *testing.T) {
	for _, id := range []string{"mistral-ocr", "openai-compatible"} {
		t.Run(id, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer workspace-key" {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if id == "mistral-ocr" {
					_, _ = w.Write([]byte(`{"model":"registered","pages":[{"markdown":"checked"}]}`))
				} else {
					_, _ = w.Write([]byte(`{"model":"registered","choices":[{"message":{"content":"checked"}}]}`))
				}
			}))
			defer server.Close()
			cfg := config.Config{}
			providerConfig := config.HTTPProviderConfig{URL: server.URL, Model: "registered", Models: []string{"registered"}}
			if id == "mistral-ocr" {
				cfg.LLM.Mistral = providerConfig
			} else {
				cfg.LLM.OpenAICompatible = providerConfig
			}
			registry := New(cfg)
			descriptor, ok := registry.Provider(id)
			if !ok || descriptor.Endpoint.URL != server.URL || descriptor.Endpoint.Mode != EndpointVendor || !descriptor.Credentials.Required() {
				t.Fatalf("descriptor %+v", descriptor)
			}
			if _, err := descriptor.NewClient("unregistered"); err == nil {
				t.Fatal("accepted unregistered model")
			}
			client, err := descriptor.NewClient("registered")
			if err != nil {
				t.Fatal(err)
			}
			request := providers.Request{Model: "registered", Prompt: "transcribe", Image: providers.Image{Data: []byte("bounded fixture"), MediaType: "image/png"}}
			if _, err := client.Extract(context.Background(), request); err == nil {
				t.Fatal("missing workspace key accepted")
			}
			result, err := client.Extract(WithCredential(context.Background(), id, "api_key", "workspace-key"), request)
			if err != nil || result.Text != "checked" {
				t.Fatalf("result %+v %v", result, err)
			}
		})
	}
}
