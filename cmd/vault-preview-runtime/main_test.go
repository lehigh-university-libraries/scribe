package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lehigh-university-libraries/scribe/internal/vaultpreviewruntime"
	"golang.org/x/oauth2"
)

const (
	commandTestProject       = "example-project"
	commandTestProjectNumber = "123456789012"
	commandTestRegion        = "us-east5"
	commandTestVaultURL      = "https://vault-server-dev-" + commandTestProjectNumber + ".us-east5.run.app"
	commandTestVaultToken    = "vault-token-canary"
	commandTestAdminToken    = "admin-token-canary"
	commandTestAccessor      = "auth_gcp_1234abcd"
	commandTestRoleID        = "12345678-1234-1234-1234-123456789abc"
)

func TestRunEmitsOnlyExactSuccessMarker(t *testing.T) {
	t.Setenv("VAULT_ADDR", "https://attacker.example")

	for _, mode := range []string{"check", "apply"} {
		t.Run(mode, func(t *testing.T) {
			var stdout bytes.Buffer
			err := run(context.Background(), []string{"-mode=" + mode}, &stdout, commandTestDependencies(
				exactCommandVaultHandler(t),
				map[string]string{},
			))
			if err != nil {
				t.Fatalf("run returned error: %v", err)
			}
			want := vaultpreviewruntime.SuccessMarker + "\n"
			if got := stdout.String(); got != want {
				t.Fatalf("stdout = %q, want %q", got, want)
			}
		})
	}
}

func TestRunUsesConfiguredRegion(t *testing.T) {
	const region = "us-central1"
	var stdout bytes.Buffer
	deps := commandTestDependencies(exactCommandVaultHandler(t), map[string]string{
		"SCRIBE_REGION": region,
		"VAULT_ADDR":    "https://vault-server-dev-123456789012." + region + ".run.app",
	})
	if err := run(context.Background(), nil, &stdout, deps); err != nil {
		t.Fatalf("run returned error: %v", err)
	}
}

func TestRunFailsClosedWithoutOutputOrSecretDisclosure(t *testing.T) {
	const dependencySecret = "dependency-secret-canary"
	tests := []struct {
		name   string
		args   []string
		env    map[string]string
		deps   func(dependencies) dependencies
		client http.Handler
	}{
		{
			name: "invalid mode",
			args: []string{"-mode=repair"},
		},
		{
			name: "missing Vault address",
			env:  map[string]string{"VAULT_ADDR": ""},
		},
		{
			name: "untrusted Vault address",
			env:  map[string]string{"VAULT_ADDR": "https://attacker.example"},
		},
		{
			name: "missing project number",
			env:  map[string]string{"GCLOUD_PROJECT_NUMBER": ""},
		},
		{
			name: "invalid project number",
			env:  map[string]string{"GCLOUD_PROJECT_NUMBER": "not-numeric"},
		},
		{
			name: "wrong project number",
			env:  map[string]string{"GCLOUD_PROJECT_NUMBER": "999999999999"},
		},
		{
			name: "token source failure",
			deps: func(deps dependencies) dependencies {
				deps.tokenSource = func(context.Context) (oauth2.TokenSource, error) {
					return nil, errors.New(dependencySecret)
				}
				return deps
			},
		},
		{
			name: "Vault response failure",
			client: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, dependencySecret+" "+commandTestVaultToken+" "+commandTestAdminToken)
			}),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := test.client
			if handler == nil {
				handler = exactCommandVaultHandler(t)
			}
			deps := commandTestDependencies(handler, test.env)
			if test.deps != nil {
				deps = test.deps(deps)
			}
			var stdout bytes.Buffer
			err := run(context.Background(), test.args, &stdout, deps)
			if err == nil {
				t.Fatal("run succeeded, want error")
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
			for _, secret := range []string{dependencySecret, commandTestVaultToken, commandTestAdminToken} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error exposed secret %q: %v", secret, err)
				}
			}
		})
	}
}

func TestADCTokenScopesAreExplicit(t *testing.T) {
	if googleCloudScope != "https://www.googleapis.com/auth/cloud-platform" {
		t.Fatalf("cloud scope = %q", googleCloudScope)
	}
	if googleEmailScope != "https://www.googleapis.com/auth/userinfo.email" {
		t.Fatalf("email scope = %q", googleEmailScope)
	}
}

func commandTestDependencies(handler http.Handler, overrides map[string]string) dependencies {
	env := map[string]string{
		"VAULT_ADDR":            commandTestVaultURL,
		"VAULT_TOKEN":           commandTestVaultToken,
		"GCLOUD_PROJECT":        commandTestProject,
		"GCLOUD_PROJECT_NUMBER": commandTestProjectNumber,
	}
	for key, value := range overrides {
		env[key] = value
	}
	return dependencies{
		getenv: func(key string) string {
			return env[key]
		},
		httpClient: &http.Client{Transport: commandRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			return recorder.Result(), nil
		})},
		tokenSource: func(context.Context) (oauth2.TokenSource, error) {
			return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: commandTestAdminToken}), nil
		},
	}
}

func exactCommandVaultHandler(t *testing.T) http.Handler {
	t.Helper()
	policy := "path \"secret/data/scribe/previews/{{identity.entity.aliases." +
		commandTestAccessor +
		".metadata.service_account_email}}/database/app\" {\n  capabilities = [\"read\"]\n}\n"

	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("X-Vault-Token"); got != commandTestVaultToken {
			t.Errorf("X-Vault-Token = %q", got)
		}
		if got := request.Header.Get("X-Admin-Token"); got != commandTestAdminToken {
			t.Errorf("X-Admin-Token = %q", got)
		}
		if request.Method != http.MethodGet {
			t.Errorf("unexpected mutation: %s %s", request.Method, request.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		switch request.URL.Path {
		case "/v1/sys/auth":
			writeCommandJSON(t, w, map[string]any{"data": map[string]any{
				"gcp/": map[string]any{"type": "gcp", "accessor": commandTestAccessor},
			}})
		case "/v1/auth/gcp/config":
			writeCommandMountedJSON(t, w, map[string]any{
				"disable_automated_rotation": false,
				"gce_metadata": []string{
					"instance_creation_timestamp",
					"instance_id",
					"instance_name",
					"project_id",
					"project_number",
					"role",
					"service_account_id",
					"service_account_email",
					"zone",
				},
				"iam_alias":               "unique_id",
				"iam_metadata":            []string{"service_account_email"},
				"identity_token_audience": "",
				"identity_token_ttl":      0,
				"rotation_period":         0,
				"rotation_schedule":       "",
				"rotation_window":         0,
			})
		case "/v1/sys/policies/acl/scribe-preview-app":
			writeCommandJSON(t, w, map[string]any{"data": map[string]any{
				"name":   "scribe-preview-app",
				"policy": policy,
			}})
		case "/v1/auth/gcp/role/scribe-preview-app":
			writeCommandMountedJSON(t, w, map[string]any{
				"role_id":                 commandTestRoleID,
				"type":                    "iam",
				"bound_service_accounts":  []string{"*"},
				"bound_projects":          []string{commandTestProject},
				"add_group_aliases":       false,
				"alias_metadata":          map[string]string{},
				"max_jwt_exp":             900,
				"allow_gce_inference":     false,
				"token_bound_cidrs":       []string{},
				"token_explicit_max_ttl":  0,
				"token_max_ttl":           900,
				"token_no_default_policy": true,
				"token_num_uses":          0,
				"token_period":            0,
				"token_policies":          []string{"scribe-preview-app"},
				"token_ttl":               300,
				"token_type":              "default",
			})
		default:
			t.Errorf("unexpected request: %s", request.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func writeCommandMountedJSON(t *testing.T, w http.ResponseWriter, data any) {
	t.Helper()
	writeCommandJSON(t, w, map[string]any{
		"data":       data,
		"mount_type": "gcp",
	})
}

func writeCommandJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

type commandRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn commandRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}
