package auth

import (
	"testing"
)

func TestGoogleProviderUsesBoundedHTTPClient(t *testing.T) {
	t.Parallel()
	manager, err := NewGoogleOAuthManager("client-id", "client-secret")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := manager.provider("https://scribe.example/auth/callback/google")
	if err != nil {
		t.Fatal(err)
	}
	if provider.HTTPClient == nil || provider.HTTPClient.Timeout != googleOAuthHTTPTimeout {
		t.Fatalf("Google HTTP client = %#v, want timeout %s", provider.HTTPClient, googleOAuthHTTPTimeout)
	}
	transport, ok := provider.HTTPClient.Transport.(boundedOAuthTransport)
	if !ok || transport.maxResponseBytes != googleOAuthMaxResponse {
		t.Fatalf("Google HTTP transport = %#v, want %d-byte bound", provider.HTTPClient.Transport, googleOAuthMaxResponse)
	}
}
