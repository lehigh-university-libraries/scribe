package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const oauthStateLifetime = 10 * time.Minute

type OAuthState struct {
	State        string
	RedirectPath string
	CreatedAt    time.Time
}

// OAuthStateManager signs self-contained OAuth state values. Stateless state
// allows any API replica to receive the callback without a sticky session.
type OAuthStateManager struct {
	key []byte
	now func() time.Time
}

type oauthStatePayload struct {
	Nonce        string `json:"nonce"`
	RedirectPath string `json:"redirect_path"`
	IssuedAt     int64  `json:"issued_at"`
}

func NewOAuthStateManager(secret string) *OAuthStateManager {
	digest := sha256.Sum256([]byte("scribe/oauth-state/v1\x00" + strings.TrimSpace(secret)))
	return &OAuthStateManager{key: digest[:], now: time.Now}
}

func (m *OAuthStateManager) New(redirectPath string) (OAuthState, error) {
	if m == nil || len(m.key) == 0 {
		return OAuthState{}, fmt.Errorf("oauth state signer is not configured")
	}
	nonce, err := randomString(32)
	if err != nil {
		return OAuthState{}, err
	}
	createdAt := m.now().UTC()
	payload, err := json.Marshal(oauthStatePayload{
		Nonce:        nonce,
		RedirectPath: safeRedirectPath(redirectPath),
		IssuedAt:     createdAt.Unix(),
	})
	if err != nil {
		return OAuthState{}, fmt.Errorf("encode oauth state: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	state := encoded + "." + base64.RawURLEncoding.EncodeToString(m.sign(encoded))
	return OAuthState{State: state, RedirectPath: safeRedirectPath(redirectPath), CreatedAt: createdAt}, nil
}

func (m *OAuthStateManager) Consume(state string) (OAuthState, error) {
	if m == nil || len(m.key) == 0 {
		return OAuthState{}, fmt.Errorf("oauth state signer is not configured")
	}
	parts := strings.Split(strings.TrimSpace(state), ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return OAuthState{}, fmt.Errorf("invalid or expired oauth state")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(signature, m.sign(parts[0])) {
		return OAuthState{}, fmt.Errorf("invalid or expired oauth state")
	}
	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return OAuthState{}, fmt.Errorf("invalid or expired oauth state")
	}
	var payload oauthStatePayload
	if err := json.Unmarshal(payloadJSON, &payload); err != nil || strings.TrimSpace(payload.Nonce) == "" {
		return OAuthState{}, fmt.Errorf("invalid or expired oauth state")
	}
	createdAt := time.Unix(payload.IssuedAt, 0).UTC()
	now := m.now().UTC()
	if createdAt.After(now.Add(time.Minute)) || now.Sub(createdAt) > oauthStateLifetime {
		return OAuthState{}, fmt.Errorf("invalid or expired oauth state")
	}
	return OAuthState{
		State:        state,
		RedirectPath: safeRedirectPath(payload.RedirectPath),
		CreatedAt:    createdAt,
	}, nil
}

func (m *OAuthStateManager) sign(payload string) []byte {
	mac := hmac.New(sha256.New, m.key)
	_, _ = mac.Write([]byte(payload))
	return mac.Sum(nil)
}

func randomString(length int) (string, error) {
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf)[:length], nil
}
