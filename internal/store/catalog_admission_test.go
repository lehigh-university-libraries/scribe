package store_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

func TestConcurrentAPIKeyAdmissionEnforcesWorkspaceLimit(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	userID, workspaceID := createUploadBatchIdentity(t, database)
	for index := 0; index < store.MaxAPIKeysPerWorkspace-1; index++ {
		_, err := database.ExecContext(ctx, `
INSERT INTO api_keys (workspace_id, created_by_user_id, name, key_prefix, key_hash, role)
VALUES (?, ?, ?, ?, ?, 'read')`, workspaceID, userID, fmt.Sprintf("seed-%d", index), fmt.Sprintf("seed-%d", index), fmt.Sprintf("%064x", index+1))
		if err != nil {
			t.Fatalf("seed API key %d: %v", index, err)
		}
	}

	keys := store.NewAPIKeyStore(database)
	results := runConcurrentAdmissions(func(index int) error {
		_, _, err := keys.Create(ctx, workspaceID, userID, fmt.Sprintf("racing-key-%d", index), "read", nil, nil)
		return err
	})
	assertOneAdmissionAtLimit(t, results, store.ErrAPIKeyLimit)
	assertTableCount(t, database, "api_keys", "workspace_id", workspaceID, store.MaxAPIKeysPerWorkspace)
}

func TestConcurrentProviderSecretAdmissionEnforcesWorkspaceLimit(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	_, workspaceID := createUploadBatchIdentity(t, database)
	t.Cleanup(func() {
		_, _ = database.Exec(`DELETE FROM provider_secrets WHERE workspace_id = ?`, workspaceID)
	})
	for index := 0; index < store.MaxProviderSecretsPerWorkspace-1; index++ {
		_, err := database.ExecContext(ctx, `
INSERT INTO provider_secrets (workspace_id, provider, name, vault_path)
VALUES (?, 'openai', ?, ?)`, workspaceID, fmt.Sprintf("seed-%d", index), fmt.Sprintf("scribe/workspaces/%d/provider-seed-%d", workspaceID, index))
		if err != nil {
			t.Fatalf("seed provider secret %d: %v", index, err)
		}
	}

	secrets := store.NewProviderSecretStore(database)
	results := runConcurrentAdmissions(func(index int) error {
		_, err := secrets.Create(ctx, store.ProviderSecret{
			WorkspaceID: workspaceID,
			Provider:    "openai",
			Name:        fmt.Sprintf("racing-secret-%d", index),
			VaultPath:   fmt.Sprintf("scribe/workspaces/%d/provider-race-%d", workspaceID, index),
		})
		return err
	})
	assertOneAdmissionAtLimit(t, results, store.ErrProviderSecretLimit)
	assertTableCount(t, database, "provider_secrets", "workspace_id", workspaceID, store.MaxProviderSecretsPerWorkspace)
}

func TestConcurrentWorkspaceAdmissionEnforcesUserAccessLimit(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	userID, personalWorkspaceID := createUploadBatchIdentity(t, database)
	t.Cleanup(func() {
		_, _ = database.Exec(`DELETE wm FROM workspace_members wm JOIN workspaces w ON w.id = wm.workspace_id WHERE w.created_by_user_id = ? AND w.id <> ?`, userID, personalWorkspaceID)
		_, _ = database.Exec(`DELETE quota FROM storage_quota_usage quota JOIN workspaces w ON w.id = quota.workspace_id WHERE w.created_by_user_id = ? AND w.id <> ?`, userID, personalWorkspaceID)
		_, _ = database.Exec(`DELETE FROM workspaces WHERE created_by_user_id = ? AND id <> ?`, userID, personalWorkspaceID)
	})
	for index := 1; index < store.MaxWorkspaceAccessPerUser-1; index++ {
		result, err := database.ExecContext(ctx, `
INSERT INTO workspaces (owner_user_id, name, slug, created_by_user_id)
VALUES (?, ?, ?, ?)`, userID, fmt.Sprintf("seed workspace %d", index), fmt.Sprintf("admission-%d-%s", index, uuid.NewString()), userID)
		if err != nil {
			t.Fatalf("seed workspace %d: %v", index, err)
		}
		workspaceRaw, err := result.LastInsertId()
		if err != nil {
			t.Fatalf("seed workspace %d id: %v", index, err)
		}
		if _, err := database.ExecContext(ctx, `INSERT INTO storage_quota_usage (workspace_id) VALUES (?)`, workspaceRaw); err != nil {
			t.Fatalf("seed workspace quota row %d: %v", index, err)
		}
		if _, err := database.ExecContext(ctx, `INSERT INTO workspace_members (workspace_id, user_id, role) VALUES (?, ?, 'admin')`, workspaceRaw, userID); err != nil {
			t.Fatalf("seed workspace membership %d: %v", index, err)
		}
	}

	identities := store.NewIdentityStore(database)
	results := runConcurrentAdmissions(func(index int) error {
		_, err := identities.CreateWorkspaceForUser(ctx, userID, fmt.Sprintf("racing workspace %d %s", index, uuid.NewString()))
		return err
	})
	assertOneAdmissionAtLimit(t, results, store.ErrWorkspaceAccessLimit)
	assertTableCount(t, database, "workspace_members", "user_id", userID, store.MaxWorkspaceAccessPerUser)
}

func TestConcurrentWorkspaceMemberAdmissionEnforcesMemberLimit(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	_, workspaceID := createUploadBatchIdentity(t, database)
	if _, err := database.ExecContext(ctx, `UPDATE workspaces SET is_personal = FALSE WHERE id = ?`, workspaceID); err != nil {
		t.Fatalf("make collaborative workspace: %v", err)
	}
	emailPrefix := "member-admission-" + uuid.NewString()
	t.Cleanup(func() {
		_, _ = database.Exec(`DELETE FROM users WHERE email LIKE ?`, emailPrefix+"%")
	})
	for index := 1; index < store.MaxWorkspaceMembers-1; index++ {
		result, err := database.ExecContext(ctx, `
INSERT INTO users (name, email, google_subject) VALUES (?, ?, ?)`,
			fmt.Sprintf("seed member %d", index), fmt.Sprintf("%s-seed-%d@example.test", emailPrefix, index), fmt.Sprintf("%s-seed-%d", emailPrefix, index))
		if err != nil {
			t.Fatalf("seed member user %d: %v", index, err)
		}
		userRaw, err := result.LastInsertId()
		if err != nil {
			t.Fatalf("seed member user %d id: %v", index, err)
		}
		if _, err := database.ExecContext(ctx, `INSERT INTO workspace_members (workspace_id, user_id, role) VALUES (?, ?, 'read')`, workspaceID, userRaw); err != nil {
			t.Fatalf("seed member %d: %v", index, err)
		}
	}
	candidateEmails := [2]string{
		fmt.Sprintf("%s-race-0@example.test", emailPrefix),
		fmt.Sprintf("%s-race-1@example.test", emailPrefix),
	}
	for index, email := range candidateEmails {
		if _, err := database.ExecContext(ctx, `INSERT INTO users (name, email, google_subject) VALUES (?, ?, ?)`, fmt.Sprintf("candidate %d", index), email, fmt.Sprintf("%s-race-%d", emailPrefix, index)); err != nil {
			t.Fatalf("create candidate %d: %v", index, err)
		}
	}

	identities := store.NewIdentityStore(database)
	results := runConcurrentAdmissions(func(index int) error {
		_, err := identities.AddWorkspaceMemberByEmail(ctx, workspaceID, candidateEmails[index], "read")
		return err
	})
	assertOneAdmissionAtLimit(t, results, store.ErrWorkspaceMemberLimit)
	assertTableCount(t, database, "workspace_members", "workspace_id", workspaceID, store.MaxWorkspaceMembers)
}

func TestAuthSessionAdmissionEvictsOldestAndRetentionDeletesExpired(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	identities := store.NewIdentityStore(database)
	suffix := uuid.NewString()
	user, personalWorkspace, err := identities.EnsureGoogleUser(ctx, store.GoogleProfile{
		Subject: "session-admission-" + suffix,
		Email:   "session-admission-" + suffix + "@example.test",
		Name:    "Session admission",
	})
	if err != nil {
		t.Fatalf("create session user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = database.Exec(`DELETE FROM auth_sessions WHERE user_id = ?`, user.ID)
		_, _ = database.Exec(`DELETE FROM workspace_members WHERE workspace_id = ?`, personalWorkspace.ID)
		_, _ = database.Exec(`DELETE FROM storage_quota_usage WHERE workspace_id = ?`, personalWorkspace.ID)
		_, _ = database.Exec(`DELETE FROM workspaces WHERE id = ?`, personalWorkspace.ID)
		_, _ = database.Exec(`DELETE FROM users WHERE id = ?`, user.ID)
	})

	for index := 0; index < store.MaxAuthSessionsPerUser+5; index++ {
		if err := identities.CreateSession(ctx, user.ID, fmt.Sprintf("session-token-%02d-%s", index, suffix), "test", "192.0.2.1", time.Hour); err != nil {
			t.Fatalf("create session %d: %v", index, err)
		}
	}
	assertTableCount(t, database, "auth_sessions", "user_id", user.ID, store.MaxAuthSessionsPerUser)
	if _, err := identities.GetSession(ctx, fmt.Sprintf("session-token-%02d-%s", 0, suffix)); err == nil {
		t.Fatal("oldest session was not evicted")
	}
	if _, err := identities.GetSession(ctx, fmt.Sprintf("session-token-%02d-%s", store.MaxAuthSessionsPerUser+4, suffix)); err != nil {
		t.Fatalf("newest session is unavailable: %v", err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO auth_sessions (token_hash, user_id, expires_at) VALUES (?, ?, ?)`, fmt.Sprintf("%064x", time.Now().UnixNano()), user.ID, time.Now().UTC().Add(-time.Hour)); err != nil {
		t.Fatalf("seed expired session: %v", err)
	}
	if err := identities.RetainExpiredSessions(ctx, time.Now().UTC()); err != nil {
		t.Fatalf("retain expired sessions: %v", err)
	}
	assertTableCount(t, database, "auth_sessions", "user_id", user.ID, store.MaxAuthSessionsPerUser)
}

func TestConcurrentEditorReviewTokenAdmissionEnforcesWorkspaceLimit(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	identities := store.NewIdentityStore(database)
	userID, workspaceID := createUploadBatchIdentity(t, database)
	cleanupEditorReviewAdmissionFixture(t, database, workspaceID)
	suffix := uuid.NewString()
	itemID := "review-token-limit-" + suffix
	if _, err := database.ExecContext(ctx, `
INSERT INTO items (id, user_id, workspace_id, name, source_type, metadata)
VALUES (?, ?, ?, 'Review token admission', 'url', JSON_OBJECT())`, itemID, userID, workspaceID); err != nil {
		t.Fatalf("create review-token item: %v", err)
	}
	result, err := database.ExecContext(ctx, `
INSERT INTO item_images (workspace_id, item_id, sequence, image_url, canvas_uri, width, height)
VALUES (?, ?, 0, ?, ?, 100, 100)`, workspaceID, itemID, "https://images.example/"+suffix+".jpg", "https://iiif.example/canvas/"+suffix)
	if err != nil {
		t.Fatalf("create review-token image: %v", err)
	}
	imageIDRaw, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	imageID := uint64(imageIDRaw) // #nosec G115 -- positive test fixture identifier.
	createGrant := func(index int) error {
		return identities.CreateEditorReviewGrant(ctx, store.EditorReviewGrant{
			ID:                  uuid.NewString(),
			TokenHash:           editorReviewAdmissionDigest(suffix, "grant", index),
			WorkspaceID:         workspaceID,
			ItemID:              itemID,
			ItemImageID:         imageID,
			IssuedByUserID:      userID,
			ReviewerSubjectHash: editorReviewAdmissionDigest(suffix, "reviewer", index),
			ReviewerName:        "Review token admission",
			SessionTTL:          time.Hour,
			ExpiresAt:           time.Now().UTC().Add(5 * time.Minute),
		})
	}
	for index := 0; index < store.MaxActiveEditorReviewTokensPerWorkspace-1; index++ {
		if err := createGrant(index); err != nil {
			t.Fatalf("seed review grant %d: %v", index, err)
		}
	}
	results := runConcurrentAdmissions(func(index int) error {
		return createGrant(store.MaxActiveEditorReviewTokensPerWorkspace + index)
	})
	assertOneAdmissionAtLimit(t, results, store.ErrEditorReviewTokenLimit)
	assertTableCount(t, database, "editor_review_tokens", "workspace_id", workspaceID, store.MaxActiveEditorReviewTokensPerWorkspace)
}

func TestEditorReviewSessionAdmissionEvictsOldestAndBoundsAuditMetadata(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	identities := store.NewIdentityStore(database)
	userID, workspaceID := createUploadBatchIdentity(t, database)
	cleanupEditorReviewAdmissionFixture(t, database, workspaceID)
	suffix := uuid.NewString()
	itemID := "review-admission-" + suffix
	if _, err := database.ExecContext(ctx, `
INSERT INTO items (id, user_id, workspace_id, name, source_type, metadata)
VALUES (?, ?, ?, 'Review admission', 'url', JSON_OBJECT())`, itemID, userID, workspaceID); err != nil {
		t.Fatalf("create review item: %v", err)
	}
	result, err := database.ExecContext(ctx, `
INSERT INTO item_images (workspace_id, item_id, sequence, image_url, canvas_uri, width, height)
VALUES (?, ?, 0, ?, ?, 100, 100)`, workspaceID, itemID, "https://images.example/"+suffix+".jpg", "https://iiif.example/canvas/"+suffix)
	if err != nil {
		t.Fatalf("create review image: %v", err)
	}
	imageIDRaw, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	imageID := uint64(imageIDRaw) // #nosec G115 -- positive test fixture identifier.

	for index := 0; index < store.MaxActiveEditorReviewSessionsPerWorkspace+5; index++ {
		grantToken := fmt.Sprintf("grant-%03d-%s", index, suffix)
		grantHash := editorReviewAdmissionDigest(suffix, "grant", index)
		grantID := uuid.NewString()
		if err := identities.CreateEditorReviewGrant(ctx, store.EditorReviewGrant{
			ID:                  grantID,
			TokenHash:           grantHash,
			WorkspaceID:         workspaceID,
			ItemID:              itemID,
			ItemImageID:         imageID,
			IssuedByUserID:      userID,
			ReviewerSubjectHash: editorReviewAdmissionDigest(suffix, "reviewer", index),
			ReviewerName:        "Review admission",
			SessionTTL:          time.Hour,
			ExpiresAt:           time.Now().UTC().Add(5 * time.Minute),
		}); err != nil {
			t.Fatalf("create review grant %d: %v", index, err)
		}
		sessionToken := fmt.Sprintf("session-%03d-%s", index, suffix)
		if _, err := identities.RedeemEditorReviewGrant(ctx, store.RedeemEditorReviewGrantParams{
			TokenHash:       grantHash,
			GrantID:         grantID,
			WorkspaceID:     workspaceID,
			ItemID:          itemID,
			ItemImageID:     imageID,
			RawSessionToken: sessionToken,
			UserAgent:       strings.Repeat("browser", 300) + "\n",
			IPAddress:       strings.Repeat("1", 300),
		}); err != nil {
			t.Fatalf("redeem review grant %d (%s): %v", index, grantToken, err)
		}
	}

	assertTableCount(t, database, "editor_review_sessions", "workspace_id", workspaceID, store.MaxActiveEditorReviewSessionsPerWorkspace)
	if _, err := identities.GetEditorReviewSession(ctx, fmt.Sprintf("session-%03d-%s", 0, suffix)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("oldest review session error = %v, want sql.ErrNoRows", err)
	}
	newest := fmt.Sprintf("session-%03d-%s", store.MaxActiveEditorReviewSessionsPerWorkspace+4, suffix)
	if _, err := identities.GetEditorReviewSession(ctx, newest); err != nil {
		t.Fatalf("newest review session is unavailable: %v", err)
	}
	var userAgent, ipAddress string
	if err := database.QueryRowContext(ctx, `
SELECT user_agent, ip_address
FROM editor_review_sessions
WHERE workspace_id = ?
ORDER BY id DESC
LIMIT 1`, workspaceID).Scan(&userAgent, &ipAddress); err != nil {
		t.Fatalf("load review audit metadata: %v", err)
	}
	if len([]rune(userAgent)) > 1024 || strings.ContainsAny(userAgent, "\r\n") || len([]rune(ipAddress)) > 255 {
		t.Fatalf("unbounded review audit metadata: user_agent=%d runes ip=%d runes", len([]rune(userAgent)), len([]rune(ipAddress)))
	}
}

func editorReviewAdmissionDigest(suffix, kind string, index int) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d", suffix, kind, index))))
}

func cleanupEditorReviewAdmissionFixture(t *testing.T, database *sql.DB, workspaceID uint64) {
	t.Helper()
	t.Cleanup(func() {
		if _, err := database.Exec(`DELETE FROM editor_review_sessions WHERE workspace_id = ?`, workspaceID); err != nil {
			t.Errorf("clean up editor review admission sessions: %v", err)
		}
		if _, err := database.Exec(`DELETE FROM editor_review_tokens WHERE workspace_id = ?`, workspaceID); err != nil {
			t.Errorf("clean up editor review admission tokens: %v", err)
		}
		// The admission fixtures insert their item tuple directly so the cap tests
		// remain focused. Reconcile that tuple before the shared identity cleanup
		// asks ItemStore to remove the quota-accounted resource graph.
		if err := store.NewItemStore(database).RebuildStorageQuotaUsage(context.Background()); err != nil {
			t.Errorf("rebuild quota before editor review admission cleanup: %v", err)
		}
	})
}

func runConcurrentAdmissions(admit func(index int) error) []error {
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	workers.Add(2)
	for index := 0; index < 2; index++ {
		index := index
		go func() {
			defer workers.Done()
			<-start
			results <- admit(index)
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	out := make([]error, 0, 2)
	for err := range results {
		out = append(out, err)
	}
	return out
}

func assertOneAdmissionAtLimit(t *testing.T, results []error, limitErr error) {
	t.Helper()
	succeeded, limited := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, limitErr):
			limited++
		default:
			t.Fatalf("unexpected concurrent admission error: %v", err)
		}
	}
	if succeeded != 1 || limited != 1 {
		t.Fatalf("concurrent outcomes = success:%d limited:%d, want 1/1", succeeded, limited)
	}
}

func assertTableCount(t *testing.T, database interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, table, column string, id uint64, want int) {
	t.Helper()
	var got int
	query := fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE %s = ?", table, column) // #nosec G201 -- table and column are fixed test constants.
	if err := database.QueryRowContext(context.Background(), query, id).Scan(&got); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if got != want {
		t.Fatalf("%s count = %d, want %d", table, got, want)
	}
}
