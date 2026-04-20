package auth

import (
	"net/http/httptest"
	"testing"

	"github.com/lehigh-university-libraries/scribe/internal/config"
)

func TestNewManagerRequiresGoogleSecrets(t *testing.T) {
	t.Parallel()

	_, err := NewManager(config.Config{
		PublicBaseURL: "https://scribe.example",
		Auth: config.AuthConfig{
			CookieName:         "scribe_session",
			GoogleCallbackPath: "/auth/callback/google",
		},
	}, config.Secrets{}, nil, nil, nil, nil, nil, nil, nil)
	if err == nil {
		t.Fatal("NewManager succeeded without Google OAuth secrets")
	}
}

func TestNewManagerWithPartialGoogleSecretsFails(t *testing.T) {
	t.Parallel()

	_, err := NewManager(config.Config{
		PublicBaseURL: "https://scribe.example",
		Auth: config.AuthConfig{
			CookieName:         "scribe_session",
			GoogleCallbackPath: "/auth/callback/google",
		},
	}, config.Secrets{
		GoogleOAuthClientID: "client-id-only",
	}, nil, nil, nil, nil, nil, nil, nil)
	if err == nil {
		t.Fatal("NewManager succeeded with incomplete Google OAuth configuration")
	}
}

func TestNewManagerRequiresPublicBaseURL(t *testing.T) {
	t.Parallel()

	_, err := NewManager(config.Config{
		Auth: config.AuthConfig{
			CookieName:         "scribe_session",
			GoogleCallbackPath: "/auth/callback/google",
		},
	}, config.Secrets{
		GoogleOAuthClientID:     "client-id",
		GoogleOAuthClientSecret: "client-secret",
	}, nil, nil, nil, nil, nil, nil, nil)
	if err == nil {
		t.Fatal("NewManager succeeded without public base URL")
	}
}

func TestNewManagerWithCompleteGoogleConfigEnablesAuth(t *testing.T) {
	t.Parallel()

	manager, err := NewManager(config.Config{
		PublicBaseURL: "https://scribe.example",
		Auth: config.AuthConfig{
			CookieName:         "scribe_session",
			GoogleCallbackPath: "/auth/callback/google",
		},
	}, config.Secrets{
		GoogleOAuthClientID:     "client-id",
		GoogleOAuthClientSecret: "client-secret",
	}, nil, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewManager returned error: %v", err)
	}
	if !manager.Enabled() {
		t.Fatal("manager.Enabled() = false, want true when Google OAuth is fully configured")
	}
}

func TestRequestedWorkspaceID(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest("GET", "https://scribe.example/v1/items", nil)
	req.Header.Set("X-Scribe-Workspace-ID", "42")
	got, err := requestedWorkspaceID(req)
	if err != nil {
		t.Fatalf("requestedWorkspaceID returned error: %v", err)
	}
	if got != 42 {
		t.Fatalf("requestedWorkspaceID = %d, want 42", got)
	}
}

func TestRequestedWorkspaceIDRejectsInvalidHeader(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest("GET", "https://scribe.example/v1/items", nil)
	req.Header.Set("X-Scribe-Workspace-ID", "bad")
	if _, err := requestedWorkspaceID(req); err == nil {
		t.Fatal("requestedWorkspaceID succeeded with invalid header")
	}
}

func TestExtractAPIKeyFromRequest(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest("GET", "https://scribe.example/v1/items", nil)
	req.Header.Set("X-Scribe-API-Key", "scribe_header")
	req.Header.Set("Authorization", "Bearer scribe_bearer")
	if got := extractAPIKeyFromRequest(req); got != "scribe_header" {
		t.Fatalf("extractAPIKeyFromRequest = %q, want header token", got)
	}

	req = httptest.NewRequest("GET", "https://scribe.example/v1/items", nil)
	req.Header.Set("Authorization", "Bearer scribe_bearer")
	if got := extractAPIKeyFromRequest(req); got != "scribe_bearer" {
		t.Fatalf("extractAPIKeyFromRequest = %q, want bearer token", got)
	}
}
