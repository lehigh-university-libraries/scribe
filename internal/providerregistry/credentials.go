package providerregistry

import (
	"context"
	"strings"
)

type credentialContextKey struct{}
type administratorCredentialFallbackDisabledKey struct{}

// WithCredential returns a context containing one request-scoped provider
// credential. The prior immutable credential set is copied so concurrent
// workspace requests can never share or mutate secret material.
func WithCredential(ctx context.Context, providerID, fieldID, value string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	providerID = normalizeID(providerID)
	fieldID = normalizeID(fieldID)
	if providerID == "" || fieldID == "" || strings.TrimSpace(value) == "" {
		return ctx
	}

	current, _ := ctx.Value(credentialContextKey{}).(map[string]map[string]string)
	next := make(map[string]map[string]string, len(current)+1)
	for provider, fields := range current {
		fieldCopy := make(map[string]string, len(fields))
		for field, existing := range fields {
			fieldCopy[field] = existing
		}
		next[provider] = fieldCopy
	}
	fields := next[providerID]
	if fields == nil {
		fields = make(map[string]string, 1)
		next[providerID] = fields
	}
	fields[fieldID] = value
	return context.WithValue(ctx, credentialContextKey{}, next)
}

// ContextCredential returns one request-scoped credential without falling
// back to process configuration.
func ContextCredential(ctx context.Context, providerID, fieldID string) string {
	if ctx == nil {
		return ""
	}
	values, _ := ctx.Value(credentialContextKey{}).(map[string]map[string]string)
	return values[normalizeID(providerID)][normalizeID(fieldID)]
}

// WithoutCredentials returns a context that cannot observe any ambient
// request-scoped provider credentials. Durable workers use this at their trust
// boundary before loading the workspace-owned credential for the queued job.
func WithoutCredentials(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, credentialContextKey{}, map[string]map[string]string{})
}

// WithoutAdministratorCredentialFallback marks a trust boundary where only an
// explicitly attached credential may be used. Durable tenant work uses this so
// a missing workspace secret cannot silently spend a process-wide key.
func WithoutAdministratorCredentialFallback(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, administratorCredentialFallbackDisabledKey{}, true)
}

func administratorCredentialFallbackDisabled(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	disabled, _ := ctx.Value(administratorCredentialFallbackDisabledKey{}).(bool)
	return disabled
}

// ContextCredentialValues returns defensive copies of request-scoped secret
// values for audit redaction. It never includes descriptor metadata or
// administrator fallback credentials.
func ContextCredentialValues(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}
	values, _ := ctx.Value(credentialContextKey{}).(map[string]map[string]string)
	secrets := make([]string, 0, len(values))
	for _, fields := range values {
		for _, value := range fields {
			if strings.TrimSpace(value) != "" {
				secrets = append(secrets, value)
			}
		}
	}
	return secrets
}
