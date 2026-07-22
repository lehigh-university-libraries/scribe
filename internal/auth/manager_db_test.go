package auth

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	_ "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/database"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
	optionsv1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1/options/v1"
)

func openAuthTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DSN")
	if dsn == "" {
		t.Skip("TEST_DSN not set; skipping auth persistence integration test")
	}
	databasePool, err := database.NewPool(dsn, database.DefaultConfig())
	if err != nil {
		t.Fatalf("open auth test database: %v", err)
	}
	if err := database.Migrate(databasePool); err != nil {
		_ = databasePool.Close()
		t.Fatalf("migrate auth test database: %v", err)
	}
	t.Cleanup(func() { _ = databasePool.Close() })
	return databasePool
}

func TestExactBatchAndSelectionRuleAuthorizationIsWorkspaceScoped(t *testing.T) {
	databasePool := openAuthTestDB(t)
	ctx := context.Background()
	identities := store.NewIdentityStore(databasePool)
	itemStore := store.NewItemStore(databasePool)
	contextStore := store.NewContextStore(databasePool)

	userA, workspaceA := ensureAuthTestUser(t, identities, uuid.NewString())
	userB, workspaceB := ensureAuthTestUser(t, identities, uuid.NewString())
	createContext := func(user store.User, workspace store.Workspace) store.Context {
		t.Helper()
		processingContext, err := contextStore.Create(ctx, store.Context{
			UserID:                &user.ID,
			WorkspaceID:           &workspace.ID,
			Name:                  "auth-resource-" + uuid.NewString(),
			SegmentationModel:     "tesseract",
			TranscriptionProvider: "tesseract",
			TranscriptionModel:    "eng",
		})
		if err != nil {
			t.Fatalf("create context: %v", err)
		}
		return processingContext
	}
	contextA := createContext(userA, workspaceA)
	contextB := createContext(userB, workspaceB)

	ruleA, err := contextStore.CreateRuleForWorkspace(ctx, workspaceA.ID, store.ContextSelectionRule{ContextID: contextA.ID})
	if err != nil {
		t.Fatalf("create workspace A rule: %v", err)
	}
	ruleB, err := contextStore.CreateRuleForWorkspace(ctx, workspaceB.ID, store.ContextSelectionRule{ContextID: contextB.ID})
	if err != nil {
		t.Fatalf("create workspace B rule: %v", err)
	}
	createBatch := func(user store.User, workspace store.Workspace, processingContext store.Context) store.UploadBatch {
		t.Helper()
		batch, err := itemStore.StartUploadBatch(ctx, store.StartUploadBatchParams{
			WorkspaceID: workspace.ID,
			UserID:      user.ID,
			BatchID:     "auth-" + uuid.NewString(),
			ItemID:      "item_" + uuid.NewString(),
			Name:        "authorization batch",
			Context:     processingContext,
			RequestHash: strings.Repeat("a", 64),
			Files: []store.UploadBatchFileInput{{
				Filename:      "page.png",
				Size:          1,
				ContentSHA256: strings.Repeat("b", 64),
			}},
		})
		if err != nil {
			t.Fatalf("create upload batch: %v", err)
		}
		return batch
	}
	batchA := createBatch(userA, workspaceA, contextA)
	batchB := createBatch(userB, workspaceB, contextB)

	manager := &Manager{items: itemStore, contexts: contextStore}
	principal := Principal{Authenticated: true, AuthType: "session", WorkspaceID: workspaceA.ID, WorkspaceRole: "admin"}
	for _, test := range []struct {
		name       string
		procedure  string
		resource   optionsv1.ResourceType
		level      optionsv1.AccessLevel
		resourceID string
		want       bool
	}{
		{name: "own batch", procedure: "/scribe.v1.ItemService/GetUploadBatch", resource: optionsv1.ResourceType_RESOURCE_TYPE_UPLOAD_BATCH, level: optionsv1.AccessLevel_ACCESS_LEVEL_READ, resourceID: batchA.ID, want: true},
		{name: "foreign batch", procedure: "/scribe.v1.ItemService/GetUploadBatch", resource: optionsv1.ResourceType_RESOURCE_TYPE_UPLOAD_BATCH, level: optionsv1.AccessLevel_ACCESS_LEVEL_READ, resourceID: batchB.ID, want: false},
		{name: "own rule", procedure: "/scribe.v1.ContextService/DeleteSelectionRule", resource: optionsv1.ResourceType_RESOURCE_TYPE_SELECTION_RULE, level: optionsv1.AccessLevel_ACCESS_LEVEL_WRITE, resourceID: fmt.Sprint(ruleA.ID), want: true},
		{name: "foreign rule", procedure: "/scribe.v1.ContextService/DeleteSelectionRule", resource: optionsv1.ResourceType_RESOURCE_TYPE_SELECTION_RULE, level: optionsv1.AccessLevel_ACCESS_LEVEL_WRITE, resourceID: fmt.Sprint(ruleB.ID), want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			allowed, err := manager.authorizeResource(ctx, principal, test.procedure, test.resource, test.level, test.resourceID)
			if err != nil || allowed != test.want {
				t.Fatalf("authorizeResource = %t/%v, want %t/nil", allowed, err, test.want)
			}
		})
	}
}

func TestExternalServiceIdentityRequiresCurrentWorkspaceMembership(t *testing.T) {
	databasePool := openAuthTestDB(t)
	ctx := context.Background()
	identities := store.NewIdentityStore(databasePool)
	userA, workspaceA := ensureAuthTestUser(t, identities, uuid.NewString())
	userB, _ := ensureAuthTestUser(t, identities, uuid.NewString())
	manager := &Manager{identities: identities}

	user, access, err := manager.resolveExternalServiceIdentity(ctx, userA.ID, workspaceA.ID)
	if err != nil || user.ID != userA.ID || access.Workspace.ID != workspaceA.ID {
		t.Fatalf("owned service identity = %+v/%+v/%v", user, access, err)
	}
	if _, _, err := manager.resolveExternalServiceIdentity(ctx, userB.ID, workspaceA.ID); err == nil {
		t.Fatal("external service identity accepted a user outside the configured workspace")
	}
}

func ensureAuthTestUser(t *testing.T, identities *store.IdentityStore, suffix string) (store.User, store.Workspace) {
	t.Helper()
	user, workspace, err := identities.EnsureGoogleUser(context.Background(), store.GoogleProfile{
		Subject: "auth-test-" + suffix, Email: "auth-test-" + suffix + "@example.org", Name: "Auth Test " + suffix,
	})
	if err != nil {
		t.Fatalf("EnsureGoogleUser: %v", err)
	}
	return user, workspace
}

func TestAuthenticationMiddlewareDoesNotMutateSessionPersistence(t *testing.T) {
	databasePool := openAuthTestDB(t)
	ctx := context.Background()
	identities := store.NewIdentityStore(databasePool)
	suffix := uuid.NewString()
	user, _ := ensureAuthTestUser(t, identities, suffix)
	token := "session-" + suffix
	if err := identities.CreateSession(ctx, user.ID, token, "test-agent", "192.0.2.1", time.Hour); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	var expiresBefore, createdBefore time.Time
	var agentBefore, ipBefore sql.NullString
	if err := databasePool.QueryRowContext(ctx, `SELECT expires_at, user_agent, ip_address, created_at FROM auth_sessions WHERE user_id = ?`, user.ID).Scan(&expiresBefore, &agentBefore, &ipBefore, &createdBefore); err != nil {
		t.Fatalf("load session before authentication: %v", err)
	}
	var workspaceCountBefore, memberCountBefore int
	if err := databasePool.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspaces WHERE owner_user_id = ?`, user.ID).Scan(&workspaceCountBefore); err != nil {
		t.Fatal(err)
	}
	if err := databasePool.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspace_members WHERE user_id = ?`, user.ID).Scan(&memberCountBefore); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{auth: config.AuthConfig{CookieName: "scribe_session"}, identities: identities}
	handler := manager.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	request := httptest.NewRequest(http.MethodGet, "https://scribe.example/readyz", nil)
	request.AddCookie(&http.Cookie{Name: "scribe_session", Value: token})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("authenticated middleware status = %d", response.Code)
	}
	var expiresAfter, createdAfter time.Time
	var agentAfter, ipAfter sql.NullString
	if err := databasePool.QueryRowContext(ctx, `SELECT expires_at, user_agent, ip_address, created_at FROM auth_sessions WHERE user_id = ?`, user.ID).Scan(&expiresAfter, &agentAfter, &ipAfter, &createdAfter); err != nil {
		t.Fatalf("load session after authentication: %v", err)
	}
	var workspaceCountAfter, memberCountAfter int
	_ = databasePool.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspaces WHERE owner_user_id = ?`, user.ID).Scan(&workspaceCountAfter)
	_ = databasePool.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspace_members WHERE user_id = ?`, user.ID).Scan(&memberCountAfter)
	if !expiresAfter.Equal(expiresBefore) || !createdAfter.Equal(createdBefore) || agentAfter != agentBefore || ipAfter != ipBefore || workspaceCountAfter != workspaceCountBefore || memberCountAfter != memberCountBefore {
		t.Fatalf("authentication mutated persistence: workspaces=%d/%d members=%d/%d", workspaceCountBefore, workspaceCountAfter, memberCountBefore, memberCountAfter)
	}
}

func TestRejectedSessionsDoNotRepairOrDeletePersistence(t *testing.T) {
	databasePool := openAuthTestDB(t)
	ctx := context.Background()
	identities := store.NewIdentityStore(databasePool)
	manager := &Manager{auth: config.AuthConfig{CookieName: "scribe_session"}, identities: identities}
	handler := manager.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))

	requestSession := func(token string) int {
		request := httptest.NewRequest(http.MethodGet, "https://scribe.example/scribe.v1.ItemService/ListItems", nil)
		request.AddCookie(&http.Cookie{Name: "scribe_session", Value: token})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response.Code
	}

	missingSuffix := uuid.NewString()
	result, err := databasePool.ExecContext(ctx, `INSERT INTO users (name, email) VALUES (?, ?)`, "Missing Workspace", "missing-"+missingSuffix+"@example.org")
	if err != nil {
		t.Fatal(err)
	}
	missingUserID, _ := result.LastInsertId()
	missingToken := "missing-" + missingSuffix
	if err := identities.CreateSession(ctx, uint64(missingUserID), missingToken, "", "", time.Hour); err != nil {
		t.Fatal(err)
	}
	if status := requestSession(missingToken); status != http.StatusUnauthorized {
		t.Fatalf("missing-workspace session status = %d, want 401", status)
	}
	var workspaceCount, missingSessionCount int
	_ = databasePool.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspaces WHERE owner_user_id = ?`, missingUserID).Scan(&workspaceCount)
	_ = databasePool.QueryRowContext(ctx, `SELECT COUNT(*) FROM auth_sessions WHERE user_id = ?`, missingUserID).Scan(&missingSessionCount)
	if workspaceCount != 0 || missingSessionCount != 1 {
		t.Fatalf("missing-workspace authentication repaired/deleted state: workspaces=%d sessions=%d", workspaceCount, missingSessionCount)
	}

	expiredUser, _ := ensureAuthTestUser(t, identities, uuid.NewString())
	expiredToken := "expired-" + uuid.NewString()
	if err := identities.CreateSession(ctx, expiredUser.ID, expiredToken, "", "", time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := databasePool.ExecContext(ctx, `UPDATE auth_sessions SET expires_at = DATE_SUB(NOW(), INTERVAL 1 HOUR) WHERE user_id = ?`, expiredUser.ID); err != nil {
		t.Fatal(err)
	}
	if status := requestSession(expiredToken); status != http.StatusUnauthorized {
		t.Fatalf("expired session status = %d, want 401", status)
	}
	var expiredSessionCount int
	_ = databasePool.QueryRowContext(ctx, `SELECT COUNT(*) FROM auth_sessions WHERE user_id = ?`, expiredUser.ID).Scan(&expiredSessionCount)
	if expiredSessionCount != 1 {
		t.Fatalf("expired authentication deleted session rows = %d, want 1", expiredSessionCount)
	}
}

func TestAPIKeyAuthenticationIsReadOnlyAndTracksCurrentMembership(t *testing.T) {
	databasePool := openAuthTestDB(t)
	ctx := context.Background()
	identities := store.NewIdentityStore(databasePool)
	apiKeys := store.NewAPIKeyStore(databasePool)
	creator, _ := ensureAuthTestUser(t, identities, uuid.NewString())
	backupAdmin, _ := ensureAuthTestUser(t, identities, uuid.NewString())
	workspaceAccess, err := identities.CreateWorkspaceForUser(ctx, creator.ID, "Delegated Workspace")
	if err != nil {
		t.Fatalf("CreateWorkspaceForUser: %v", err)
	}
	if _, err := identities.AddWorkspaceMemberByEmail(ctx, workspaceAccess.Workspace.ID, backupAdmin.Email, "admin"); err != nil {
		t.Fatalf("AddWorkspaceMemberByEmail: %v", err)
	}
	_, rawKey, err := apiKeys.Create(ctx, workspaceAccess.Workspace.ID, creator.ID, "delegated", "admin", []string{"*"}, nil)
	if err != nil {
		t.Fatalf("Create API key: %v", err)
	}
	manager := &Manager{identities: identities, apiKeys: apiKeys}
	principal, err := manager.apiKeyPrincipal(ctx, rawKey)
	if err != nil || principal.WorkspaceRole != "admin" {
		t.Fatalf("initial API key principal = %+v/%v", principal, err)
	}
	if _, err := identities.UpdateWorkspaceMemberRole(ctx, workspaceAccess.Workspace.ID, creator.ID, "read"); err != nil {
		t.Fatalf("demote API key creator: %v", err)
	}
	principal, err = manager.apiKeyPrincipal(ctx, rawKey)
	if err != nil || principal.WorkspaceRole != "read" || principalHasPermission(principal, "items:write") {
		t.Fatalf("demoted API key principal = %+v/%v", principal, err)
	}
	if err := identities.RemoveWorkspaceMember(ctx, workspaceAccess.Workspace.ID, creator.ID); err != nil {
		t.Fatalf("remove API key creator: %v", err)
	}
	if _, err := manager.apiKeyPrincipal(ctx, rawKey); err == nil {
		t.Fatal("removed workspace member's API key remained valid")
	}
}

func TestItemsOnlyAPIKeyCannotReadPreparedCanonicalExports(t *testing.T) {
	databasePool := openAuthTestDB(t)
	ctx := context.Background()
	identities := store.NewIdentityStore(databasePool)
	apiKeys := store.NewAPIKeyStore(databasePool)
	creator, workspace := ensureAuthTestUser(t, identities, uuid.NewString())
	_, rawKey, err := apiKeys.Create(ctx, workspace.ID, creator.ID, "items-only", "read", []string{"items:read"}, nil)
	if err != nil {
		t.Fatalf("create items-only API key: %v", err)
	}
	manager := &Manager{identities: identities, apiKeys: apiKeys}
	reached := false
	handler := manager.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, path := range []string{
		"/v1/item-exports/signed-token",
		"/v1/item-images/42/annotations/revisions/7/hocr",
	} {
		reached = false
		request := httptest.NewRequest(http.MethodGet, "https://scribe.example"+path, nil)
		request.Header.Set("X-Scribe-API-Key", rawKey)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden || reached {
			t.Fatalf("items-only canonical export %s = status %d/reached %t, want 403/false", path, response.Code, reached)
		}
	}

	principal, err := manager.apiKeyPrincipal(ctx, rawKey)
	if err != nil {
		t.Fatalf("authenticate items-only API key: %v", err)
	}
	err = manager.authorizeConnectRequest(
		ctx,
		principal,
		"/scribe.v1.ItemService/PrepareItemExport",
		&scribev1.PrepareItemExportRequest{ItemId: "owned-item"},
	)
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("items-only PrepareItemExport authorization = %v, want permission denied", err)
	}
}
