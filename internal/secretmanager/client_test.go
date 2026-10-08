package secretmanager

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/api/option"
)

func TestCredentialLifecycleAndDeploymentIsolation(t *testing.T) {
	var stored string
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if !strings.HasPrefix(r.URL.Path, "/v1/projects/test-project/") {
			t.Errorf("unexpected endpoint %s", r.URL.Path)
		}
		switch {
		case strings.HasSuffix(r.URL.Path, ":access"):
			if stored == "" {
				w.WriteHeader(404)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"payload": map[string]string{"data": stored}})
		case strings.HasSuffix(r.URL.Path, ":addVersion"):
			var request struct {
				Payload struct {
					Data string `json:"data"`
				} `json:"payload"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			stored = request.Payload.Data
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodDelete:
			if stored == "" {
				w.WriteHeader(404)
				return
			}
			stored = ""
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusConflict)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	ctx := context.Background()
	client, err := New(ctx, "test-project", "scribe-dev-secret", option.WithEndpoint(server.URL+"/"), option.WithHTTPClient(server.Client()), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	path := "scribe/dev/provider-secrets/workspaces/7/openai/credential"
	name, err := client.Name(path)
	if err != nil || !strings.Contains(name, "/scribe-dev-secret-provider-") {
		t.Fatalf("name = %s, err = %v", name, err)
	}
	other := *client
	other.prefix = "scribe-secret"
	otherName, _ := other.Name(path)
	if otherName == name {
		t.Fatal("deployments share a secret name")
	}
	if _, err := client.Read(ctx, path); !IsNotFound(err) {
		t.Fatalf("missing = %v", err)
	}
	if err := client.Write(ctx, path, map[string]string{"api_key": "opaque\ncredential"}); err != nil {
		t.Fatal(err)
	}
	values, err := client.Read(ctx, path)
	if err != nil || values["api_key"] != "opaque\ncredential" {
		t.Fatalf("read failed: %v", err)
	}
	decoded, _ := base64.StdEncoding.DecodeString(stored)
	if !json.Valid(decoded) {
		t.Fatal("credential map was not encoded as JSON")
	}
	if err := client.Delete(ctx, path); err != nil {
		t.Fatal(err)
	}
	if err := client.Delete(ctx, path); err != nil {
		t.Fatal("delete must be idempotent")
	}
	before := calls
	for _, unsafe := range []string{"", "../outside", "scribe//secret", " secret", "scribe/secret\n"} {
		if _, err := client.Read(ctx, unsafe); err == nil {
			t.Fatalf("accepted unsafe locator %q", unsafe)
		}
	}
	if calls != before {
		t.Fatal("unsafe locators reached Google API")
	}
}
