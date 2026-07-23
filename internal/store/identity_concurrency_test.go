package store_test

import (
	"context"
	"database/sql"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

func TestConcurrentEnsureGoogleUserConvergesAndRepairsIdentityGraph(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	profile := store.GoogleProfile{
		Subject: "identity-convergence-" + suffix,
		Email:   "identity-convergence-" + suffix + "@example.test",
		Name:    "Identity convergence",
	}
	t.Cleanup(func() { cleanupGoogleIdentityFixture(database, profile) })
	identities := store.NewIdentityStore(database)

	const callers = 8
	start := make(chan struct{})
	results := make(chan struct {
		user      store.User
		workspace store.Workspace
		err       error
	}, callers)
	var wait sync.WaitGroup
	wait.Add(callers)
	for index := 0; index < callers; index++ {
		go func() {
			defer wait.Done()
			<-start
			user, workspace, err := identities.EnsureGoogleUser(ctx, profile)
			results <- struct {
				user      store.User
				workspace store.Workspace
				err       error
			}{user: user, workspace: workspace, err: err}
		}()
	}
	close(start)
	wait.Wait()
	close(results)

	var userID, workspaceID uint64
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent EnsureGoogleUser: %v", result.err)
		}
		if userID == 0 {
			userID, workspaceID = result.user.ID, result.workspace.ID
		}
		if result.user.ID != userID || result.workspace.ID != workspaceID {
			t.Fatalf("identity did not converge: user/workspace %d/%d, want %d/%d", result.user.ID, result.workspace.ID, userID, workspaceID)
		}
	}
	var users, workspaces, memberships, quotaRows int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE google_subject = ?`, profile.Subject).Scan(&users); err != nil {
		t.Fatalf("count converged users: %v", err)
	}
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspaces WHERE owner_user_id = ? AND is_personal = TRUE`, userID).Scan(&workspaces); err != nil {
		t.Fatalf("count converged personal workspaces: %v", err)
	}
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspace_members WHERE workspace_id = ? AND user_id = ? AND role = 'admin'`, workspaceID, userID).Scan(&memberships); err != nil {
		t.Fatalf("count converged workspace memberships: %v", err)
	}
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM storage_quota_usage WHERE workspace_id = ?`, workspaceID).Scan(&quotaRows); err != nil {
		t.Fatalf("count converged quota rows: %v", err)
	}
	if users != 1 || workspaces != 1 || memberships != 1 || quotaRows != 1 {
		t.Fatalf("converged identity graph = users:%d workspaces:%d memberships:%d quotas:%d, want 1 each", users, workspaces, memberships, quotaRows)
	}

	if _, err := database.ExecContext(ctx, `DELETE FROM workspace_members WHERE workspace_id = ? AND user_id = ?`, workspaceID, userID); err != nil {
		t.Fatalf("remove membership for recovery fixture: %v", err)
	}
	if _, err := database.ExecContext(ctx, `DELETE FROM storage_quota_usage WHERE workspace_id = ?`, workspaceID); err != nil {
		t.Fatalf("remove quota row for recovery fixture: %v", err)
	}
	repairedUser, repairedWorkspace, err := identities.EnsureGoogleUser(ctx, profile)
	if err != nil {
		t.Fatalf("repair existing identity graph: %v", err)
	}
	if repairedUser.ID != userID || repairedWorkspace.ID != workspaceID {
		t.Fatalf("identity repair created replacement rows: user/workspace %d/%d", repairedUser.ID, repairedWorkspace.ID)
	}
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspace_members WHERE workspace_id = ? AND user_id = ? AND role = 'admin'`, workspaceID, userID).Scan(&memberships); err != nil {
		t.Fatalf("count repaired membership: %v", err)
	}
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM storage_quota_usage WHERE workspace_id = ?`, workspaceID).Scan(&quotaRows); err != nil {
		t.Fatalf("count repaired quota row: %v", err)
	}
	if memberships != 1 || quotaRows != 1 {
		t.Fatalf("repaired identity graph = memberships:%d quotas:%d, want 1/1", memberships, quotaRows)
	}
}

func cleanupGoogleIdentityFixture(database *sql.DB, profile store.GoogleProfile) {
	rows, err := database.Query(`SELECT id FROM users WHERE google_subject = ? OR email = ?`, profile.Subject, profile.Email)
	if err != nil {
		return
	}
	var userIDs []uint64
	for rows.Next() {
		var userID uint64
		if rows.Scan(&userID) == nil {
			userIDs = append(userIDs, userID)
		}
	}
	_ = rows.Close()
	for _, userID := range userIDs {
		workspaceRows, queryErr := database.Query(`SELECT id FROM workspaces WHERE owner_user_id = ?`, userID)
		if queryErr == nil {
			var workspaceIDs []uint64
			for workspaceRows.Next() {
				var workspaceID uint64
				if workspaceRows.Scan(&workspaceID) == nil {
					workspaceIDs = append(workspaceIDs, workspaceID)
				}
			}
			_ = workspaceRows.Close()
			for _, workspaceID := range workspaceIDs {
				_, _ = database.Exec(`DELETE FROM workspace_members WHERE workspace_id = ?`, workspaceID)
				_, _ = database.Exec(`DELETE FROM storage_quota_usage WHERE workspace_id = ?`, workspaceID)
				_, _ = database.Exec(`DELETE FROM workspaces WHERE id = ?`, workspaceID)
			}
		}
		_, _ = database.Exec(`DELETE FROM auth_sessions WHERE user_id = ?`, userID)
		_, _ = database.Exec(`DELETE FROM users WHERE id = ?`, userID)
	}
}
