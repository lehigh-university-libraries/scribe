package providerregistry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lehigh-university-libraries/htr/pkg/providers"
)

func TestGeminiClientUsesModelCompatibleRequestAndRegisteredVendorBase(t *testing.T) {
	const credential = "workspace-gemini-key"
	legacyTemperature := 0.2
	tests := []struct {
		name            string
		model           string
		wantTemperature *float64
	}{
		{name: "Gemini 3 uses model default", model: "gemini-3.5-flash"},
		{name: "legacy model keeps configured sampling", model: "gemini-2.5-flash", wantTemperature: &legacyTemperature},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/v1beta/models/"+test.model+":generateContent" {
					t.Errorf("path = %q", request.URL.Path)
				}
				if request.URL.RawQuery != "" || request.Header.Get("x-goog-api-key") != credential {
					t.Errorf("credential placement: query=%q header=%q", request.URL.RawQuery, request.Header.Get("x-goog-api-key"))
				}
				var payload map[string]any
				if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				assertGeminiImageRequest(t, payload)
				configuration, ok := payload["generationConfig"].(map[string]any)
				if !ok {
					t.Fatalf("generationConfig = %#v", payload["generationConfig"])
				}
				temperature, hasTemperature := configuration["temperature"].(float64)
				if test.wantTemperature == nil && hasTemperature {
					t.Errorf("deprecated Gemini 3 temperature was serialized: %v", temperature)
				}
				if test.wantTemperature != nil && (!hasTemperature || temperature != *test.wantTemperature) {
					t.Errorf("temperature = %v, present=%v, want %v", temperature, hasTemperature, *test.wantTemperature)
				}
				_ = json.NewEncoder(w).Encode(geminiResponse("STOP", "café 世界"))
			}))
			defer server.Close()

			descriptor := testGeminiDescriptor(test.model, server.URL+"/v1beta")
			client, err := descriptor.NewClient(test.model)
			if err != nil {
				t.Fatal(err)
			}
			ctx := WithCredential(context.Background(), "gemini", providerAPIKeyField, credential)
			result, err := client.Extract(ctx, providers.Request{
				Model: test.model, Prompt: "transcribe", Temperature: legacyTemperature,
				Image: providers.Image{Data: []byte("image"), MediaType: "image/png"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if strings.TrimSpace(result.Text) != "café 世界" || result.EffectiveModel != "gemini-resolved" {
				t.Fatalf("result = %#v", result)
			}
		})
	}
}

func TestGeminiClientFailsTruncatedLineWithoutRetry(t *testing.T) {
	var configurations []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		configuration, ok := payload["generationConfig"].(map[string]any)
		if !ok {
			t.Fatalf("generationConfig = %#v", payload["generationConfig"])
		}
		configurations = append(configurations, configuration)
		if len(configurations) == 1 {
			_ = json.NewEncoder(w).Encode(geminiResponse("MAX_TOKENS", "retry"))
			return
		}
		_ = json.NewEncoder(w).Encode(geminiResponse("STOP", "finished"))
	}))
	defer server.Close()

	const model = "gemini-3.5-flash"
	descriptor := testGeminiDescriptor(model, server.URL+"/v1beta")
	client, err := descriptor.NewClient(model)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithCredential(context.Background(), "gemini", providerAPIKeyField, "credential")
	result, err := client.Extract(ctx, providers.Request{
		Model: model, Prompt: "transcribe", Temperature: 0.8,
		Image: providers.Image{Data: []byte("image"), MediaType: "image/png"},
	})
	if err == nil || len(configurations) != 1 {
		t.Fatalf("result=%#v err=%v calls=%d", result, err, len(configurations))
	}
	if _, ok := configurations[0]["temperature"]; ok {
		t.Fatal("temperature retained")
	}
	if configurations[0]["maxOutputTokens"] != float64(1024) {
		t.Fatal("token limit missing")
	}
}

func testGeminiDescriptor(model, endpoint string) Provider {
	descriptor := newProvider(
		"gemini", "Gemini", []string{model}, model, ExecutionAdapter,
		Capabilities{SystemPrompt: true, Temperature: true}, apiKeySchema(),
		EndpointPolicy{Mode: EndpointVendor, ServerOwned: true, URL: endpoint},
		newGeminiClient, nil, nil,
	)
	descriptor.Limits.Timeout = 10 * time.Second
	descriptor.Limits.MaxResponseBytes = 8 << 10
	return descriptor
}

func geminiResponse(finishReason, text string) map[string]any {
	return map[string]any{
		"modelVersion": "gemini-resolved",
		"candidates": []any{map[string]any{
			"finishReason": finishReason,
			"content": map[string]any{"parts": []any{
				map[string]any{"text": "private reasoning", "thought": true},
				map[string]any{"text": text},
			}},
		}},
	}
}

func assertGeminiImageRequest(t *testing.T, payload map[string]any) {
	t.Helper()
	contents, ok := payload["contents"].([]any)
	if !ok || len(contents) != 1 {
		t.Fatalf("contents = %#v", payload["contents"])
	}
	content, ok := contents[0].(map[string]any)
	if !ok {
		t.Fatalf("content = %#v", contents[0])
	}
	parts, ok := content["parts"].([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("parts = %#v", content["parts"])
	}
	prompt, ok := parts[0].(map[string]any)
	if !ok || prompt["text"] != "transcribe" {
		t.Fatalf("prompt part = %#v", parts[0])
	}
	imagePart, ok := parts[1].(map[string]any)
	if !ok {
		t.Fatalf("image part = %#v", parts[1])
	}
	inlineData, ok := imagePart["inline_data"].(map[string]any)
	if !ok || inlineData["mime_type"] != "image/png" || inlineData["data"] != base64.StdEncoding.EncodeToString([]byte("image")) {
		t.Fatalf("inline image = %#v", imagePart["inline_data"])
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestGeminiThinkingPolicyIsServerOwned(t *testing.T) {
	model := "gemini-3.5-flash"
	descriptor := testGeminiDescriptor(model, "https://example.test/v1beta")
	descriptor.thinkingLevels = map[string]string{model: "minimal"}
	descriptor.outputTokenLimits = map[string]int{model: 2048}
	descriptor.vendorHTTPClient = &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		cfg := payload["generationConfig"].(map[string]any)
		if cfg["thinkingConfig"].(map[string]any)["thinkingLevel"] != "MINIMAL" || cfg["maxOutputTokens"] != float64(2048) {
			t.Fatalf("config=%#v", cfg)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"café"}]}}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":3,"thoughtsTokenCount":12}}`)), Header: make(http.Header)}, nil
	})}
	c, err := descriptor.NewClient(model)
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.Extract(WithCredential(context.Background(), "gemini", providerAPIKeyField, "key"), providers.Request{Model: model, Prompt: "transcribe", Image: providers.Image{Data: []byte("image"), MediaType: "image/png"}})
	if err != nil || result.Usage.ThoughtTokens != 12 {
		t.Fatalf("result=%#v error=%v", result, err)
	}
}
