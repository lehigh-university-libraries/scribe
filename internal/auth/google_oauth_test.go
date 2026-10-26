package auth

import (
	"testing"

	"github.com/markbates/goth"
)

func TestGoogleProfileRequiresVerifiedEmailBeforeClaimingMembership(t *testing.T) {
	for _, verified := range []any{nil, false, "true", true} {
		user := goth.User{UserID: "google-subject", Email: "member@example.test", RawData: map[string]any{"verified_email": verified}}
		profile, err := googleProfileFromUser(user)
		if verified == true {
			if err != nil || profile.Email != user.Email || profile.Subject != user.UserID {
				t.Fatalf("verified identity = %+v/%v", profile, err)
			}
		} else if err == nil {
			t.Fatalf("email verification %v accepted", verified)
		}
	}
}

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
