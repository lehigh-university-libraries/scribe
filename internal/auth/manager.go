package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"connectrpc.com/connect"
	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	"github.com/lehigh-university-libraries/scribe/internal/uploadref"
	optionsv1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1/options/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

type Manager struct {
	auth                       config.AuthConfig
	publicBaseURL              string
	providerSecretsVaultPrefix string
	sourceReadToken            string
	identities                 *store.IdentityStore
	apiKeys                    *store.APIKeyStore
	providerSecrets            providerSecretRepository
	items                      *store.ItemStore
	contexts                   *store.ContextStore
	jobs                       *store.TranscriptionJobStore
	google                     *GoogleOAuthManager
	reviewTokens               *editorReviewTokenSigner
	vault                      vaultClient
	jwksMu                     sync.Mutex
	jwksCache                  map[string]cachedJWKS
	jwksRefresh                singleflight.Group
	externalIdentityResolver   func(context.Context, uint64, uint64) (store.User, store.WorkspaceAccess, error)
	backgroundMu               sync.Mutex
	backgroundWG               sync.WaitGroup
	backgroundWaiting          bool
	providerCleanupStarted     bool
	sessionRetentionStarted    bool
}

const unmappedProcedurePermission = "authz:unmapped"

type vaultClient interface {
	Read(context.Context, string) (map[string]string, error)
	Write(context.Context, string, map[string]string) error
	Delete(context.Context, string) error
}

type providerSecretRepository interface {
	Create(context.Context, store.ProviderSecret) (store.ProviderSecret, error)
	Activate(context.Context, uint64, uint64) (store.ProviderSecret, error)
	GetLifecycle(context.Context, uint64, uint64) (store.ProviderSecret, error)
	MarkPendingCleanup(context.Context, uint64, uint64) error
	MarkActiveCleanup(context.Context, uint64, uint64) error
	ListCleanupCandidates(context.Context, time.Time, int) ([]store.ProviderSecret, error)
	DeleteInactive(context.Context, uint64, uint64) error
	ListVisible(context.Context, uint64, uint64) ([]store.ProviderSecret, error)
	GetVisible(context.Context, uint64, uint64, uint64) (store.ProviderSecret, error)
}

func NewManager(
	cfg config.Config,
	secrets config.Secrets,
	identities *store.IdentityStore,
	apiKeys *store.APIKeyStore,
	providerSecrets *store.ProviderSecretStore,
	items *store.ItemStore,
	contexts *store.ContextStore,
	jobs *store.TranscriptionJobStore,
	vault vaultClient,
) (*Manager, error) {
	manager := &Manager{
		auth:                       cfg.Auth,
		publicBaseURL:              cfg.PublicBaseURL,
		providerSecretsVaultPrefix: strings.Trim(strings.TrimSpace(cfg.Vault.Paths.ProviderSecrets), "/"),
		sourceReadToken:            strings.TrimSpace(cfg.IIIF.SourceReadToken),
		identities:                 identities,
		apiKeys:                    apiKeys,
		providerSecrets:            providerSecrets,
		items:                      items,
		contexts:                   contexts,
		jobs:                       jobs,
		vault:                      vault,
		jwksCache:                  make(map[string]cachedJWKS),
		reviewTokens:               newEditorReviewTokenSigner(cfg.Pagination.SigningKey),
	}
	clientID := strings.TrimSpace(secrets.GoogleOAuthClientID)
	clientSecret := strings.TrimSpace(secrets.GoogleOAuthClientSecret)
	if cfg.Auth.PreviewAnonymous {
		return manager, nil
	}
	if clientID == "" || clientSecret == "" {
		return nil, fmt.Errorf("google oauth requires client id and client secret in Vault")
	}
	googleManager, err := NewGoogleOAuthManager(secrets.GoogleOAuthClientID, secrets.GoogleOAuthClientSecret)
	if err != nil {
		return nil, err
	}
	manager.google = googleManager
	return manager, nil
}

func (m *Manager) Enabled() bool {
	return m.google != nil
}

func (m *Manager) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, err := m.authenticateRequest(r)
		if err != nil {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		if principal.Anonymous() && m.requiresAuthenticatedAPI(r) {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		if permission := requiredPermissionForPath(r.URL.Path, r.Method); permission != "" && !principalHasPermission(principal, permission) {
			http.Error(w, "permission denied", http.StatusForbidden)
			return
		}
		if principal.ScopedItemImageID > 0 && !editorReviewSessionAllowsHTTP(principal, r) {
			http.Error(w, "permission denied", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
	})
}

func (m *Manager) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /logout", m.handleLogout)
	mux.HandleFunc("GET /auth/review", m.handleEditorReviewRedeem)
	if m.auth.PreviewAnonymous {
		unavailable := func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "OAuth is disabled for isolated previews", http.StatusNotFound)
		}
		mux.HandleFunc("GET /auth/google", unavailable)
		mux.HandleFunc("GET /auth/callback/google", unavailable)
		return
	}
	if m.google != nil {
		mux.HandleFunc("GET /auth/google", m.handleGoogleLogin)
		mux.HandleFunc("GET /auth/callback/google", m.handleGoogleCallback)
	}
}

func (m *Manager) Interceptor() connect.Interceptor {
	return authzInterceptor{manager: m}
}

type authzInterceptor struct {
	manager *Manager
}

func (i authzInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		principal := i.manager.connectPrincipal(ctx)
		if err := i.manager.authorizeConnectRequest(ctx, principal, req.Spec().Procedure, req.Any()); err != nil {
			return nil, err
		}
		return next(ctx, req)
	}
}

func (i authzInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i authzInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		principal := i.manager.connectPrincipal(ctx)
		rule, err := extractAuthzRule(conn.Spec().Procedure)
		if err != nil {
			return connect.NewError(connect.CodePermissionDenied, err)
		}
		if rule == nil {
			return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("no authz rule for %s", conn.Spec().Procedure))
		}
		if rule.GetAllowAnonymous() {
			return next(ctx, conn)
		}
		if principal.Anonymous() {
			return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required"))
		}
		if rule.GetSessionOnly() && !strings.EqualFold(principal.AuthType, "session") {
			return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("session authentication required"))
		}
		if strings.TrimSpace(rule.GetResourceIdField()) == "" {
			if err := i.manager.authorizeConnectRule(ctx, principal, conn.Spec().Procedure, rule, nil); err != nil {
				return err
			}
			return next(ctx, conn)
		}
		return next(ctx, &authzStreamingHandlerConn{
			StreamingHandlerConn: conn,
			manager:              i.manager,
			ctx:                  ctx,
			principal:            principal,
			procedure:            conn.Spec().Procedure,
			rule:                 rule,
		})
	}
}

type authzStreamingHandlerConn struct {
	connect.StreamingHandlerConn
	manager   *Manager
	ctx       context.Context
	principal Principal
	procedure string
	rule      *optionsv1.AuthzRule
}

func (c *authzStreamingHandlerConn) Receive(msg any) error {
	if err := c.StreamingHandlerConn.Receive(msg); err != nil {
		return err
	}
	return c.manager.authorizeConnectRule(c.ctx, c.principal, c.procedure, c.rule, msg)
}

func (m *Manager) connectPrincipal(ctx context.Context) Principal {
	principal, ok := PrincipalFromContext(ctx)
	if !ok {
		return m.anonymousPrincipal()
	}
	return principal
}

func (m *Manager) authorizeConnectRequest(ctx context.Context, principal Principal, procedure string, request any) error {
	rule, err := extractAuthzRule(procedure)
	if err != nil {
		return connect.NewError(connect.CodePermissionDenied, err)
	}
	if rule == nil {
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("no authz rule for %s", procedure))
	}
	return m.authorizeConnectRule(ctx, principal, procedure, rule, request)
}

func (m *Manager) authorizeConnectRule(ctx context.Context, principal Principal, procedure string, rule *optionsv1.AuthzRule, request any) error {
	if rule.GetAllowAnonymous() {
		return nil
	}
	if principal.Anonymous() {
		return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required"))
	}
	if rule.GetSessionOnly() && !strings.EqualFold(principal.AuthType, "session") {
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("session authentication required"))
	}
	if principal.ScopedItemImageID > 0 {
		if err := m.authorizeEditorReviewSessionRequest(ctx, principal, procedure, rule, request); err != nil {
			return err
		}
	}

	resourceIDField := strings.TrimSpace(rule.GetResourceIdField())
	if resourceIDField == "" {
		allowed, authErr := m.authorizeProcedure(ctx, principal, procedure, rule.GetLevel())
		if authErr != nil {
			return connect.NewError(connect.CodeInternal, authErr)
		}
		if !allowed {
			return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("permission denied"))
		}
		return nil
	}

	resourceID, ok := extractFieldValue(request, resourceIDField)
	if !ok || strings.TrimSpace(resourceID) == "" {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s is required", resourceIDField))
	}

	allowed, authErr := m.authorizeResource(ctx, principal, procedure, rule.GetResource(), rule.GetLevel(), resourceID)
	if authErr != nil {
		return connect.NewError(connect.CodeInternal, authErr)
	}
	if !allowed {
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("permission denied"))
	}
	return nil
}

func (m *Manager) anonymousPrincipal() Principal {
	return Principal{
		Name:          "unauthenticated",
		Authenticated: false,
	}
}

func (m *Manager) previewPrincipal() Principal {
	return Principal{
		UserID:             store.AnonymousUserID,
		Email:              "preview@invalid",
		Name:               "Scribe preview",
		IsAdmin:            true,
		Authenticated:      true,
		AuthType:           "session",
		WorkspaceID:        store.AnonymousWorkspaceID,
		WorkspaceName:      "Preview",
		WorkspaceRole:      "admin",
		DefaultWorkspaceID: store.AnonymousWorkspaceID,
	}
}

func (m *Manager) handleGoogleLogin(w http.ResponseWriter, r *http.Request) {
	if m.google == nil {
		http.Error(w, "Google OAuth is not configured", http.StatusNotFound)
		return
	}
	redirectPath := safeRedirectPath(r.URL.Query().Get("redirect"))
	if redirectPath == "" {
		redirectPath = "/"
	}
	authURL, stateValue, err := m.google.BeginAuth(m.googleCallbackURL(r), redirectPath)
	if err != nil {
		slog.Error("start google oauth", "error_type", fmt.Sprintf("%T", err))
		http.Error(w, "authentication service unavailable", http.StatusInternalServerError)
		return
	}
	m.setOAuthStateCookie(w, stateValue.State)
	http.Redirect(w, r, authURL, http.StatusFound) // #nosec G710 -- BeginAuth returns the fixed Google OAuth authorization endpoint plus signed state.
}

func (m *Manager) handleGoogleCallback(w http.ResponseWriter, r *http.Request) {
	if m.google == nil {
		http.Error(w, "Google OAuth is not configured", http.StatusNotFound)
		return
	}
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	if code == "" || state == "" {
		http.Error(w, "missing OAuth code or state", http.StatusBadRequest)
		return
	}
	stateCookie, err := r.Cookie("scribe_oauth_state")
	m.clearOAuthStateCookie(w)
	if err != nil || !hmac.Equal([]byte(strings.TrimSpace(stateCookie.Value)), []byte(state)) {
		http.Error(w, "invalid OAuth browser state", http.StatusUnauthorized)
		return
	}
	profile, stateValue, err := m.google.CompleteAuth(r.Context(), m.googleCallbackURL(r), code, state)
	if err != nil {
		// OAuth provider errors can embed request or response details. Keep the
		// client response and structured log free of that untrusted content.
		slog.Warn("google oauth callback failed", "error_type", fmt.Sprintf("%T", err))
		http.Error(w, "OAuth authentication failed", http.StatusUnauthorized)
		return
	}
	if !m.emailAllowed(profile.Email) {
		http.Error(w, "account is not allowed to sign in", http.StatusForbidden)
		return
	}
	user, _, err := m.identities.EnsureGoogleUser(r.Context(), store.GoogleProfile{
		Subject:    profile.Subject,
		Email:      profile.Email,
		Name:       profile.Name,
		PictureURL: profile.PictureURL,
		IsAdmin:    emailInList(profile.Email, m.auth.AdminEmails),
	})
	if err != nil {
		slog.Error("persist google user", "error_type", fmt.Sprintf("%T", err))
		http.Error(w, "authentication service unavailable", http.StatusInternalServerError)
		return
	}
	token, err := randomString(48)
	if err != nil {
		slog.Error("generate auth session token", "error_type", fmt.Sprintf("%T", err))
		http.Error(w, "authentication service unavailable", http.StatusInternalServerError)
		return
	}
	if err := m.identities.CreateSession(r.Context(), user.ID, token, r.UserAgent(), ClientIP(r), m.auth.SessionTTL); err != nil {
		slog.Error("create auth session", "error_type", fmt.Sprintf("%T", err))
		http.Error(w, "authentication service unavailable", http.StatusInternalServerError)
		return
	}
	m.setSessionCookie(w, r, token)
	http.Redirect(w, r, safeRedirectPath(stateValue.RedirectPath), http.StatusFound)
}

func (m *Manager) handleLogout(w http.ResponseWriter, r *http.Request) {
	if !m.logoutRequestSameOrigin(r) {
		http.Error(w, "cross-origin logout is not allowed", http.StatusForbidden)
		return
	}
	if cookie, err := r.Cookie(m.auth.CookieName); err == nil && strings.TrimSpace(cookie.Value) != "" {
		_ = m.identities.DeleteSession(r.Context(), cookie.Value)
		_ = m.identities.DeleteEditorReviewSession(r.Context(), cookie.Value)
	}
	m.clearSessionCookie(w, r)
	if strings.Contains(strings.ToLower(r.Header.Get("Accept")), "application/json") {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

func (m *Manager) logoutRequestSameOrigin(r *http.Request) bool {
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "cross-site") {
		return false
	}
	rawOrigin := strings.TrimSpace(r.Header.Get("Origin"))
	if rawOrigin == "" {
		if referer := strings.TrimSpace(r.Header.Get("Referer")); referer != "" {
			parsed, err := url.Parse(referer)
			if err != nil {
				return false
			}
			parsed.Path, parsed.RawQuery, parsed.Fragment = "", "", ""
			rawOrigin = parsed.String()
		}
	}
	if rawOrigin == "" {
		// Non-browser API clients do not necessarily send Origin. Browser cross-
		// site POSTs carry Origin and modern fetch metadata, both checked above.
		return true
	}
	origin, err := url.Parse(rawOrigin)
	if err != nil || origin.Scheme == "" || origin.Host == "" || origin.User != nil {
		return false
	}
	expectedRaw := strings.TrimSpace(m.publicBaseURL)
	if expectedRaw == "" {
		expectedRaw = requestPublicBaseURL(r, "")
	}
	expected, err := url.Parse(expectedRaw)
	if err != nil {
		return false
	}
	return strings.EqualFold(origin.Scheme, expected.Scheme) && strings.EqualFold(origin.Host, expected.Host)
}

func (m *Manager) authenticateRequest(r *http.Request) (Principal, error) {
	if bearer := bearerTokenFromRequest(r); bearer != "" && constantTimeTokenEqual(bearer, m.sourceReadToken) {
		if !isExactTripletSourceRequest(r) {
			return Principal{}, fmt.Errorf("triplet source credential is not valid for this route")
		}
		return Principal{
			Name:          "Triplet image source reader",
			Authenticated: true,
			AuthType:      "triplet_source",
		}, nil
	}
	if m.auth.PreviewAnonymous {
		return m.previewPrincipal(), nil
	}
	if rawKey := strings.TrimSpace(r.Header.Get("X-Scribe-API-Key")); rawKey != "" {
		return m.apiKeyPrincipal(r.Context(), rawKey)
	}
	if bearer := bearerTokenFromRequest(r); bearer != "" {
		if looksLikeJWT(bearer) && len(m.auth.ExternalJWTIssuers) > 0 {
			return m.externalJWTPrincipal(r.Context(), bearer)
		}
		return m.apiKeyPrincipal(r.Context(), bearer)
	}
	cookie, err := r.Cookie(m.auth.CookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return m.anonymousPrincipal(), nil
	}
	requestedWorkspaceID, err := requestedWorkspaceID(r)
	if err != nil {
		return Principal{}, err
	}
	session, err := m.identities.GetSession(r.Context(), cookie.Value)
	if errors.Is(err, sql.ErrNoRows) {
		return m.editorReviewSessionPrincipal(r.Context(), cookie.Value, requestedWorkspaceID)
	}
	if err != nil {
		return Principal{}, err
	}
	workspaceID := requestedWorkspaceID
	if workspaceID == 0 {
		workspaceID = session.Workspace.ID
	}
	access, err := m.identities.GetWorkspaceAccess(r.Context(), session.User.ID, workspaceID)
	if err != nil {
		return Principal{}, err
	}
	return Principal{
		UserID:             session.User.ID,
		Email:              session.User.Email,
		Name:               session.User.Name,
		PictureURL:         session.User.PictureURL,
		IsAdmin:            session.User.IsAdmin,
		Authenticated:      true,
		AuthType:           "session",
		WorkspaceID:        access.Workspace.ID,
		WorkspaceName:      access.Workspace.Name,
		WorkspaceRole:      access.Role,
		DefaultWorkspaceID: session.Workspace.ID,
	}, nil
}

func (m *Manager) apiKeyPrincipal(ctx context.Context, rawKey string) (Principal, error) {
	if m.apiKeys == nil || m.identities == nil {
		return Principal{}, fmt.Errorf("api key authentication is unavailable")
	}
	apiKey, err := m.apiKeys.GetByToken(ctx, rawKey)
	if err != nil {
		return Principal{}, err
	}
	user, err := m.identities.GetUser(ctx, apiKey.CreatedByUserID)
	if err != nil {
		return Principal{}, err
	}
	access, err := m.identities.GetWorkspaceAccess(ctx, apiKey.CreatedByUserID, apiKey.WorkspaceID)
	if err != nil {
		return Principal{}, err
	}
	return Principal{
		UserID:             apiKey.CreatedByUserID,
		Email:              user.Email,
		Name:               user.Name,
		PictureURL:         user.PictureURL,
		Authenticated:      true,
		AuthType:           "api_key",
		WorkspaceID:        apiKey.WorkspaceID,
		WorkspaceName:      access.Workspace.Name,
		WorkspaceRole:      leastPrivilegedWorkspaceRole(apiKey.Role, access.Role),
		DefaultWorkspaceID: apiKey.WorkspaceID,
		APIKeyID:           apiKey.ID,
		APIKeyName:         apiKey.Name,
		Scopes:             apiKey.Scopes,
	}, nil
}

func constantTimeTokenEqual(candidate, expected string) bool {
	if expected == "" {
		return false
	}
	candidateDigest := sha256.Sum256([]byte(candidate))
	expectedDigest := sha256.Sum256([]byte(expected))
	return subtle.ConstantTimeCompare(candidateDigest[:], expectedDigest[:]) == 1
}

func isExactTripletSourceRequest(r *http.Request) bool {
	if r == nil || r.URL == nil || r.URL.RawQuery != "" || r.URL.Fragment != "" {
		return false
	}
	if !IsPublicUploadSourceRequest(r.URL.Path, r.Method) {
		return false
	}
	return r.URL.RawPath == "" || r.URL.EscapedPath() == r.URL.Path
}

func requestedWorkspaceID(r *http.Request) (uint64, error) {
	raw := strings.TrimSpace(r.Header.Get("X-Scribe-Workspace-ID"))
	if raw == "" {
		raw = strings.TrimSpace(r.URL.Query().Get("workspace_id"))
	}
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid workspace header")
	}
	return value, nil
}

func extractAPIKeyFromRequest(r *http.Request) string {
	if raw := strings.TrimSpace(r.Header.Get("X-Scribe-API-Key")); raw != "" {
		return raw
	}
	return bearerTokenFromRequest(r)
}

func bearerTokenFromRequest(r *http.Request) string {
	authz := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(authz), "bearer ") {
		return strings.TrimSpace(authz[7:])
	}
	return ""
}

func looksLikeJWT(raw string) bool {
	return strings.Count(strings.TrimSpace(raw), ".") == 2
}

func (m *Manager) requiresAuthenticatedAPI(r *http.Request) bool {
	path := r.URL.Path
	if IsPublicUploadSourceRequest(path, r.Method) {
		return false
	}
	if strings.HasPrefix(path, "/scribe.v1.") {
		rule, err := extractAuthzRule(path)
		// Unknown procedures and descriptor failures are protected at the HTTP
		// boundary. This rejects anonymous large RPCs before Connect reads or
		// validates their bodies while preserving explicitly anonymous methods.
		return err != nil || rule == nil || !rule.GetAllowAnonymous()
	}
	return strings.HasPrefix(path, "/v1/") ||
		strings.HasPrefix(path, "/static/uploads/")
}

// IsPublicUploadSourceRequest identifies the exact immutable source-byte
// routes that may have a public publication grant. Reaching the handler is not
// itself authorization: the handler still requires either annotation-read
// access to a referencing workspace or an explicit published page snapshot.
func IsPublicUploadSourceRequest(requestPath, method string) bool {
	if method != http.MethodGet && method != http.MethodHead {
		return false
	}
	_, ok := uploadref.ImmutableNameFromURL(requestPath)
	return ok
}

func (m *Manager) authorizeProcedure(ctx context.Context, principal Principal, procedure string, level optionsv1.AccessLevel) (bool, error) {
	permission := requiredPermissionForProcedure(procedure, level)
	if permission == "" {
		return principal.Authenticated, nil
	}
	return principalHasPermission(principal, permission), nil
}

func (m *Manager) authorizeResource(ctx context.Context, principal Principal, procedure string, resource optionsv1.ResourceType, level optionsv1.AccessLevel, resourceID string) (bool, error) {
	if allowed, err := m.authorizeProcedure(ctx, principal, procedure, level); err != nil || !allowed {
		return allowed, err
	}
	switch resource {
	case optionsv1.ResourceType_RESOURCE_TYPE_SYSTEM:
		return principal.IsAdmin, nil
	case optionsv1.ResourceType_RESOURCE_TYPE_ITEM:
		if principal.ScopedItemImageID > 0 && resourceID != principal.ScopedItemID {
			return false, nil
		}
		return m.items.WorkspaceOwnsItem(ctx, principal.WorkspaceID, resourceID)
	case optionsv1.ResourceType_RESOURCE_TYPE_ITEM_IMAGE:
		itemImageID, err := strconv.ParseUint(resourceID, 10, 64)
		if err != nil {
			return false, nil
		}
		if principal.ScopedItemImageID > 0 && itemImageID != principal.ScopedItemImageID {
			return false, nil
		}
		return m.items.WorkspaceOwnsItemImage(ctx, principal.WorkspaceID, itemImageID)
	case optionsv1.ResourceType_RESOURCE_TYPE_CONTEXT:
		contextID, err := strconv.ParseUint(resourceID, 10, 64)
		if err != nil {
			return false, nil
		}
		if level >= optionsv1.AccessLevel_ACCESS_LEVEL_WRITE {
			return m.contexts.WorkspaceCanWriteContext(ctx, principal.WorkspaceID, contextID)
		}
		return m.contexts.WorkspaceCanReadContext(ctx, principal.WorkspaceID, contextID)
	case optionsv1.ResourceType_RESOURCE_TYPE_TRANSCRIPTION_JOB:
		jobID, err := strconv.ParseUint(resourceID, 10, 64)
		if err != nil {
			return false, nil
		}
		if principal.ScopedItemImageID > 0 {
			job, loadErr := m.jobs.Get(ctx, jobID)
			if errors.Is(loadErr, sql.ErrNoRows) {
				return false, nil
			}
			if loadErr != nil {
				return false, loadErr
			}
			if job.WorkspaceID != principal.WorkspaceID || job.ItemImageID != principal.ScopedItemImageID {
				return false, nil
			}
		}
		return m.jobs.WorkspaceOwnsJob(ctx, principal.WorkspaceID, jobID)
	case optionsv1.ResourceType_RESOURCE_TYPE_WORKSPACE:
		workspaceID, err := strconv.ParseUint(resourceID, 10, 64)
		if err != nil {
			return false, nil
		}
		access, err := m.identities.GetWorkspaceAccess(ctx, principal.UserID, workspaceID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return false, nil
			}
			return false, err
		}
		if level >= optionsv1.AccessLevel_ACCESS_LEVEL_ADMIN {
			return workspaceRoleAllowsPermission(access.Role, "workspaces:admin"), nil
		}
		return workspaceRoleAllowsPermission(access.Role, "workspaces:read"), nil
	case optionsv1.ResourceType_RESOURCE_TYPE_UPLOAD_BATCH:
		return m.items.WorkspaceOwnsUploadBatch(ctx, principal.WorkspaceID, resourceID)
	case optionsv1.ResourceType_RESOURCE_TYPE_SELECTION_RULE:
		ruleID, err := strconv.ParseUint(resourceID, 10, 64)
		if err != nil {
			return false, nil
		}
		return m.contexts.WorkspaceOwnsSelectionRule(ctx, principal.WorkspaceID, ruleID)
	case optionsv1.ResourceType_RESOURCE_TYPE_API_KEY:
		keyID, err := strconv.ParseUint(resourceID, 10, 64)
		if err != nil {
			return false, nil
		}
		key, err := m.apiKeys.Get(ctx, keyID)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return key.WorkspaceID == principal.WorkspaceID, nil
	case optionsv1.ResourceType_RESOURCE_TYPE_PROVIDER_SECRET:
		secretID, err := strconv.ParseUint(resourceID, 10, 64)
		if err != nil {
			return false, nil
		}
		secret, err := m.providerSecrets.GetVisible(ctx, secretID, principal.WorkspaceID, principal.UserID)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if secret.Scope == "workspace" && !principal.IsAdmin && !strings.EqualFold(principal.WorkspaceRole, "admin") {
			return false, nil
		}
		return true, nil
	case optionsv1.ResourceType_RESOURCE_TYPE_USER:
		return principal.Authenticated, nil
	default:
		return false, fmt.Errorf("unsupported authorization resource %s", resource.String())
	}
}

func leastPrivilegedWorkspaceRole(left, right string) string {
	rank := func(role string) int {
		switch strings.ToLower(strings.TrimSpace(role)) {
		case "admin":
			return 4
		case "write":
			return 3
		case "create":
			return 2
		case "read":
			return 1
		default:
			return 0
		}
	}
	if rank(left) <= rank(right) {
		return strings.ToLower(strings.TrimSpace(left))
	}
	return strings.ToLower(strings.TrimSpace(right))
}

func requiredPermissionForProcedure(procedure string, level optionsv1.AccessLevel) string {
	switch procedure {
	case "/scribe.v1.WorkspaceService/ListWorkspaceMembers", "/scribe.v1.WorkspaceService/UpdateWorkspace", "/scribe.v1.WorkspaceService/AddWorkspaceMember", "/scribe.v1.WorkspaceService/UpdateWorkspaceMember", "/scribe.v1.WorkspaceService/DeleteWorkspaceMember":
		return ""
	case "/scribe.v1.WorkspaceService/ListWorkspaces":
		return "workspaces:read"
	case "/scribe.v1.WorkspaceService/CreateWorkspace":
		return ""
	case "/scribe.v1.ItemService/ListItems", "/scribe.v1.ItemService/GetItem", "/scribe.v1.ItemService/GetEditorManifest", "/scribe.v1.ItemService/GetUploadBatch", "/scribe.v1.ItemService/ListItemProviderCallAudits", "/scribe.v1.ImageProcessingService/GetOCRRun":
		return "items:read"
	case "/scribe.v1.ItemService/ImportManifest", "/scribe.v1.ItemService/StartUploadBatch", "/scribe.v1.ImageProcessingService/ProcessImageURL", "/scribe.v1.ImageProcessingService/ProcessHOCR":
		return "items:create"
	case "/scribe.v1.ItemService/UploadItemImage", "/scribe.v1.ItemService/CancelUploadBatch", "/scribe.v1.ItemService/DeleteItem", "/scribe.v1.ImageProcessingService/ReprocessItemImage":
		return "items:write"
	case "/scribe.v1.ContextService/ListContexts", "/scribe.v1.ContextService/GetContext", "/scribe.v1.ContextService/ListSelectionRules", "/scribe.v1.ContextService/ResolveContext", "/scribe.v1.ContextService/GetModelCatalog", "/scribe.v1.ContextService/GetContextMetrics":
		return "contexts:read"
	case "/scribe.v1.ContextService/CreateContext", "/scribe.v1.ContextService/UpdateContext", "/scribe.v1.ContextService/DeleteContext", "/scribe.v1.ContextService/CreateSelectionRule", "/scribe.v1.ContextService/DeleteSelectionRule":
		return "contexts:write"
	case "/scribe.v1.TranscriptionService/GetTranscriptionJob", "/scribe.v1.TranscriptionService/ListTranscriptionJobs", "/scribe.v1.TranscriptionService/StreamTranscriptionJob":
		return "transcription:read"
	case "/scribe.v1.TranscriptionService/CreateTranscriptionJob", "/scribe.v1.TranscriptionService/CancelTranscriptionJob":
		return "transcription:write"
	case "/scribe.v1.AuthService/ListAPIKeys", "/scribe.v1.AuthService/CreateAPIKey", "/scribe.v1.AuthService/DeleteAPIKey":
		return "admin:api_keys"
	case "/scribe.v1.WebhookService/CreateWebhook", "/scribe.v1.WebhookService/ListWebhooks", "/scribe.v1.WebhookService/DeleteWebhook":
		return "admin:webhooks"
	case "/scribe.v1.AuthService/ListProviderSecrets":
		return "contexts:read"
	case "/scribe.v1.AuthService/CreateEditorReviewToken":
		return "review_tokens:create"
	case "/scribe.v1.AuthService/CreateProviderSecret", "/scribe.v1.AuthService/DeleteProviderSecret":
		return "contexts:write"
	case "/scribe.v1.AnnotationService/GetAnnotationPage", "/scribe.v1.AnnotationService/SearchAnnotations", "/scribe.v1.AnnotationService/GetAnnotation", "/scribe.v1.AnnotationService/ExportAnnotationPage", "/scribe.v1.ItemService/PrepareItemExport":
		return "annotations:read"
	case "/scribe.v1.AnnotationService/SaveAnnotationPage", "/scribe.v1.AnnotationService/PublishItemImageEdits", "/scribe.v1.AnnotationService/EnrichAnnotation", "/scribe.v1.AnnotationService/SplitLineIntoWords", "/scribe.v1.AnnotationService/SplitPageIntoWords", "/scribe.v1.AnnotationService/SplitLineIntoTwoLines", "/scribe.v1.AnnotationService/JoinLines", "/scribe.v1.AnnotationService/JoinWordsIntoLine":
		return "annotations:write"
	default:
		return unmappedProcedurePermission
	}
}

func requiredPermissionForPath(path, method string) string {
	if IsPublicUploadSourceRequest(path, method) {
		return ""
	}
	if strings.HasPrefix(path, "/scribe.v1.") {
		// Connect authorization is owned by the protobuf method descriptor and
		// authz interceptor. The HTTP middleware only authenticates and attaches
		// the principal; duplicating RPC policy here can contradict allow_anonymous
		// or resource-scoped rules before the interceptor sees the request.
		return ""
	}
	switch {
	case (method == http.MethodGet || method == http.MethodHead) && strings.HasPrefix(path, "/static/uploads/"):
		return "annotations:read"
	case path == "/v1/events":
		return "transcription:read"
	case (method == http.MethodGet || method == http.MethodHead) && strings.HasPrefix(path, "/v1/item-exports/"):
		return "annotations:read"
	case (method == http.MethodGet || method == http.MethodHead) && strings.HasSuffix(path, "/hocr"):
		return "annotations:read"
	default:
		return ""
	}
}

func secretKeyHint(raw string) string {
	runes := []rune(raw)
	if len(runes) == 0 {
		return ""
	}
	if len(runes) <= 4 {
		return strings.Repeat("*", len(runes))
	}
	return "…" + string(runes[len(runes)-4:])
}

func principalHasPermission(principal Principal, permission string) bool {
	// An RPC missing from the explicit procedure table is a deployment error,
	// never a permission namespace that an administrator or wildcard API key
	// may inherit. Keep this check ahead of every administrator/scope bypass.
	if !principal.Authenticated || permission == unmappedProcedurePermission {
		return false
	}
	// API keys and mapped external JWTs are delegated credentials whose
	// configured workspace role and scopes are always authoritative. They must
	// never inherit broader session or system-administrator privileges.
	if principal.AuthType == "api_key" || principal.AuthType == "external_jwt" || principal.AuthType == "review_session" {
		return workspaceRoleAllowsPermission(principal.WorkspaceRole, permission) &&
			scopeListAllows(principal.Scopes, permission)
	}
	if principal.IsAdmin {
		return true
	}
	if !workspaceRoleAllowsPermission(principal.WorkspaceRole, permission) {
		return false
	}
	return true
}

func workspaceRoleAllowsPermission(role, permission string) bool {
	role = strings.ToLower(strings.TrimSpace(role))
	switch role {
	case "admin":
		return true
	case "write":
		return !strings.HasPrefix(permission, "admin:")
	case "create":
		return strings.HasSuffix(permission, ":read") || permission == "items:create"
	case "read":
		return strings.HasSuffix(permission, ":read")
	default:
		return false
	}
}

func scopeListAllows(scopes []string, permission string) bool {
	if len(scopes) == 0 {
		return false
	}
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		switch {
		case scope == permission:
			return true
		case strings.HasSuffix(scope, ":*") && strings.HasPrefix(permission, strings.TrimSuffix(scope, "*")):
			return true
		case scope == "*":
			return true
		}
	}
	return false
}

func (m *Manager) googleCallbackURL(r *http.Request) string {
	baseURL := requestPublicBaseURL(r, m.publicBaseURL)
	if baseURL == "" {
		return ""
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	u.Path = strings.TrimRight(u.Path, "/") + m.auth.GoogleCallbackPath
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func requestPublicBaseURL(r *http.Request, fallback string) string {
	fallback = strings.TrimRight(strings.TrimSpace(fallback), "/")
	if fallback != "" {
		return fallback
	}
	if r != nil {
		host := strings.TrimSpace(r.Host)
		if host != "" {
			scheme := "http"
			switch {
			case r.URL != nil && strings.TrimSpace(r.URL.Scheme) != "":
				scheme = strings.TrimSpace(r.URL.Scheme)
			case r.TLS != nil:
				scheme = "https"
			}
			return (&url.URL{Scheme: scheme, Host: host}).String()
		}
	}
	return ""
}

func (m *Manager) setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	m.setSessionCookieTTL(w, token, m.auth.SessionTTL)
}

func (m *Manager) setSessionCookieTTL(w http.ResponseWriter, token string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     m.auth.CookieName,
		Value:    token,
		Path:     "/",
		Domain:   m.auth.CookieDomain,
		MaxAge:   maxAgeSeconds(ttl),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

func (m *Manager) setOAuthStateCookie(w http.ResponseWriter, state string) {
	http.SetCookie(w, &http.Cookie{
		Name:     "scribe_oauth_state",
		Value:    state,
		Path:     m.auth.GoogleCallbackPath,
		MaxAge:   maxAgeSeconds(oauthStateLifetime),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

func (m *Manager) clearOAuthStateCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     "scribe_oauth_state",
		Value:    "",
		Path:     m.auth.GoogleCallbackPath,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

func (m *Manager) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     m.auth.CookieName,
		Value:    "",
		Path:     "/",
		Domain:   m.auth.CookieDomain,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

func maxAgeSeconds(ttl time.Duration) int {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return int(ttl / time.Second)
}

// ClientIP resolves the closest untrusted address in a proxy chain. Forwarded
// headers are ignored unless the direct peer belongs to an explicitly
// configured trusted network, so private-address spoofing fails closed.
func ClientIP(r *http.Request) string {
	return clientIPFrom(r, config.Get().Config.Server.TrustedProxyCIDRs)
}

func clientIPFrom(r *http.Request, trustedProxyCIDRs config.CIDRList) string {
	remote := strings.TrimSpace(r.RemoteAddr)
	if host, _, err := net.SplitHostPort(remote); err == nil {
		remote = host
	}
	if !trustedProxyIP(remote, trustedProxyCIDRs) {
		return remote
	}

	chain := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(chain) - 1; i >= 0; i-- {
		candidate := strings.Trim(strings.TrimSpace(chain[i]), "[]")
		ip := net.ParseIP(candidate)
		if ip == nil || trustedProxyIP(candidate, trustedProxyCIDRs) {
			continue
		}
		return ip.String()
	}
	if realIP := strings.Trim(strings.TrimSpace(r.Header.Get("X-Real-Ip")), "[]"); net.ParseIP(realIP) != nil && !trustedProxyIP(realIP, trustedProxyCIDRs) {
		return net.ParseIP(realIP).String()
	}
	return remote
}

func trustedProxyIP(raw string, cidrs config.CIDRList) bool {
	return config.AddressInCIDRs(raw, cidrs)
}

func safeRedirectPath(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 2048 || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || strings.ContainsAny(raw, "\\\r\n") {
		return "/"
	}
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed.IsAbs() || parsed.Host != "" {
		return "/"
	}
	return raw
}

func (m *Manager) emailAllowed(email string) bool {
	domain := ""
	if parts := strings.Split(strings.ToLower(strings.TrimSpace(email)), "@"); len(parts) == 2 {
		domain = parts[1]
	}
	if domain == "" {
		return false
	}
	for _, pattern := range m.auth.DeniedDomains {
		if domainMatches(domain, pattern) {
			return false
		}
	}
	if len(m.auth.AllowedDomains) == 0 {
		return true
	}
	for _, pattern := range m.auth.AllowedDomains {
		if domainMatches(domain, pattern) {
			return true
		}
	}
	return false
}

func emailInList(email string, values []string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	for _, value := range values {
		if email == strings.ToLower(strings.TrimSpace(value)) {
			return true
		}
	}
	return false
}

func domainMatches(domain, pattern string) bool {
	domain = strings.ToLower(strings.TrimSpace(domain))
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	if domain == "" || pattern == "" {
		return false
	}
	if strings.HasPrefix(pattern, "*.") {
		return strings.HasSuffix(domain, strings.TrimPrefix(pattern, "*"))
	}
	return domain == pattern
}

func UserIDFromContext(ctx context.Context) uint64 {
	if principal, ok := PrincipalFromContext(ctx); ok && principal.UserID > 0 {
		return principal.UserID
	}
	return 0
}

func WorkspaceIDFromContext(ctx context.Context) uint64 {
	if principal, ok := PrincipalFromContext(ctx); ok && principal.WorkspaceID > 0 {
		return principal.WorkspaceID
	}
	return 0
}

func extractAuthzRule(procedure string) (*optionsv1.AuthzRule, error) {
	procedure = strings.TrimPrefix(procedure, "/")
	procedure = strings.ReplaceAll(procedure, "/", ".")
	methodDesc, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(procedure))
	if err != nil {
		return nil, fmt.Errorf("authorization configuration error: %w", err)
	}
	methodOptions, ok := methodDesc.(protoreflect.MethodDescriptor).Options().(*descriptorpb.MethodOptions)
	if !ok || !proto.HasExtension(methodOptions, optionsv1.E_Authz) {
		return nil, nil
	}
	return proto.GetExtension(methodOptions, optionsv1.E_Authz).(*optionsv1.AuthzRule), nil
}

func extractFieldValue(message any, fieldPath string) (string, bool) {
	protoMessage, ok := message.(proto.Message)
	if !ok {
		return "", false
	}
	current := protoMessage.ProtoReflect()
	for _, rawSegment := range strings.Split(fieldPath, ".") {
		segment := strings.TrimSpace(rawSegment)
		if segment == "" || !current.IsValid() {
			return "", false
		}
		field := findFieldByPathSegment(current.Descriptor(), segment)
		if field == nil || !current.Has(field) {
			return "", false
		}
		value := current.Get(field)
		if field.Kind() == protoreflect.MessageKind || field.Kind() == protoreflect.GroupKind {
			current = value.Message()
			continue
		}
		return scalarFieldValue(value, field)
	}
	return "", false
}

func findFieldByPathSegment(desc protoreflect.MessageDescriptor, raw string) protoreflect.FieldDescriptor {
	fields := desc.Fields()
	camel := toCamelCase(raw)
	for i := 0; i < fields.Len(); i++ {
		field := fields.Get(i)
		if string(field.Name()) == raw || field.JSONName() == raw || field.JSONName() == camel {
			return field
		}
	}
	return nil
}

func scalarFieldValue(value protoreflect.Value, field protoreflect.FieldDescriptor) (string, bool) {
	switch field.Kind() {
	case protoreflect.StringKind:
		return value.String(), true
	case protoreflect.Uint32Kind, protoreflect.Uint64Kind, protoreflect.Fixed32Kind, protoreflect.Fixed64Kind:
		return strconv.FormatUint(value.Uint(), 10), true
	case protoreflect.Int32Kind, protoreflect.Int64Kind, protoreflect.Sint32Kind, protoreflect.Sint64Kind, protoreflect.Sfixed32Kind, protoreflect.Sfixed64Kind:
		return strconv.FormatInt(value.Int(), 10), true
	case protoreflect.BoolKind:
		return strconv.FormatBool(value.Bool()), true
	default:
		return value.String(), true
	}
}

func toCamelCase(raw string) string {
	if raw == "" {
		return raw
	}
	parts := strings.Split(raw, "_")
	for idx := 1; idx < len(parts); idx++ {
		if parts[idx] == "" {
			continue
		}
		parts[idx] = strings.ToUpper(parts[idx][:1]) + parts[idx][1:]
	}
	return strings.Join(parts, "")
}
