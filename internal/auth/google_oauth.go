package auth

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/markbates/goth/providers/google"
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
		state:        NewOAuthStateManager(),
	}, nil
}

func (m *GoogleOAuthManager) provider(callbackURL string) (*google.Provider, error) {
	callbackURL = strings.TrimSpace(callbackURL)
	if callbackURL == "" {
		return nil, fmt.Errorf("google oauth requires callback url")
	}
	return google.New(m.clientID, m.clientSecret, callbackURL, "email", "profile"), nil
}

func (m *GoogleOAuthManager) BeginAuth(callbackURL, redirectPath string) (string, error) {
	stateValue, err := m.state.New(redirectPath)
	if err != nil {
		return "", err
	}
	provider, err := m.provider(callbackURL)
	if err != nil {
		return "", err
	}
	session, err := provider.BeginAuth(stateValue.State)
	if err != nil {
		return "", fmt.Errorf("begin google auth: %w", err)
	}
	authURL, err := session.GetAuthURL()
	if err != nil {
		return "", fmt.Errorf("get google auth url: %w", err)
	}
	return authURL, nil
}

func (m *GoogleOAuthManager) CompleteAuth(ctx context.Context, callbackURL, code, state string) (GoogleProfile, OAuthState, error) {
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
