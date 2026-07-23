package store_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	dbstore "github.com/lehigh-university-libraries/scribe/internal/db"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

func TestUserCanReadImageURLRequiresExactReferenceInMemberWorkspace(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	userA, workspaceA := createUploadBatchIdentity(t, database)
	userB, workspaceB := createUploadBatchIdentity(t, database)
	itemStore := store.NewItemStore(database)
	itemID := "image-auth-" + uuid.NewString()
	if _, err := itemStore.Create(ctx, dbstore.CreateItemParams{
		ID:          itemID,
		UserID:      userB,
		WorkspaceID: workspaceB,
		Name:        "image source authorization",
		SourceType:  "manifest",
		SourceURL:   "https://source.example/manifest/" + uuid.NewString(),
	}); err != nil {
		t.Fatalf("create item: %v", err)
	}
	image, err := itemStore.AddImage(ctx, dbstore.CreateItemImageParams{
		ItemID:    itemID,
		Sequence:  1,
		ImageURL:  "https://source.example/image/" + uuid.NewString() + ".png",
		CanvasURI: "https://source.example/canvas/" + uuid.NewString(),
		Width:     100,
		Height:    100,
	})
	if err != nil {
		t.Fatalf("create item image: %v", err)
	}

	if allowed, err := itemStore.UserCanReadImageURL(ctx, userB, image.ImageURL); err != nil || !allowed {
		t.Fatalf("owner membership access = %t, %v; want true", allowed, err)
	}
	if allowed, err := itemStore.UserCanReadImageURL(ctx, userA, image.ImageURL); err != nil || allowed {
		t.Fatalf("unrelated user access = %t, %v; want false", allowed, err)
	}
	if _, err := database.ExecContext(ctx,
		`INSERT INTO workspace_members (workspace_id, user_id, role) VALUES (?, ?, 'read')`,
		workspaceB, userA,
	); err != nil {
		t.Fatalf("add cross-workspace membership: %v", err)
	}
	if allowed, err := itemStore.UserCanReadImageURL(ctx, userA, image.ImageURL); err != nil || !allowed {
		t.Fatalf("member access = %t, %v; want true", allowed, err)
	}
	if allowed, err := itemStore.UserCanReadImageURL(ctx, userA, image.ImageURL+"-near-match"); err != nil || allowed {
		t.Fatalf("near-match access = %t, %v; want false", allowed, err)
	}
	if allowed, err := itemStore.WorkspaceOwnsImageURL(ctx, workspaceA, image.ImageURL); err != nil || allowed {
		t.Fatalf("unrelated workspace access = %t, %v; want false", allowed, err)
	}

	// Without foreign keys, authorization queries must fail closed when a
	// corrupt child carries a workspace different from its owning item.
	if _, err := database.ExecContext(ctx, `UPDATE item_images SET workspace_id = ? WHERE id = ?`, workspaceA, image.ID); err != nil {
		t.Fatalf("corrupt image workspace fixture: %v", err)
	}
	if allowed, err := itemStore.UserCanReadImageURL(ctx, userA, image.ImageURL); err != nil || allowed {
		t.Fatalf("corrupt relationship access = %t, %v; want false", allowed, err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE item_images SET workspace_id = ? WHERE id = ?`, workspaceB, image.ID); err != nil {
		t.Fatalf("restore image workspace fixture: %v", err)
	}
}
