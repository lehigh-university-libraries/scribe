package deployauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/database"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

type fakeBrowserSessionIdentities struct {
	user          store.User
	accesses      []store.WorkspaceAccess
	getUserErr    error
	listErr       error
	createErr     error
	deleteErr     error
	createCalls   int
	deleteCalls   int
	createdUserID uint64
	createdToken  string
	createdAgent  string
	createdIP     string
	createdTTL    time.Duration
}

func (f *fakeBrowserSessionIdentities) GetUser(context.Context, uint64) (store.User, error) {
	return f.user, f.getUserErr
}

func (f *fakeBrowserSessionIdentities) ListWorkspaceAccessByUser(context.Context, uint64) ([]store.WorkspaceAccess, error) {
	return f.accesses, f.listErr
}

func (f *fakeBrowserSessionIdentities) CreateSession(_ context.Context, userID uint64, token, agent, ip string, ttl time.Duration) error {
	f.createCalls++
	f.createdUserID = userID
	f.createdToken = token
	f.createdAgent = agent
	f.createdIP = ip
	f.createdTTL = ttl
	return f.createErr
}

func (f *fakeBrowserSessionIdentities) DeleteSession(_ context.Context, token string) error {
	f.deleteCalls++
	if token != f.createdToken {
		return fmt.Errorf("deleted a different session")
	}
	return f.deleteErr
}

func TestMintBrowserSessionFileWritesBoundedPlaywrightState(t *testing.T) {
	if BrowserSessionTTL != 50*time.Minute {
		t.Fatalf("browser session TTL = %s, want 50m", BrowserSessionTTL)
	}
	outputPath := newBrowserSessionOutputPath(t)
	now := time.Date(2026, time.August, 4, 12, 0, 0, 0, time.UTC)
	identities := reservedBrowserSessionIdentities()
	minter := browserSessionMinter{
		identities: identities,
		random:     bytes.NewReader(bytes.Repeat([]byte{0x5a}, 48)),
		now:        func() time.Time { return now },
	}

	if err := minter.mintFile(
		context.Background(),
		"https://scribe.example",
		"scribe_session",
		"",
		outputPath,
	); err != nil {
		t.Fatalf("mint browser session: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(outputPath) })

	if identities.createCalls != 1 || identities.deleteCalls != 0 ||
		identities.createdUserID != store.AnonymousUserID ||
		identities.createdTTL != BrowserSessionTTL ||
		identities.createdAgent != browserSessionUserAgent || identities.createdIP != "" {
		t.Fatalf("created session = calls %d delete %d user %d ttl %s agent %q ip %q",
			identities.createCalls,
			identities.deleteCalls,
			identities.createdUserID,
			identities.createdTTL,
			identities.createdAgent,
			identities.createdIP,
		)
	}
	if len(identities.createdToken) != 64 || strings.ContainsAny(identities.createdToken, "+/=") {
		t.Fatalf("created token shape = length %d value alphabet-valid %t", len(identities.createdToken), !strings.ContainsAny(identities.createdToken, "+/="))
	}

	info, err := os.Stat(outputPath)
	if err != nil {
		t.Fatalf("stat browser session output: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("browser session output mode = %o, want 600", got)
	}
	raw, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read browser session output: %v", err)
	}
	var state playwrightStorageState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("decode browser session output: %v", err)
	}
	if len(state.Cookies) != 1 || len(state.Origins) != 0 {
		t.Fatalf("storage state = %+v", state)
	}
	cookie := state.Cookies[0]
	if cookie.Name != "scribe_session" || cookie.Value != identities.createdToken ||
		cookie.Domain != "scribe.example" || cookie.Path != "/" ||
		cookie.Expires != now.Add(BrowserSessionTTL).Unix() ||
		!cookie.HTTPOnly || !cookie.Secure || cookie.SameSite != "Lax" {
		t.Fatalf("storage-state cookie = %+v", cookie)
	}
}

func TestMintBrowserSessionFileFailsClosedWhenReservedIdentityDrifts(t *testing.T) {
	tests := map[string]func(*fakeBrowserSessionIdentities){
		"system administrator": func(identities *fakeBrowserSessionIdentities) {
			identities.user.IsAdmin = true
		},
		"OAuth identity": func(identities *fakeBrowserSessionIdentities) {
			identities.user.GoogleSubject = "unexpected"
		},
		"multiple workspaces": func(identities *fakeBrowserSessionIdentities) {
			identities.accesses = append(identities.accesses, identities.accesses[0])
		},
		"non-personal workspace": func(identities *fakeBrowserSessionIdentities) {
			identities.accesses[0].Workspace.IsPersonal = false
		},
		"non-admin membership": func(identities *fakeBrowserSessionIdentities) {
			identities.accesses[0].Role = "write"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			outputPath := newBrowserSessionOutputPath(t)
			identities := reservedBrowserSessionIdentities()
			mutate(identities)
			minter := browserSessionMinter{
				identities: identities,
				random:     bytes.NewReader(bytes.Repeat([]byte{0x5a}, 48)),
				now:        time.Now,
			}
			if err := minter.mintFile(context.Background(), "https://scribe.example", "scribe_session", "", outputPath); err == nil {
				t.Fatal("mint browser session succeeded after reserved identity drift")
			}
			if identities.createCalls != 0 {
				t.Fatalf("CreateSession calls = %d, want 0", identities.createCalls)
			}
			if _, err := os.Stat(outputPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("output after rejected identity drift: %v", err)
			}
		})
	}
}

func TestMintBrowserSessionFileRefusesUnsafeOrExistingOutput(t *testing.T) {
	identities := reservedBrowserSessionIdentities()
	minter := browserSessionMinter{
		identities: identities,
		random:     bytes.NewReader(bytes.Repeat([]byte{0x5a}, 96)),
		now:        time.Now,
	}
	for _, outputPath := range []string{
		"relative.json",
		filepath.Join(t.TempDir(), "scribe-browser-session-test.json"),
		"/tmp/not-a-browser-session.json",
		"/tmp/scribe-browser-session-line\nbreak.json",
	} {
		if err := minter.mintFile(context.Background(), "https://scribe.example", "scribe_session", "", outputPath); err == nil {
			t.Fatalf("mint browser session accepted unsafe output path %q", outputPath)
		}
	}

	existing := newBrowserSessionOutputPath(t)
	if err := os.WriteFile(existing, []byte("operator-owned"), 0o600); err != nil {
		t.Fatalf("create existing output: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(existing) })
	if err := minter.mintFile(context.Background(), "https://scribe.example", "scribe_session", "", existing); err == nil {
		t.Fatal("mint browser session overwrote an existing output")
	}
	raw, err := os.ReadFile(existing)
	if err != nil || string(raw) != "operator-owned" {
		t.Fatalf("existing output = %q, %v", raw, err)
	}
	if identities.createCalls != 0 {
		t.Fatalf("CreateSession calls = %d, want 0", identities.createCalls)
	}
}

func TestMintBrowserSessionFileRemovesOutputWhenSessionCreationFails(t *testing.T) {
	outputPath := newBrowserSessionOutputPath(t)
	identities := reservedBrowserSessionIdentities()
	identities.createErr = errors.New("database unavailable")
	minter := browserSessionMinter{
		identities: identities,
		random:     bytes.NewReader(bytes.Repeat([]byte{0x5a}, 48)),
		now:        time.Now,
	}
	if err := minter.mintFile(context.Background(), "https://scribe.example", "scribe_session", "", outputPath); err == nil {
		t.Fatal("mint browser session succeeded when session persistence failed")
	}
	if identities.createCalls != 1 || identities.deleteCalls != 0 {
		t.Fatalf("session calls = create %d delete %d", identities.createCalls, identities.deleteCalls)
	}
	if _, err := os.Stat(outputPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("output after session persistence failure: %v", err)
	}
}

func TestBrowserCookieDomainRequiresMatchingHTTPSOrigin(t *testing.T) {
	tests := []struct {
		origin string
		domain string
		want   string
	}{
		{origin: "https://scribe.example", want: "scribe.example"},
		{origin: "https://preview.scribe.example", domain: ".scribe.example", want: ".scribe.example"},
		{origin: "http://scribe.example"},
		{origin: "https://scribe.example/path"},
		{origin: "https://scribe.example", domain: "attacker.example"},
	}
	for _, test := range tests {
		got, err := browserCookieDomain(test.origin, "scribe_session", test.domain)
		if test.want == "" {
			if err == nil {
				t.Errorf("browserCookieDomain(%q, %q) = %q, want error", test.origin, test.domain, got)
			}
			continue
		}
		if err != nil || got != test.want {
			t.Errorf("browserCookieDomain(%q, %q) = %q, %v; want %q", test.origin, test.domain, got, err, test.want)
		}
	}
}

func TestMintBrowserSessionFileAuthenticatesReservedIdentity(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_DSN"))
	if dsn == "" {
		t.Skip("TEST_DSN not set; skipping browser session persistence integration test")
	}
	databasePool, err := database.NewPool(dsn, database.DefaultConfig())
	if err != nil {
		t.Fatalf("open browser session test database: %v", err)
	}
	t.Cleanup(func() { _ = databasePool.Close() })
	if err := database.Migrate(databasePool); err != nil {
		t.Fatalf("migrate browser session test database: %v", err)
	}

	outputPath := newBrowserSessionOutputPath(t)
	t.Cleanup(func() { _ = os.Remove(outputPath) })
	identities := store.NewIdentityStore(databasePool)
	if err := MintBrowserSessionFile(
		context.Background(),
		identities,
		"https://scribe.example",
		"scribe_session",
		"",
		outputPath,
	); err != nil {
		t.Fatalf("mint persisted browser session: %v", err)
	}
	raw, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read persisted browser session state: %v", err)
	}
	var state playwrightStorageState
	if err := json.Unmarshal(raw, &state); err != nil || len(state.Cookies) != 1 {
		t.Fatalf("decode persisted browser session state: cookies %d, error %v", len(state.Cookies), err)
	}
	rawToken := state.Cookies[0].Value
	t.Cleanup(func() { _ = identities.DeleteSession(context.Background(), rawToken) })
	session, err := identities.GetSession(context.Background(), rawToken)
	if err != nil {
		t.Fatalf("authenticate persisted browser session: %v", err)
	}
	if session.User.ID != store.AnonymousUserID ||
		session.Workspace.ID != store.AnonymousWorkspaceID || session.Role != "admin" {
		t.Fatalf("persisted browser session = user %d workspace %d role %q",
			session.User.ID,
			session.Workspace.ID,
			session.Role,
		)
	}
	var expiresAt time.Time
	if err := databasePool.QueryRowContext(
		context.Background(),
		`SELECT expires_at FROM auth_sessions WHERE user_id = ? AND user_agent = ? ORDER BY id DESC LIMIT 1`,
		store.AnonymousUserID,
		browserSessionUserAgent,
	).Scan(&expiresAt); err != nil {
		t.Fatalf("load persisted browser session expiry: %v", err)
	}
	remaining := time.Until(expiresAt)
	if remaining < BrowserSessionTTL-10*time.Second || remaining > BrowserSessionTTL+5*time.Second {
		t.Fatalf("persisted browser session remaining lifetime = %s", remaining)
	}
}

func reservedBrowserSessionIdentities() *fakeBrowserSessionIdentities {
	ownerID := store.AnonymousUserID
	return &fakeBrowserSessionIdentities{
		user: store.User{ID: store.AnonymousUserID, Name: "anonymous"},
		accesses: []store.WorkspaceAccess{{
			Workspace: store.Workspace{
				ID:          store.AnonymousWorkspaceID,
				Name:        "Anonymous",
				Slug:        "anonymous",
				IsPersonal:  true,
				OwnerUserID: &ownerID,
			},
			Role: "admin",
		}},
	}
}

func newBrowserSessionOutputPath(t *testing.T) string {
	t.Helper()
	file, err := os.CreateTemp("/tmp", browserSessionOutputPrefix+"test-*"+browserSessionOutputSuffix)
	if err != nil {
		t.Fatalf("reserve browser session output name: %v", err)
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		t.Fatalf("close reserved browser session output: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("release browser session output name: %v", err)
	}
	return path
}
