package auth

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestEditorReviewTokenIsOneTimeAndSessionIsImageScoped(t *testing.T) {
	databasePool := openAuthTestDB(t)
	ctx := context.Background()
	identities := store.NewIdentityStore(databasePool)
	issuer, workspace := ensureAuthTestUser(t, identities, uuid.NewString())
	itemID := "review-" + uuid.NewString()
	if _, err := databasePool.ExecContext(ctx, `
INSERT INTO items (id, user_id, workspace_id, name, source_type, metadata)
VALUES (?, ?, ?, 'Review token item', 'url', JSON_OBJECT())`, itemID, issuer.ID, workspace.ID); err != nil {
		t.Fatalf("create review item: %v", err)
	}
	createImage := func(canvas string) uint64 {
		t.Helper()
		result, err := databasePool.ExecContext(ctx, `
INSERT INTO item_images (workspace_id, item_id, sequence, image_url, canvas_uri, width, height)
VALUES (?, ?, (SELECT COUNT(*) FROM item_images existing WHERE existing.item_id = ?), ?, ?, 100, 100)`,
			workspace.ID, itemID, itemID, "https://images.example/"+uuid.NewString()+".jpg", canvas)
		if err != nil {
			t.Fatalf("create review item image: %v", err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		return uint64(id) // #nosec G115 -- positive test fixture identifier.
	}
	imageID := createImage("https://iiif.example/canvas/" + uuid.NewString())
	otherImageID := createImage("https://iiif.example/canvas/" + uuid.NewString())
	t.Cleanup(func() {
		_, _ = databasePool.Exec(`DELETE FROM editor_review_sessions WHERE workspace_id = ?`, workspace.ID)
		_, _ = databasePool.Exec(`DELETE FROM editor_review_tokens WHERE workspace_id = ?`, workspace.ID)
		_, _ = databasePool.Exec(`DELETE FROM item_images WHERE item_id = ?`, itemID)
		_, _ = databasePool.Exec(`DELETE FROM items WHERE id = ?`, itemID)
	})

	manager := &Manager{
		auth:          config.AuthConfig{CookieName: "scribe_session", SessionTTL: 24 * time.Hour},
		publicBaseURL: "https://scribe.example",
		identities:    identities,
		items:         store.NewItemStore(databasePool),
		reviewTokens:  newEditorReviewTokenSigner(strings.Repeat("r", 32)),
	}
	principal := Principal{
		UserID: issuer.ID, Authenticated: true, AuthType: "external_jwt",
		WorkspaceID: workspace.ID, WorkspaceRole: "admin",
		ExternalIssuer: "https://islandora.example", ExternalSubject: "service",
	}
	created, err := manager.CreateEditorReviewToken(WithPrincipal(ctx, principal), connect.NewRequest(&scribev1.CreateEditorReviewTokenRequest{
		ItemImageId: imageID, ReviewerSubject: "islandora-user-7", ReviewerName: "Review User",
		ReviewerEmail: "reviewer@example.org", TokenTtlSeconds: 300, SessionTtlSeconds: 3600,
	}))
	if err != nil {
		t.Fatalf("CreateEditorReviewToken: %v", err)
	}
	reviewURL, err := url.Parse(created.Msg.GetReviewUrl())
	if err != nil || reviewURL.Scheme != "https" || reviewURL.Host != "scribe.example" || reviewURL.Path != "/auth/review" || reviewURL.Query().Get("token") == "" {
		t.Fatalf("review URL = %q/%v", created.Msg.GetReviewUrl(), err)
	}
	if strings.Contains(created.Msg.GetReviewUrl(), "islandora-user-7") {
		t.Fatal("raw reviewer subject leaked into review URL")
	}

	redeemRequest := httptest.NewRequest(http.MethodGet, created.Msg.GetReviewUrl(), nil)
	redeemResponse := httptest.NewRecorder()
	manager.handleEditorReviewRedeem(redeemResponse, redeemRequest)
	if redeemResponse.Code != http.StatusSeeOther {
		t.Fatalf("redeem status = %d body=%q", redeemResponse.Code, redeemResponse.Body.String())
	}
	location, err := url.Parse(redeemResponse.Header().Get("Location"))
	if err != nil || location.Path != "/editor" || location.Query().Get("itemImageId") != fmt.Sprint(imageID) || location.Query().Get("workspace_id") != fmt.Sprint(workspace.ID) || location.Query().Get("token") != "" {
		t.Fatalf("clean redirect = %q/%v", redeemResponse.Header().Get("Location"), err)
	}
	cookies := redeemResponse.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("review session cookie = %+v", cookies)
	}

	replayResponse := httptest.NewRecorder()
	manager.handleEditorReviewRedeem(replayResponse, httptest.NewRequest(http.MethodGet, created.Msg.GetReviewUrl(), nil))
	if replayResponse.Code != http.StatusUnauthorized {
		t.Fatalf("replayed review URL status = %d, want 401", replayResponse.Code)
	}

	authRequest := httptest.NewRequest(http.MethodGet, "https://scribe.example/readyz?workspace_id="+fmt.Sprint(workspace.ID), nil)
	authRequest.AddCookie(cookies[0])
	reviewPrincipal, err := manager.authenticateRequest(authRequest)
	if err != nil || reviewPrincipal.AuthType != "review_session" || reviewPrincipal.ScopedItemImageID != imageID || reviewPrincipal.ScopedItemID != itemID {
		t.Fatalf("review principal = %+v/%v", reviewPrincipal, err)
	}
	if err := manager.authorizeConnectRequest(ctx, reviewPrincipal, "/scribe.v1.AnnotationService/GetAnnotationPage", &scribev1.GetAnnotationPageRequest{ItemImageId: imageID}); err != nil {
		t.Fatalf("authorize scoped image: %v", err)
	}
	if err := manager.authorizeConnectRequest(ctx, reviewPrincipal, "/scribe.v1.AnnotationService/GetAnnotationPage", &scribev1.GetAnnotationPageRequest{ItemImageId: otherImageID}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("authorize other image = %v, want permission denied", err)
	}
	if err := manager.authorizeConnectRequest(ctx, reviewPrincipal, "/scribe.v1.TranscriptionService/ListTranscriptionJobs", &scribev1.ListTranscriptionJobsRequest{}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("workspace-wide job list = %v, want permission denied", err)
	}
	if err := manager.authorizeConnectRequest(ctx, reviewPrincipal, "/scribe.v1.TranscriptionService/ListTranscriptionJobs", &scribev1.ListTranscriptionJobsRequest{ItemImageId: imageID}); err != nil {
		t.Fatalf("scoped job list: %v", err)
	}
}
