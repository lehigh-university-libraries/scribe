package auth

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lehigh-university-libraries/scribe/internal/config"
)

func TestPrincipalHasPermissionForSessionRoles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		principal  Principal
		permission string
		want       bool
	}{
		{
			name: "workspace admin can manage api keys",
			principal: Principal{
				Authenticated: true,
				AuthType:      "session",
				WorkspaceRole: "admin",
			},
			permission: "admin:api_keys",
			want:       true,
		},
		{
			name: "workspace writer cannot manage api keys",
			principal: Principal{
				Authenticated: true,
				AuthType:      "session",
				WorkspaceRole: "write",
			},
			permission: "admin:api_keys",
			want:       false,
		},
		{
			name: "workspace creator can create items",
			principal: Principal{
				Authenticated: true,
				AuthType:      "session",
				WorkspaceRole: "create",
			},
			permission: "items:create",
			want:       true,
		},
		{
			name: "workspace creator cannot edit annotations",
			principal: Principal{
				Authenticated: true,
				AuthType:      "session",
				WorkspaceRole: "create",
			},
			permission: "annotations:write",
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := principalHasPermission(tt.principal, tt.permission); got != tt.want {
				t.Fatalf("principalHasPermission(%+v, %q) = %v, want %v", tt.principal, tt.permission, got, tt.want)
			}
		})
	}
}

func TestPrincipalHasPermissionForAPIKeyScopes(t *testing.T) {
	t.Parallel()

	principal := Principal{
		Authenticated: true,
		AuthType:      "api_key",
		WorkspaceRole: "write",
		Scopes:        []string{"items:*", "annotations:read"},
	}

	if !principalHasPermission(principal, "items:write") {
		t.Fatal("api key with items:* should allow items:write")
	}
	if principalHasPermission(principal, "annotations:write") {
		t.Fatal("api key without annotations:write should not allow annotations:write")
	}
}

func TestAPIKeyRoleStillBoundsScopes(t *testing.T) {
	t.Parallel()

	principal := Principal{
		Authenticated: true,
		AuthType:      "api_key",
		WorkspaceRole: "read",
		Scopes:        []string{"items:*"},
	}

	if principalHasPermission(principal, "items:write") {
		t.Fatal("read-scoped api key should not be able to elevate through items:*")
	}
	if !principalHasPermission(principal, "items:read") {
		t.Fatal("read-scoped api key should still allow items:read")
	}
}

func TestAPIKeyNeverInheritsCreatorAdminBypass(t *testing.T) {
	t.Parallel()

	principal := Principal{
		Authenticated: true,
		AuthType:      "api_key",
		IsAdmin:       true,
		WorkspaceRole: "read",
		Scopes:        []string{"items:read"},
	}

	if !principalHasPermission(principal, "items:read") {
		t.Fatal("admin-created API key lost its explicitly delegated read permission")
	}
	for _, permission := range []string{"items:write", "annotations:write", "admin:api_keys"} {
		if principalHasPermission(principal, permission) {
			t.Fatalf("admin-created API key bypassed role/scopes for %q", permission)
		}
	}
}

func TestRequiredPermissionForPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path   string
		method string
		want   string
	}{
		{path: "/v1/events", method: "GET", want: "transcription:read"},
		{path: "/static/uploads/page.jpg", method: http.MethodGet, want: "annotations:read"},
		{path: "/static/uploads/page.jpg", method: http.MethodHead, want: "annotations:read"},
		{path: "/static/uploads/0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef-12345678-1234-4123-8123-123456789abc.jpg", method: http.MethodGet, want: ""},
		{path: "/v1/item-images/42/annotations", method: "GET", want: ""},
		{path: "/v1/item-images/42/annotations", method: "POST", want: ""},
		{path: "/scribe.v1.AnnotationService/GetAnnotationPage", method: "POST", want: ""},
		{path: "/scribe.v1.AnnotationService/SaveAnnotationPage", method: "POST", want: ""},
		{path: "/scribe.v1.AnnotationService/ExportAnnotationPage", method: "POST", want: ""},
		{path: "/scribe.v1.ItemService/PrepareItemExport", method: "POST", want: ""},
		{path: "/v1/item-exports/signed-token", method: http.MethodGet, want: "annotations:read"},
		{path: "/v1/item-images/42/annotations/revisions/7/hocr", method: http.MethodGet, want: "annotations:read"},
		{path: "/scribe.v1.AnnotationService/PublishItemImageEdits", method: "POST", want: ""},
		{path: "/scribe.v1.ContextService/GetContextMetrics", method: "POST", want: ""},
		{path: "/scribe.v1.ContextService/GetModelCatalog", method: "POST", want: ""},
		{path: "/scribe.v1.ItemService/ListItemProviderCallAudits", method: "POST", want: ""},
	}

	for _, tt := range tests {
		if got := requiredPermissionForPath(tt.path, tt.method); got != tt.want {
			t.Fatalf("requiredPermissionForPath(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestOnlyImmutableUploadSourcesReachAnonymousAuthorizationFallback(t *testing.T) {
	t.Parallel()

	manager := &Manager{}
	next := http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		principal, ok := PrincipalFromContext(request.Context())
		if !ok || !principal.Anonymous() {
			t.Fatalf("principal = %+v, %t; want anonymous", principal, ok)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	for _, test := range []struct {
		name       string
		method     string
		path       string
		wantStatus int
	}{
		{
			name:       "immutable GET",
			method:     http.MethodGet,
			path:       "/static/uploads/0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef-12345678-1234-4123-8123-123456789abc.jpg",
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "immutable HEAD",
			method:     http.MethodHead,
			path:       "/static/uploads/0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef-12345678-1234-4123-8123-123456789abc.jpg",
			wantStatus: http.StatusNoContent,
		},
		{name: "mutable alias", method: http.MethodGet, path: "/static/uploads/private.jpg", wantStatus: http.StatusUnauthorized},
		{name: "mutation", method: http.MethodPost, path: "/static/uploads/0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef-12345678-1234-4123-8123-123456789abc.jpg", wantStatus: http.StatusUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			manager.Middleware(next).ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
		})
	}
}

func TestPublicUploadSourceRoutesAreExact(t *testing.T) {
	t.Parallel()
	const immutable = "/static/uploads/0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef-12345678-1234-4123-8123-123456789abc.jpg"
	for _, test := range []struct {
		path   string
		method string
		want   bool
	}{
		{path: immutable, method: http.MethodGet, want: true},
		{path: immutable, method: http.MethodHead, want: true},
		{path: immutable, method: http.MethodPost, want: false},
		{path: immutable + "/nested", method: http.MethodGet, want: false},
		{path: immutable + "?download=1", method: http.MethodGet, want: false},
		{path: "/static/uploads/page.jpg", method: http.MethodGet, want: false},
		{path: "/static/uploads/../" + strings.TrimPrefix(immutable, "/static/uploads/"), method: http.MethodGet, want: false},
	} {
		if got := IsPublicUploadSourceRequest(test.path, test.method); got != test.want {
			t.Errorf("IsPublicUploadSourceRequest(%q, %q) = %t, want %t", test.path, test.method, got, test.want)
		}
	}
}

func TestMiddlewareRequiresAuthenticationForRetiredIIIFRoutes(t *testing.T) {
	t.Parallel()

	manager := &Manager{}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	for _, requestPath := range []string{
		"/v1/items/01HZX6K9QW/manifest",
		"/v1/item-images/42/manifest",
		"/v1/item-images/42/manifest/canvas/page-1",
		"/v1/item-images/42/manifest/painting",
		"/v1/item-images/42/manifest/painting/items/image",
		"/v1/item-images/42/annotations",
		"/v1/item-images/42/annotations/items/line-1",
	} {
		response := httptest.NewRecorder()
		manager.Middleware(next).ServeHTTP(response, httptest.NewRequest(http.MethodGet, requestPath, nil))
		if response.Code != http.StatusUnauthorized {
			t.Errorf("GET %s status = %d, want %d", requestPath, response.Code, http.StatusUnauthorized)
		}
	}
}

func TestRequiredPermissionForAuthProcedures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		procedure string
		want      string
	}{
		{procedure: "/scribe.v1.AuthService/ListAPIKeys", want: "admin:api_keys"},
		{procedure: "/scribe.v1.AuthService/ListProviderSecrets", want: "contexts:read"},
		{procedure: "/scribe.v1.AuthService/CreateProviderSecret", want: "contexts:write"},
		{procedure: "/scribe.v1.ContextService/GetContextMetrics", want: "contexts:read"},
		{procedure: "/scribe.v1.ContextService/GetModelCatalog", want: "contexts:read"},
		{procedure: "/scribe.v1.ItemService/GetEditorManifest", want: "items:read"},
		{procedure: "/scribe.v1.ItemService/ListItemProviderCallAudits", want: "items:read"},
		{procedure: "/scribe.v1.ItemService/PrepareItemExport", want: "annotations:read"},
		{procedure: "/scribe.v1.UnknownService/Unknown", want: unmappedProcedurePermission},
	}

	for _, tt := range tests {
		if got := requiredPermissionForProcedure(tt.procedure, 0); got != tt.want {
			t.Fatalf("requiredPermissionForProcedure(%q) = %q, want %q", tt.procedure, got, tt.want)
		}
	}
}

func TestItemReadScopeCannotAuthorizeCanonicalTextExports(t *testing.T) {
	t.Parallel()
	principal := Principal{
		Authenticated: true,
		AuthType:      "api_key",
		WorkspaceRole: "read",
		Scopes:        []string{"items:read"},
	}
	for _, permission := range []string{
		requiredPermissionForProcedure("/scribe.v1.ItemService/PrepareItemExport", 0),
		requiredPermissionForPath("/v1/item-exports/signed-token", http.MethodGet),
	} {
		if permission != "annotations:read" {
			t.Fatalf("canonical export permission = %q, want annotations:read", permission)
		}
		if principalHasPermission(principal, permission) {
			t.Fatalf("items-only API key authorized canonical export permission %q", permission)
		}
	}
}

func TestUnmappedProcedurePermissionFailsClosedForEveryCredential(t *testing.T) {
	t.Parallel()

	principals := []Principal{
		{Authenticated: true, AuthType: "session", IsAdmin: true, WorkspaceRole: "admin"},
		{Authenticated: true, AuthType: "session", WorkspaceRole: "write"},
		{Authenticated: true, AuthType: "api_key", WorkspaceRole: "admin", Scopes: []string{"*"}},
		{Authenticated: true, AuthType: "external_jwt", WorkspaceRole: "admin", Scopes: []string{"*"}},
	}
	for _, principal := range principals {
		if principalHasPermission(principal, unmappedProcedurePermission) {
			t.Fatalf("%s credential allowed an unmapped procedure", principal.AuthType)
		}
	}
}

func TestTripletSourceCredentialIsRestrictedToExactImmutableReads(t *testing.T) {
	token := "test-triplet-source-reader-token-32-bytes"
	name := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-123e4567-e89b-42d3-a456-426614174000.jpg"
	manager := &Manager{sourceReadToken: token}
	handler := manager.Middleware(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		principal, ok := PrincipalFromContext(request.Context())
		if !ok || !principal.CanReadRawSource() {
			t.Error("request did not receive the constrained source principal")
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		request := httptest.NewRequest(method, "/static/uploads/"+name, nil)
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNoContent {
			t.Fatalf("%s exact source status = %d", method, response.Code)
		}
	}

	for _, test := range []struct {
		name   string
		method string
		path   string
		token  string
	}{
		{name: "invalid token", method: http.MethodGet, path: "/static/uploads/" + name, token: "wrong"},
		{name: "query", method: http.MethodGet, path: "/static/uploads/" + name + "?download=1", token: token},
		{name: "mutable name", method: http.MethodGet, path: "/static/uploads/source.jpg", token: token},
		{name: "write", method: http.MethodPost, path: "/static/uploads/" + name, token: token},
		{name: "business RPC", method: http.MethodPost, path: "/scribe.v1.ItemService/GetItem", token: token},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, nil)
			request.Header.Set("Authorization", "Bearer "+test.token)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", response.Code)
			}
		})
	}
}

func TestScopeListAllowsEmptyDeniesAPIKeyScope(t *testing.T) {
	t.Parallel()

	if scopeListAllows(nil, "items:write") {
		t.Fatal("scopeListAllows(nil, items:write) = true, want false")
	}
	if scopeListAllows([]string{}, "annotations:read") {
		t.Fatal("scopeListAllows(empty, annotations:read) = true, want false")
	}
}

func TestSecretKeyHint(t *testing.T) {
	t.Parallel()

	if got := secretKeyHint("abcd1234"); got != "…1234" {
		t.Fatalf("secretKeyHint = %q, want %q", got, "…1234")
	}
	if got := secretKeyHint("abc"); got != "***" {
		t.Fatalf("secretKeyHint short = %q, want %q", got, "***")
	}
	if got := secretKeyHint("密钥凭证甲乙丙丁"); got != "…甲乙丙丁" {
		t.Fatalf("secretKeyHint unicode = %q, want %q", got, "…甲乙丙丁")
	}
	if got := secretKeyHint(""); got != "" {
		t.Fatalf("secretKeyHint empty = %q, want empty", got)
	}
}

func TestRequestIPRejectsForwardedSpoofingAndUsesTrustedChain(t *testing.T) {
	direct := httptest.NewRequest("GET", "https://scribe.example", nil)
	direct.RemoteAddr = "203.0.113.8:1234"
	direct.Header.Set("X-Forwarded-For", "198.51.100.99")
	if got := clientIPFrom(direct, nil); got != "203.0.113.8" {
		t.Fatalf("direct ClientIP = %q, want immediate peer", got)
	}

	proxied := httptest.NewRequest("GET", "https://scribe.example", nil)
	proxied.RemoteAddr = "172.18.0.5:4321"
	proxied.Header.Set("X-Forwarded-For", "192.0.2.44, 10.1.2.3")
	trusted := config.CIDRList{"172.18.0.5/32", "10.0.0.0/8"}
	if got := clientIPFrom(proxied, trusted); got != "192.0.2.44" {
		t.Fatalf("proxied ClientIP = %q, want closest non-private client", got)
	}

	for _, remote := range []string{"172.18.0.5:4321", "169.254.169.254:80", "[::1]:8080"} {
		spoofed := httptest.NewRequest("GET", "https://scribe.example", nil)
		spoofed.RemoteAddr = remote
		spoofed.Header.Set("X-Forwarded-For", "198.51.100.99")
		want := remote
		if host, _, err := net.SplitHostPort(remote); err == nil {
			want = host
		}
		if got := clientIPFrom(spoofed, nil); got != want {
			t.Fatalf("unconfigured peer %q resolved to %q; want direct %q", remote, got, want)
		}
	}
}
