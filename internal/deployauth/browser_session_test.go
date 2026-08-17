package deployauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

func TestCleanupBrowserSessionFileWaitsForLateMintWrite(t *testing.T) {
	outputPath := newProductionBrowserSessionOutputPath(t)
	started := make(chan struct{})
	allowWrite := make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		writerDone <- withBrowserSessionLock(context.Background(), func() error {
			close(started)
			<-allowWrite
			return os.WriteFile(outputPath, []byte("late credential material"), 0o600)
		})
	}()
	<-started

	cleanupDone := make(chan error, 1)
	go func() {
		cleanupDone <- CleanupBrowserSessionFile(context.Background(), outputPath)
	}()
	select {
	case err := <-cleanupDone:
		t.Fatalf("cleanup returned before in-flight mint settled: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(allowWrite)
	if err := <-writerDone; err != nil {
		t.Fatalf("late writer failed: %v", err)
	}
	if err := <-cleanupDone; err != nil {
		t.Fatalf("cleanup failed: %v", err)
	}
	if _, err := os.Lstat(outputPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("late-written credential remained after cleanup: %v", err)
	}
}

func TestCleanupBrowserSessionFileHonorsLockCancellation(t *testing.T) {
	outputPath := newProductionBrowserSessionOutputPath(t)
	started := make(chan struct{})
	release := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- withBrowserSessionLock(context.Background(), func() error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	err := CleanupBrowserSessionFile(ctx, outputPath)
	cancel()
	if err == nil {
		t.Fatal("cleanup ignored lock cancellation")
	}
	close(release)
	if err := <-holderDone; err != nil {
		t.Fatalf("lock holder failed: %v", err)
	}
}

func TestReservedMintCannotWriteAfterCleanupFence(t *testing.T) {
	outputPath := newProductionBrowserSessionOutputPath(t)
	if err := ReserveBrowserSessionFile(context.Background(), outputPath); err != nil {
		t.Fatalf("reserve browser session output: %v", err)
	}
	if err := CleanupBrowserSessionFile(context.Background(), outputPath); err != nil {
		t.Fatalf("cleanup browser session reservation: %v", err)
	}
	identities := reservedBrowserSessionIdentities()
	minter := browserSessionMinter{
		identities:         identities,
		random:             bytes.NewReader(bytes.Repeat([]byte{0x5a}, 48)),
		now:                time.Now,
		requireReservation: true,
	}
	err := withBrowserSessionLock(context.Background(), func() error {
		return minter.mintFile(context.Background(), "https://scribe.example", "scribe_session", "", outputPath)
	})
	if err == nil {
		t.Fatal("late reserved mint recreated material after cleanup")
	}
	if identities.createCalls != 0 {
		t.Fatalf("late reserved mint created %d sessions", identities.createCalls)
	}
	if _, statErr := os.Lstat(outputPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("late reserved mint left output: %v", statErr)
	}
}

func TestReservedMintHoldsCleanupUntilCredentialWriteCompletes(t *testing.T) {
	outputPath := newProductionBrowserSessionOutputPath(t)
	if err := ReserveBrowserSessionFile(context.Background(), outputPath); err != nil {
		t.Fatalf("reserve browser session output: %v", err)
	}
	reader := &gatedReader{started: make(chan struct{}), release: make(chan struct{}), data: bytes.Repeat([]byte{0x5a}, 48)}
	identities := reservedBrowserSessionIdentities()
	minter := browserSessionMinter{
		identities:         identities,
		random:             reader,
		now:                time.Now,
		requireReservation: true,
	}
	mintDone := make(chan error, 1)
	go func() {
		mintDone <- withBrowserSessionLock(context.Background(), func() error {
			return minter.mintFile(context.Background(), "https://scribe.example", "scribe_session", "", outputPath)
		})
	}()
	<-reader.started
	cleanupDone := make(chan error, 1)
	go func() { cleanupDone <- CleanupBrowserSessionFile(context.Background(), outputPath) }()
	select {
	case err := <-cleanupDone:
		t.Fatalf("cleanup returned before credential write settled: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(reader.release)
	if err := <-mintDone; err != nil {
		t.Fatalf("reserved mint failed: %v", err)
	}
	if err := <-cleanupDone; err != nil {
		t.Fatalf("cleanup failed: %v", err)
	}
	if _, err := os.Lstat(outputPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("credential remained after serialized cleanup: %v", err)
	}
}

func TestExportBrowserSessionFileStreamsOnlyExactPrivateProductionState(t *testing.T) {
	outputPath := newProductionBrowserSessionOutputPath(t)
	want := bytes.Repeat([]byte("x"), minimumBrowserSessionBytes)
	if err := os.WriteFile(outputPath, want, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(outputPath, 0o600); err != nil {
		t.Fatal(err)
	}
	var destination bytes.Buffer
	if err := ExportBrowserSessionFile(context.Background(), outputPath, &destination); err != nil {
		t.Fatalf("export browser session: %v", err)
	}
	if !bytes.Equal(destination.Bytes(), want) {
		t.Fatalf("exported browser session bytes = %q", destination.Bytes())
	}
	if info, err := os.Lstat(outputPath); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("export removed the cleanup-owned source: %v, %v", info, err)
	}

	if err := os.Chmod(outputPath, 0o640); err != nil {
		t.Fatal(err)
	}
	destination.Reset()
	if err := ExportBrowserSessionFile(context.Background(), outputPath, &destination); err == nil {
		t.Fatal("export accepted a non-private source")
	}
	if destination.Len() != 0 {
		t.Fatal("rejected export wrote credential bytes")
	}
}

func TestExportBrowserSessionFileEnforcesStateSizeBoundaries(t *testing.T) {
	for _, test := range []struct {
		name    string
		size    int
		wantErr bool
	}{
		{name: "below minimum", size: minimumBrowserSessionBytes - 1, wantErr: true},
		{name: "minimum", size: minimumBrowserSessionBytes},
		{name: "maximum", size: maximumBrowserSessionBytes},
		{name: "above maximum", size: maximumBrowserSessionBytes + 1, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			outputPath := newProductionBrowserSessionOutputPath(t)
			contents := bytes.Repeat([]byte("x"), test.size)
			if err := os.WriteFile(outputPath, contents, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(outputPath, 0o600); err != nil {
				t.Fatal(err)
			}
			var destination bytes.Buffer
			err := ExportBrowserSessionFile(context.Background(), outputPath, &destination)
			if test.wantErr {
				if err == nil {
					t.Fatal("export accepted an out-of-bounds state")
				}
				if destination.Len() != 0 {
					t.Fatal("rejected state wrote credential bytes")
				}
				return
			}
			if err != nil || !bytes.Equal(destination.Bytes(), contents) {
				t.Fatalf("boundary export = bytes:%d error:%v", destination.Len(), err)
			}
		})
	}
}

func TestExportBrowserSessionFileRejectsNoncanonicalAndSymlinkSources(t *testing.T) {
	var destination bytes.Buffer
	if err := ExportBrowserSessionFile(
		context.Background(),
		"/tmp/scribe-browser-session-test.json",
		&destination,
	); err == nil {
		t.Fatal("export accepted a non-production path")
	}

	outputPath := newProductionBrowserSessionOutputPath(t)
	target := newBrowserSessionOutputPath(t)
	if err := os.WriteFile(target, []byte("credential"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(target) })
	if err := os.Symlink(target, outputPath); err != nil {
		t.Fatal(err)
	}
	if err := ExportBrowserSessionFile(context.Background(), outputPath, &destination); err == nil {
		t.Fatal("export followed a planted symlink")
	}
	if destination.Len() != 0 {
		t.Fatal("rejected symlink export wrote credential bytes")
	}
}

func TestProductionReservationRejectsPlantedMaterialAndReuse(t *testing.T) {
	outputPath := newProductionBrowserSessionOutputPath(t)
	if err := os.WriteFile(outputPath, []byte("planted"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ReserveBrowserSessionFile(context.Background(), outputPath); err == nil {
		t.Fatal("reservation replaced a planted regular file")
	}
	if raw, err := os.ReadFile(outputPath); err != nil || string(raw) != "planted" {
		t.Fatalf("planted file changed: %q, %v", raw, err)
	}
	if err := os.Remove(outputPath); err != nil {
		t.Fatal(err)
	}
	target := newBrowserSessionOutputPath(t)
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(target) })
	if err := os.Symlink(target, outputPath); err != nil {
		t.Fatal(err)
	}
	if err := ReserveBrowserSessionFile(context.Background(), outputPath); err == nil {
		t.Fatal("reservation replaced a planted symlink")
	}
	if err := os.Remove(outputPath); err != nil {
		t.Fatal(err)
	}
	if err := ReserveBrowserSessionFile(context.Background(), outputPath); err != nil {
		t.Fatalf("first reservation failed: %v", err)
	}
	if err := ReserveBrowserSessionFile(context.Background(), outputPath); err == nil {
		t.Fatal("duplicate reservation was accepted")
	}
	if err := CleanupBrowserSessionFile(context.Background(), outputPath); err != nil {
		t.Fatalf("reservation cleanup failed: %v", err)
	}
}

func TestProductionCleanupUsesCanonicalBoundedNamespace(t *testing.T) {
	for _, path := range []string{
		"/tmp/scribe-browser-session-test.json",
		"/tmp/scribe-browser-session-01-1.json",
		"/tmp/scribe-browser-session-1-01.json",
		" /tmp/scribe-browser-session-1-1.json",
	} {
		if err := CleanupBrowserSessionFile(context.Background(), path); err == nil {
			t.Fatalf("CleanupBrowserSessionFile(%q) accepted noncanonical path", path)
		}
	}

	paths := make([]string, 0, maximumBrowserSessionFiles+1)
	seed := time.Now().UnixNano()
	for index := 0; index <= maximumBrowserSessionFiles; index++ {
		path := fmt.Sprintf("/tmp/%s%d-1%s", browserSessionOutputPrefix, seed+int64(index), browserSessionOutputSuffix)
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	t.Cleanup(func() {
		for _, path := range paths {
			_ = os.Remove(path)
		}
	})
	if err := CleanupBrowserSessionFiles(context.Background()); err == nil {
		t.Fatal("CleanupBrowserSessionFiles accepted more than its bounded inventory")
	}
	for _, path := range paths {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("bounded failure partially removed %s: %v", path, err)
		}
	}
}

type gatedReader struct {
	started chan struct{}
	release chan struct{}
	data    []byte
	once    bool
}

func (reader *gatedReader) Read(target []byte) (int, error) {
	if !reader.once {
		reader.once = true
		close(reader.started)
		<-reader.release
	}
	if len(reader.data) == 0 {
		return 0, io.EOF
	}
	count := copy(target, reader.data)
	reader.data = reader.data[count:]
	return count, nil
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

func newProductionBrowserSessionOutputPath(t *testing.T) string {
	t.Helper()
	path := fmt.Sprintf("/tmp/%s%d-1%s", browserSessionOutputPrefix, time.Now().UnixNano(), browserSessionOutputSuffix)
	_ = os.Remove(path)
	t.Cleanup(func() { _ = os.Remove(path) })
	return path
}
