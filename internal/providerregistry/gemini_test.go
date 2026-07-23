package providerregistry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lehigh-university-libraries/htr/pkg/providers"
)

func TestGeminiClientUsesHeaderCredentialAndRegisteredVendorBase(t *testing.T) {
	const credential = "workspace-gemini-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1beta/models/gemini-test:generateContent" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if request.URL.RawQuery != "" || request.Header.Get("x-goog-api-key") != credential {
			t.Errorf("credential placement: query=%q header=%q", request.URL.RawQuery, request.Header.Get("x-goog-api-key"))
		}
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"modelVersion": "gemini-resolved",
			"candidates": []any{map[string]any{
				"finishReason": "STOP",
				"content": map[string]any{"parts": []any{
					map[string]any{"text": "private reasoning", "thought": true},
					map[string]any{"text": "café 世界"},
				}},
			}},
		})
	}))
	defer server.Close()

	descriptor := newProvider(
		"gemini", "Gemini", []string{"gemini-test"}, "gemini-test", ExecutionAdapter,
		Capabilities{SystemPrompt: true, Temperature: true}, apiKeySchema(),
		EndpointPolicy{Mode: EndpointVendor, ServerOwned: true, URL: server.URL + "/v1beta"},
		newGeminiClient, nil, nil,
	)
	descriptor.Limits.Timeout = 10 * time.Second
	descriptor.Limits.MaxResponseBytes = 8 << 10
	client, err := descriptor.NewClient("gemini-test")
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithCredential(context.Background(), "gemini", providerAPIKeyField, credential)
	result, err := client.Extract(ctx, providers.Request{
		Model: "gemini-test", Prompt: "transcribe", Temperature: 0.2,
		Image: providers.Image{Data: []byte("image"), MediaType: "image/png"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(result.Text) != "café 世界" || result.EffectiveModel != "gemini-resolved" {
		t.Fatalf("result = %#v", result)
	}
}
