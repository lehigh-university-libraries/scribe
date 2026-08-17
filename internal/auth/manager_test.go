package auth

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
	optionsv1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1/options/v1"
	"github.com/lehigh-university-libraries/scribe/proto/scribe/v1/scribev1connect"
)

func TestNewManagerRequiresGoogleSecrets(t *testing.T) {
	t.Parallel()

	_, err := NewManager(config.Config{
		PublicBaseURL: "https://scribe.example",
		Auth: config.AuthConfig{
			CookieName:         "scribe_session",
			GoogleCallbackPath: "/auth/callback/google",
		},
	}, config.Secrets{}, nil, nil, nil, nil, nil, nil, nil)
	if err == nil {
		t.Fatal("NewManager succeeded without Google OAuth secrets")
	}
}

func TestPreviewAnonymousManagerIsIsolatedAndEmitsNoOAuthRedirect(t *testing.T) {
	t.Parallel()

	manager, err := NewManager(config.Config{
		PublicBaseURL: "https://scribe-pr-75-123.us-east5.run.app",
		Auth: config.AuthConfig{
			CookieName:         "scribe_session",
			GoogleCallbackPath: "/auth/callback/google",
			PreviewAnonymous:   true,
		},
	}, config.Secrets{}, nil, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewManager preview mode: %v", err)
	}

	var principal Principal
	request := httptest.NewRequest(http.MethodGet, "https://scribe-pr-75-123.us-east5.run.app/", nil)
	response := httptest.NewRecorder()
	manager.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, _ = PrincipalFromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || !principal.Authenticated || principal.AuthType != "session" || principal.WorkspaceID != store.AnonymousWorkspaceID || !principal.IsAdmin {
		t.Fatalf("preview principal/status = %+v/%d", principal, response.Code)
	}

	mux := http.NewServeMux()
	manager.RegisterRoutes(mux)
	oauthResponse := httptest.NewRecorder()
	mux.ServeHTTP(oauthResponse, httptest.NewRequest(http.MethodGet, "https://scribe-pr-75-123.us-east5.run.app/auth/google", nil))
	if oauthResponse.Code != http.StatusNotFound || strings.Contains(oauthResponse.Header().Get("Location"), "accounts.google.com") {
		t.Fatalf("preview OAuth response = %d/%q", oauthResponse.Code, oauthResponse.Header().Get("Location"))
	}

	me, err := manager.GetAuthMe(WithPrincipal(context.Background(), principal), connect.NewRequest(&scribev1.GetAuthMeRequest{}))
	if err != nil || !me.Msg.GetAuthenticated() || me.Msg.GetLoginUrl() != "" || me.Msg.GetLogoutUrl() != "" {
		t.Fatalf("preview GetAuthMe = %+v/%v", me, err)
	}
}

type successfulAuthVault struct {
	deletes   int
	writes    int
	last      map[string]string
	writeErr  error
	deleteErr error
}

func (*successfulAuthVault) Read(context.Context, string) (map[string]string, error) {
	return nil, nil
}

func (v *successfulAuthVault) Write(_ context.Context, _ string, values map[string]string) error {
	v.writes++
	v.last = make(map[string]string, len(values))
	for key, value := range values {
		v.last[key] = value
	}
	return v.writeErr
}

func (v *successfulAuthVault) Delete(context.Context, string) error {
	v.deletes++
	return v.deleteErr
}

type lifecycleProviderSecretRepository struct {
	secret                  *store.ProviderSecret
	markCalls               int
	deleteCalls             int
	activationResponseError error
}

type logoutSessionStoreStub struct {
	deleteSessionErr             error
	deleteEditorReviewSessionErr error
	deletedSessionTokens         []string
	deletedReviewSessionTokens   []string
}

func (s *logoutSessionStoreStub) DeleteSession(_ context.Context, rawToken string) error {
	s.deletedSessionTokens = append(s.deletedSessionTokens, rawToken)
	return s.deleteSessionErr
}

func (s *logoutSessionStoreStub) DeleteEditorReviewSession(_ context.Context, rawToken string) error {
	s.deletedReviewSessionTokens = append(s.deletedReviewSessionTokens, rawToken)
	return s.deleteEditorReviewSessionErr
}

func (r *lifecycleProviderSecretRepository) Create(_ context.Context, secret store.ProviderSecret) (store.ProviderSecret, error) {
	secret.ID = 41
	secret.LifecycleState = store.ProviderSecretPendingWrite
	r.secret = &secret
	return secret, nil
}

func (r *lifecycleProviderSecretRepository) Activate(context.Context, uint64, uint64) (store.ProviderSecret, error) {
	if r.secret == nil {
		return store.ProviderSecret{}, sql.ErrNoRows
	}
	r.secret.LifecycleState = store.ProviderSecretActive
	return *r.secret, r.activationResponseError
}

func (r *lifecycleProviderSecretRepository) GetLifecycle(context.Context, uint64, uint64) (store.ProviderSecret, error) {
	if r.secret == nil {
		return store.ProviderSecret{}, sql.ErrNoRows
	}
	return *r.secret, nil
}

func (r *lifecycleProviderSecretRepository) MarkPendingCleanup(_ context.Context, id, workspaceID uint64) error {
	r.markCalls++
	if r.secret == nil || r.secret.ID != id || r.secret.WorkspaceID != workspaceID || r.secret.LifecycleState != store.ProviderSecretPendingWrite {
		return sql.ErrNoRows
	}
	r.secret.LifecycleState = store.ProviderSecretCleanupPending
	return nil
}

func (r *lifecycleProviderSecretRepository) MarkActiveCleanup(_ context.Context, id, workspaceID uint64) error {
	r.markCalls++
	if r.secret == nil || r.secret.ID != id || r.secret.WorkspaceID != workspaceID || r.secret.LifecycleState != store.ProviderSecretActive {
		return sql.ErrNoRows
	}
	r.secret.LifecycleState = store.ProviderSecretCleanupPending
	return nil
}

func (r *lifecycleProviderSecretRepository) ListCleanupCandidates(context.Context, time.Time, int) ([]store.ProviderSecret, error) {
	if r.secret == nil || r.secret.LifecycleState == store.ProviderSecretActive {
		return nil, nil
	}
	return []store.ProviderSecret{*r.secret}, nil
}

func (r *lifecycleProviderSecretRepository) DeleteInactive(_ context.Context, id, workspaceID uint64) error {
	r.deleteCalls++
	if r.secret == nil {
		return sql.ErrNoRows
	}
	if r.secret.ID != id || r.secret.WorkspaceID != workspaceID || r.secret.LifecycleState == store.ProviderSecretActive {
		return sql.ErrNoRows
	}
	r.secret = nil
	return nil
}

func (r *lifecycleProviderSecretRepository) ListVisible(context.Context, uint64, uint64) ([]store.ProviderSecret, error) {
	if r.secret == nil || r.secret.LifecycleState != store.ProviderSecretActive {
		return nil, nil
	}
	return []store.ProviderSecret{*r.secret}, nil
}

func (r *lifecycleProviderSecretRepository) GetVisible(context.Context, uint64, uint64, uint64) (store.ProviderSecret, error) {
	if r.secret == nil || r.secret.LifecycleState != store.ProviderSecretActive {
		return store.ProviderSecret{}, sql.ErrNoRows
	}
	return *r.secret, nil
}

func TestAmbiguousVaultWriteRetainsHiddenLocatorUntilReconciled(t *testing.T) {
	repository := &lifecycleProviderSecretRepository{}
	vault := &successfulAuthVault{
		writeErr:  errors.New("ambiguous Vault write"),
		deleteErr: errors.New("vault temporarily unavailable"),
	}
	manager := &Manager{providerSecrets: repository, vault: vault}
	ctx := WithPrincipal(context.Background(), Principal{
		UserID: 7, WorkspaceID: 8, WorkspaceRole: "admin", Authenticated: true, AuthType: "session",
	})

	_, err := manager.CreateProviderSecret(ctx, connect.NewRequest(&scribev1.CreateProviderSecretRequest{
		Provider: "openai", Name: "tracked ambiguous write", ApiKey: "opaque-secret", Scope: "user",
	}))
	if connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("CreateProviderSecret error = %v, want internal", err)
	}
	if repository.secret == nil || repository.secret.LifecycleState != store.ProviderSecretCleanupPending {
		t.Fatalf("ambiguous write locator = %+v, want cleanup_pending", repository.secret)
	}
	if repository.deleteCalls != 0 {
		t.Fatalf("metadata delete calls while Vault cleanup failed = %d, want 0", repository.deleteCalls)
	}
	visible, err := repository.ListVisible(ctx, 8, 7)
	if err != nil || len(visible) != 0 {
		t.Fatalf("cleanup-pending secret visibility = %+v/%v, want hidden", visible, err)
	}

	vault.deleteErr = nil
	cleaned, err := manager.ReconcileProviderSecretCleanups(ctx)
	if err != nil || cleaned != 1 || repository.secret != nil {
		t.Fatalf("reconcile cleanup = %d/%v with locator %+v, want 1/nil", cleaned, err, repository.secret)
	}
	cleaned, err = manager.ReconcileProviderSecretCleanups(ctx)
	if err != nil || cleaned != 0 {
		t.Fatalf("idempotent reconcile cleanup = %d/%v, want 0/nil", cleaned, err)
	}
}

func TestAmbiguousActivationResponseReturnsCommittedActiveSecret(t *testing.T) {
	repository := &lifecycleProviderSecretRepository{activationResponseError: errors.New("connection lost after commit")}
	vault := &successfulAuthVault{}
	manager := &Manager{providerSecrets: repository, vault: vault}
	ctx := WithPrincipal(context.Background(), Principal{
		UserID: 7, WorkspaceID: 8, WorkspaceRole: "admin", Authenticated: true, AuthType: "session",
	})

	response, err := manager.CreateProviderSecret(ctx, connect.NewRequest(&scribev1.CreateProviderSecretRequest{
		Provider: "openai", Name: "committed activation", ApiKey: "opaque-secret", Scope: "user",
	}))
	if err != nil || response.Msg.GetProviderSecret().GetId() != 41 {
		t.Fatalf("CreateProviderSecret response/error = %+v/%v, want committed secret", response, err)
	}
	if repository.secret == nil || repository.secret.LifecycleState != store.ProviderSecretActive {
		t.Fatalf("committed activation locator = %+v, want active", repository.secret)
	}
	if repository.markCalls != 0 || repository.deleteCalls != 0 || vault.deletes != 0 || vault.writes != 1 {
		t.Fatalf("ambiguous activation compensation = marks %d metadata deletes %d Vault writes/deletes %d/%d", repository.markCalls, repository.deleteCalls, vault.writes, vault.deletes)
	}
}

func TestProviderSecretDefaultsToWorkspaceForDurableProcessing(t *testing.T) {
	repository := &lifecycleProviderSecretRepository{}
	vault := &successfulAuthVault{}
	manager := &Manager{providerSecrets: repository, vault: vault}
	ctx := WithPrincipal(context.Background(), Principal{
		UserID: 7, WorkspaceID: 8, WorkspaceRole: "admin", Authenticated: true, AuthType: "session",
	})

	response, err := manager.CreateProviderSecret(ctx, connect.NewRequest(&scribev1.CreateProviderSecretRequest{
		Provider: "openai", Name: "queued processing", ApiKey: "opaque-secret",
	}))
	if err != nil {
		t.Fatalf("CreateProviderSecret: %v", err)
	}
	if response.Msg.GetProviderSecret().GetScope() != "workspace" || repository.secret == nil || repository.secret.UserID != nil {
		t.Fatalf("default provider secret = response %+v / stored %+v, want workspace scope", response.Msg.GetProviderSecret(), repository.secret)
	}
}

func TestPersistenceFailuresUseInternalCodeWithoutStoreDetails(t *testing.T) {
	databasePool, err := sql.Open("mysql", "scribe:secret@tcp(127.0.0.1:1)/closed")
	if err != nil {
		t.Fatal(err)
	}
	if err := databasePool.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := WithPrincipal(context.Background(), Principal{
		UserID: 7, WorkspaceID: 8, WorkspaceRole: "admin", Authenticated: true, AuthType: "session",
	})

	apiManager := &Manager{apiKeys: store.NewAPIKeyStore(databasePool)}
	_, err = apiManager.CreateAPIKey(ctx, connect.NewRequest(&scribev1.CreateAPIKeyRequest{Name: "test", Role: "read"}))
	if connect.CodeOf(err) != connect.CodeInternal || strings.Contains(strings.ToLower(err.Error()), "database") || strings.Contains(strings.ToLower(err.Error()), "sql") {
		t.Fatalf("CreateAPIKey persistence error = %v, want sanitized internal", err)
	}

	vault := &successfulAuthVault{}
	secretManager := &Manager{providerSecrets: store.NewProviderSecretStore(databasePool), vault: vault}
	_, err = secretManager.CreateProviderSecret(ctx, connect.NewRequest(&scribev1.CreateProviderSecretRequest{
		Provider: "openai", Name: "test", ApiKey: "short", Scope: "user",
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument || vault.writes != 0 {
		t.Fatalf("short CreateProviderSecret = %v with %d Vault writes, want invalid argument before persistence", err, vault.writes)
	}

	const opaqueAPIKey = " secret-key "
	_, err = secretManager.CreateProviderSecret(ctx, connect.NewRequest(&scribev1.CreateProviderSecretRequest{
		Provider: "openai", Name: "test", ApiKey: opaqueAPIKey, Scope: "user",
	}))
	if connect.CodeOf(err) != connect.CodeInternal || strings.Contains(strings.ToLower(err.Error()), "database") || strings.Contains(strings.ToLower(err.Error()), "sql") {
		t.Fatalf("CreateProviderSecret persistence error = %v, want sanitized internal", err)
	}
	if vault.deletes != 0 {
		t.Fatalf("Vault deletes after pre-write persistence failure = %d, want 0", vault.deletes)
	}
	if vault.writes != 0 || vault.last["api_key"] != "" {
		t.Fatalf("Vault credential write count/value = %d/%q, want no write before durable metadata", vault.writes, vault.last["api_key"])
	}

	workspaceManager := &Manager{identities: store.NewIdentityStore(databasePool)}
	_, err = workspaceManager.CreateWorkspace(ctx, connect.NewRequest(&scribev1.CreateWorkspaceRequest{Name: "test"}))
	if connect.CodeOf(err) != connect.CodeInternal || strings.Contains(strings.ToLower(err.Error()), "database") || strings.Contains(strings.ToLower(err.Error()), "sql") {
		t.Fatalf("CreateWorkspace persistence error = %v, want sanitized internal", err)
	}
}

func TestNewManagerWithPartialGoogleSecretsFails(t *testing.T) {
	t.Parallel()

	_, err := NewManager(config.Config{
		PublicBaseURL: "https://scribe.example",
		Auth: config.AuthConfig{
			CookieName:         "scribe_session",
			GoogleCallbackPath: "/auth/callback/google",
		},
	}, config.Secrets{
		GoogleOAuthClientID: "client-id-only",
	}, nil, nil, nil, nil, nil, nil, nil)
	if err == nil {
		t.Fatal("NewManager succeeded with incomplete Google OAuth configuration")
	}
}

func TestNewManagerWithoutPublicBaseURLEnablesAuth(t *testing.T) {
	t.Parallel()

	manager, err := NewManager(config.Config{
		Auth: config.AuthConfig{
			CookieName:         "scribe_session",
			GoogleCallbackPath: "/auth/callback/google",
		},
	}, config.Secrets{
		GoogleOAuthClientID:     "client-id",
		GoogleOAuthClientSecret: "client-secret",
	}, nil, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewManager returned error without public base URL: %v", err)
	}
	if !manager.Enabled() {
		t.Fatal("manager.Enabled() = false, want true without public base URL")
	}
}

func TestGetAuthMeAnonymousDescriptorSurvivesHTTPMiddleware(t *testing.T) {
	t.Parallel()
	manager := &Manager{}
	path, rpcHandler := scribev1connect.NewAuthServiceHandler(manager, connect.WithInterceptors(manager.Interceptor()))
	mux := http.NewServeMux()
	mux.Handle(path, rpcHandler)
	server := httptest.NewServer(manager.Middleware(mux))
	t.Cleanup(server.Close)

	client := scribev1connect.NewAuthServiceClient(http.DefaultClient, server.URL)
	response, err := client.GetAuthMe(context.Background(), connect.NewRequest(&scribev1.GetAuthMeRequest{}))
	if err != nil {
		t.Fatalf("anonymous GetAuthMe: %v", err)
	}
	if response.Msg.GetAuthenticated() {
		t.Fatal("anonymous GetAuthMe reported an authenticated principal")
	}
	if _, err := client.ListAPIKeys(context.Background(), connect.NewRequest(&scribev1.ListAPIKeysRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("anonymous protected RPC error = %v, want unauthenticated", err)
	}

	for _, role := range []string{"read", "create", "write", "admin"} {
		principal := Principal{Authenticated: true, WorkspaceRole: role}
		if err := manager.authorizeConnectRequest(context.Background(), principal, scribev1connect.AuthServiceGetAuthMeProcedure, &scribev1.GetAuthMeRequest{}); err != nil {
			t.Errorf("GetAuthMe descriptor rejected %s role: %v", role, err)
		}
	}
}

type readTrackingBody struct {
	reads int
}

func (b *readTrackingBody) Read([]byte) (int, error) {
	b.reads++
	return 0, io.EOF
}

func (*readTrackingBody) Close() error { return nil }

func TestAnonymousProtectedRPCIsRejectedBeforeBodyRead(t *testing.T) {
	t.Parallel()
	manager := &Manager{}
	body := &readTrackingBody{}
	request := httptest.NewRequest(http.MethodPost, "https://scribe.example/scribe.v1.ItemService/UploadItemImage", nil)
	request.Body = body
	request.ContentLength = 1 << 30
	response := httptest.NewRecorder()
	dispatched := false

	manager.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		dispatched = true
	})).ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized || dispatched || body.reads != 0 {
		t.Fatalf("anonymous protected RPC status/dispatched/body reads = %d/%t/%d, want 401/false/0", response.Code, dispatched, body.reads)
	}
}

func TestRequestedWorkspaceID(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest("GET", "https://scribe.example/v1/items", nil)
	req.Header.Set("X-Scribe-Workspace-ID", "42")
	got, err := requestedWorkspaceID(req)
	if err != nil {
		t.Fatalf("requestedWorkspaceID returned error: %v", err)
	}
	if got != 42 {
		t.Fatalf("requestedWorkspaceID = %d, want 42", got)
	}
}

func TestRequestedWorkspaceIDRejectsInvalidHeader(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest("GET", "https://scribe.example/v1/items", nil)
	req.Header.Set("X-Scribe-Workspace-ID", "bad")
	if _, err := requestedWorkspaceID(req); err == nil {
		t.Fatal("requestedWorkspaceID succeeded with invalid header")
	}
}

func TestGoogleCallbackURLUsesConfiguredPublicBaseURL(t *testing.T) {
	t.Parallel()

	manager := &Manager{
		auth: config.AuthConfig{
			GoogleCallbackPath: "/auth/callback/google",
		},
		publicBaseURL: "https://scribe.example",
	}
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/auth/google", nil)
	req.Host = "internal-service"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "attacker.example")

	if got := manager.googleCallbackURL(req); got != "https://scribe.example/auth/callback/google" {
		t.Fatalf("googleCallbackURL = %q, want %q", got, "https://scribe.example/auth/callback/google")
	}
}

func TestGoogleCallbackURLFallsBackToRequestOrigin(t *testing.T) {
	t.Parallel()

	manager := &Manager{
		auth: config.AuthConfig{
			GoogleCallbackPath: "/auth/callback/google",
		},
	}
	req := httptest.NewRequest(http.MethodGet, "https://scribe.example/auth/google", nil)
	req.Host = "scribe.example"
	req.Header.Set("X-Forwarded-Host", "attacker.example")

	if got := manager.googleCallbackURL(req); got != "https://scribe.example/auth/callback/google" {
		t.Fatalf("googleCallbackURL = %q, want %q", got, "https://scribe.example/auth/callback/google")
	}
}

func TestExtractAPIKeyFromRequest(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest("GET", "https://scribe.example/v1/items", nil)
	req.Header.Set("X-Scribe-API-Key", "scribe_header")
	req.Header.Set("Authorization", "Bearer scribe_bearer")
	if got := extractAPIKeyFromRequest(req); got != "scribe_header" {
		t.Fatalf("extractAPIKeyFromRequest = %q, want header token", got)
	}

	req = httptest.NewRequest("GET", "https://scribe.example/v1/items", nil)
	req.Header.Set("Authorization", "Bearer scribe_bearer")
	if got := extractAPIKeyFromRequest(req); got != "scribe_bearer" {
		t.Fatalf("extractAPIKeyFromRequest = %q, want bearer token", got)
	}
}

func TestLogoutIsPOSTOnlyAndRejectsCrossOriginBrowsers(t *testing.T) {
	t.Parallel()
	manager := &Manager{
		auth:          config.AuthConfig{CookieName: "scribe_session"},
		publicBaseURL: "https://scribe.example",
	}
	mux := http.NewServeMux()
	manager.RegisterRoutes(mux)

	getRequest := httptest.NewRequest(http.MethodGet, "https://scribe.example/logout", nil)
	getResponse := httptest.NewRecorder()
	mux.ServeHTTP(getResponse, getRequest)
	if getResponse.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /logout status = %d, want %d", getResponse.Code, http.StatusMethodNotAllowed)
	}

	crossOrigin := httptest.NewRequest(http.MethodPost, "https://scribe.example/logout", nil)
	crossOrigin.Header.Set("Origin", "https://attacker.example")
	crossOrigin.Header.Set("Sec-Fetch-Site", "cross-site")
	crossResponse := httptest.NewRecorder()
	mux.ServeHTTP(crossResponse, crossOrigin)
	if crossResponse.Code != http.StatusForbidden {
		t.Fatalf("cross-origin POST /logout status = %d, want %d", crossResponse.Code, http.StatusForbidden)
	}

	sameOrigin := httptest.NewRequest(http.MethodPost, "https://scribe.example/logout", nil)
	sameOrigin.Header.Set("Origin", "https://scribe.example")
	sameOrigin.Header.Set("Accept", "application/json")
	sameResponse := httptest.NewRecorder()
	mux.ServeHTTP(sameResponse, sameOrigin)
	if sameResponse.Code != http.StatusOK {
		t.Fatalf("same-origin POST /logout status = %d, want %d", sameResponse.Code, http.StatusOK)
	}
}

func TestLogoutFailsClosedWhenSessionRevocationFails(t *testing.T) {
	t.Parallel()

	const rawToken = "sensitive-session-token"
	for _, test := range []struct {
		name                         string
		deleteSessionErr             error
		deleteEditorReviewSessionErr error
	}{
		{
			name:             "browser session deletion fails",
			deleteSessionErr: errors.New("database rejected sensitive-session-token"),
		},
		{
			name:                         "editor review session deletion fails",
			deleteEditorReviewSessionErr: errors.New("database rejected sensitive-session-token"),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			sessions := &logoutSessionStoreStub{
				deleteSessionErr:             test.deleteSessionErr,
				deleteEditorReviewSessionErr: test.deleteEditorReviewSessionErr,
			}
			manager := &Manager{
				auth:           config.AuthConfig{CookieName: "scribe_session"},
				publicBaseURL:  "https://scribe.example",
				logoutSessions: sessions,
			}

			request := httptest.NewRequest(http.MethodPost, "https://scribe.example/logout", nil)
			request.Header.Set("Origin", "https://scribe.example")
			request.Header.Set("Accept", "application/json")
			request.AddCookie(&http.Cookie{Name: "scribe_session", Value: rawToken})
			response := httptest.NewRecorder()
			manager.handleLogout(response, request)

			if response.Code != http.StatusInternalServerError {
				t.Fatalf("POST /logout status = %d, want %d", response.Code, http.StatusInternalServerError)
			}
			if got := response.Header().Get("Location"); got != "" {
				t.Fatalf("POST /logout Location = %q, want empty", got)
			}
			if cookies := response.Header().Values("Set-Cookie"); len(cookies) != 0 {
				t.Fatalf("POST /logout cleared the cookie after failed revocation: %v", cookies)
			}
			if body := response.Body.String(); body != "logout service unavailable\n" || strings.Contains(body, rawToken) {
				t.Fatalf("POST /logout body = %q, want categorical error without token", body)
			}
			if len(sessions.deletedSessionTokens) != 1 || sessions.deletedSessionTokens[0] != rawToken {
				t.Fatalf("browser session delete calls = %v, want [%q]", sessions.deletedSessionTokens, rawToken)
			}
			if len(sessions.deletedReviewSessionTokens) != 1 || sessions.deletedReviewSessionTokens[0] != rawToken {
				t.Fatalf("review session delete calls = %v, want [%q]", sessions.deletedReviewSessionTokens, rawToken)
			}
		})
	}
}

func TestLogoutClearsCookieAndSucceedsAfterAllSessionRevocationsSucceed(t *testing.T) {
	t.Parallel()

	const rawToken = "session-token"
	sessions := &logoutSessionStoreStub{}
	manager := &Manager{
		auth:           config.AuthConfig{CookieName: "scribe_session"},
		publicBaseURL:  "https://scribe.example",
		logoutSessions: sessions,
	}
	request := httptest.NewRequest(http.MethodPost, "https://scribe.example/logout", nil)
	request.Header.Set("Origin", "https://scribe.example")
	request.AddCookie(&http.Cookie{Name: "scribe_session", Value: rawToken})
	response := httptest.NewRecorder()
	manager.handleLogout(response, request)

	if response.Code != http.StatusFound || response.Header().Get("Location") != "/" {
		t.Fatalf("POST /logout response = %d/%q, want %d/%q", response.Code, response.Header().Get("Location"), http.StatusFound, "/")
	}
	if len(sessions.deletedSessionTokens) != 1 || len(sessions.deletedReviewSessionTokens) != 1 {
		t.Fatalf("POST /logout delete calls = %v/%v, want one each", sessions.deletedSessionTokens, sessions.deletedReviewSessionTokens)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "scribe_session" || cookies[0].MaxAge >= 0 || cookies[0].Value != "" {
		t.Fatalf("POST /logout cookies = %+v, want one expired empty session cookie", cookies)
	}
}

func TestAuthorizationFailsClosedForUnspecifiedResources(t *testing.T) {
	t.Parallel()
	manager := &Manager{}
	principal := Principal{Authenticated: true, WorkspaceRole: "admin"}
	allowed, err := manager.authorizeResource(
		context.Background(), principal, "/scribe.v1.AuthService/ListAPIKeys",
		optionsv1.ResourceType_RESOURCE_TYPE_UNSPECIFIED, optionsv1.AccessLevel_ACCESS_LEVEL_READ, "1",
	)
	if err == nil || allowed {
		t.Fatalf("unspecified resource authorization = %t/%v, want fail closed", allowed, err)
	}
	allowed, err = manager.authorizeResource(
		context.Background(), principal, "/scribe.v1.WorkspaceService/ListWorkspaces",
		optionsv1.ResourceType_RESOURCE_TYPE_USER, optionsv1.AccessLevel_ACCESS_LEVEL_READ, "1",
	)
	if err != nil || !allowed {
		t.Fatalf("principal-scoped USER authorization = %t/%v, want allowed", allowed, err)
	}
}

func TestLeastPrivilegedWorkspaceRole(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ key, member, want string }{
		{key: "admin", member: "read", want: "read"},
		{key: "write", member: "admin", want: "write"},
		{key: "admin", member: "create", want: "create"},
		{key: "read", member: "write", want: "read"},
	} {
		if got := leastPrivilegedWorkspaceRole(test.key, test.member); got != test.want {
			t.Errorf("leastPrivilegedWorkspaceRole(%q, %q) = %q, want %q", test.key, test.member, got, test.want)
		}
	}
}
