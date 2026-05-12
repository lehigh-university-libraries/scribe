package vaultkv

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSignServiceAccountJWT(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: mustMarshalPKCS8(t, privateKey),
	})
	now := time.Unix(1_700_000_000, 0).UTC()

	token, err := signServiceAccountJWT(
		"scribe@example-project.iam.gserviceaccount.com",
		string(privateKeyPEM),
		"kid-123",
		"scribe-app",
		now,
	)
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("expected 3 jwt parts, got %d", len(parts))
	}

	var header map[string]string
	decodeSegment(t, parts[0], &header)
	if got := header["alg"]; got != "RS256" {
		t.Fatalf("header alg = %q, want RS256", got)
	}
	if got := header["kid"]; got != "kid-123" {
		t.Fatalf("header kid = %q, want kid-123", got)
	}

	var claims map[string]any
	decodeSegment(t, parts[1], &claims)
	if got := claims["aud"]; got != "vault/scribe-app" {
		t.Fatalf("claims aud = %v, want vault/scribe-app", got)
	}
	if got := claims["sub"]; got != "scribe@example-project.iam.gserviceaccount.com" {
		t.Fatalf("claims sub = %v", got)
	}
	if got := int64(claims["iat"].(float64)); got != now.Unix() {
		t.Fatalf("claims iat = %d, want %d", got, now.Unix())
	}
	if got := int64(claims["exp"].(float64)); got != now.Add(10*time.Minute).Unix() {
		t.Fatalf("claims exp = %d, want %d", got, now.Add(10*time.Minute).Unix())
	}

	signingInput := parts[0] + "." + parts[1]
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	sum := sha256.Sum256([]byte(signingInput))
	if err := rsa.VerifyPKCS1v15(&privateKey.PublicKey, crypto.SHA256, sum[:], signature); err != nil {
		t.Fatalf("verify signature: %v", err)
	}
}

func TestRenewSelfExtendsCachedToken(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	var renewCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/auth/token/renew-self" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("X-Vault-Token"); got != "old-token" {
			t.Fatalf("X-Vault-Token = %q", got)
		}
		renewCalled = true
		_ = json.NewEncoder(w).Encode(map[string]any{
			"auth": map[string]any{
				"client_token":   "renewed-token",
				"lease_duration": 300,
				"renewable":      true,
			},
		})
	}))
	defer srv.Close()

	client := New(srv.URL, "", "secret", "scribe-app")
	client.now = func() time.Time { return now }
	client.cachedToken = "old-token"
	client.expiresAt = now.Add(20 * time.Second)
	client.renewable = true

	token, err := client.authToken(context.Background())
	if err != nil {
		t.Fatalf("authToken returned error: %v", err)
	}
	if token != "renewed-token" {
		t.Fatalf("token = %q, want renewed-token", token)
	}
	if !renewCalled {
		t.Fatal("renew endpoint was not called")
	}
	if !client.expiresAt.Equal(now.Add(300 * time.Second)) {
		t.Fatalf("expiresAt = %s, want %s", client.expiresAt, now.Add(300*time.Second))
	}
}

func mustMarshalPKCS8(t *testing.T, privateKey *rsa.PrivateKey) []byte {
	t.Helper()
	raw, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("marshal pkcs8: %v", err)
	}
	return raw
}

func decodeSegment(t *testing.T, segment string, target any) {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		t.Fatalf("decode jwt segment: %v", err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("unmarshal jwt segment: %v", err)
	}
}
