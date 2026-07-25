package vaultpreviewruntime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"golang.org/x/oauth2"
)

const (
	testProject       = "example-project"
	testVaultToken    = "vault-token-secret"
	testAdminToken    = "admin-token-secret"
	testAccessor      = "auth_gcp_1234abcd"
	testRoleID        = "12345678-1234-1234-1234-123456789abc"
	testRegion        = "us-east5"
	testProjectNumber = "123456789012"
	testVaultURL      = "https://vault-server-dev-" + testProjectNumber + ".us-east5.run.app"
)

func TestReconcileModes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		mode          Mode
		configure     func(*vaultFixture)
		wantErr       bool
		wantMutations []string
	}{
		{
			name: "check exact is read only",
			mode: ModeCheck,
		},
		{
			name: "check drift fails read only",
			mode: ModeCheck,
			configure: func(f *vaultFixture) {
				f.policy = "drifted-policy"
				f.role.TokenPolicies = []string{"overprivileged"}
			},
			wantErr: true,
		},
		{
			name: "apply exact is idempotent",
			mode: ModeApply,
		},
		{
			name: "apply replaces drift and verifies readback",
			mode: ModeApply,
			configure: func(f *vaultFixture) {
				f.policy = "drifted-policy"
				f.role.AllowGCEInference = true
				f.role.TokenPolicies = []string{"overprivileged"}
			},
			wantMutations: []string{"policy:put", "role:put"},
		},
		{
			name: "apply creates missing resources",
			mode: ModeApply,
			configure: func(f *vaultFixture) {
				f.policyFound = false
				f.roleFound = false
			},
			wantMutations: []string{"policy:put", "role:put"},
		},
		{
			name: "apply recreates immutable wrong role type",
			mode: ModeApply,
			configure: func(f *vaultFixture) {
				f.gceRole = realisticGCERole()
			},
			wantMutations: []string{"role:delete", "role:put"},
		},
		{
			name: "apply fails closed on readback mismatch",
			mode: ModeApply,
			configure: func(f *vaultFixture) {
				f.policy = "drifted-policy"
				f.refusePolicyUpdate = true
			},
			wantErr:       true,
			wantMutations: []string{"policy:put"},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newVaultFixture()
			if test.configure != nil {
				test.configure(fixture)
			}
			server := httptest.NewTLSServer(fixture.handler(t))
			defer server.Close()

			reconciler := mustReconciler(t, server)
			err := reconciler.Reconcile(context.Background(), test.mode)
			if test.wantErr && err == nil {
				t.Fatal("Reconcile succeeded, want error")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("Reconcile returned error: %v", err)
			}
			if got := fixture.mutationLog(); !sameStringsInOrder(got, test.wantMutations) {
				t.Fatalf("mutations = %v, want %v", got, test.wantMutations)
			}
		})
	}
}

func TestGCPBackendValidationFailsClosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		authBody string
	}{
		{
			name:     "missing",
			authBody: `{"data":{"token/":{"type":"token","accessor":"auth_token_1234"}}}`,
		},
		{
			name:     "wrong type",
			authBody: `{"data":{"gcp/":{"type":"userpass","accessor":"auth_userpass_1234"}}}`,
		},
		{
			name:     "invalid accessor",
			authBody: `{"data":{"gcp/":{"type":"gcp","accessor":"auth_gcp_bad\"accessor"}}}`,
		},
		{
			name:     "duplicate JSON field",
			authBody: `{"data":{"gcp/":{"type":"gcp","accessor":"auth_gcp_1234","accessor":"auth_gcp_5678"}}}`,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assertSecretHeaders(t, r)
				_, _ = io.WriteString(w, test.authBody)
			}))
			defer server.Close()

			reconciler := mustReconciler(t, server)
			if err := reconciler.Reconcile(context.Background(), ModeCheck); err == nil {
				t.Fatal("Reconcile succeeded for invalid auth backend response")
			}
		})
	}
}

func TestAdditionalGCPMountDoesNotAffectExactMount(t *testing.T) {
	t.Parallel()

	fixture := newVaultFixture()
	fixture.additionalGCPMount = true
	server := httptest.NewTLSServer(fixture.handler(t))
	defer server.Close()

	if err := mustReconciler(t, server).Reconcile(context.Background(), ModeCheck); err != nil {
		t.Fatalf("Reconcile rejected an unrelated GCP mount: %v", err)
	}
}

func TestReconcileNeverFollowsRedirects(t *testing.T) {
	t.Parallel()

	var redirected atomic.Bool
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Store(true)
		if r.Header.Get("X-Vault-Token") != "" || r.Header.Get("X-Admin-Token") != "" {
			t.Error("redirect target received a secret header")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/capture", http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	reconciler := mustReconciler(t, source)
	err := reconciler.Reconcile(context.Background(), ModeCheck)
	if err == nil {
		t.Fatal("Reconcile followed a redirect, want error")
	}
	if redirected.Load() {
		t.Fatal("redirect target was contacted")
	}
	assertRedactedError(t, err)
}

func TestReconcileErrorsAreBoundedAndRedacted(t *testing.T) {
	t.Parallel()

	const responseSecret = "response-body-secret"
	tests := []struct {
		name        string
		handler     http.Handler
		tokenSource oauth2.TokenSource
		client      *http.Client
	}{
		{
			name: "HTTP error body",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, `{"errors":["`+responseSecret+`"]}`)
			}),
		},
		{
			name: "invalid JSON body",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, responseSecret)
			}),
		},
		{
			name: "oversized body",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, strings.Repeat(responseSecret, int(maxResponseBytes/int64(len(responseSecret)))+2))
			}),
		},
		{
			name:        "token source error",
			handler:     http.NotFoundHandler(),
			tokenSource: errorTokenSource{err: errors.New(responseSecret + " " + testVaultToken)},
		},
		{
			name:    "transport error",
			handler: http.NotFoundHandler(),
			client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New(responseSecret + " " + testAdminToken + " " + testVaultToken)
			})},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewTLSServer(test.handler)
			defer server.Close()

			tokenSource := test.tokenSource
			if tokenSource == nil {
				tokenSource = oauth2.StaticTokenSource(&oauth2.Token{AccessToken: testAdminToken})
			}
			client := test.client
			if client == nil {
				client = clientForServer(t, server)
			}
			reconciler, err := New(Config{
				VaultAddress:     testVaultURL,
				VaultToken:       testVaultToken,
				ProjectID:        testProject,
				ProjectNumber:    testProjectNumber,
				Region:           testRegion,
				AdminTokenSource: tokenSource,
				HTTPClient:       client,
			})
			if err != nil {
				t.Fatalf("New returned error: %v", err)
			}
			err = reconciler.Reconcile(context.Background(), ModeCheck)
			if err == nil {
				t.Fatal("Reconcile succeeded, want error")
			}
			assertRedactedError(t, err)
			if strings.Contains(err.Error(), responseSecret) {
				t.Fatalf("error exposed response content: %v", err)
			}
		})
	}
}

func TestWrongTypeRoleRecreationRecoversAfterCreateFailure(t *testing.T) {
	t.Parallel()

	fixture := newVaultFixture()
	fixture.gceRole = realisticGCERole()
	fixture.roleWriteFailures = 1
	server := httptest.NewTLSServer(fixture.handler(t))
	defer server.Close()
	reconciler := mustReconciler(t, server)

	err := reconciler.Reconcile(context.Background(), ModeApply)
	if err == nil {
		t.Fatal("first Reconcile succeeded, want injected role write failure")
	}
	assertRedactedError(t, err)
	if fixture.roleExists() {
		t.Fatal("wrong-type role still exists after successful deletion")
	}

	if err := reconciler.Reconcile(context.Background(), ModeApply); err != nil {
		t.Fatalf("retry Reconcile returned error: %v", err)
	}
	wantMutations := []string{"role:delete", "role:put-failed", "role:put"}
	if got := fixture.mutationLog(); !sameStringsInOrder(got, wantMutations) {
		t.Fatalf("mutations = %v, want %v", got, wantMutations)
	}
}

func TestGCPConfigPreconditionFailsBeforeMutation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		configure func(*vaultFixture)
	}{
		{
			name: "missing",
			configure: func(f *vaultFixture) {
				f.configFound = false
			},
		},
		{
			name: "wrong IAM alias",
			configure: func(f *vaultFixture) {
				f.iamAlias = "role_id"
			},
		},
		{
			name: "missing service account email metadata",
			configure: func(f *vaultFixture) {
				f.iamMetadata = []string{"project_id"}
			},
		},
		{
			name: "extra IAM metadata",
			configure: func(f *vaultFixture) {
				f.iamMetadata = []string{"service_account_email", "project_id"}
			},
		},
		{
			name: "custom Google endpoint",
			configure: func(f *vaultFixture) {
				f.customEndpoint = map[string]string{"iam": "https://attacker.example"}
			},
		},
		{
			name: "static credential identity",
			configure: func(f *vaultFixture) {
				f.clientEmail = "vault@other-project.iam.gserviceaccount.com"
			},
		},
		{
			name: "workload identity audience",
			configure: func(f *vaultFixture) {
				f.identityTokenAudience = "https://attacker.example"
			},
		},
		{
			name: "wrong response mount type",
			configure: func(f *vaultFixture) {
				f.configMountType = "userpass"
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newVaultFixture()
			test.configure(fixture)
			fixture.policy = "drifted-policy"
			fixture.role.TokenPolicies = []string{"overprivileged"}
			server := httptest.NewTLSServer(fixture.handler(t))
			defer server.Close()

			if err := mustReconciler(t, server).Reconcile(context.Background(), ModeApply); err == nil {
				t.Fatal("Reconcile accepted unsafe GCP auth config")
			}
			if got := fixture.mutationLog(); len(got) != 0 {
				t.Fatalf("mutations = %v, want none", got)
			}
		})
	}
}

func TestReconcileRejectsNilContextWithoutRequest(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()
	reconciler := mustReconciler(t, server)

	var missingContext context.Context
	if err := reconciler.Reconcile(missingContext, ModeCheck); err == nil {
		t.Fatal("Reconcile accepted a nil context")
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("requests = %d, want 0", got)
	}
}

func TestNewRejectsUnsafeConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		configure func(*Config)
	}{
		{
			name: "HTTP address",
			configure: func(config *Config) {
				config.VaultAddress = "http://vault-server-dev-" + testProjectNumber + ".us-east5.run.app"
			},
		},
		{
			name: "address path",
			configure: func(config *Config) {
				config.VaultAddress = testVaultURL + "/untrusted"
			},
		},
		{
			name: "untrusted HTTPS origin",
			configure: func(config *Config) {
				config.VaultAddress = "https://evil.example"
			},
		},
		{
			name: "address port",
			configure: func(config *Config) {
				config.VaultAddress = testVaultURL + ":443"
			},
		},
		{
			name: "missing project number",
			configure: func(config *Config) {
				config.ProjectNumber = ""
			},
		},
		{
			name: "invalid project number",
			configure: func(config *Config) {
				config.ProjectNumber = "not-numeric"
			},
		},
		{
			name: "wrong project number",
			configure: func(config *Config) {
				config.ProjectNumber = "999999999999"
			},
		},
		{
			name: "header injection",
			configure: func(config *Config) {
				config.VaultToken = "secret\r\nInjected: true"
			},
		},
		{
			name: "header tab",
			configure: func(config *Config) {
				config.VaultToken = "secret\tvalue"
			},
		},
		{
			name: "oversized token",
			configure: func(config *Config) {
				config.VaultToken = strings.Repeat("x", maxSecretBytes+1)
			},
		},
		{
			name: "invalid project",
			configure: func(config *Config) {
				config.ProjectID = "INVALID"
			},
		},
		{
			name: "invalid region",
			configure: func(config *Config) {
				config.Region = "INVALID"
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			config := Config{
				VaultAddress:     testVaultURL,
				VaultToken:       testVaultToken,
				ProjectID:        testProject,
				ProjectNumber:    testProjectNumber,
				Region:           testRegion,
				AdminTokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: testAdminToken}),
			}
			test.configure(&config)
			if _, err := New(config); err == nil {
				t.Fatal("New accepted unsafe configuration")
			}
		})
	}
}

type vaultFixture struct {
	mu                    sync.Mutex
	accessor              string
	additionalGCPMount    bool
	configFound           bool
	configMountType       string
	iamAlias              string
	iamMetadata           []string
	customEndpoint        map[string]string
	clientEmail           string
	identityTokenAudience string
	policy                string
	policyFound           bool
	role                  roleRead
	gceRole               *gceRoleRead
	roleFound             bool
	refusePolicyUpdate    bool
	roleWriteFailures     int
	mutations             []string
}

func newVaultFixture() *vaultFixture {
	role := exactRole(testProject)
	role.RoleID = testRoleID
	return &vaultFixture{
		accessor:        testAccessor,
		configFound:     true,
		configMountType: "gcp",
		iamAlias:        "unique_id",
		iamMetadata:     []string{"service_account_email"},
		policy:          renderPolicy(testAccessor),
		policyFound:     true,
		role:            role,
		roleFound:       true,
	}
}

func (f *vaultFixture) handler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertSecretHeaders(t, r)
		f.mu.Lock()
		defer f.mu.Unlock()

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sys/auth":
			mounts := map[string]any{
				"token/": map[string]any{"type": "token", "accessor": "auth_token_1234"},
				"gcp/":   map[string]any{"type": "gcp", "accessor": f.accessor},
			}
			if f.additionalGCPMount {
				mounts["unrelated/"] = map[string]any{"type": "gcp", "accessor": "auth_gcp_unrelated"}
			}
			writeJSONResponse(t, w, map[string]any{"data": mounts})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/auth/gcp/config":
			if !f.configFound {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			data := map[string]any{
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
				"iam_alias":               f.iamAlias,
				"iam_metadata":            f.iamMetadata,
				"identity_token_audience": f.identityTokenAudience,
				"identity_token_ttl":      0,
				"rotation_period":         0,
				"rotation_schedule":       "",
				"rotation_window":         0,
			}
			if len(f.customEndpoint) != 0 {
				data["custom_endpoint"] = f.customEndpoint
			}
			if f.clientEmail != "" {
				data["client_email"] = f.clientEmail
			}
			writeMountedJSONResponse(t, w, f.configMountType, data)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sys/policies/acl/"+policyName:
			if !f.policyFound {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			writeJSONResponse(t, w, map[string]any{"data": policyRead{Name: policyName, Policy: f.policy}})
		case r.Method == http.MethodPut && r.URL.Path == "/v1/sys/policies/acl/"+policyName:
			var request policyWrite
			decodeRequest(t, w, r, &request)
			f.mutations = append(f.mutations, "policy:put")
			if !f.refusePolicyUpdate {
				f.policy = request.Policy
				f.policyFound = true
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/auth/gcp/role/"+policyName:
			if !f.roleFound {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if f.gceRole != nil {
				writeMountedJSONResponse(t, w, "gcp", *f.gceRole)
				return
			}
			writeMountedJSONResponse(t, w, "gcp", f.role)
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/auth/gcp/role/"+policyName:
			f.mutations = append(f.mutations, "role:delete")
			f.roleFound = false
			f.gceRole = nil
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPut && r.URL.Path == "/v1/auth/gcp/role/"+policyName:
			var request roleWrite
			decodeRequest(t, w, r, &request)
			if !exactRoleWrite(request, testProject) {
				t.Errorf("role request is not exact: %#v", request)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if f.roleWriteFailures > 0 {
				f.roleWriteFailures--
				f.mutations = append(f.mutations, "role:put-failed")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, testVaultToken+" "+testAdminToken)
				return
			}
			f.mutations = append(f.mutations, "role:put")
			f.role = exactRole(testProject)
			f.role.RoleID = testRoleID
			f.gceRole = nil
			f.roleFound = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func (f *vaultFixture) roleExists() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.roleFound
}

func (f *vaultFixture) mutationLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.mutations...)
}

func mustReconciler(t *testing.T, server *httptest.Server) *Reconciler {
	t.Helper()
	reconciler, err := New(Config{
		VaultAddress:     testVaultURL,
		VaultToken:       testVaultToken,
		ProjectID:        testProject,
		ProjectNumber:    testProjectNumber,
		Region:           testRegion,
		AdminTokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: testAdminToken}),
		HTTPClient:       clientForServer(t, server),
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	return reconciler
}

func assertSecretHeaders(t *testing.T, r *http.Request) {
	t.Helper()
	if got := r.Header.Get("X-Vault-Token"); got != testVaultToken {
		t.Errorf("X-Vault-Token = %q, want sentinel", got)
	}
	if got := r.Header.Get("X-Admin-Token"); got != testAdminToken {
		t.Errorf("X-Admin-Token = %q, want sentinel", got)
	}
}

func assertRedactedError(t *testing.T, err error) {
	t.Helper()
	for _, secret := range []string{testVaultToken, testAdminToken} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error exposed secret: %v", err)
		}
	}
}

func writeJSONResponse(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

func writeMountedJSONResponse(t *testing.T, w http.ResponseWriter, mountType string, data any) {
	t.Helper()
	writeJSONResponse(t, w, map[string]any{
		"data":       data,
		"mount_type": mountType,
	})
}

func decodeRequest(t *testing.T, w http.ResponseWriter, r *http.Request, target any) {
	t.Helper()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		t.Errorf("decode request: %v", err)
		w.WriteHeader(http.StatusBadRequest)
	}
}

func exactRoleWrite(got roleWrite, projectID string) bool {
	return reflect.DeepEqual(got, canonicalRoleWrite(projectID))
}

func realisticGCERole() *gceRoleRead {
	return &gceRoleRead{
		RoleID:               testRoleID,
		Type:                 "gce",
		BoundServiceAccounts: []string{"*"},
		BoundProjects:        []string{testProject},
		AddGroupAliases:      true,
		AliasMetadata:        map[string]string{"environment": "preview"},
		BoundRegions:         []string{"us-east5"},
		BoundZones:           []string{"us-east5-a"},
		BoundInstanceGroups:  []string{"preview-group"},
		BoundLabels:          map[string]string{"environment": "preview"},
		TokenBoundCIDRs:      []string{"10.0.0.0/8"},
		TokenExplicitMaxTTL:  60,
		TokenMaxTTL:          600,
		TokenNoDefaultPolicy: false,
		TokenPeriod:          0,
		TokenPolicies:        []string{"overprivileged"},
		TokenType:            "service",
		TokenTTL:             300,
		TokenNumUses:         2,
	}
}

func sameStringsInOrder(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

type errorTokenSource struct {
	err error
}

func (s errorTokenSource) Token() (*oauth2.Token, error) {
	return nil, s.err
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func clientForServer(t *testing.T, server *httptest.Server) *http.Client {
	t.Helper()
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	transport := server.Client().Transport
	return &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		cloned := request.Clone(request.Context())
		clonedURL := *request.URL
		clonedURL.Scheme = target.Scheme
		clonedURL.Host = target.Host
		cloned.URL = &clonedURL
		cloned.Host = ""
		return transport.RoundTrip(cloned)
	})}
}
