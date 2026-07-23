package store_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/store"
)

func TestConcurrentAdminDemotionAndRemovalRetainOneAdministrator(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	firstUser, workspaceID := createUploadBatchIdentity(t, database)
	if _, err := database.ExecContext(ctx, `UPDATE workspaces SET is_personal = FALSE WHERE id = ?`, workspaceID); err != nil {
		t.Fatalf("make collaborative workspace: %v", err)
	}
	result, err := database.ExecContext(ctx, `
INSERT INTO users (name, email, google_subject)
VALUES (?, ?, ?)
`, "second admin", fmt.Sprintf("second-admin-%d@example.test", time.Now().UnixNano()), fmt.Sprintf("second-admin-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("create second admin: %v", err)
	}
	secondID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("second admin id: %v", err)
	}
	secondUser := uint64(secondID)
	if _, err := database.ExecContext(ctx, `
INSERT INTO workspace_members (workspace_id, user_id, role)
VALUES (?, ?, 'admin')
`, workspaceID, secondUser); err != nil {
		t.Fatalf("add second admin membership: %v", err)
	}
	identities := store.NewIdentityStore(database)
	start := make(chan struct{})
	errorsSeen := make(chan error, 2)
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		<-start
		_, err := identities.UpdateWorkspaceMemberRole(ctx, workspaceID, firstUser, "write")
		errorsSeen <- err
	}()
	go func() {
		defer wait.Done()
		<-start
		errorsSeen <- identities.RemoveWorkspaceMember(ctx, workspaceID, secondUser)
	}()
	close(start)
	wait.Wait()
	close(errorsSeen)
	succeeded, retained := 0, 0
	for err := range errorsSeen {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, store.ErrLastWorkspaceAdmin):
			retained++
		default:
			t.Fatalf("concurrent membership mutation error = %v", err)
		}
	}
	if succeeded != 1 || retained != 1 {
		t.Fatalf("concurrent outcomes = success:%d last-admin:%d, want 1/1", succeeded, retained)
	}
	var admins int
	if err := database.QueryRowContext(ctx, `
SELECT COUNT(*) FROM workspace_members WHERE workspace_id = ? AND role = 'admin'
`, workspaceID).Scan(&admins); err != nil || admins != 1 {
		t.Fatalf("remaining admin count = %d/%v, want 1", admins, err)
	}
}
