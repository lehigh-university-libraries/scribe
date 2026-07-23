package providerregistry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lehigh-university-libraries/htr/pkg/providers"
	"github.com/lehigh-university-libraries/scribe/internal/config"
)

func TestOpenAIClientKeepsWorkspaceCredentialsIsolated(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		credential := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
		if credential != "workspace-a-key" && credential != "workspace-b-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mu.Lock()
		seen[credential]++
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":   "gpt-test",
			"choices": []any{map[string]any{"message": map[string]any{"content": credential}}},
		})
	}))
	defer server.Close()

	descriptor := testOpenAIDescriptor(server.URL + "/v1/chat/completions")
	client, err := descriptor.NewClient("gpt-test")
	if err != nil {
		t.Fatal(err)
	}
	request := providers.Request{Model: "gpt-test", Prompt: "transcribe", Image: providers.Image{Data: []byte("image"), MediaType: "image/png"}}
	base := context.Background()
	contexts := []struct {
		ctx  context.Context
		want string
	}{
		{WithCredential(base, "openai", providerAPIKeyField, "workspace-a-key"), "workspace-a-key"},
		{WithCredential(base, "openai", providerAPIKeyField, "workspace-b-key"), "workspace-b-key"},
	}

	const calls = 20
	errors := make(chan error, calls*len(contexts))
	var wait sync.WaitGroup
	for range calls {
		for _, invocation := range contexts {
			wait.Add(1)
			go func() {
				defer wait.Done()
				result, callErr := client.Extract(invocation.ctx, request)
				if callErr != nil {
					errors <- callErr
					return
				}
				if result.Text != invocation.want {
					errors <- fmt.Errorf("text = %q, want %q", result.Text, invocation.want)
				}
			}()
		}
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if seen["workspace-a-key"] != calls || seen["workspace-b-key"] != calls {
		t.Fatalf("credential counts = %#v", seen)
	}
}

func TestOpenAIClientDoesNotUseProcessCredential(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "unsafe-process-key")
	config.Init(config.Runtime{})
	t.Cleanup(func() { config.Init(config.Runtime{}) })

	descriptor := testOpenAIDescriptor("https://api.openai.invalid/v1/chat/completions")
	client, err := descriptor.NewClient("gpt-test")
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Extract(context.Background(), providers.Request{
		Model: "gpt-test", Prompt: "prompt", Image: providers.Image{Data: []byte("image"), MediaType: "image/jpeg"},
	})
	var providerError *providers.Error
	if !errors.As(err, &providerError) || providerError.Kind != providers.ErrorAuthentication {
		t.Fatalf("expected typed authentication failure, got %v (%T)", err, err)
	}
}

func testOpenAIDescriptor(endpoint string) Provider {
	descriptor := newProvider(
		"openai", "OpenAI", []string{"gpt-test"}, "gpt-test", ExecutionAdapter,
		Capabilities{SystemPrompt: true, Temperature: true}, apiKeySchema(),
		EndpointPolicy{Mode: EndpointVendor, ServerOwned: true, URL: endpoint}, newOpenAIClient, nil, nil,
	)
	descriptor.Limits.Timeout = 10 * time.Second
	descriptor.Limits.MaxResponseBytes = 8 << 10
	return descriptor
}
