package auth

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/markbates/goth"
	"github.com/markbates/goth/providers/google"
)

type GoogleProfile struct {
	Subject    string
	Email      string
	Name       string
	PictureURL string
}

type GoogleOAuthManager struct {
	provider goth.Provider
	state    *OAuthStateManager
}

func NewGoogleOAuthManager(clientID, clientSecret, callbackURL string) (*GoogleOAuthManager, error) {
	if strings.TrimSpace(clientID) == "" || strings.TrimSpace(clientSecret) == "" || strings.TrimSpace(callbackURL) == "" {
		return nil, fmt.Errorf("google oauth requires client id, client secret, and callback url")
	}
	return &GoogleOAuthManager{
		provider: google.New(clientID, clientSecret, callbackURL, "email", "profile"),
		state:    NewOAuthStateManager(),
	}, nil
}

func (m *GoogleOAuthManager) BeginAuth(redirectPath string) (string, error) {
	stateValue, err := m.state.New(redirectPath)
	if err != nil {
		return "", err
	}
	session, err := m.provider.BeginAuth(stateValue.State)
	if err != nil {
		return "", fmt.Errorf("begin google auth: %w", err)
	}
	authURL, err := session.GetAuthURL()
	if err != nil {
		return "", fmt.Errorf("get google auth url: %w", err)
	}
	return authURL, nil
}

func (m *GoogleOAuthManager) CompleteAuth(ctx context.Context, code, state string) (GoogleProfile, OAuthState, error) {
	stateValue, err := m.state.Consume(state)
	if err != nil {
		return GoogleProfile{}, OAuthState{}, err
	}

	session, err := m.provider.BeginAuth(state)
	if err != nil {
		return GoogleProfile{}, OAuthState{}, fmt.Errorf("begin google auth callback: %w", err)
	}
	params := url.Values{}
	params.Set("code", code)
	if _, err := session.Authorize(m.provider, params); err != nil {
		return GoogleProfile{}, OAuthState{}, fmt.Errorf("authorize google callback: %w", err)
	}
	user, err := m.provider.FetchUser(session)
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
