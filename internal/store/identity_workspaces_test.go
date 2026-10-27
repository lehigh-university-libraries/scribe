package store_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/store"
)

func TestWorkspaceMemberCanBeAddedBeforeFirstSignIn(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	identities := store.NewIdentityStore(database)
	ownerProfile := store.GoogleProfile{Subject: fmt.Sprintf("invite-owner-%d", time.Now().UnixNano()), Email: fmt.Sprintf("invite-owner-%d@example.test", time.Now().UnixNano()), Name: "Owner"}
	t.Cleanup(func() { cleanupGoogleIdentityFixture(database, ownerProfile) })
	_, workspace, err := identities.EnsureGoogleUser(ctx, ownerProfile)
	if err != nil {
		t.Fatal(err)
	}
	profile := store.GoogleProfile{Subject: fmt.Sprintf("invitee-%d", time.Now().UnixNano()), Email: fmt.Sprintf("invitee-%d@example.test", time.Now().UnixNano()), Name: "New colleague"}
	t.Cleanup(func() { cleanupGoogleIdentityFixture(database, profile) })
	member, err := identities.AddWorkspaceMemberByEmail(ctx, workspace.ID, " "+strings.ToUpper(profile.Email)+" ", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if member.Role != "admin" || member.User.Email != profile.Email || member.User.GoogleSubject != "" || member.User.IsAdmin || !member.User.LastLoginAt.IsZero() {
		t.Fatalf("reserved identity has incorrect access or login state: %+v", member)
	}
	user, defaultWorkspace, err := identities.EnsureGoogleUser(ctx, profile)
	if err != nil {
		t.Fatal(err)
	}
	if user.ID != member.User.ID || user.GoogleSubject != profile.Subject || defaultWorkspace.ID != workspace.ID {
		t.Fatalf("first login did not claim the reserved membership: %+v/%+v", user, defaultWorkspace)
	}
	access, err := identities.GetWorkspaceAccess(ctx, user.ID, workspace.ID)
	if err != nil || access.Role != "admin" {
		t.Fatalf("claimed membership = %+v/%v", access, err)
	}
	if _, err := identities.AddWorkspaceMemberByEmail(ctx, workspace.ID, profile.Email, "read"); !errors.Is(err, store.ErrWorkspaceMemberExists) {
		t.Fatalf("duplicate invitation changed membership: %v", err)
	}
	user, err = identities.GetUser(ctx, user.ID)
	if err != nil || user.Name != profile.Name || user.GoogleSubject != profile.Subject || user.IsAdmin {
		t.Fatalf("re-adding a member overwrote their identity: %+v/%v", user, err)
	}
}

func TestLoginWorkspaceSupportsCollaborationAndHonorsMembershipChanges(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	identities := store.NewIdentityStore(database)
	profile := store.GoogleProfile{
		Subject: fmt.Sprintf("collaborate-%d", time.Now().UnixNano()),
		Email:   fmt.Sprintf("collaborate-%d@example.test", time.Now().UnixNano()),
		Name:    "Workspace creator",
	}
	t.Cleanup(func() { cleanupGoogleIdentityFixture(database, profile) })
	owner, workspace, err := identities.EnsureGoogleUser(ctx, profile)
	if err != nil {
		t.Fatal(err)
	}
	memberProfile := store.GoogleProfile{
		Subject: fmt.Sprintf("collaborator-%d", time.Now().UnixNano()),
		Email:   fmt.Sprintf("collaborator-%d@example.test", time.Now().UnixNano()),
		Name:    "Collaborator",
	}
	t.Cleanup(func() { cleanupGoogleIdentityFixture(database, memberProfile) })
	member, _, err := identities.EnsureGoogleUser(ctx, memberProfile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := identities.UpdateWorkspaceName(ctx, workspace.ID, "Shared collection"); err != nil {
		t.Fatal(err)
	}
	if _, err := identities.AddWorkspaceMemberByEmail(ctx, workspace.ID, member.Email, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := identities.UpdateWorkspaceMemberRole(ctx, workspace.ID, owner.ID, "read"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := identities.EnsureGoogleUser(ctx, profile); err != nil {
		t.Fatal(err)
	}
	access, err := identities.GetWorkspaceAccess(ctx, owner.ID, workspace.ID)
	if err != nil || access.Role != "read" {
		t.Fatalf("login restored creator privileges: %+v/%v", access, err)
	}
	if err := identities.RemoveWorkspaceMember(ctx, workspace.ID, member.ID); !errors.Is(err, store.ErrLastWorkspaceAdmin) {
		t.Fatalf("removing last administrator = %v", err)
	}
	if err := identities.RemoveWorkspaceMember(ctx, workspace.ID, owner.ID); err != nil {
		t.Fatal(err)
	}
	_, replacement, err := identities.EnsureGoogleUser(ctx, profile)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.ID == workspace.ID {
		t.Fatal("login restored revoked workspace access")
	}
	if _, err := identities.GetWorkspaceAccess(ctx, owner.ID, workspace.ID); err == nil {
		t.Fatal("removed creator regained access")
	}
}

func TestConcurrentAdminDemotionAndRemovalRetainOneAdministrator(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	firstUser, workspaceID := createUploadBatchIdentity(t, database)
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
