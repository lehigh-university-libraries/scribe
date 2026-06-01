package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/store"
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
	identities                 *store.IdentityStore
	apiKeys                    *store.APIKeyStore
	providerSecrets            *store.ProviderSecretStore
	items                      *store.ItemStore
	contexts                   *store.ContextStore
	jobs                       *store.TranscriptionJobStore
	google                     *GoogleOAuthManager
	vault                      vaultClient
	jwksMu                     sync.Mutex
	jwksCache                  map[string]cachedJWKS
}

type vaultClient interface {
	Read(context.Context, string) (map[string]string, error)
	Write(context.Context, string, map[string]string) error
	Delete(context.Context, string) error
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
		identities:                 identities,
		apiKeys:                    apiKeys,
		providerSecrets:            providerSecrets,
		items:                      items,
		contexts:                   contexts,
		jobs:                       jobs,
		vault:                      vault,
		jwksCache:                  make(map[string]cachedJWKS),
	}
	clientID := strings.TrimSpace(secrets.GoogleOAuthClientID)
	clientSecret := strings.TrimSpace(secrets.GoogleOAuthClientSecret)
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
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
	})
}

func (m *Manager) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /logout", m.handleLogout)
	mux.HandleFunc("POST /logout", m.handleLogout)
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

func (m *Manager) handleGoogleLogin(w http.ResponseWriter, r *http.Request) {
	if m.google == nil {
		http.Error(w, "Google OAuth is not configured", http.StatusNotFound)
		return
	}
	redirectPath := safeRedirectPath(r.URL.Query().Get("redirect"))
	if redirectPath == "" {
		redirectPath = "/"
	}
	authURL, err := m.google.BeginAuth(m.googleCallbackURL(r), redirectPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("start google oauth: %v", err), http.StatusInternalServerError)
		return
	}
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
	profile, stateValue, err := m.google.CompleteAuth(r.Context(), m.googleCallbackURL(r), code, state)
	if err != nil {
		http.Error(w, fmt.Sprintf("complete google oauth: %v", err), http.StatusUnauthorized)
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
		http.Error(w, fmt.Sprintf("persist google user: %v", err), http.StatusInternalServerError)
		return
	}
	token, err := randomString(48)
	if err != nil {
		http.Error(w, fmt.Sprintf("generate session token: %v", err), http.StatusInternalServerError)
		return
	}
	if err := m.identities.CreateSession(r.Context(), user.ID, token, r.UserAgent(), requestIP(r), m.auth.SessionTTL); err != nil {
		http.Error(w, fmt.Sprintf("create auth session: %v", err), http.StatusInternalServerError)
		return
	}
	m.setSessionCookie(w, r, token)
	http.Redirect(w, r, safeRedirectPath(stateValue.RedirectPath), http.StatusFound)
}

func (m *Manager) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(m.auth.CookieName); err == nil && strings.TrimSpace(cookie.Value) != "" {
		_ = m.identities.DeleteSession(r.Context(), cookie.Value)
	}
	m.clearSessionCookie(w, r)
	if strings.Contains(strings.ToLower(r.Header.Get("Accept")), "application/json") {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

func (m *Manager) authenticateRequest(r *http.Request) (Principal, error) {
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
		return m.anonymousPrincipal(), nil
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
	apiKey, err := m.apiKeys.GetByToken(ctx, rawKey)
	if err != nil {
		return Principal{}, err
	}
	user, err := m.identities.GetUser(ctx, apiKey.CreatedByUserID)
	if err != nil {
		return Principal{}, err
	}
	workspaceName := ""
	if access, accessErr := m.identities.GetWorkspaceAccess(ctx, apiKey.CreatedByUserID, apiKey.WorkspaceID); accessErr == nil {
		workspaceName = access.Workspace.Name
	}
	return Principal{
		UserID:             apiKey.CreatedByUserID,
		Email:              user.Email,
		Name:               user.Name,
		PictureURL:         user.PictureURL,
		IsAdmin:            user.IsAdmin,
		Authenticated:      true,
		AuthType:           "api_key",
		WorkspaceID:        apiKey.WorkspaceID,
		WorkspaceName:      workspaceName,
		WorkspaceRole:      apiKey.Role,
		DefaultWorkspaceID: apiKey.WorkspaceID,
		APIKeyID:           apiKey.ID,
		APIKeyName:         apiKey.Name,
		Scopes:             apiKey.Scopes,
	}, nil
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
	path := strings.TrimSpace(r.URL.Path)
	return strings.HasPrefix(path, "/v1/") ||
		strings.HasPrefix(path, "/scribe.v1.")
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
		return m.items.WorkspaceOwnsItem(ctx, principal.WorkspaceID, resourceID)
	case optionsv1.ResourceType_RESOURCE_TYPE_ITEM_IMAGE:
		itemImageID, err := strconv.ParseUint(resourceID, 10, 64)
		if err != nil {
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
	default:
		return true, nil
	}
}

func requiredPermissionForProcedure(procedure string, level optionsv1.AccessLevel) string {
	switch procedure {
	case "/scribe.v1.WorkspaceService/ListWorkspaceMembers", "/scribe.v1.WorkspaceService/UpdateWorkspace", "/scribe.v1.WorkspaceService/AddWorkspaceMember", "/scribe.v1.WorkspaceService/UpdateWorkspaceMember", "/scribe.v1.WorkspaceService/DeleteWorkspaceMember":
		return ""
	case "/scribe.v1.WorkspaceService/ListWorkspaces":
		return "workspaces:read"
	case "/scribe.v1.WorkspaceService/CreateWorkspace":
		return ""
	case "/scribe.v1.ItemService/ListItems", "/scribe.v1.ItemService/GetItem", "/scribe.v1.ImageProcessingService/GetOCRRun":
		return "items:read"
	case "/scribe.v1.ItemService/CreateItem", "/scribe.v1.ImageProcessingService/ProcessImageURL", "/scribe.v1.ImageProcessingService/ProcessImageUpload", "/scribe.v1.ImageProcessingService/ProcessHOCR":
		return "items:create"
	case "/scribe.v1.ItemService/UploadItemImage", "/scribe.v1.ItemService/DeleteItem", "/scribe.v1.ImageProcessingService/ReprocessItemImage":
		return "items:write"
	case "/scribe.v1.ContextService/ListContexts", "/scribe.v1.ContextService/GetContext", "/scribe.v1.ContextService/ListSelectionRules", "/scribe.v1.ContextService/ResolveContext":
		return "contexts:read"
	case "/scribe.v1.ContextService/CreateContext", "/scribe.v1.ContextService/UpdateContext", "/scribe.v1.ContextService/DeleteContext", "/scribe.v1.ContextService/CreateSelectionRule", "/scribe.v1.ContextService/DeleteSelectionRule":
		return "contexts:write"
	case "/scribe.v1.TranscriptionService/GetTranscriptionJob", "/scribe.v1.TranscriptionService/ListTranscriptionJobs", "/scribe.v1.TranscriptionService/StreamTranscriptionJob":
		return "transcription:read"
	case "/scribe.v1.TranscriptionService/CreateTranscriptionJob":
		return "transcription:write"
	case "/scribe.v1.AuthService/ListAPIKeys", "/scribe.v1.AuthService/CreateAPIKey", "/scribe.v1.AuthService/DeleteAPIKey":
		return "admin:api_keys"
	case "/scribe.v1.AuthService/ListProviderSecrets":
		return "contexts:read"
	case "/scribe.v1.AuthService/CreateProviderSecret", "/scribe.v1.AuthService/DeleteProviderSecret":
		return "contexts:write"
	case "/scribe.v1.AnnotationService/SearchAnnotations", "/scribe.v1.AnnotationService/GetAnnotation", "/scribe.v1.AnnotationService/CrosswalkToPlainText", "/scribe.v1.AnnotationService/CrosswalkToHOCR", "/scribe.v1.AnnotationService/CrosswalkToPageXML", "/scribe.v1.AnnotationService/CrosswalkToALTOXML":
		return "annotations:read"
	case "/scribe.v1.AnnotationService/CreateAnnotation", "/scribe.v1.AnnotationService/UpdateAnnotation", "/scribe.v1.AnnotationService/DeleteAnnotation", "/scribe.v1.AnnotationService/PublishItemImageEdits", "/scribe.v1.AnnotationService/EnrichAnnotation", "/scribe.v1.AnnotationService/SplitLineIntoWords", "/scribe.v1.AnnotationService/SplitLineIntoTwoLines", "/scribe.v1.AnnotationService/JoinLines", "/scribe.v1.AnnotationService/JoinWordsIntoLine", "/scribe.v1.ImageProcessingService/SaveOCREdits":
		return "annotations:write"
	default:
		return "authz:unmapped"
	}
}

func requiredPermissionForPath(path, method string) string {
	if strings.HasPrefix(path, "/scribe.v1.") {
		return requiredPermissionForProcedure(path, optionsv1.AccessLevel_ACCESS_LEVEL_READ)
	}
	switch {
	case path == "/v1/events":
		return "transcription:read"
	case strings.HasSuffix(path, "/provider-call-audits"):
		return "items:read"
	case strings.HasSuffix(path, "/manifest"), strings.HasSuffix(path, "/hocr"), strings.HasSuffix(path, "/export"):
		return "items:read"
	case strings.HasSuffix(path, "/annotations"):
		return "annotations:read"
	case strings.HasPrefix(path, "/v1/contexts/") && strings.HasSuffix(path, "/metrics"):
		return "contexts:read"
	default:
		return ""
	}
}

func secretKeyHint(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if len(raw) <= 4 {
		return raw
	}
	return raw[len(raw)-4:]
}

func principalHasPermission(principal Principal, permission string) bool {
	if !principal.Authenticated {
		return false
	}
	if principal.IsAdmin {
		return true
	}
	if !workspaceRoleAllowsPermission(principal.WorkspaceRole, permission) {
		return false
	}
	if principal.AuthType != "api_key" {
		return true
	}
	return scopeListAllows(principal.Scopes, permission)
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
	http.SetCookie(w, &http.Cookie{
		Name:     m.auth.CookieName,
		Value:    token,
		Path:     "/",
		Domain:   m.auth.CookieDomain,
		MaxAge:   maxAgeSeconds(m.auth.SessionTTL),
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

func requestIP(r *http.Request) string {
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); forwarded != "" {
		parts := strings.Split(forwarded, ",")
		if len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}
	if realIP := strings.TrimSpace(r.Header.Get("X-Real-Ip")); realIP != "" {
		return realIP
	}
	return r.RemoteAddr
}

func safeRedirectPath(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
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
