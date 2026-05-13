package auth

import "testing"

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

func TestRequiredPermissionForPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path   string
		method string
		want   string
	}{
		{path: "/v1/events", method: "GET", want: "transcription:read"},
		{path: "/scribe.v1.AnnotationService/CreateAnnotation", method: "POST", want: "annotations:write"},
		{path: "/scribe.v1.AnnotationService/CrosswalkToHOCR", method: "POST", want: "annotations:read"},
	}

	for _, tt := range tests {
		if got := requiredPermissionForPath(tt.path, tt.method); got != tt.want {
			t.Fatalf("requiredPermissionForPath(%q) = %q, want %q", tt.path, got, tt.want)
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
		{procedure: "/scribe.v1.UnknownService/Unknown", want: "authz:unmapped"},
	}

	for _, tt := range tests {
		if got := requiredPermissionForProcedure(tt.procedure, 0); got != tt.want {
			t.Fatalf("requiredPermissionForProcedure(%q) = %q, want %q", tt.procedure, got, tt.want)
		}
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

	if got := secretKeyHint("abcd1234"); got != "1234" {
		t.Fatalf("secretKeyHint = %q, want %q", got, "1234")
	}
	if got := secretKeyHint("abc"); got != "abc" {
		t.Fatalf("secretKeyHint short = %q, want %q", got, "abc")
	}
	if got := secretKeyHint(""); got != "" {
		t.Fatalf("secretKeyHint empty = %q, want empty", got)
	}
}
