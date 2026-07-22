package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
