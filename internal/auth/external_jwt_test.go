package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/safehttp"
)

func TestExternalJWTPrincipalValidatesJWKS(t *testing.T) {
	t.Setenv(safehttp.AllowPrivateFetchesEnv, "true")
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	issuer := "https://islandora.example"
	jwks := jwksResponse{Keys: []jwkKey{{
		Kty: "RSA",
		Kid: "islandora-key",
		Alg: "RS256",
		N:   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		E:   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
	}}}
	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jwks)
	}))
	defer jwksServer.Close()

	manager := &Manager{
		auth: config.AuthConfig{
			ExternalJWTIssuers: []config.ExternalJWTIssuerConfig{{
				Issuer:      issuer,
				Audience:    "islandora-scribe",
				JWKSURL:     jwksServer.URL,
				WorkspaceID: 42,
				RoleMappings: []config.ExternalJWTRoleMapping{{
					Roles:  []string{"administrator"},
					Role:   "write",
					Scopes: []string{"items:create"},
				}},
			}},
		},
		jwksCache: make(map[string]cachedJWKS),
	}
	token := signTestJWT(t, key, "islandora-key", map[string]any{
		"iss":   issuer,
		"aud":   []string{"islandora-scribe"},
		"sub":   "islandora-user",
		"webid": "123",
		"roles": []string{"administrator"},
		"iat":   time.Now().Unix(),
		"exp":   time.Now().Add(time.Hour).Unix(),
	})

	principal, err := manager.externalJWTPrincipal(context.Background(), token)
	if err != nil {
		t.Fatalf("externalJWTPrincipal returned error: %v", err)
	}
	if principal.AuthType != "external_jwt" {
		t.Fatalf("AuthType = %q", principal.AuthType)
	}
	if principal.WorkspaceID != 42 || principal.WorkspaceRole != "write" {
		t.Fatalf("workspace = %d/%q", principal.WorkspaceID, principal.WorkspaceRole)
	}
	if principal.UserID != 123 {
		t.Fatalf("UserID = %d, want 123", principal.UserID)
	}
}

func TestExternalJWTPrincipalRejectsBadAudience(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	manager := &Manager{
		auth: config.AuthConfig{
			ExternalJWTIssuers: []config.ExternalJWTIssuerConfig{{
				Issuer:        "https://islandora.example",
				Audience:      "expected",
				JWKSURL:       "https://unused.example/keys",
				WorkspaceID:   42,
				RequiredRoles: []string{"administrator"},
			}},
		},
		jwksCache: make(map[string]cachedJWKS),
	}
	token := signTestJWT(t, key, "islandora-key", map[string]any{
		"iss": "https://islandora.example",
		"aud": "other",
		"exp": time.Now().Add(time.Hour).Unix(),
	})

	if _, err := manager.externalJWTPrincipal(context.Background(), token); err == nil || !strings.Contains(err.Error(), "audience") {
		t.Fatalf("expected audience error, got %v", err)
	}
}

func TestExternalJWTAccessRequiresExplicitAllowlist(t *testing.T) {
	_, _, err := externalJWTAccess([]string{"administrator"}, config.ExternalJWTIssuerConfig{
		WorkspaceID: 42,
	})
	if err == nil || !strings.Contains(err.Error(), "roles") {
		t.Fatalf("externalJWTAccess without required roles returned %v, want role rejection", err)
	}
}

func TestValidateExternalClaimsRejectsFutureNBF(t *testing.T) {
	err := validateExternalClaims(externalJWTClaims{
		Iss: "https://issuer.example",
		Exp: time.Now().Add(time.Hour).Unix(),
		Nbf: time.Now().Add(time.Hour).Unix(),
	}, config.ExternalJWTIssuerConfig{Issuer: "https://issuer.example"})
	if err == nil || !strings.Contains(err.Error(), "not yet valid") {
		t.Fatalf("validateExternalClaims returned %v, want nbf rejection", err)
	}
}

func TestJWKRSAPublicKeyRejectsWrongMetadata(t *testing.T) {
	for name, jwk := range map[string]jwkKey{
		"kty": {Kty: "EC", Use: "sig", Alg: "RS256"},
		"use": {Kty: "RSA", Use: "enc", Alg: "RS256"},
		"alg": {Kty: "RSA", Use: "sig", Alg: "HS256"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := jwkRSAPublicKey(jwk); err == nil {
				t.Fatalf("jwkRSAPublicKey accepted invalid %s metadata", name)
			}
		})
	}
}

func TestExternalJWTPrincipalRequiresKid(t *testing.T) {
	t.Setenv(safehttp.AllowPrivateFetchesEnv, "true")
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	issuer := "https://islandora.example"
	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jwksResponse{Keys: []jwkKey{{
			Kty: "RSA",
			Kid: "islandora-key",
			Alg: "RS256",
			N:   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			E:   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	}))
	defer jwksServer.Close()

	manager := &Manager{
		auth: config.AuthConfig{
			ExternalJWTIssuers: []config.ExternalJWTIssuerConfig{{
				Issuer:        issuer,
				Audience:      "islandora-scribe",
				JWKSURL:       jwksServer.URL,
				WorkspaceID:   42,
				RequiredRoles: []string{"administrator"},
				Role:          "write",
			}},
		},
		jwksCache: make(map[string]cachedJWKS),
	}
	token := signTestJWT(t, key, "", map[string]any{
		"iss":   issuer,
		"aud":   "islandora-scribe",
		"sub":   "islandora-user",
		"webid": "123",
		"roles": []string{"administrator"},
		"iat":   time.Now().Unix(),
		"exp":   time.Now().Add(time.Hour).Unix(),
	})

	if _, err := manager.externalJWTPrincipal(context.Background(), token); err == nil || !strings.Contains(err.Error(), "kid") {
		t.Fatalf("expected kid error, got %v", err)
	}
}

func TestValidateExternalClaimsUsesTightClockSkew(t *testing.T) {
	cfg := config.ExternalJWTIssuerConfig{Audience: "scribe"}
	if err := validateExternalClaims(externalJWTClaims{
		Iss: "https://issuer.example",
		Aud: json.RawMessage(`"scribe"`),
		Exp: time.Now().Add(-31 * time.Second).Unix(),
	}, cfg); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected expired token after 30s skew, got %v", err)
	}
	if err := validateExternalClaims(externalJWTClaims{
		Iss: "https://issuer.example",
		Aud: json.RawMessage(`"scribe"`),
		Exp: time.Now().Add(time.Hour).Unix(),
		Iat: time.Now().Add(31 * time.Second).Unix(),
	}, cfg); err == nil || !strings.Contains(err.Error(), "future") {
		t.Fatalf("expected future iat after 30s skew, got %v", err)
	}
}

func TestExternalJWTPrincipalRejectsUnmappedRole(t *testing.T) {
	t.Setenv(safehttp.AllowPrivateFetchesEnv, "true")
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	issuer := "https://islandora.example"
	jwks := jwksResponse{Keys: []jwkKey{{
		Kty: "RSA",
		Kid: "islandora-key",
		Alg: "RS256",
		N:   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		E:   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
	}}}
	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jwks)
	}))
	defer jwksServer.Close()

	manager := &Manager{
		auth: config.AuthConfig{
			ExternalJWTIssuers: []config.ExternalJWTIssuerConfig{{
				Issuer:      issuer,
				Audience:    "islandora-scribe",
				JWKSURL:     jwksServer.URL,
				WorkspaceID: 42,
				RoleMappings: []config.ExternalJWTRoleMapping{{
					Roles: []string{"administrator"},
					Role:  "write",
				}},
			}},
		},
		jwksCache: make(map[string]cachedJWKS),
	}
	token := signTestJWT(t, key, "islandora-key", map[string]any{
		"iss":   issuer,
		"aud":   "islandora-scribe",
		"sub":   "islandora-user",
		"webid": "123",
		"roles": []string{"anonymous"},
		"iat":   time.Now().Unix(),
		"exp":   time.Now().Add(time.Hour).Unix(),
	})

	if _, err := manager.externalJWTPrincipal(context.Background(), token); err == nil || !strings.Contains(err.Error(), "roles") {
		t.Fatalf("expected role rejection, got %v", err)
	}
}

func TestParseJWTUserIDRejectsPartialNumericString(t *testing.T) {
	raw := json.RawMessage(`"123abc"`)
	if got := parseJWTUserID(raw); got != 0 {
		t.Fatalf("parseJWTUserID = %d, want 0", got)
	}
}

func signTestJWT(t *testing.T, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	header := map[string]any{"alg": "RS256", "typ": "JWT", "kid": kid}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	signingInput := fmt.Sprintf("%s.%s",
		base64.RawURLEncoding.EncodeToString(headerJSON),
		base64.RawURLEncoding.EncodeToString(claimsJSON),
	)
	sum := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}
