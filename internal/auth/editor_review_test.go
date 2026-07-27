package auth

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEditorReviewTokenSignerRejectsTamperingAndExpiry(t *testing.T) {
	now := time.Date(2026, 7, 27, 15, 0, 0, 0, time.UTC)
	signer := newEditorReviewTokenSigner(strings.Repeat("s", 32))
	signer.now = func() time.Time { return now }
	token, issued, err := signer.issue(42, "item-42", 99, 5*time.Minute)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	claims, err := signer.consume(token)
	if err != nil {
		t.Fatalf("consume genuine token: %v", err)
	}
	if claims != issued || claims.WorkspaceID != 42 || claims.ItemImageID != 99 {
		t.Fatalf("claims = %+v, want %+v", claims, issued)
	}

	parts := strings.Split(token, ".")
	tamperedPayload := base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"jti":"forged","wid":7,"item":"foreign","image":8,"iat":1,"exp":9999999999}`))
	for name, candidate := range map[string]string{
		"payload":   tamperedPayload + "." + parts[1],
		"signature": parts[0] + "." + strings.Repeat("A", len(parts[1])),
		"shape":     token + ".extra",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := signer.consume(candidate); err == nil {
				t.Fatal("tampered token was accepted")
			}
		})
	}

	signer.now = func() time.Time { return now.Add(5 * time.Minute) }
	if _, err := signer.consume(token); err == nil {
		t.Fatal("token remained valid at its exact expiration")
	}
}

func TestEditorReviewTokenSignerSeparatesPurposesAndBoundsLifetime(t *testing.T) {
	signer := newEditorReviewTokenSigner(strings.Repeat("k", 32))
	if signer == nil {
		t.Fatal("signer was not configured")
	}
	if _, _, err := signer.issue(1, "item", 1, maximumEditorReviewTokenTTL+time.Second); err == nil {
		t.Fatal("oversized token lifetime was accepted")
	}
	if got, other := editorReviewerSubjectHash("https://issuer-a.example", "reviewer"), editorReviewerSubjectHash("https://issuer-b.example", "reviewer"); got == other {
		t.Fatal("reviewer subject digest was not issuer-bound")
	}
}

func TestEditorReviewSessionHTTPBoundaryRequiresExactImage(t *testing.T) {
	principal := Principal{ScopedItemImageID: 42}
	for _, test := range []struct {
		path string
		want bool
	}{
		{path: "/scribe.v1.AnnotationService/GetAnnotationPage", want: true},
		{path: "/v1/events", want: true},
		{path: "/v1/events?item_image_id=42", want: true},
		{path: "/v1/events?item_image_id=43", want: false},
		{path: "/v1/item-images/42/annotations/revisions/3/hocr", want: true},
		{path: "/v1/item-images/43/annotations/revisions/3/hocr", want: false},
		{path: "/v1/item-exports/opaque", want: false},
	} {
		t.Run(test.path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "https://scribe.example"+test.path, nil)
			if got := editorReviewSessionAllowsHTTP(principal, request); got != test.want {
				t.Fatalf("editorReviewSessionAllowsHTTP(%s) = %t, want %t", test.path, got, test.want)
			}
		})
	}
}

func TestReviewerIdentityValidationRejectsDisplayAddressAndControls(t *testing.T) {
	if got, err := normalizeReviewerEmail("Reviewer <reviewer@example.org>"); err == nil || got != "" {
		t.Fatalf("display address = %q/%v, want rejection", got, err)
	}
	if got, err := normalizeReviewerEmail("REVIEWER@example.org"); err != nil || got != "reviewer@example.org" {
		t.Fatalf("normalized address = %q/%v", got, err)
	}
	if boundedReviewIdentity("reviewer\nadmin", 255) {
		t.Fatal("control character was accepted in reviewer identity")
	}
}
