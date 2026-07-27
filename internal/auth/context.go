package auth

import "context"

type contextKey string

const principalContextKey contextKey = "scribe.principal"

type Principal struct {
	UserID             uint64
	Email              string
	Name               string
	PictureURL         string
	IsAdmin            bool
	Authenticated      bool
	AuthType           string
	WorkspaceID        uint64
	WorkspaceName      string
	WorkspaceRole      string
	DefaultWorkspaceID uint64
	APIKeyID           uint64
	APIKeyName         string
	Scopes             []string
	ExternalIssuer     string
	ExternalSubject    string
	ScopedItemID       string
	ScopedItemImageID  uint64
}

func (p Principal) Anonymous() bool {
	return !p.Authenticated
}

// HasPermission applies both workspace-role and delegated-credential scope
// restrictions. HTTP handlers with a public fallback use this to avoid
// exposing a private representation merely because the caller supplied valid
// credentials.
func (p Principal) HasPermission(permission string) bool {
	return principalHasPermission(p, permission)
}

// CanReadRawSource recognizes the deliberately narrow Triplet principal. It
// grants access only when the HTTP middleware has already constrained the
// request to an immutable GET/HEAD source route.
func (p Principal) CanReadRawSource() bool {
	return p.Authenticated && p.AuthType == "triplet_source"
}

func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalContextKey, principal)
}

func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalContextKey).(Principal)
	return principal, ok
}
