package vaultkv

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
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
