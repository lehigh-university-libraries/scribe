package serviceauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type cachedToken struct {
	token     string
	expiresAt time.Time
}

type CloudRunTokenSource struct {
	client *http.Client
	now    func() time.Time

	mu     sync.Mutex
	tokens map[string]cachedToken
}

func NewCloudRunTokenSource(client *http.Client) *CloudRunTokenSource {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &CloudRunTokenSource{
		client: client,
		now:    time.Now,
		tokens: make(map[string]cachedToken),
	}
}

func (s *CloudRunTokenSource) AuthorizationHeader(ctx context.Context, audience string) (string, error) {
	audience = strings.TrimSpace(audience)
	if audience == "" {
		return "", nil
	}

	token, err := s.Token(ctx, audience)
	if err != nil {
		return "", err
	}
	return "Bearer " + token, nil
}

func (s *CloudRunTokenSource) Token(ctx context.Context, audience string) (string, error) {
	audience = strings.TrimSpace(audience)
	if audience == "" {
		return "", nil
	}

	s.mu.Lock()
	if cached, ok := s.tokens[audience]; ok && cached.token != "" && s.now().Add(30*time.Second).Before(cached.expiresAt) {
		s.mu.Unlock()
		return cached.token, nil
	}
	s.mu.Unlock()

	token, expiresAt, err := s.fetchMetadataIdentityToken(ctx, audience)
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	s.tokens[audience] = cachedToken{token: token, expiresAt: expiresAt}
	s.mu.Unlock()
	return token, nil
}

func (s *CloudRunTokenSource) fetchMetadataIdentityToken(ctx context.Context, audience string) (string, time.Time, error) {
	endpoint := "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/identity?audience=" + url.QueryEscape(audience) + "&format=full"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Metadata-Flavor", "Google")

	resp, err := s.client.Do(req)
	if err != nil {
		return "", time.Time{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", time.Time{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", time.Time{}, fmt.Errorf("metadata identity token status %d: %s", resp.StatusCode, string(body))
	}

	token := strings.TrimSpace(string(body))
	if token == "" {
		return "", time.Time{}, fmt.Errorf("metadata identity token response was empty")
	}

	expiresAt, err := tokenExpiry(token)
	if err != nil {
		expiresAt = s.now().Add(5 * time.Minute)
	}
	return token, expiresAt, nil
}

func tokenExpiry(token string) (time.Time, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return time.Time{}, fmt.Errorf("invalid jwt")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, err
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}, err
	}
	if claims.Exp <= 0 {
		return time.Time{}, fmt.Errorf("jwt missing exp")
	}
	return time.Unix(claims.Exp, 0), nil
}
