// Package deployauth contains credentials that are available only to trusted
// deployment processes running inside the backend container.
package deployauth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/store"
)

const (
	// BrowserSessionTTL is deliberately fixed so the trusted deployment helper
	// cannot create a durable interactive credential.
	BrowserSessionTTL = 50 * time.Minute

	browserSessionOutputPrefix = "scribe-browser-session-"
	browserSessionOutputSuffix = ".json"
	browserSessionUserAgent    = "scribe-deployment-browser-smoke/v1"
	browserSessionCleanupTTL   = 5 * time.Second
)

type browserSessionIdentityStore interface {
	GetUser(context.Context, uint64) (store.User, error)
	ListWorkspaceAccessByUser(context.Context, uint64) ([]store.WorkspaceAccess, error)
	CreateSession(context.Context, uint64, string, string, string, time.Duration) error
	DeleteSession(context.Context, string) error
}

type browserSessionMinter struct {
	identities browserSessionIdentityStore
	random     io.Reader
	now        func() time.Time
}

type playwrightStorageState struct {
	Cookies []playwrightCookie `json:"cookies"`
	Origins []any              `json:"origins"`
}

type playwrightCookie struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Domain   string `json:"domain"`
	Path     string `json:"path"`
	Expires  int64  `json:"expires"`
	HTTPOnly bool   `json:"httpOnly"`
	Secure   bool   `json:"secure"`
	SameSite string `json:"sameSite"`
}

// MintBrowserSessionFile creates one fixed-lifetime session for the reserved
// user/workspace pair and writes a Playwright storage-state document to a new
// mode-0600 file in /tmp. It deliberately returns no credential-bearing value.
// Callers must delete the file as soon as the browser context has loaded it.
func MintBrowserSessionFile(
	ctx context.Context,
	identities *store.IdentityStore,
	publicBaseURL string,
	cookieName string,
	cookieDomain string,
	outputPath string,
) error {
	return browserSessionMinter{
		identities: identities,
		random:     rand.Reader,
		now:        time.Now,
	}.mintFile(ctx, publicBaseURL, cookieName, cookieDomain, outputPath)
}

func (m browserSessionMinter) mintFile(
	ctx context.Context,
	publicBaseURL string,
	cookieName string,
	cookieDomain string,
	outputPath string,
) (returnErr error) {
	if m.identities == nil || m.random == nil || m.now == nil {
		return fmt.Errorf("mint browser session: helper is not configured")
	}
	cookieName = strings.TrimSpace(cookieName)
	domain, err := browserCookieDomain(publicBaseURL, cookieName, cookieDomain)
	if err != nil {
		return err
	}
	outputName, err := validateBrowserSessionOutputPath(outputPath)
	if err != nil {
		return err
	}
	if err := m.validateReservedIdentity(ctx); err != nil {
		return err
	}

	outputRoot, err := os.OpenRoot("/tmp")
	if err != nil {
		return fmt.Errorf("open browser session output root: %w", err)
	}
	defer func() {
		// Closing the directory capability cannot affect a fully synced output.
		// Preserve a prior failure, but do not turn a successful mint into a
		// credential-bearing partial failure solely because close(2) failed.
		if closeErr := outputRoot.Close(); returnErr != nil {
			returnErr = errors.Join(returnErr, closeErr)
		}
	}()
	output, err := outputRoot.OpenFile(outputName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create browser session output: %w", err)
	}
	removeOutput := true
	defer func() {
		if output != nil {
			returnErr = errors.Join(returnErr, output.Close())
		}
		if removeOutput {
			returnErr = errors.Join(returnErr, outputRoot.Remove(outputName))
		}
	}()

	rawToken, err := randomBrowserSessionToken(m.random)
	if err != nil {
		return err
	}
	createdAt := m.now().UTC()
	if err := m.identities.CreateSession(
		ctx,
		store.AnonymousUserID,
		rawToken,
		browserSessionUserAgent,
		"",
		BrowserSessionTTL,
	); err != nil {
		return fmt.Errorf("create browser session: %w", err)
	}
	sessionCreated := true
	defer func() {
		if sessionCreated {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), browserSessionCleanupTTL)
			defer cancel()
			returnErr = errors.Join(returnErr, m.identities.DeleteSession(cleanupCtx, rawToken))
		}
	}()

	state := playwrightStorageState{
		Cookies: []playwrightCookie{{
			Name:     cookieName,
			Value:    rawToken,
			Domain:   domain,
			Path:     "/",
			Expires:  createdAt.Add(BrowserSessionTTL).Unix(),
			HTTPOnly: true,
			Secure:   true,
			SameSite: "Lax",
		}},
		Origins: []any{},
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode browser session state: %w", err)
	}
	encoded = append(encoded, '\n')
	if _, err := output.Write(encoded); err != nil {
		return fmt.Errorf("write browser session output: %w", err)
	}
	if err := output.Sync(); err != nil {
		return fmt.Errorf("sync browser session output: %w", err)
	}
	if err := output.Close(); err != nil {
		output = nil
		return fmt.Errorf("close browser session output: %w", err)
	}
	output = nil
	removeOutput = false
	sessionCreated = false
	return nil
}

func (m browserSessionMinter) validateReservedIdentity(ctx context.Context) error {
	user, err := m.identities.GetUser(ctx, store.AnonymousUserID)
	if err != nil {
		return fmt.Errorf("validate reserved browser identity: %w", err)
	}
	if user.ID != store.AnonymousUserID || user.IsAdmin ||
		strings.TrimSpace(user.Email) != "" || strings.TrimSpace(user.GoogleSubject) != "" {
		return fmt.Errorf("validate reserved browser identity: reserved user invariant is not satisfied")
	}
	accesses, err := m.identities.ListWorkspaceAccessByUser(ctx, store.AnonymousUserID)
	if err != nil {
		return fmt.Errorf("validate reserved browser workspace: %w", err)
	}
	if len(accesses) != 1 {
		return fmt.Errorf("validate reserved browser workspace: reserved user must have exactly one workspace")
	}
	access := accesses[0]
	workspace := access.Workspace
	if workspace.ID != store.AnonymousWorkspaceID || !workspace.IsPersonal ||
		workspace.OwnerUserID == nil || *workspace.OwnerUserID != store.AnonymousUserID ||
		workspace.Slug != "anonymous" || !strings.EqualFold(strings.TrimSpace(access.Role), "admin") {
		return fmt.Errorf("validate reserved browser workspace: reserved workspace invariant is not satisfied")
	}
	return nil
}

func randomBrowserSessionToken(source io.Reader) (string, error) {
	randomBytes := make([]byte, 48)
	if _, err := io.ReadFull(source, randomBytes); err != nil {
		return "", fmt.Errorf("generate browser session credential: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(randomBytes), nil
}

func browserCookieDomain(publicBaseURL, cookieName, configuredDomain string) (string, error) {
	base, err := url.Parse(strings.TrimSpace(publicBaseURL))
	if err != nil || base.Scheme != "https" || base.Hostname() == "" || base.User != nil ||
		base.RawQuery != "" || base.Fragment != "" || (base.Path != "" && base.Path != "/") {
		return "", fmt.Errorf("configure browser session cookie: public base URL must be an HTTPS origin")
	}
	host := strings.ToLower(strings.TrimSpace(base.Hostname()))
	domain := strings.ToLower(strings.TrimSpace(configuredDomain))
	if domain == "" {
		domain = host
	}
	normalizedDomain := strings.TrimPrefix(domain, ".")
	if host != normalizedDomain && !strings.HasSuffix(host, "."+normalizedDomain) {
		return "", fmt.Errorf("configure browser session cookie: cookie domain does not contain the public origin")
	}
	cookie := http.Cookie{
		Name:     strings.TrimSpace(cookieName),
		Value:    "placeholder",
		Path:     "/",
		Domain:   domain,
		MaxAge:   int(BrowserSessionTTL / time.Second),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}
	if err := cookie.Valid(); err != nil {
		return "", fmt.Errorf("configure browser session cookie: invalid cookie configuration")
	}
	return domain, nil
}

func validateBrowserSessionOutputPath(raw string) (string, error) {
	cleaned := filepath.Clean(strings.TrimSpace(raw))
	base := filepath.Base(cleaned)
	runID := strings.TrimSuffix(strings.TrimPrefix(base, browserSessionOutputPrefix), browserSessionOutputSuffix)
	if !filepath.IsAbs(cleaned) || filepath.Dir(cleaned) != "/tmp" ||
		!strings.HasPrefix(base, browserSessionOutputPrefix) ||
		!strings.HasSuffix(base, browserSessionOutputSuffix) ||
		len(runID) == 0 || len(runID) > 128 || !isBrowserSessionRunID(runID) {
		return "", fmt.Errorf("configure browser session output: use a new /tmp/%s<run-id>%s path", browserSessionOutputPrefix, browserSessionOutputSuffix)
	}
	return base, nil
}

func isBrowserSessionRunID(value string) bool {
	for _, character := range value {
		switch {
		case character >= 'a' && character <= 'z':
		case character >= 'A' && character <= 'Z':
		case character >= '0' && character <= '9':
		case character == '-', character == '_', character == '.':
		default:
			return false
		}
	}
	return true
}
