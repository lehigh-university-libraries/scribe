package auth

import (
	"strings"
	"testing"
	"time"
)

func TestOAuthStateIsSignedStatelessAndExpires(t *testing.T) {
	manager := NewOAuthStateManager("shared-secret")
	now := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }

	created, err := manager.New("/editor?itemImageId=4")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	// A different replica with the same secret can validate the callback.
	otherReplica := NewOAuthStateManager("shared-secret")
	otherReplica.now = func() time.Time { return now.Add(time.Minute) }
	consumed, err := otherReplica.Consume(created.State)
	if err != nil {
		t.Fatalf("Consume() error = %v", err)
	}
	if consumed.RedirectPath != "/editor?itemImageId=4" {
		t.Fatalf("RedirectPath = %q", consumed.RedirectPath)
	}

	parts := strings.Split(created.State, ".")
	replacement := "A"
	if strings.HasPrefix(parts[0], replacement) {
		replacement = "B"
	}
	tampered := replacement + parts[0][1:] + "." + parts[1]
	if _, err := otherReplica.Consume(tampered); err == nil {
		t.Fatal("Consume() accepted tampered state")
	}

	otherReplica.now = func() time.Time { return now.Add(oauthStateLifetime + time.Second) }
	if _, err := otherReplica.Consume(created.State); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("Consume() expired error = %v", err)
	}
}

func TestOAuthStateSanitizesRedirectPath(t *testing.T) {
	manager := NewOAuthStateManager("shared-secret")
	for _, redirect := range []string{"https://attacker.test", "//attacker.test", "/\\attacker.test", "/ok\r\nLocation: https://attacker.test"} {
		state, err := manager.New(redirect)
		if err != nil {
			t.Fatalf("New(%q) error = %v", redirect, err)
		}
		consumed, err := manager.Consume(state.State)
		if err != nil {
			t.Fatalf("Consume(%q) error = %v", redirect, err)
		}
		if consumed.RedirectPath != "/" {
			t.Errorf("redirect %q sanitized to %q, want /", redirect, consumed.RedirectPath)
		}
	}
}
