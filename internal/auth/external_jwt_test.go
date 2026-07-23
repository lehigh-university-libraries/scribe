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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/safehttp"
	"github.com/lehigh-university-libraries/scribe/internal/store"
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
				Issuer:        issuer,
				Audience:      "islandora-scribe",
				JWKSURL:       jwksServer.URL,
				WorkspaceID:   42,
				ServiceUserID: 900,
				RoleMappings: []config.ExternalJWTRoleMapping{{
					Roles:  []string{"administrator"},
					Role:   "write",
					Scopes: []string{"items:create"},
				}},
			}},
		},
		jwksCache: make(map[string]cachedJWKS),
		externalIdentityResolver: func(_ context.Context, userID, workspaceID uint64) (store.User, store.WorkspaceAccess, error) {
			if userID != 900 || workspaceID != 42 {
				t.Fatalf("service identity lookup = user %d workspace %d, want 900/42", userID, workspaceID)
			}
			return store.User{ID: 900, Name: "Islandora service", Email: "islandora@example.org"}, store.WorkspaceAccess{
				Workspace: store.Workspace{ID: 42, Name: "Islandora imports"}, Role: "write",
			}, nil
		},
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
	if principal.UserID != 900 {
		t.Fatalf("UserID = %d, want configured service user 900; raw webid 123 must never become a Scribe foreign key", principal.UserID)
	}
	if !principalHasPermission(principal, "items:create") {
		t.Fatal("mapped external JWT lost its explicitly delegated items:create scope")
	}
	for _, permission := range []string{"items:write", "annotations:write", "admin:api_keys"} {
		if principalHasPermission(principal, permission) {
			t.Fatalf("mapped external JWT exceeded its scopes for %q", permission)
		}
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
		"sub": "service",
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
		Sub: "service",
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
		Sub: "service",
		Aud: json.RawMessage(`"scribe"`),
		Exp: time.Now().Add(-31 * time.Second).Unix(),
	}, cfg); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected expired token after 30s skew, got %v", err)
	}
	if err := validateExternalClaims(externalJWTClaims{
		Iss: "https://issuer.example",
		Sub: "service",
		Aud: json.RawMessage(`"scribe"`),
		Exp: time.Now().Add(time.Hour).Unix(),
		Iat: time.Now().Add(31 * time.Second).Unix(),
	}, cfg); err == nil || !strings.Contains(err.Error(), "future") {
		t.Fatalf("expected future iat after 30s skew, got %v", err)
	}
}

func TestValidateExternalClaimsRequiresSubject(t *testing.T) {
	err := validateExternalClaims(externalJWTClaims{
		Iss: "https://issuer.example",
		Exp: time.Now().Add(time.Hour).Unix(),
	}, config.ExternalJWTIssuerConfig{})
	if err == nil || !strings.Contains(err.Error(), "subject") {
		t.Fatalf("validateExternalClaims returned %v, want subject rejection", err)
	}
}

func TestJWKRSAPublicKeyBoundsKeyMaterial(t *testing.T) {
	validExponent := base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1})
	tests := map[string]jwkKey{
		"weak modulus": {
			Kty: "RSA", Alg: "RS256",
			N: base64.RawURLEncoding.EncodeToString(new(big.Int).Lsh(big.NewInt(1), 1023).Bytes()),
			E: validExponent,
		},
		"oversized modulus": {
			Kty: "RSA", Alg: "RS256",
			N: base64.RawURLEncoding.EncodeToString(new(big.Int).Lsh(big.NewInt(1), maxJWKRSAKeyBits).Bytes()),
			E: validExponent,
		},
		"oversized exponent": {
			Kty: "RSA", Alg: "RS256",
			N: base64.RawURLEncoding.EncodeToString(new(big.Int).Lsh(big.NewInt(1), 2047).Bytes()),
			E: base64.RawURLEncoding.EncodeToString([]byte{1, 0, 0, 0, 1}),
		},
		"even exponent": {
			Kty: "RSA", Alg: "RS256",
			N: base64.RawURLEncoding.EncodeToString(new(big.Int).Lsh(big.NewInt(1), 2047).Bytes()),
			E: base64.RawURLEncoding.EncodeToString([]byte{4}),
		},
	}
	for name, key := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := jwkRSAPublicKey(key); err == nil {
				t.Fatal("invalid RSA key was accepted")
			}
		})
	}
}

func TestFetchJWKSRejectsInvalidCatalogShape(t *testing.T) {
	t.Setenv(safehttp.AllowPrivateFetchesEnv, "true")
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	valid := jwkKey{
		Kty: "RSA", Kid: "key-1", Alg: "RS256",
		N: base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		E: base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
	}
	for name, keys := range map[string][]jwkKey{
		"blank kid": {func() jwkKey { copy := valid; copy.Kid = ""; return copy }()},
		"duplicate": {valid, valid},
		"too many":  make([]jwkKey, maxJWKSKeys+1),
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(jwksResponse{Keys: keys})
			}))
			defer server.Close()
			if _, err := fetchJWKS(context.Background(), server.URL); err == nil {
				t.Fatal("invalid JWKS catalog was accepted")
			}
		})
	}
}

func TestJWKSKeyMissRefreshesOnceForConcurrentCallers(t *testing.T) {
	t.Setenv(safehttp.AllowPrivateFetchesEnv, "true")
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_ = json.NewEncoder(w).Encode(jwksResponse{Keys: []jwkKey{{
			Kty: "RSA", Kid: "rotated", Alg: "RS256",
			N: base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			E: base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	}))
	defer server.Close()
	manager := &Manager{jwksCache: map[string]cachedJWKS{
		server.URL: {keys: map[string]*rsa.PublicKey{"old": &key.PublicKey}, misses: make(map[string]time.Time), expiresAt: time.Now().Add(time.Hour)},
	}}
	cfg := config.ExternalJWTIssuerConfig{JWKSURL: server.URL}
	start := make(chan struct{})
	var wait sync.WaitGroup
	errors := make(chan error, 20)
	for range 20 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, keyErr := manager.jwksKey(context.Background(), cfg, "rotated")
			errors <- keyErr
		}()
	}
	close(start)
	wait.Wait()
	close(errors)
	for keyErr := range errors {
		if keyErr != nil {
			t.Fatalf("jwksKey: %v", keyErr)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("JWKS refresh requests = %d, want 1", got)
	}
}

func TestJWKSKeyDistinctMissesShareURLRefreshAndBoundNegativeCache(t *testing.T) {
	t.Setenv(safehttp.AllowPrivateFetchesEnv, "true")
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	releaseResponses := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseResponses) }) }
	defer release()
	firstRequest := make(chan struct{})
	secondRequest := make(chan struct{})
	var firstOnce, secondOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestNumber := requests.Add(1)
		if requestNumber == 1 {
			firstOnce.Do(func() { close(firstRequest) })
		} else {
			secondOnce.Do(func() { close(secondRequest) })
		}
		<-releaseResponses
		_ = json.NewEncoder(w).Encode(jwksResponse{Keys: []jwkKey{{
			Kty: "RSA", Kid: "current", Alg: "RS256",
			N: base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			E: base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	}))
	defer server.Close()
	manager := &Manager{jwksCache: map[string]cachedJWKS{
		server.URL: {keys: map[string]*rsa.PublicKey{"old": &key.PublicKey}, misses: make(map[string]time.Time), expiresAt: time.Now().Add(time.Hour)},
	}}
	cfg := config.ExternalJWTIssuerConfig{JWKSURL: server.URL}
	start := make(chan struct{})
	var wait sync.WaitGroup
	var callersReady sync.WaitGroup
	callersReady.Add(32)
	errorsSeen := make(chan error, 32)
	for index := range 32 {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			callersReady.Done()
			_, keyErr := manager.jwksKey(context.Background(), cfg, fmt.Sprintf("attacker-%d", index))
			errorsSeen <- keyErr
		}()
	}
	close(start)
	callersReady.Wait()
	select {
	case <-firstRequest:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the coalesced JWKS refresh")
	}
	secondFetchStarted := false
	select {
	case <-secondRequest:
		secondFetchStarted = true
	case <-time.After(100 * time.Millisecond):
	}
	release()
	wait.Wait()
	close(errorsSeen)
	for keyErr := range errorsSeen {
		if keyErr == nil {
			t.Fatal("unknown key ID unexpectedly resolved")
		}
	}
	if got := requests.Load(); secondFetchStarted || got != 1 {
		t.Fatalf("concurrent distinct-kid JWKS requests = %d, want 1", got)
	}
	if _, err := manager.jwksKey(context.Background(), cfg, "current"); err != nil {
		t.Fatalf("refreshed known key is unavailable: %v", err)
	}

	// Alternating attacker-controlled key IDs must neither clear prior misses
	// nor grow the per-issuer cache without bound during the refresh cooldown.
	for index := 0; index < maxJWKSNegativeMisses*2; index++ {
		_, _ = manager.jwksKey(context.Background(), cfg, fmt.Sprintf("alternating-%d", index))
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("alternating distinct-kid JWKS requests = %d, want 1 during cooldown", got)
	}
	manager.jwksMu.Lock()
	cached := manager.jwksCache[server.URL]
	missCount := len(cached.misses)
	refreshAfter := cached.refreshAfter
	manager.jwksMu.Unlock()
	if missCount > maxJWKSNegativeMisses {
		t.Fatalf("negative JWKS cache size = %d, maximum %d", missCount, maxJWKSNegativeMisses)
	}
	if !time.Now().UTC().Before(refreshAfter) {
		t.Fatal("successful JWKS refresh did not establish a cooldown")
	}
}

func TestJWKSRefreshFailureEstablishesCooldown(t *testing.T) {
	t.Setenv(safehttp.AllowPrivateFetchesEnv, "true")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	manager := &Manager{jwksCache: make(map[string]cachedJWKS)}
	cfg := config.ExternalJWTIssuerConfig{JWKSURL: server.URL}
	if _, err := manager.jwksKey(context.Background(), cfg, "unknown-a"); err == nil {
		t.Fatal("failed JWKS refresh unexpectedly resolved a key")
	}
	if _, err := manager.jwksKey(context.Background(), cfg, "unknown-b"); err == nil {
		t.Fatal("cooldown lookup unexpectedly resolved a key")
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("failed JWKS refresh requests = %d, want 1 during cooldown", got)
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

func TestExternalJWTTransportRejectsPlaintextRemoteKeys(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"http://identity.example/keys",
		"https://identity.example/keys?token=secret",
		"https://user:pass@identity.example/keys",
	} {
		if err := validateExternalJWTTransportURL(raw); err == nil {
			t.Fatalf("validateExternalJWTTransportURL(%q) accepted an unsafe URL", raw)
		}
	}
	for _, raw := range []string{
		"https://identity.example/keys",
		"http://127.0.0.1:8080/keys",
		"http://[::1]:8080/keys",
		"http://localhost:8080/keys",
	} {
		if err := validateExternalJWTTransportURL(raw); err != nil {
			t.Fatalf("validateExternalJWTTransportURL(%q) = %v", raw, err)
		}
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
