package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	optionsv1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1/options"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

type Manager struct {
	auth         config.AuthConfig
	cookieSecure bool
	identities   *store.IdentityStore
	apiKeys      *store.APIKeyStore
	providerSecrets *store.ProviderSecretStore
	items        *store.ItemStore
	contexts     *store.ContextStore
	jobs         *store.TranscriptionJobStore
	google       *GoogleOAuthManager
	vault        vaultClient
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
		auth:            cfg.Auth,
		cookieSecure:    cfg.CookieSecureResolved(),
		identities:      identities,
		apiKeys:         apiKeys,
		providerSecrets: providerSecrets,
		items:           items,
		contexts:        contexts,
		jobs:            jobs,
		vault:           vault,
	}
	callbackURL := cfg.GoogleCallbackURL()
	clientID := strings.TrimSpace(secrets.GoogleOAuthClientID)
	clientSecret := strings.TrimSpace(secrets.GoogleOAuthClientSecret)
	if clientID == "" || clientSecret == "" || callbackURL == "" {
		return nil, fmt.Errorf("google oauth requires public_base_url plus client id and client secret in Vault")
	}
	googleManager, err := NewGoogleOAuthManager(secrets.GoogleOAuthClientID, secrets.GoogleOAuthClientSecret, callbackURL)
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
	mux.HandleFunc("GET /auth/me", m.handleMe)
	mux.HandleFunc("GET /auth/api-keys", m.handleListAPIKeys)
	mux.HandleFunc("POST /auth/api-keys", m.handleCreateAPIKey)
	mux.HandleFunc("DELETE /auth/api-keys/{key_id}", m.handleDeleteAPIKey)
	mux.HandleFunc("GET /auth/provider-secrets", m.handleListProviderSecrets)
	mux.HandleFunc("POST /auth/provider-secrets", m.handleCreateProviderSecret)
	mux.HandleFunc("DELETE /auth/provider-secrets/{secret_id}", m.handleDeleteProviderSecret)
	mux.HandleFunc("GET /logout", m.handleLogout)
	mux.HandleFunc("POST /logout", m.handleLogout)
	if m.google != nil {
		mux.HandleFunc("GET /auth/google", m.handleGoogleLogin)
		mux.HandleFunc("GET /auth/callback/google", m.handleGoogleCallback)
	}
}

func (m *Manager) Interceptor() connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			principal, ok := PrincipalFromContext(ctx)
			if !ok {
				principal = m.anonymousPrincipal()
			}
			if principal.Anonymous() {
				return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required"))
			}

			rule, err := extractAuthzRule(req.Spec().Procedure)
			if err != nil {
				return nil, connect.NewError(connect.CodePermissionDenied, err)
			}
			if rule == nil {
				return next(ctx, req)
			}

			resourceIDField := strings.TrimSpace(rule.GetResourceIdField())
			if resourceIDField == "" {
				allowed, authErr := m.authorizeProcedure(ctx, principal, req.Spec().Procedure, rule.GetLevel())
				if authErr != nil {
					return nil, connect.NewError(connect.CodeInternal, authErr)
				}
				if !allowed {
					return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("permission denied"))
				}
				return next(ctx, req)
			}

			resourceID, ok := extractFieldValue(req.Any(), resourceIDField)
			if !ok || strings.TrimSpace(resourceID) == "" {
				return next(ctx, req)
			}

			allowed, authErr := m.authorizeResource(ctx, principal, req.Spec().Procedure, rule.GetResource(), rule.GetLevel(), resourceID)
			if authErr != nil {
				return nil, connect.NewError(connect.CodeInternal, authErr)
			}
			if !allowed {
				return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("permission denied"))
			}
			return next(ctx, req)
		}
	})
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
	authURL, err := m.google.BeginAuth(redirectPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("start google oauth: %v", err), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, authURL, http.StatusFound)
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
	profile, stateValue, err := m.google.CompleteAuth(r.Context(), code, state)
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
	m.setSessionCookie(w, token)
	http.Redirect(w, r, safeRedirectPath(stateValue.RedirectPath), http.StatusFound)
}

func (m *Manager) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(m.auth.CookieName); err == nil && strings.TrimSpace(cookie.Value) != "" {
		_ = m.identities.DeleteSession(r.Context(), cookie.Value)
	}
	m.clearSessionCookie(w)
	if strings.Contains(strings.ToLower(r.Header.Get("Accept")), "application/json") {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

func (m *Manager) handleMe(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		principal = m.anonymousPrincipal()
	}
	response := map[string]any{
		"authenticated": principal.Authenticated,
		"authType":      principal.AuthType,
		"loginUrl":      "/auth/google",
		"logoutUrl":     "/logout",
	}
	if principal.Authenticated {
		response["user"] = map[string]any{
			"id":                 principal.UserID,
			"email":              principal.Email,
			"name":               principal.Name,
			"pictureUrl":         principal.PictureURL,
			"isAdmin":            principal.IsAdmin,
			"defaultWorkspaceId": principal.DefaultWorkspaceID,
		}
		response["workspace"] = map[string]any{
			"id":   principal.WorkspaceID,
			"name": principal.WorkspaceName,
			"role": principal.WorkspaceRole,
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func (m *Manager) handleListAPIKeys(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok || !principal.Authenticated || principal.AuthType != "session" {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if !principalHasPermission(principal, "admin:api_keys") {
		http.Error(w, "permission denied", http.StatusForbidden)
		return
	}
	keys, err := m.apiKeys.ListByWorkspace(r.Context(), principal.WorkspaceID)
	if err != nil {
		http.Error(w, fmt.Sprintf("list api keys: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"apiKeys": keys})
}

func (m *Manager) handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok || !principal.Authenticated || principal.AuthType != "session" {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if !principalHasPermission(principal, "admin:api_keys") {
		http.Error(w, "permission denied", http.StatusForbidden)
		return
	}
	var req struct {
		Name      string   `json:"name"`
		Role      string   `json:"role"`
		Scopes    []string `json:"scopes"`
		ExpiresAt string   `json:"expiresAt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	var expiresAt *time.Time
	if raw := strings.TrimSpace(req.ExpiresAt); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			http.Error(w, "invalid expiresAt", http.StatusBadRequest)
			return
		}
		expiresAt = &parsed
	}
	apiKey, rawKey, err := m.apiKeys.Create(r.Context(), principal.WorkspaceID, principal.UserID, req.Name, req.Role, req.Scopes, expiresAt)
	if err != nil {
		http.Error(w, fmt.Sprintf("create api key: %v", err), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"apiKey": apiKey,
		"key":    rawKey,
	})
}

func (m *Manager) handleDeleteAPIKey(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok || !principal.Authenticated || principal.AuthType != "session" {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if !principalHasPermission(principal, "admin:api_keys") {
		http.Error(w, "permission denied", http.StatusForbidden)
		return
	}
	keyID, err := strconv.ParseUint(strings.TrimSpace(r.PathValue("key_id")), 10, 64)
	if err != nil || keyID == 0 {
		http.Error(w, "invalid key_id", http.StatusBadRequest)
		return
	}
	if err := m.apiKeys.DeleteForWorkspace(r.Context(), keyID, principal.WorkspaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "api key not found", http.StatusNotFound)
			return
		}
		http.Error(w, fmt.Sprintf("delete api key: %v", err), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *Manager) handleListProviderSecrets(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok || !principal.Authenticated || principal.AuthType != "session" {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if !principalHasPermission(principal, "contexts:read") {
		http.Error(w, "permission denied", http.StatusForbidden)
		return
	}
	if m.providerSecrets == nil {
		http.Error(w, "provider secret storage is not configured", http.StatusServiceUnavailable)
		return
	}
	secrets, err := m.providerSecrets.ListVisible(r.Context(), principal.WorkspaceID, principal.UserID)
	if err != nil {
		http.Error(w, fmt.Sprintf("list provider secrets: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"providerSecrets": secrets})
}

func (m *Manager) handleCreateProviderSecret(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok || !principal.Authenticated || principal.AuthType != "session" {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if !principalHasPermission(principal, "contexts:write") {
		http.Error(w, "permission denied", http.StatusForbidden)
		return
	}
	if m.providerSecrets == nil || m.vault == nil {
		http.Error(w, "provider secret storage is not configured", http.StatusServiceUnavailable)
		return
	}

	var req struct {
		Provider string `json:"provider"`
		Name     string `json:"name"`
		APIKey   string `json:"apiKey"`
		Scope    string `json:"scope"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	provider := strings.ToLower(strings.TrimSpace(req.Provider))
	if provider != "gemini" {
		http.Error(w, "unsupported provider secret provider", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	apiKey := strings.TrimSpace(req.APIKey)
	if apiKey == "" {
		http.Error(w, "apiKey is required", http.StatusBadRequest)
		return
	}

	scope := strings.ToLower(strings.TrimSpace(req.Scope))
	if scope == "" {
		scope = "user"
	}
	if scope != "user" && scope != "workspace" {
		http.Error(w, "scope must be 'user' or 'workspace'", http.StatusBadRequest)
		return
	}
	if scope == "workspace" && !strings.EqualFold(principal.WorkspaceRole, "admin") && !principal.IsAdmin {
		http.Error(w, "workspace secrets require admin role", http.StatusForbidden)
		return
	}

	pathSuffix, err := randomString(12)
	if err != nil {
		http.Error(w, fmt.Sprintf("generate secret path: %v", err), http.StatusInternalServerError)
		return
	}
	vaultPath := fmt.Sprintf(
		"scribe/provider-secrets/workspaces/%d/%s/%s-%s",
		principal.WorkspaceID,
		provider,
		store.Slugify(name),
		strings.ToLower(pathSuffix),
	)
	if err := m.vault.Write(r.Context(), vaultPath, map[string]string{
		"api_key":  apiKey,
		"provider": provider,
		"name":     name,
	}); err != nil {
		http.Error(w, fmt.Sprintf("write provider secret: %v", err), http.StatusInternalServerError)
		return
	}

	var userID *uint64
	workspaceID := principal.WorkspaceID
	if scope == "user" {
		userID = &principal.UserID
	}
	secret, err := m.providerSecrets.Create(r.Context(), store.ProviderSecret{
		UserID:      userID,
		WorkspaceID: &workspaceID,
		Provider:    provider,
		Name:        name,
		VaultPath:   vaultPath,
		KeyHint:     secretKeyHint(apiKey),
		Scope:       scope,
	})
	if err != nil {
		_ = m.vault.Delete(r.Context(), vaultPath)
		http.Error(w, fmt.Sprintf("create provider secret: %v", err), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"providerSecret": secret,
	})
}

func (m *Manager) handleDeleteProviderSecret(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok || !principal.Authenticated || principal.AuthType != "session" {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if !principalHasPermission(principal, "contexts:write") {
		http.Error(w, "permission denied", http.StatusForbidden)
		return
	}
	if m.providerSecrets == nil || m.vault == nil {
		http.Error(w, "provider secret storage is not configured", http.StatusServiceUnavailable)
		return
	}

	secretID, err := strconv.ParseUint(strings.TrimSpace(r.PathValue("secret_id")), 10, 64)
	if err != nil || secretID == 0 {
		http.Error(w, "invalid secret_id", http.StatusBadRequest)
		return
	}

	secret, err := m.providerSecrets.GetVisible(r.Context(), secretID, principal.WorkspaceID, principal.UserID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "provider secret not found", http.StatusNotFound)
			return
		}
		http.Error(w, fmt.Sprintf("load provider secret: %v", err), http.StatusInternalServerError)
		return
	}
	if secret.Scope == "workspace" && !strings.EqualFold(principal.WorkspaceRole, "admin") && !principal.IsAdmin {
		http.Error(w, "workspace secrets require admin role", http.StatusForbidden)
		return
	}

	if secret.Scope == "workspace" {
		err = m.providerSecrets.DeleteWorkspaceSecret(r.Context(), secretID, principal.WorkspaceID)
	} else {
		err = m.providerSecrets.DeleteUserSecret(r.Context(), secretID, principal.WorkspaceID, principal.UserID)
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "provider secret not found", http.StatusNotFound)
			return
		}
		http.Error(w, fmt.Sprintf("delete provider secret: %v", err), http.StatusInternalServerError)
		return
	}
	_ = m.vault.Delete(r.Context(), secret.VaultPath)
	w.WriteHeader(http.StatusNoContent)
}

func (m *Manager) authenticateRequest(r *http.Request) (Principal, error) {
	if rawKey := extractAPIKeyFromRequest(r); rawKey != "" {
		return m.apiKeyPrincipal(r.Context(), rawKey)
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
	authz := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(authz), "bearer ") {
		return strings.TrimSpace(authz[7:])
	}
	return ""
}

func (m *Manager) requiresAuthenticatedAPI(r *http.Request) bool {
	path := strings.TrimSpace(r.URL.Path)
	return strings.HasPrefix(path, "/v1/") ||
		strings.HasPrefix(path, "/scribe.v1.") ||
		strings.HasPrefix(path, "/auth/api-keys") ||
		strings.HasPrefix(path, "/auth/provider-secrets")
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
	default:
		return true, nil
	}
}

func requiredPermissionForProcedure(procedure string, level optionsv1.AccessLevel) string {
	switch procedure {
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
	case "/scribe.v1.AnnotationService/SearchAnnotations", "/scribe.v1.AnnotationService/GetAnnotation", "/scribe.v1.AnnotationService/CrosswalkToPlainText", "/scribe.v1.AnnotationService/CrosswalkToHOCR", "/scribe.v1.AnnotationService/CrosswalkToPageXML", "/scribe.v1.AnnotationService/CrosswalkToALTOXML":
		return "annotations:read"
	case "/scribe.v1.AnnotationService/CreateAnnotation", "/scribe.v1.AnnotationService/UpdateAnnotation", "/scribe.v1.AnnotationService/DeleteAnnotation", "/scribe.v1.AnnotationService/EnrichAnnotation", "/scribe.v1.AnnotationService/SplitLineIntoWords", "/scribe.v1.AnnotationService/SplitLineIntoTwoLines", "/scribe.v1.AnnotationService/JoinLines", "/scribe.v1.AnnotationService/JoinWordsIntoLine", "/scribe.v1.ImageProcessingService/SaveOCREdits":
		return "annotations:write"
	case "/scribe.v1.AnnotationService/PublishItemImageEdits":
		return "annotations:read"
	default:
		if level >= optionsv1.AccessLevel_ACCESS_LEVEL_WRITE {
			return "items:write"
		}
		return "items:read"
	}
}

func requiredPermissionForPath(path, method string) string {
	if strings.HasPrefix(path, "/scribe.v1.") {
		return requiredPermissionForProcedure(path, optionsv1.AccessLevel_ACCESS_LEVEL_READ)
	}
	if strings.HasPrefix(path, "/auth/api-keys") {
		return "admin:api_keys"
	}
	if strings.HasPrefix(path, "/auth/provider-secrets") {
		if strings.EqualFold(method, http.MethodGet) {
			return "contexts:read"
		}
		return "contexts:write"
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
		return true
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

func (m *Manager) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     m.auth.CookieName,
		Value:    token,
		Path:     "/",
		Domain:   m.auth.CookieDomain,
		MaxAge:   maxAgeSeconds(m.auth.SessionTTL),
		HttpOnly: true,
		Secure:   m.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (m *Manager) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     m.auth.CookieName,
		Value:    "",
		Path:     "/",
		Domain:   m.auth.CookieDomain,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   m.cookieSecure,
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
	body, err := protojson.Marshal(protoMessage)
	if err != nil {
		return "", false
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", false
	}
	var current any = payload
	for _, rawSegment := range strings.Split(fieldPath, ".") {
		segment := toCamelCase(strings.TrimSpace(rawSegment))
		obj, ok := current.(map[string]any)
		if !ok {
			return "", false
		}
		next, ok := obj[segment]
		if !ok {
			return "", false
		}
		current = next
	}
	switch value := current.(type) {
	case string:
		return value, true
	case float64:
		return strconv.FormatUint(uint64(value), 10), true
	case json.Number:
		return value.String(), true
	default:
		return fmt.Sprintf("%v", value), true
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
