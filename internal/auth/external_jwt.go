package auth

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/safehttp"
)

const (
	externalJWTClockSkew = 30 * time.Second
	maxJWKSResponseBytes = 1 << 20
)

type cachedJWKS struct {
	keys      map[string]*rsa.PublicKey
	expiresAt time.Time
}

type jwtHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	Typ string `json:"typ"`
}

type externalJWTClaims struct {
	Iss   string          `json:"iss"`
	Sub   string          `json:"sub"`
	Aud   json.RawMessage `json:"aud"`
	Exp   int64           `json:"exp"`
	Iat   int64           `json:"iat"`
	WebID json.RawMessage `json:"webid"`
	Roles []string        `json:"roles"`
}

type jwksResponse struct {
	Keys []jwkKey `json:"keys"`
}

type jwkKey struct {
	Kty string   `json:"kty"`
	Kid string   `json:"kid"`
	Use string   `json:"use"`
	Alg string   `json:"alg"`
	N   string   `json:"n"`
	E   string   `json:"e"`
	X5C []string `json:"x5c"`
}

func (m *Manager) externalJWTPrincipal(ctx context.Context, rawToken string) (Principal, error) {
	header, claims, signingInput, signature, err := parseJWT(rawToken)
	if err != nil {
		return Principal{}, err
	}
	issuerCfg, ok := m.externalIssuerConfig(claims.Iss)
	if !ok {
		return Principal{}, fmt.Errorf("untrusted jwt issuer")
	}
	if err := validateExternalClaims(claims, issuerCfg); err != nil {
		return Principal{}, err
	}
	key, err := m.jwksKey(ctx, issuerCfg, header.Kid)
	if err != nil {
		return Principal{}, err
	}
	if err := verifyJWTSignature(header, key, signingInput, signature); err != nil {
		return Principal{}, err
	}
	role, scopes, err := externalJWTAccess(claims.Roles, issuerCfg)
	if err != nil {
		return Principal{}, err
	}
	userID := parseJWTUserID(claims.WebID)
	if userID == 0 {
		return Principal{}, fmt.Errorf("missing numeric webid claim")
	}
	return Principal{
		UserID:             userID,
		Name:               firstNonEmpty(claims.Sub, fmt.Sprintf("%s#%d", claims.Iss, userID)),
		Authenticated:      true,
		AuthType:           "external_jwt",
		WorkspaceID:        issuerCfg.WorkspaceID,
		WorkspaceRole:      role,
		DefaultWorkspaceID: issuerCfg.WorkspaceID,
		Scopes:             scopes,
	}, nil
}

func (m *Manager) externalIssuerConfig(issuer string) (config.ExternalJWTIssuerConfig, bool) {
	issuer = strings.TrimRight(strings.TrimSpace(issuer), "/")
	for _, cfg := range m.auth.ExternalJWTIssuers {
		if strings.TrimRight(strings.TrimSpace(cfg.Issuer), "/") == issuer && cfg.WorkspaceID > 0 {
			cfg.Issuer = issuer
			if strings.TrimSpace(cfg.JWKSURL) == "" {
				cfg.JWKSURL = issuer + "/oauth/discovery/keys"
			}
			return cfg, true
		}
	}
	return config.ExternalJWTIssuerConfig{}, false
}

func parseJWT(rawToken string) (jwtHeader, externalJWTClaims, string, []byte, error) {
	parts := strings.Split(rawToken, ".")
	if len(parts) != 3 {
		return jwtHeader{}, externalJWTClaims{}, "", nil, fmt.Errorf("invalid jwt")
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return jwtHeader{}, externalJWTClaims{}, "", nil, fmt.Errorf("decode jwt header: %w", err)
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return jwtHeader{}, externalJWTClaims{}, "", nil, fmt.Errorf("decode jwt claims: %w", err)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return jwtHeader{}, externalJWTClaims{}, "", nil, fmt.Errorf("decode jwt signature: %w", err)
	}
	var header jwtHeader
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return jwtHeader{}, externalJWTClaims{}, "", nil, fmt.Errorf("parse jwt header: %w", err)
	}
	var claims externalJWTClaims
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return jwtHeader{}, externalJWTClaims{}, "", nil, fmt.Errorf("parse jwt claims: %w", err)
	}
	return header, claims, parts[0] + "." + parts[1], signature, nil
}

func validateExternalClaims(claims externalJWTClaims, cfg config.ExternalJWTIssuerConfig) error {
	now := time.Now().UTC()
	if strings.TrimSpace(claims.Iss) == "" {
		return fmt.Errorf("missing issuer")
	}
	if claims.Exp == 0 || now.After(time.Unix(claims.Exp, 0).Add(externalJWTClockSkew)) {
		return fmt.Errorf("jwt expired")
	}
	if claims.Iat != 0 && time.Unix(claims.Iat, 0).After(now.Add(externalJWTClockSkew)) {
		return fmt.Errorf("jwt issued in the future")
	}
	if aud := strings.TrimSpace(cfg.Audience); aud != "" && !claimHasAudience(claims.Aud, aud) {
		return fmt.Errorf("jwt audience mismatch")
	}
	return nil
}

func externalJWTAccess(claimRoles []string, cfg config.ExternalJWTIssuerConfig) (string, []string, error) {
	normalizedClaims := make(map[string]struct{}, len(claimRoles))
	for _, role := range claimRoles {
		role = strings.ToLower(strings.TrimSpace(role))
		if role != "" {
			normalizedClaims[role] = struct{}{}
		}
	}
	for _, mapping := range cfg.RoleMappings {
		if !roleListIntersects(normalizedClaims, mapping.Roles) {
			continue
		}
		role := normalizeExternalRole(mapping.Role)
		if role == "" {
			return "", nil, fmt.Errorf("invalid external jwt mapped role")
		}
		return role, mapping.Scopes, nil
	}
	if len(cfg.RoleMappings) > 0 {
		return "", nil, fmt.Errorf("jwt roles are not allowed")
	}
	if !roleListIntersects(normalizedClaims, cfg.RequiredRoles) {
		return "", nil, fmt.Errorf("jwt roles are not allowed")
	}
	role := normalizeExternalRole(cfg.Role)
	if role == "" {
		role = "read"
	}
	return role, cfg.Scopes, nil
}

func roleListIntersects(claims map[string]struct{}, required []string) bool {
	if len(required) == 0 {
		return false
	}
	for _, role := range required {
		if _, ok := claims[strings.ToLower(strings.TrimSpace(role))]; ok {
			return true
		}
	}
	return false
}

func normalizeExternalRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "admin", "write", "create", "read":
		return strings.ToLower(strings.TrimSpace(role))
	default:
		return ""
	}
}

func claimHasAudience(raw json.RawMessage, want string) bool {
	want = strings.TrimSpace(want)
	if want == "" || len(raw) == 0 {
		return false
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return single == want
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err == nil {
		for _, got := range many {
			if got == want {
				return true
			}
		}
	}
	return false
}

func (m *Manager) jwksKey(ctx context.Context, cfg config.ExternalJWTIssuerConfig, kid string) (*rsa.PublicKey, error) {
	jwksURL := strings.TrimSpace(cfg.JWKSURL)
	if _, err := url.ParseRequestURI(jwksURL); err != nil {
		return nil, fmt.Errorf("invalid jwks url: %w", err)
	}
	kid = strings.TrimSpace(kid)
	if kid == "" {
		return nil, fmt.Errorf("jwt header kid is required")
	}
	m.jwksMu.Lock()
	cached, ok := m.jwksCache[jwksURL]
	if ok && time.Now().UTC().Before(cached.expiresAt) {
		key := cached.keys[kid]
		m.jwksMu.Unlock()
		if key != nil {
			return key, nil
		}
		return nil, fmt.Errorf("jwt signing key not found")
	}
	m.jwksMu.Unlock()

	keys, err := fetchJWKS(ctx, jwksURL)
	if err != nil {
		return nil, err
	}
	m.jwksMu.Lock()
	m.jwksCache[jwksURL] = cachedJWKS{keys: keys, expiresAt: time.Now().UTC().Add(10 * time.Minute)}
	key := keys[kid]
	m.jwksMu.Unlock()
	if key == nil {
		return nil, fmt.Errorf("jwt signing key not found")
	}
	return key, nil
}

func fetchJWKS(ctx context.Context, jwksURL string) (map[string]*rsa.PublicKey, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, jwksURL, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch jwks: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch jwks: status %d", resp.StatusCode)
	}
	body, err := safehttp.ReadAllLimit(resp.Body, maxJWKSResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("read jwks: %w", err)
	}
	var decoded jwksResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("decode jwks: %w", err)
	}
	keys := make(map[string]*rsa.PublicKey, len(decoded.Keys))
	for i, jwk := range decoded.Keys {
		key, err := jwkRSAPublicKey(jwk)
		if err != nil {
			continue
		}
		kid := strings.TrimSpace(jwk.Kid)
		if kid == "" {
			kid = fmt.Sprintf("key-%d", i)
		}
		keys[kid] = key
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("jwks contains no usable rsa keys")
	}
	return keys, nil
}

func jwkRSAPublicKey(jwk jwkKey) (*rsa.PublicKey, error) {
	if len(jwk.X5C) > 0 {
		certBytes, err := base64.StdEncoding.DecodeString(jwk.X5C[0])
		if err != nil {
			return nil, err
		}
		cert, err := x509.ParseCertificate(certBytes)
		if err != nil {
			return nil, err
		}
		key, ok := cert.PublicKey.(*rsa.PublicKey)
		if !ok {
			return nil, fmt.Errorf("x5c key is not rsa")
		}
		return key, nil
	}
	nBytes, err := base64.RawURLEncoding.DecodeString(jwk.N)
	if err != nil {
		return nil, err
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(jwk.E)
	if err != nil {
		return nil, err
	}
	e := 0
	for _, b := range eBytes {
		e = e<<8 + int(b)
	}
	if e == 0 {
		return nil, fmt.Errorf("invalid rsa exponent")
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: e}, nil
}

func verifyJWTSignature(header jwtHeader, key *rsa.PublicKey, signingInput string, signature []byte) error {
	if header.Alg != "RS256" {
		return fmt.Errorf("unsupported jwt alg %q", header.Alg)
	}
	sum := sha256.Sum256([]byte(signingInput))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], signature); err != nil {
		return fmt.Errorf("verify jwt signature: %w", err)
	}
	return nil
}

func parseJWTUserID(raw json.RawMessage) uint64 {
	if len(raw) == 0 {
		return 0
	}
	var asNumber uint64
	if err := json.Unmarshal(raw, &asNumber); err == nil {
		return asNumber
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		parsed, _ := strconv.ParseUint(strings.TrimSpace(asString), 10, 64)
		return parsed
	}
	return 0
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
