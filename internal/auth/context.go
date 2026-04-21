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
}

func (p Principal) Anonymous() bool {
	return !p.Authenticated
}

func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalContextKey, principal)
}

func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalContextKey).(Principal)
	return principal, ok
}
