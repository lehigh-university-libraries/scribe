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
	"math"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/safehttp"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

const (
	externalJWTClockSkew  = 30 * time.Second
	maxJWKSResponseBytes  = 1 << 20
	maxJWKSKeys           = 64
	maxJWKKeyIDBytes      = 128
	minJWKRSAKeyBits      = 2048
	maxJWKRSAKeyBits      = 8192
	maxJWKExponentBytes   = 4
	maxJWKSNegativeMisses = 256
	jwksCacheTTL          = 10 * time.Minute
	jwksRefreshCooldown   = time.Minute
	jwksNegativeCacheTTL  = time.Minute
)

type cachedJWKS struct {
	keys         map[string]*rsa.PublicKey
	misses       map[string]time.Time
	expiresAt    time.Time
	refreshAfter time.Time
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
	Nbf   int64           `json:"nbf"`
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
	user, access, err := m.resolveExternalServiceIdentity(ctx, issuerCfg.ServiceUserID, issuerCfg.WorkspaceID)
	if err != nil {
		return Principal{}, fmt.Errorf("external jwt service principal is not authorized")
	}
	return Principal{
		UserID:             user.ID,
		Email:              user.Email,
		Name:               user.Name,
		PictureURL:         user.PictureURL,
		Authenticated:      true,
		AuthType:           "external_jwt",
		WorkspaceID:        access.Workspace.ID,
		WorkspaceName:      access.Workspace.Name,
		WorkspaceRole:      leastPrivilegedWorkspaceRole(role, access.Role),
		DefaultWorkspaceID: access.Workspace.ID,
		Scopes:             scopes,
	}, nil
}

// resolveExternalServiceIdentity binds an issuer to one explicitly configured
// internal service account and proves current membership on every token. Raw
// external subject/WebID values are never interpreted as Scribe foreign keys.
func (m *Manager) resolveExternalServiceIdentity(ctx context.Context, userID, workspaceID uint64) (store.User, store.WorkspaceAccess, error) {
	if userID == 0 || workspaceID == 0 {
		return store.User{}, store.WorkspaceAccess{}, fmt.Errorf("service user and workspace are required")
	}
	var (
		user   store.User
		access store.WorkspaceAccess
		err    error
	)
	if m.externalIdentityResolver != nil {
		user, access, err = m.externalIdentityResolver(ctx, userID, workspaceID)
	} else {
		if m.identities == nil {
			return store.User{}, store.WorkspaceAccess{}, fmt.Errorf("identity store is unavailable")
		}
		user, err = m.identities.GetUser(ctx, userID)
		if err == nil {
			access, err = m.identities.GetWorkspaceAccess(ctx, userID, workspaceID)
		}
	}
	if err != nil {
		return store.User{}, store.WorkspaceAccess{}, err
	}
	if user.ID != userID || access.Workspace.ID != workspaceID || normalizeExternalRole(access.Role) == "" {
		return store.User{}, store.WorkspaceAccess{}, fmt.Errorf("service principal identity mismatch")
	}
	return user, access, nil
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
	if strings.TrimSpace(claims.Sub) == "" {
		return fmt.Errorf("missing subject")
	}
	if claims.Exp == 0 || now.After(time.Unix(claims.Exp, 0).Add(externalJWTClockSkew)) {
		return fmt.Errorf("jwt expired")
	}
	if claims.Iat != 0 && time.Unix(claims.Iat, 0).After(now.Add(externalJWTClockSkew)) {
		return fmt.Errorf("jwt issued in the future")
	}
	if claims.Nbf != 0 && time.Unix(claims.Nbf, 0).After(now.Add(externalJWTClockSkew)) {
		return fmt.Errorf("jwt not yet valid")
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
	if err := validateExternalJWTTransportURL(jwksURL); err != nil {
		return nil, fmt.Errorf("invalid jwks url: %w", err)
	}
	kid = strings.TrimSpace(kid)
	if kid == "" || len(kid) > maxJWKKeyIDBytes || strings.IndexFunc(kid, unicode.IsControl) >= 0 {
		return nil, fmt.Errorf("jwt header kid is required")
	}
	now := time.Now().UTC()
	m.jwksMu.Lock()
	cached, ok := m.jwksCache[jwksURL]
	if ok {
		pruneJWKSNegativeMisses(cached.misses, now)
		m.jwksCache[jwksURL] = cached
	}
	if ok && now.Before(cached.expiresAt) {
		if key := cached.keys[kid]; key != nil {
			m.jwksMu.Unlock()
			return key, nil
		}
	}
	if ok {
		if until := cached.misses[kid]; now.Before(until) {
			m.jwksMu.Unlock()
			return nil, fmt.Errorf("jwt signing key not found")
		}
		if now.Before(cached.refreshAfter) {
			recordJWKSNegativeMiss(&cached, kid, now)
			m.jwksCache[jwksURL] = cached
			m.jwksMu.Unlock()
			return nil, fmt.Errorf("jwt signing key not found")
		}
	}
	m.jwksMu.Unlock()

	// Coalesce every refresh for one issuer URL. Keying this by kid permits an
	// attacker to fan out requests with distinct unknown key IDs and turn each
	// authentication attempt into a separate outbound JWKS fetch.
	_, err, _ := m.jwksRefresh.Do(jwksURL, func() (any, error) {
		refreshNow := time.Now().UTC()
		m.jwksMu.Lock()
		current, currentOK := m.jwksCache[jwksURL]
		if currentOK && refreshNow.Before(current.refreshAfter) {
			m.jwksMu.Unlock()
			return struct{}{}, nil
		}
		m.jwksMu.Unlock()

		keys, fetchErr := fetchJWKS(ctx, jwksURL)
		if fetchErr != nil {
			m.jwksMu.Lock()
			current = m.jwksCache[jwksURL]
			if current.misses == nil {
				current.misses = make(map[string]time.Time)
			}
			pruneJWKSNegativeMisses(current.misses, refreshNow)
			current.refreshAfter = time.Now().UTC().Add(jwksRefreshCooldown)
			m.jwksCache[jwksURL] = current
			m.jwksMu.Unlock()
			return nil, fetchErr
		}
		completedAt := time.Now().UTC()
		m.jwksMu.Lock()
		current = m.jwksCache[jwksURL]
		if current.misses == nil {
			current.misses = make(map[string]time.Time)
		}
		pruneJWKSNegativeMisses(current.misses, completedAt)
		for knownKid := range keys {
			delete(current.misses, knownKid)
		}
		current.keys = keys
		current.expiresAt = completedAt.Add(jwksCacheTTL)
		current.refreshAfter = completedAt.Add(jwksRefreshCooldown)
		m.jwksCache[jwksURL] = current
		m.jwksMu.Unlock()
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}

	lookupNow := time.Now().UTC()
	m.jwksMu.Lock()
	cached = m.jwksCache[jwksURL]
	if lookupNow.Before(cached.expiresAt) {
		if key := cached.keys[kid]; key != nil {
			m.jwksMu.Unlock()
			return key, nil
		}
	}
	recordJWKSNegativeMiss(&cached, kid, lookupNow)
	m.jwksCache[jwksURL] = cached
	m.jwksMu.Unlock()
	return nil, fmt.Errorf("jwt signing key not found")
}

func pruneJWKSNegativeMisses(misses map[string]time.Time, now time.Time) {
	for kid, until := range misses {
		if !now.Before(until) {
			delete(misses, kid)
		}
	}
}

func recordJWKSNegativeMiss(cached *cachedJWKS, kid string, now time.Time) {
	if cached == nil {
		return
	}
	if cached.misses == nil {
		cached.misses = make(map[string]time.Time)
	}
	pruneJWKSNegativeMisses(cached.misses, now)
	if _, exists := cached.misses[kid]; !exists && len(cached.misses) >= maxJWKSNegativeMisses {
		return
	}
	cached.misses[kid] = now.Add(jwksNegativeCacheTTL)
}

func fetchJWKS(ctx context.Context, jwksURL string) (map[string]*rsa.PublicKey, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, jwksURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := safehttp.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch jwks: %w", err)
	}
	defer resp.Body.Close()
	if resp.Request == nil || resp.Request.URL == nil {
		return nil, fmt.Errorf("fetch jwks: missing final URL")
	}
	if err := validateExternalJWTTransportURL(resp.Request.URL.String()); err != nil {
		return nil, fmt.Errorf("fetch jwks: insecure redirect target")
	}
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
	if len(decoded.Keys) == 0 || len(decoded.Keys) > maxJWKSKeys {
		return nil, fmt.Errorf("jwks key count must be between 1 and %d", maxJWKSKeys)
	}
	keys := make(map[string]*rsa.PublicKey, len(decoded.Keys))
	for _, jwk := range decoded.Keys {
		kid := strings.TrimSpace(jwk.Kid)
		if kid == "" || kid != jwk.Kid || len(kid) > maxJWKKeyIDBytes || strings.IndexFunc(kid, unicode.IsControl) >= 0 {
			return nil, fmt.Errorf("jwks contains an invalid key id")
		}
		if _, duplicate := keys[kid]; duplicate {
			return nil, fmt.Errorf("jwks contains duplicate key id")
		}
		key, err := jwkRSAPublicKey(jwk)
		if err != nil {
			return nil, fmt.Errorf("jwks key %q is invalid: %w", kid, err)
		}
		keys[kid] = key
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("jwks contains no usable rsa keys")
	}
	return keys, nil
}

func validateExternalJWTTransportURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.Opaque != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("absolute URL without credentials, query, or fragment is required")
	}
	if strings.EqualFold(parsed.Scheme, "https") {
		return nil
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if !strings.EqualFold(parsed.Scheme, "http") || (host != "localhost" && !isLoopbackIP(host)) {
		return fmt.Errorf("https is required except for a loopback development endpoint")
	}
	return nil
}

func isLoopbackIP(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func jwkRSAPublicKey(jwk jwkKey) (*rsa.PublicKey, error) {
	if !strings.EqualFold(strings.TrimSpace(jwk.Kty), "RSA") {
		return nil, fmt.Errorf("jwk kty is not rsa")
	}
	if strings.TrimSpace(jwk.Use) != "" && !strings.EqualFold(jwk.Use, "sig") {
		return nil, fmt.Errorf("jwk use is not sig")
	}
	if !strings.EqualFold(strings.TrimSpace(jwk.Alg), "RS256") {
		return nil, fmt.Errorf("jwk alg is not rs256")
	}
	if len(jwk.X5C) > 0 {
		if len(jwk.X5C) != 1 {
			return nil, fmt.Errorf("x5c must contain exactly one certificate")
		}
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
		if err := validateJWKRSAKey(key); err != nil {
			return nil, err
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
	if len(eBytes) == 0 || len(eBytes) > maxJWKExponentBytes {
		return nil, fmt.Errorf("invalid rsa exponent size")
	}
	e := uint64(0)
	for _, b := range eBytes {
		e = e<<8 + uint64(b)
	}
	if e < 3 || e > math.MaxInt32 || e%2 == 0 {
		return nil, fmt.Errorf("invalid rsa exponent")
	}
	key := &rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: int(e)}
	if err := validateJWKRSAKey(key); err != nil {
		return nil, err
	}
	return key, nil
}

func validateJWKRSAKey(key *rsa.PublicKey) error {
	if key == nil || key.N == nil || key.N.Sign() <= 0 {
		return fmt.Errorf("invalid rsa modulus")
	}
	bits := key.N.BitLen()
	if bits < minJWKRSAKeyBits || bits > maxJWKRSAKeyBits {
		return fmt.Errorf("rsa modulus must be between %d and %d bits", minJWKRSAKeyBits, maxJWKRSAKeyBits)
	}
	if key.E < 3 || key.E > math.MaxInt32 || key.E%2 == 0 {
		return fmt.Errorf("invalid rsa exponent")
	}
	return nil
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
