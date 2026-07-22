package auth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/servicehttp"
	"github.com/markbates/goth/providers/google"
)

const (
	googleOAuthHTTPTimeout = 30 * time.Second
	googleOAuthMaxResponse = int64(2 << 20)
)

type GoogleProfile struct {
	Subject    string
	Email      string
	Name       string
	PictureURL string
}

type GoogleOAuthManager struct {
	clientID     string
	clientSecret string
	state        *OAuthStateManager
}

func NewGoogleOAuthManager(clientID, clientSecret string) (*GoogleOAuthManager, error) {
	if strings.TrimSpace(clientID) == "" || strings.TrimSpace(clientSecret) == "" {
		return nil, fmt.Errorf("google oauth requires client id and client secret")
	}
	return &GoogleOAuthManager{
		clientID:     strings.TrimSpace(clientID),
		clientSecret: strings.TrimSpace(clientSecret),
		state:        NewOAuthStateManager(clientSecret),
	}, nil
}

func (m *GoogleOAuthManager) provider(callbackURL string) (*google.Provider, error) {
	callbackURL = strings.TrimSpace(callbackURL)
	if callbackURL == "" {
		return nil, fmt.Errorf("google oauth requires callback url")
	}
	provider := google.New(m.clientID, m.clientSecret, callbackURL, "email", "profile")
	client := servicehttp.NewClient(googleOAuthHTTPTimeout)
	baseTransport := client.Transport
	if baseTransport == nil {
		baseTransport = http.DefaultTransport
	}
	client.Transport = boundedOAuthTransport{base: baseTransport, maxResponseBytes: googleOAuthMaxResponse}
	provider.HTTPClient = client
	return provider, nil
}

type boundedOAuthTransport struct {
	base             http.RoundTripper
	maxResponseBytes int64
}

func (transport boundedOAuthTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.base.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	if response.ContentLength > transport.maxResponseBytes {
		_ = response.Body.Close()
		return nil, fmt.Errorf("google oauth response exceeds %d bytes", transport.maxResponseBytes)
	}
	response.Body = struct {
		io.Reader
		io.Closer
	}{Reader: io.LimitReader(response.Body, transport.maxResponseBytes+1), Closer: response.Body}
	return response, nil
}

func (m *GoogleOAuthManager) BeginAuth(callbackURL, redirectPath string) (string, OAuthState, error) {
	stateValue, err := m.state.New(redirectPath)
	if err != nil {
		return "", OAuthState{}, err
	}
	provider, err := m.provider(callbackURL)
	if err != nil {
		return "", OAuthState{}, err
	}
	session, err := provider.BeginAuth(stateValue.State)
	if err != nil {
		return "", OAuthState{}, fmt.Errorf("begin google auth: %w", err)
	}
	authURL, err := session.GetAuthURL()
	if err != nil {
		return "", OAuthState{}, fmt.Errorf("get google auth url: %w", err)
	}
	return authURL, stateValue, nil
}

func (m *GoogleOAuthManager) CompleteAuth(ctx context.Context, callbackURL, code, state string) (GoogleProfile, OAuthState, error) {
	// Goth's Google session API does not accept a caller context. The provider
	// therefore uses the bounded HTTP client above so a disconnected callback
	// cannot leave an unbounded token or profile request behind.
	_ = ctx
	stateValue, err := m.state.Consume(state)
	if err != nil {
		return GoogleProfile{}, OAuthState{}, err
	}

	provider, err := m.provider(callbackURL)
	if err != nil {
		return GoogleProfile{}, OAuthState{}, err
	}
	session, err := provider.BeginAuth(state)
	if err != nil {
		return GoogleProfile{}, OAuthState{}, fmt.Errorf("begin google auth callback: %w", err)
	}
	params := url.Values{}
	params.Set("code", code)
	if _, err := session.Authorize(provider, params); err != nil {
		return GoogleProfile{}, OAuthState{}, fmt.Errorf("authorize google callback: %w", err)
	}
	user, err := provider.FetchUser(session)
	if err != nil {
		return GoogleProfile{}, OAuthState{}, fmt.Errorf("fetch google user: %w", err)
	}

	subject := strings.TrimSpace(user.UserID)
	if subject == "" {
		if rawSub, ok := user.RawData["sub"].(string); ok {
			subject = strings.TrimSpace(rawSub)
		}
	}
	return GoogleProfile{
		Subject:    subject,
		Email:      strings.TrimSpace(user.Email),
		Name:       strings.TrimSpace(user.Name),
		PictureURL: strings.TrimSpace(user.AvatarURL),
	}, stateValue, nil
}
