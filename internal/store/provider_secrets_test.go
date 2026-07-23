package store_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

func TestProviderSecretRepositoryEnforcesOwnershipVisibilityAndCleanup(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	ownerID, workspaceID := createUploadBatchIdentity(t, database)
	suffix := uuid.NewString()

	viewerResult, err := database.ExecContext(ctx, `
INSERT INTO users (name, email, google_subject) VALUES (?, ?, ?)`,
		"provider secret viewer", "provider-secret-viewer-"+suffix+"@example.test", "provider-secret-viewer-"+suffix)
	if err != nil {
		t.Fatalf("create provider secret viewer: %v", err)
	}
	viewerRaw, _ := viewerResult.LastInsertId()
	viewerID := uint64(viewerRaw)
	if _, err := database.ExecContext(ctx, `INSERT INTO workspace_members (workspace_id, user_id, role) VALUES (?, ?, 'read')`, workspaceID, viewerID); err != nil {
		t.Fatalf("create provider secret viewer membership: %v", err)
	}
	t.Cleanup(func() {
		_, _ = database.Exec(`DELETE FROM workspace_members WHERE workspace_id = ? AND user_id = ?`, workspaceID, viewerID)
		_, _ = database.Exec(`DELETE FROM users WHERE id = ?`, viewerID)
	})

	secrets := store.NewProviderSecretStore(database)
	userSecret, err := secrets.Create(ctx, store.ProviderSecret{
		UserID:      &ownerID,
		WorkspaceID: workspaceID,
		Provider:    "openai",
		Name:        "owner only",
		VaultPath:   "scribe/test/provider-secrets/" + suffix + "/owner-only",
	})
	if err != nil {
		t.Fatalf("create user provider secret: %v", err)
	}
	userSecret, err = secrets.Activate(ctx, userSecret.ID, workspaceID)
	if err != nil {
		t.Fatalf("activate user provider secret: %v", err)
	}
	workspaceSecret, err := secrets.Create(ctx, store.ProviderSecret{
		WorkspaceID: workspaceID,
		Provider:    "gemini",
		Name:        "workspace",
		VaultPath:   "scribe/test/provider-secrets/" + suffix + "/workspace",
	})
	if err != nil {
		t.Fatalf("create workspace provider secret: %v", err)
	}
	workspaceSecret, err = secrets.Activate(ctx, workspaceSecret.ID, workspaceID)
	if err != nil {
		t.Fatalf("activate workspace provider secret: %v", err)
	}

	ownerVisible, err := secrets.ListVisible(ctx, workspaceID, ownerID)
	if err != nil || !providerSecretListContains(ownerVisible, userSecret.ID) || !providerSecretListContains(ownerVisible, workspaceSecret.ID) {
		t.Fatalf("owner provider secret visibility = %+v/%v", ownerVisible, err)
	}
	viewerVisible, err := secrets.ListVisible(ctx, workspaceID, viewerID)
	if err != nil || providerSecretListContains(viewerVisible, userSecret.ID) || !providerSecretListContains(viewerVisible, workspaceSecret.ID) {
		t.Fatalf("viewer provider secret visibility = %+v/%v", viewerVisible, err)
	}

	nonmemberResult, err := database.ExecContext(ctx, `INSERT INTO users (name) VALUES (?)`, "provider secret nonmember "+suffix)
	if err != nil {
		t.Fatalf("create nonmember: %v", err)
	}
	nonmemberRaw, _ := nonmemberResult.LastInsertId()
	nonmemberID := uint64(nonmemberRaw)
	t.Cleanup(func() { _, _ = database.Exec(`DELETE FROM users WHERE id = ?`, nonmemberID) })
	if _, err := secrets.Create(ctx, store.ProviderSecret{
		UserID: &nonmemberID, WorkspaceID: workspaceID, Provider: "openai", Name: "invalid",
		VaultPath: "scribe/test/provider-secrets/" + suffix + "/invalid-member",
	}); err == nil || !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("nonmember provider secret error = %v, want sql.ErrNoRows", err)
	}
	missingUserID := ^uint64(0)
	if _, err := secrets.Create(ctx, store.ProviderSecret{
		UserID: &missingUserID, WorkspaceID: workspaceID, Provider: "openai", Name: "missing",
		VaultPath: "scribe/test/provider-secrets/" + suffix + "/missing-user",
	}); err == nil || !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing-user provider secret error = %v, want sql.ErrNoRows", err)
	}
	if _, err := secrets.Create(ctx, store.ProviderSecret{
		WorkspaceID: ^uint64(0), Provider: "openai", Name: "missing workspace",
		VaultPath: "scribe/test/provider-secrets/" + suffix + "/missing-workspace",
	}); err == nil || !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing-workspace provider secret error = %v, want sql.ErrNoRows", err)
	}

	pending, err := secrets.Create(ctx, store.ProviderSecret{
		WorkspaceID: workspaceID,
		Provider:    "openai",
		Name:        "pending reconciliation",
		VaultPath:   "scribe/test/provider-secrets/" + suffix + "/pending",
	})
	if err != nil {
		t.Fatalf("create pending provider secret: %v", err)
	}
	candidates, err := secrets.ListCleanupCandidates(ctx, time.Now().UTC().Add(time.Minute), 100)
	if err != nil || !providerSecretListContains(candidates, pending.ID) {
		t.Fatalf("pending cleanup candidates = %+v/%v", candidates, err)
	}
	if err := secrets.MarkPendingCleanup(ctx, pending.ID, workspaceID); err != nil {
		t.Fatalf("mark pending secret cleanup: %v", err)
	}
	if err := secrets.DeleteInactive(ctx, pending.ID, workspaceID); err != nil {
		t.Fatalf("delete pending secret locator: %v", err)
	}

	for _, secret := range []store.ProviderSecret{userSecret, workspaceSecret} {
		if err := secrets.MarkActiveCleanup(ctx, secret.ID, workspaceID); err != nil {
			t.Fatalf("mark active secret %d cleanup: %v", secret.ID, err)
		}
		if err := secrets.DeleteInactive(ctx, secret.ID, workspaceID); err != nil {
			t.Fatalf("delete active secret %d locator: %v", secret.ID, err)
		}
	}
	if _, err := secrets.GetLifecycle(ctx, userSecret.ID, workspaceID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted provider secret lookup error = %v, want sql.ErrNoRows", err)
	}
	if violations, err := store.AuditRelationshipIntegrity(ctx, database); err != nil {
		t.Fatalf("audit provider secret cleanup: %v", err)
	} else if len(violations) != 0 {
		t.Fatalf("provider secret cleanup left relationship violations: %+v", violations)
	}
}

func providerSecretListContains(secrets []store.ProviderSecret, id uint64) bool {
	for _, secret := range secrets {
		if secret.ID == id {
			return true
		}
	}
	return false
}
