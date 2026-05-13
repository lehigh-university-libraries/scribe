package vaultkv

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	defaultADCFile      = "/run/secrets/GOOGLE_APPLICATION_CREDENTIALS"
	defaultGCPAuthMount = "gcp"
	defaultGCPAuthRole  = "scribe-app"
	adminTokenScope     = "https://www.googleapis.com/auth/userinfo.email"
	maxRetryAttempts    = 5
	initialRetryDelay   = 250 * time.Millisecond
	maxRetryDelay       = 4 * time.Second
)

type Client struct {
	addr        string
	staticToken string
	kvMount     string
	gcpAuthRole string
	http        *http.Client
	now         func() time.Time

	mu               sync.Mutex
	cachedToken      string
	expiresAt        time.Time
	renewable        bool
	adminTokenSource oauth2.TokenSource
}

func New(addr, token, kvMount, gcpAuthRole string) *Client {
	gcpAuthRole = strings.TrimSpace(gcpAuthRole)
	if gcpAuthRole == "" {
		gcpAuthRole = defaultGCPAuthRole
	}
	return &Client{
		addr:        strings.TrimRight(strings.TrimSpace(addr), "/"),
		staticToken: strings.TrimSpace(token),
		kvMount:     strings.TrimSpace(kvMount),
		gcpAuthRole: gcpAuthRole,
		http:        &http.Client{Timeout: 10 * time.Second},
		now:         time.Now,
	}
}

func (c *Client) Read(ctx context.Context, path string) (map[string]string, error) {
	if c == nil || c.addr == "" || c.kvMount == "" {
		return nil, fmt.Errorf("vault client is not configured")
	}
	if strings.TrimSpace(path) == "" {
		return map[string]string{}, nil
	}
	var data map[string]string
	err := c.retry(ctx, func() error {
		token, err := c.authToken(ctx)
		if err != nil {
			return err
		}
		data, err = c.readV2(ctx, token, path)
		return err
	})
	if err != nil {
		return nil, err
	}
	return data, nil
}

func (c *Client) readV2(ctx context.Context, token, path string) (map[string]string, error) {
	endpoint := fmt.Sprintf("%s/v1/%s/data/%s", c.addr, url.PathEscape(c.kvMount), pathEscape(path))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Vault-Token", token)
	if err := c.addAdminHeader(ctx, req); err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, vaultStatusError{operation: "read", path: path, statusCode: resp.StatusCode, body: string(body)}
	}

	var parsed struct {
		Data struct {
			Data map[string]any `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parse vault response: %w", err)
	}
	out := make(map[string]string, len(parsed.Data.Data))
	for key, value := range parsed.Data.Data {
		switch typed := value.(type) {
		case string:
			out[key] = typed
		default:
			raw, _ := json.Marshal(typed)
			out[key] = string(raw)
		}
	}
	return out, nil
}

func (c *Client) Write(ctx context.Context, path string, data map[string]string) error {
	if c == nil || c.addr == "" || c.kvMount == "" {
		return fmt.Errorf("vault client is not configured")
	}
	token, err := c.authToken(ctx)
	if err != nil {
		return err
	}
	return c.writeV2(ctx, token, path, data)
}

func (c *Client) writeV2(ctx context.Context, token, path string, data map[string]string) error {
	payload, err := json.Marshal(map[string]any{
		"data": data,
	})
	if err != nil {
		return fmt.Errorf("marshal vault payload: %w", err)
	}
	endpoint := fmt.Sprintf("%s/v1/%s/data/%s", c.addr, url.PathEscape(c.kvMount), pathEscape(path))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Vault-Token", token)
	if err := c.addAdminHeader(ctx, req); err != nil {
		return err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return vaultStatusError{operation: "write", path: path, statusCode: resp.StatusCode, body: string(body)}
	}
	return nil
}

func (c *Client) Delete(ctx context.Context, path string) error {
	if c == nil || c.addr == "" || c.kvMount == "" {
		return fmt.Errorf("vault client is not configured")
	}
	token, err := c.authToken(ctx)
	if err != nil {
		return err
	}
	return c.deleteV2(ctx, token, path)
}

func (c *Client) deleteV2(ctx context.Context, token, path string) error {
	endpoint := fmt.Sprintf("%s/v1/%s/metadata/%s", c.addr, url.PathEscape(c.kvMount), pathEscape(path))
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Vault-Token", token)
	if err := c.addAdminHeader(ctx, req); err != nil {
		return err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return vaultStatusError{operation: "delete", path: path, statusCode: resp.StatusCode, body: string(body)}
	}
	return nil
}

type vaultStatusError struct {
	operation  string
	path       string
	statusCode int
	body       string
}

func (e vaultStatusError) Error() string {
	return fmt.Sprintf("vault %s %s: status %d: %s", e.operation, e.path, e.statusCode, e.body)
}

func IsNotFound(err error) bool {
	var statusErr vaultStatusError
	return errors.As(err, &statusErr) && statusErr.statusCode == http.StatusNotFound
}

func IsRetryable(err error) bool {
	return isRetryable(err)
}

func (c *Client) authToken(ctx context.Context) (string, error) {
	if c.staticToken != "" {
		return c.staticToken, nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cachedToken != "" && c.now().Add(30*time.Second).Before(c.expiresAt) {
		return c.cachedToken, nil
	}
	if c.cachedToken != "" && c.renewable {
		token, expiresAt, renewable, err := c.renewSelf(ctx, c.cachedToken)
		if err == nil && token != "" && c.now().Add(30*time.Second).Before(expiresAt) {
			c.cachedToken = token
			c.expiresAt = expiresAt
			c.renewable = renewable
			return c.cachedToken, nil
		}
	}

	token, expiresAt, renewable, err := c.loginWithGCP(ctx)
	if err != nil {
		return "", err
	}
	c.cachedToken = token
	c.expiresAt = expiresAt
	c.renewable = renewable
	return token, nil
}

func (c *Client) loginWithGCP(ctx context.Context) (string, time.Time, bool, error) {
	jwt, err := c.signedJWTFromCredentials()
	if err != nil {
		credErr := err
		jwt, err = c.signedJWTFromMetadata(ctx)
		if err != nil {
			return "", time.Time{}, false, fmt.Errorf("vault gcp login: credentials auth failed: %v; metadata auth failed: %w", credErr, err)
		}
	}

	payload, err := json.Marshal(map[string]string{
		"role": c.gcpAuthRole,
		"jwt":  jwt,
	})
	if err != nil {
		return "", time.Time{}, false, fmt.Errorf("marshal vault gcp login: %w", err)
	}

	endpoint := fmt.Sprintf("%s/v1/auth/%s/login", c.addr, defaultGCPAuthMount)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", time.Time{}, false, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", time.Time{}, false, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", time.Time{}, false, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", time.Time{}, false, vaultStatusError{
			operation:  "gcp login",
			path:       "auth/gcp/login",
			statusCode: resp.StatusCode,
			body:       string(body),
		}
	}

	var parsed struct {
		Auth struct {
			ClientToken   string `json:"client_token"`
			LeaseDuration int    `json:"lease_duration"`
			Renewable     bool   `json:"renewable"`
		} `json:"auth"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", time.Time{}, false, fmt.Errorf("parse vault login response: %w", err)
	}
	if strings.TrimSpace(parsed.Auth.ClientToken) == "" {
		return "", time.Time{}, false, fmt.Errorf("vault login response missing client_token")
	}
	lease := time.Duration(parsed.Auth.LeaseDuration) * time.Second
	if lease <= 0 {
		lease = 5 * time.Minute
	}
	return parsed.Auth.ClientToken, c.now().Add(lease), parsed.Auth.Renewable, nil
}

func (c *Client) addAdminHeader(ctx context.Context, req *http.Request) error {
	token, err := c.adminAccessToken(ctx)
	if err != nil {
		if c.staticToken != "" {
			return nil
		}
		return fmt.Errorf("mint vault admin access token: %w", err)
	}
	req.Header.Set("X-Admin-Token", token)
	return nil
}

func (c *Client) adminAccessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	source := c.adminTokenSource
	c.mu.Unlock()

	if source == nil {
		creds, err := google.FindDefaultCredentials(ctx, adminTokenScope)
		if err != nil {
			return "", err
		}
		source = creds.TokenSource
		c.mu.Lock()
		if c.adminTokenSource == nil {
			c.adminTokenSource = source
		} else {
			source = c.adminTokenSource
		}
		c.mu.Unlock()
	}

	token, err := source.Token()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(token.AccessToken) == "" {
		return "", fmt.Errorf("google access token was empty")
	}
	return token.AccessToken, nil
}

func (c *Client) renewSelf(ctx context.Context, token string) (string, time.Time, bool, error) {
	endpoint := fmt.Sprintf("%s/v1/auth/token/renew-self", c.addr)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader([]byte(`{}`)))
	if err != nil {
		return "", time.Time{}, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Vault-Token", token)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", time.Time{}, false, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", time.Time{}, false, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", time.Time{}, false, vaultStatusError{
			operation:  "token renew",
			path:       "auth/token/renew-self",
			statusCode: resp.StatusCode,
			body:       string(body),
		}
	}

	var parsed struct {
		Auth struct {
			ClientToken   string `json:"client_token"`
			LeaseDuration int    `json:"lease_duration"`
			Renewable     bool   `json:"renewable"`
		} `json:"auth"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", time.Time{}, false, fmt.Errorf("parse vault renew response: %w", err)
	}
	if strings.TrimSpace(parsed.Auth.ClientToken) == "" {
		parsed.Auth.ClientToken = token
	}
	lease := time.Duration(parsed.Auth.LeaseDuration) * time.Second
	if lease <= 0 {
		lease = 5 * time.Minute
	}
	return parsed.Auth.ClientToken, c.now().Add(lease), parsed.Auth.Renewable, nil
}

func (c *Client) signedJWTFromCredentials() (string, error) {
	path := strings.TrimSpace(os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"))
	if path == "" {
		path = defaultADCFile
	}
	// Vault IAM auth signs only the compose-mounted credential file so local
	// overrides cannot silently widen which service account can mint tokens.
	if filepath.Clean(path) != defaultADCFile {
		return "", fmt.Errorf("GOOGLE_APPLICATION_CREDENTIALS must point to %s", defaultADCFile)
	}
	raw, err := os.ReadFile(defaultADCFile)
	if err != nil {
		return "", err
	}

	var creds struct {
		ClientEmail  string `json:"client_email"`
		PrivateKey   string `json:"private_key"`
		PrivateKeyID string `json:"private_key_id"`
	}
	if err := json.Unmarshal(raw, &creds); err != nil {
		return "", fmt.Errorf("parse service account credentials: %w", err)
	}
	return signServiceAccountJWT(creds.ClientEmail, creds.PrivateKey, creds.PrivateKeyID, c.gcpAuthRole, c.now())
}

func (c *Client) signedJWTFromMetadata(ctx context.Context) (string, error) {
	accessToken, err := metadataValue(ctx, c.http, "instance/service-accounts/default/token")
	if err != nil {
		return "", fmt.Errorf("read metadata access token: %w", err)
	}
	var tokenResponse struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal([]byte(accessToken), &tokenResponse); err != nil {
		return "", fmt.Errorf("parse metadata access token: %w", err)
	}
	if strings.TrimSpace(tokenResponse.AccessToken) == "" {
		return "", fmt.Errorf("metadata access token missing access_token")
	}

	serviceAccountEmail, err := metadataValue(ctx, c.http, "instance/service-accounts/default/email")
	if err != nil {
		return "", fmt.Errorf("read metadata service account email: %w", err)
	}
	serviceAccountEmail = strings.TrimSpace(serviceAccountEmail)
	if serviceAccountEmail == "" {
		return "", fmt.Errorf("metadata service account email is empty")
	}

	claims, err := json.Marshal(jwtClaims(serviceAccountEmail, c.gcpAuthRole, c.now()))
	if err != nil {
		return "", fmt.Errorf("marshal jwt claims: %w", err)
	}
	payload, err := json.Marshal(map[string]string{
		"payload": string(claims),
	})
	if err != nil {
		return "", fmt.Errorf("marshal iam signJwt payload: %w", err)
	}
	endpoint := fmt.Sprintf("https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/%s:signJwt", url.PathEscape(serviceAccountEmail))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+tokenResponse.AccessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("iam signJwt status %d: %s", resp.StatusCode, string(body))
	}

	var parsed struct {
		SignedJWT string `json:"signedJwt"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("parse iam signJwt response: %w", err)
	}
	if strings.TrimSpace(parsed.SignedJWT) == "" {
		return "", fmt.Errorf("iam signJwt response missing signedJwt")
	}
	return parsed.SignedJWT, nil
}

func signServiceAccountJWT(serviceAccountEmail, privateKeyPEM, privateKeyID, role string, now time.Time) (string, error) {
	serviceAccountEmail = strings.TrimSpace(serviceAccountEmail)
	privateKeyPEM = strings.TrimSpace(privateKeyPEM)
	privateKeyID = strings.TrimSpace(privateKeyID)
	role = strings.TrimSpace(role)
	if serviceAccountEmail == "" || privateKeyPEM == "" || privateKeyID == "" {
		return "", fmt.Errorf("service account credentials are incomplete")
	}
	if role == "" {
		role = defaultGCPAuthRole
	}

	privateKey, err := parseRSAPrivateKey(privateKeyPEM)
	if err != nil {
		return "", err
	}

	header, err := json.Marshal(map[string]string{
		"alg": "RS256",
		"kid": privateKeyID,
		"typ": "JWT",
	})
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(jwtClaims(serviceAccountEmail, role, now))
	if err != nil {
		return "", err
	}

	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	sum := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("sign jwt: %w", err)
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func jwtClaims(serviceAccountEmail, role string, now time.Time) map[string]any {
	role = strings.TrimSpace(role)
	if role == "" {
		role = defaultGCPAuthRole
	}
	return map[string]any{
		"aud": "vault/" + role,
		"exp": now.Add(10 * time.Minute).Unix(),
		"iat": now.Unix(),
		"iss": serviceAccountEmail,
		"sub": serviceAccountEmail,
	}
}

func parseRSAPrivateKey(privateKeyPEM string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(privateKeyPEM))
	if block == nil {
		return nil, fmt.Errorf("decode private key pem")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	keyAny, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	key, ok := keyAny.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key is not RSA")
	}
	return key, nil
}

func metadataValue(ctx context.Context, client *http.Client, path string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://metadata.google.internal/computeMetadata/v1/"+strings.TrimPrefix(path, "/"), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Metadata-Flavor", "Google")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("metadata status %d: %s", resp.StatusCode, string(body))
	}
	return string(body), nil
}

func (c *Client) retry(ctx context.Context, fn func() error) error {
	delay := initialRetryDelay
	for attempt := 1; attempt <= maxRetryAttempts; attempt++ {
		err := fn()
		if err == nil {
			return nil
		}
		if !isRetryable(err) || attempt == maxRetryAttempts {
			return err
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		delay *= 2
		if delay > maxRetryDelay {
			delay = maxRetryDelay
		}
	}
	return nil
}

func isRetryable(err error) bool {
	var statusErr vaultStatusError
	if !errors.As(err, &statusErr) {
		return false
	}
	return statusErr.statusCode == http.StatusTooManyRequests || statusErr.statusCode >= http.StatusInternalServerError
}

func pathEscape(path string) string {
	segments := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}
