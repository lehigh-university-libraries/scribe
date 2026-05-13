package server

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	dbstore "github.com/lehigh-university-libraries/scribe/internal/db"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

func createTestUser(t *testing.T, db *sql.DB, name string) uint64 {
	t.Helper()

	result, err := db.Exec(
		`INSERT INTO users (name, email, google_subject) VALUES (?, ?, ?)`,
		name,
		fmt.Sprintf("%s@example.test", name),
		fmt.Sprintf("sub-%s-%d", name, time.Now().UnixNano()),
	)
	if err != nil {
		t.Fatalf("insert user %q: %v", name, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id for user %q: %v", name, err)
	}
	return uint64(id)
}

func createTestWorkspace(t *testing.T, db *sql.DB, userID uint64, name string) uint64 {
	t.Helper()

	slug := fmt.Sprintf("%s-%d", store.Slugify(name), time.Now().UnixNano())
	result, err := db.Exec(
		`INSERT INTO workspaces (owner_user_id, name, slug, is_personal, created_by_user_id) VALUES (?, ?, ?, FALSE, ?)`,
		userID,
		name,
		slug,
		userID,
	)
	if err != nil {
		t.Fatalf("insert workspace %q: %v", name, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id for workspace %q: %v", name, err)
	}
	workspaceID := uint64(id)
	if _, err := db.Exec(`INSERT INTO workspace_members (workspace_id, user_id, role) VALUES (?, ?, 'admin')`, workspaceID, userID); err != nil {
		t.Fatalf("insert workspace member for %q: %v", name, err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM workspace_members WHERE workspace_id = ?`, workspaceID)
		_, _ = db.Exec(`DELETE FROM workspaces WHERE id = ?`, workspaceID)
	})
	return workspaceID
}

func TestResolveTranscriptionJobContextUsesOwningUserScope(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	itemStore := store.NewItemStore(db)
	contextStore := store.NewContextStore(db)
	handler := &Handler{
		items:    itemStore,
		contexts: contextStore,
	}

	ownerUserID := createTestUser(t, db, fmt.Sprintf("owner-%d", time.Now().UnixNano()))
	otherUserID := createTestUser(t, db, fmt.Sprintf("other-%d", time.Now().UnixNano()))
	ownerWorkspaceID := createTestWorkspace(t, db, ownerUserID, uniqueName("owner-workspace"))
	otherWorkspaceID := createTestWorkspace(t, db, otherUserID, uniqueName("other-workspace"))

	defaultCtx, err := contextStore.Create(ctx, store.Context{
		Name:                  uniqueName("job-default"),
		IsDefault:             true,
		SegmentationModel:     "scribe",
		TranscriptionProvider: "ollama",
		TranscriptionModel:    "default-model",
	})
	if err != nil {
		t.Fatalf("create default context: %v", err)
	}
	t.Cleanup(func() {
		_ = contextStore.Delete(context.Background(), defaultCtx.ID)
	})

	otherCtxUserID := otherUserID
	otherCtx, err := contextStore.Create(ctx, store.Context{
		UserID:                &otherCtxUserID,
		WorkspaceID:           &otherWorkspaceID,
		Name:                  uniqueName("other-user-context"),
		IsDefault:             false,
		SegmentationModel:     "scribe",
		TranscriptionProvider: "ollama",
		TranscriptionModel:    "other-user-model",
	})
	if err != nil {
		t.Fatalf("create other user context: %v", err)
	}
	t.Cleanup(func() {
		_ = contextStore.Delete(context.Background(), otherCtx.ID)
	})

	rule, err := contextStore.CreateRuleForWorkspace(ctx, otherWorkspaceID, store.ContextSelectionRule{
		ContextID: otherCtx.ID,
		Priority:  100,
	})
	if err != nil {
		t.Fatalf("create other workspace selection rule: %v", err)
	}
	t.Cleanup(func() {
		_ = contextStore.DeleteRule(context.Background(), rule.ID)
	})

	itemID := uniqueName("job-item")
	item, err := itemStore.Create(ctx, dbstore.CreateItemParams{
		ID:          itemID,
		UserID:      ownerUserID,
		WorkspaceID: ownerWorkspaceID,
		Name:        "Job Item",
		SourceType:  "upload",
	})
	if err != nil {
		t.Fatalf("create item: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM item_images WHERE item_id = ?`, item.ID)
		_ = itemStore.Delete(context.Background(), item.ID)
	})

	image, err := itemStore.AddImage(ctx, dbstore.CreateItemImageParams{
		ItemID:   item.ID,
		Sequence: 0,
		ImageURL: "https://example.test/image.jpg",
	})
	if err != nil {
		t.Fatalf("add item image: %v", err)
	}

	resolved, workspaceID, ownerID, err := handler.resolveTranscriptionJobContext(ctx, &store.TranscriptionJob{
		ItemImageID: image.ID,
	})
	if err != nil {
		t.Fatalf("resolveTranscriptionJobContext: %v", err)
	}
	if workspaceID != ownerWorkspaceID {
		t.Fatalf("workspaceID = %d, want %d", workspaceID, ownerWorkspaceID)
	}
	if ownerID == nil || *ownerID != ownerUserID {
		t.Fatalf("ownerID = %v, want %d", ownerID, ownerUserID)
	}
	if !resolved.IsDefault {
		t.Fatalf("resolved.IsDefault = false, want true; worker should fall back to a default context for the owning user")
	}
	if resolved.ID == otherCtx.ID {
		t.Fatalf("resolved context = %d; worker leaked another user's selection rule", resolved.ID)
	}
}

func TestResolveTranscriptionJobContextRejectsOtherUsersExplicitContext(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	itemStore := store.NewItemStore(db)
	contextStore := store.NewContextStore(db)
	handler := &Handler{
		items:    itemStore,
		contexts: contextStore,
	}

	ownerUserID := createTestUser(t, db, fmt.Sprintf("owner-explicit-%d", time.Now().UnixNano()))
	otherUserID := createTestUser(t, db, fmt.Sprintf("other-explicit-%d", time.Now().UnixNano()))
	ownerWorkspaceID := createTestWorkspace(t, db, ownerUserID, uniqueName("owner-explicit-workspace"))
	otherWorkspaceID := createTestWorkspace(t, db, otherUserID, uniqueName("other-explicit-workspace"))

	otherCtxUserID := otherUserID
	otherCtx, err := contextStore.Create(ctx, store.Context{
		UserID:                &otherCtxUserID,
		WorkspaceID:           &otherWorkspaceID,
		Name:                  uniqueName("other-explicit-context"),
		SegmentationModel:     "kraken",
		TranscriptionProvider: "ollama",
		TranscriptionModel:    "other-explicit-model",
	})
	if err != nil {
		t.Fatalf("create other user context: %v", err)
	}
	t.Cleanup(func() {
		_ = contextStore.Delete(context.Background(), otherCtx.ID)
	})

	itemID := uniqueName("job-item-explicit")
	item, err := itemStore.Create(ctx, dbstore.CreateItemParams{
		ID:          itemID,
		UserID:      ownerUserID,
		WorkspaceID: ownerWorkspaceID,
		Name:        "Job Item Explicit",
		SourceType:  "upload",
	})
	if err != nil {
		t.Fatalf("create item: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM item_images WHERE item_id = ?`, item.ID)
		_ = itemStore.Delete(context.Background(), item.ID)
	})

	image, err := itemStore.AddImage(ctx, dbstore.CreateItemImageParams{
		ItemID:   item.ID,
		Sequence: 0,
		ImageURL: "https://example.test/image-explicit.jpg",
	})
	if err != nil {
		t.Fatalf("add item image: %v", err)
	}

	otherContextID := otherCtx.ID
	if _, _, _, err := handler.resolveTranscriptionJobContext(ctx, &store.TranscriptionJob{
		ItemImageID: image.ID,
		ContextID:   &otherContextID,
	}); err == nil {
		t.Fatal("resolveTranscriptionJobContext succeeded with another user's explicit context")
	}
}
