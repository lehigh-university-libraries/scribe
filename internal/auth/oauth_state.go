package auth

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"sync"
	"time"
)

type OAuthState struct {
	State        string
	RedirectPath string
	CreatedAt    time.Time
}

type OAuthStateManager struct {
	mu     sync.Mutex
	states map[string]OAuthState
}

func NewOAuthStateManager() *OAuthStateManager {
	manager := &OAuthStateManager{
		states: make(map[string]OAuthState),
	}
	go manager.cleanup()
	return manager
}

func (m *OAuthStateManager) New(redirectPath string) (OAuthState, error) {
	state, err := randomString(32)
	if err != nil {
		return OAuthState{}, err
	}
	value := OAuthState{
		State:        state,
		RedirectPath: redirectPath,
		CreatedAt:    time.Now().UTC(),
	}
	m.mu.Lock()
	m.states[state] = value
	m.mu.Unlock()
	return value, nil
}

func (m *OAuthStateManager) Consume(state string) (OAuthState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.states[state]
	if !ok {
		return OAuthState{}, fmt.Errorf("invalid or expired oauth state")
	}
	delete(m.states, state)
	return value, nil
}

func (m *OAuthStateManager) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		cutoff := time.Now().UTC().Add(-10 * time.Minute)
		m.mu.Lock()
		for key, value := range m.states {
			if value.CreatedAt.Before(cutoff) {
				delete(m.states, key)
			}
		}
		m.mu.Unlock()
	}
}

func randomString(length int) (string, error) {
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf)[:length], nil
}
