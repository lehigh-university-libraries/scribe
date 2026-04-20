package vaultkv

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"bytes"
	"encoding/json"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	defaultCredentialsPath = "/run/secrets/scribe/GOOGLE_APPLICATION_CREDENTIALS"
	defaultGCPAuthMount    = "gcp"
	defaultGCPAuthRole     = "scribe-app"
)

type Client struct {
	addr        string
	staticToken string
	kvMount     string
	gcpAuthRole string
	http        *http.Client
	now         func() time.Time

	mu          sync.Mutex
	cachedToken string
	expiresAt   time.Time
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
	token, err := c.authToken(ctx)
	if err != nil {
		return nil, err
	}
	if values, err := c.readV2(ctx, token, path); err == nil {
		return values, nil
	} else if !isFallbackableVaultStatus(err) {
		return nil, err
	}
	return c.readV1(ctx, token, path)
}

func (c *Client) readV2(ctx context.Context, token, path string) (map[string]string, error) {
	endpoint := fmt.Sprintf("%s/v1/%s/data/%s", c.addr, url.PathEscape(c.kvMount), pathEscape(path))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Vault-Token", token)

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

func (c *Client) readV1(ctx context.Context, token, path string) (map[string]string, error) {
	endpoint := fmt.Sprintf("%s/v1/%s/%s", c.addr, url.PathEscape(c.kvMount), pathEscape(path))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Vault-Token", token)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return map[string]string{}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, vaultStatusError{operation: "read", path: path, statusCode: resp.StatusCode, body: string(body)}
	}

	var parsed struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parse vault response: %w", err)
	}
	out := make(map[string]string, len(parsed.Data))
	for key, value := range parsed.Data {
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
	if err := c.writeV2(ctx, token, path, data); err == nil {
		return nil
	} else if !isFallbackableVaultStatus(err) {
		return err
	}
	return c.writeV1(ctx, token, path, data)
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

func (c *Client) writeV1(ctx context.Context, token, path string, data map[string]string) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal vault payload: %w", err)
	}
	endpoint := fmt.Sprintf("%s/v1/%s/%s", c.addr, url.PathEscape(c.kvMount), pathEscape(path))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Vault-Token", token)

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
	if err := c.deleteV2(ctx, token, path); err == nil {
		return nil
	} else if !isFallbackableVaultStatus(err) {
		return err
	}
	return c.deleteV1(ctx, token, path)
}

func (c *Client) deleteV2(ctx context.Context, token, path string) error {
	endpoint := fmt.Sprintf("%s/v1/%s/metadata/%s", c.addr, url.PathEscape(c.kvMount), pathEscape(path))
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Vault-Token", token)

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

func (c *Client) deleteV1(ctx context.Context, token, path string) error {
	endpoint := fmt.Sprintf("%s/v1/%s/%s", c.addr, url.PathEscape(c.kvMount), pathEscape(path))
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Vault-Token", token)

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

func isFallbackableVaultStatus(err error) bool {
	var statusErr vaultStatusError
	if !errors.As(err, &statusErr) {
		return false
	}
	return statusErr.statusCode == http.StatusBadRequest || statusErr.statusCode == http.StatusNotFound
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

	token, expiresAt, err := c.loginWithGCP(ctx)
	if err != nil {
		return "", err
	}
	c.cachedToken = token
	c.expiresAt = expiresAt
	return token, nil
}

func (c *Client) loginWithGCP(ctx context.Context) (string, time.Time, error) {
	jwt, err := c.signedJWTFromCredentials()
	if err != nil {
		credErr := err
		jwt, err = c.signedJWTFromMetadata(ctx)
		if err != nil {
			return "", time.Time{}, fmt.Errorf("vault gcp login: credentials auth failed: %v; metadata auth failed: %w", credErr, err)
		}
	}

	payload, err := json.Marshal(map[string]string{
		"role": c.gcpAuthRole,
		"jwt":  jwt,
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("marshal vault gcp login: %w", err)
	}

	endpoint := fmt.Sprintf("%s/v1/auth/%s/login", c.addr, defaultGCPAuthMount)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", time.Time{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", time.Time{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", time.Time{}, fmt.Errorf("vault gcp login status %d: %s", resp.StatusCode, string(body))
	}

	var parsed struct {
		Auth struct {
			ClientToken   string `json:"client_token"`
			LeaseDuration int    `json:"lease_duration"`
		} `json:"auth"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", time.Time{}, fmt.Errorf("parse vault login response: %w", err)
	}
	if strings.TrimSpace(parsed.Auth.ClientToken) == "" {
		return "", time.Time{}, fmt.Errorf("vault login response missing client_token")
	}
	lease := time.Duration(parsed.Auth.LeaseDuration) * time.Second
	if lease <= 0 {
		lease = 5 * time.Minute
	}
	return parsed.Auth.ClientToken, c.now().Add(lease), nil
}

func (c *Client) signedJWTFromCredentials() (string, error) {
	path := strings.TrimSpace(os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"))
	if path == "" {
		path = defaultCredentialsPath
	}
	raw, err := os.ReadFile(path)
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

func pathEscape(path string) string {
	segments := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}
