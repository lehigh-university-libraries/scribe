package store_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

func TestItemListPageUsesStableWorkspaceScopedKeysets(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	workspaceID, helperImageID := createAnnotationTestResource(t, database, suffix+"-item-page", "https://source.example/canvas/page")
	otherWorkspaceID, otherHelperImageID := createAnnotationTestResource(t, database, suffix+"-item-page-other", "https://source.example/canvas/page-other")
	t.Cleanup(func() {
		_, _ = database.Exec("DELETE FROM item_images WHERE workspace_id IN (?, ?)", workspaceID, otherWorkspaceID)
		_, _ = database.Exec("DELETE FROM items WHERE workspace_id IN (?, ?)", workspaceID, otherWorkspaceID)
	})
	itemStore := store.NewItemStore(database)
	for fixtureWorkspace, fixtureImage := range map[uint64]uint64{workspaceID: helperImageID, otherWorkspaceID: otherHelperImageID} {
		var fixtureItem string
		if err := database.QueryRow(`SELECT item_id FROM item_images WHERE id = ?`, fixtureImage).Scan(&fixtureItem); err != nil {
			t.Fatalf("load helper item: %v", err)
		}
		if err := itemStore.DeleteForWorkspace(ctx, fixtureItem, fixtureWorkspace); err != nil {
			t.Fatalf("remove helper item: %v", err)
		}
	}

	var userID uint64
	if err := database.QueryRow("SELECT owner_user_id FROM workspaces WHERE id = ?", workspaceID).Scan(&userID); err != nil {
		t.Fatalf("load workspace owner: %v", err)
	}
	var otherUserID uint64
	if err := database.QueryRow("SELECT owner_user_id FROM workspaces WHERE id = ?", otherWorkspaceID).Scan(&otherUserID); err != nil {
		t.Fatalf("load other workspace owner: %v", err)
	}
	createdAt := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	itemIDs := []string{"page-c-" + suffix, "page-b-" + suffix, "page-a-" + suffix}
	externalReferenceIDs := []string{"islandora:PID-C-" + suffix, "islandora:PID-B-" + suffix, "islandora:PID-A-" + suffix}
	for index, itemID := range itemIDs {
		if _, err := database.Exec(`INSERT INTO items
  (id, user_id, workspace_id, name, source_type, external_reference_id, created_at, updated_at)
VALUES (?, ?, ?, ?, 'upload', ?, ?, ?)`, itemID, userID, workspaceID, itemID, externalReferenceIDs[index], createdAt, createdAt); err != nil {
			t.Fatalf("insert item %s: %v", itemID, err)
		}
		imageCount := 1
		if index == 0 {
			imageCount = 250
		}
		for sequence := 0; sequence < imageCount; sequence++ {
			if _, err := database.Exec(`INSERT INTO item_images
  (workspace_id, item_id, sequence, image_url, label)
VALUES (?, ?, ?, ?, ?)`, workspaceID, itemID, sequence, fmt.Sprintf("https://images.example/%s/%d.jpg", itemID, sequence), fmt.Sprintf("Page %d", sequence+1)); err != nil {
				t.Fatalf("insert image %d for %s: %v", sequence, itemID, err)
			}
		}
	}
	if _, err := database.Exec(`INSERT INTO items
  (id, user_id, workspace_id, name, source_type, external_reference_id, created_at, updated_at)
VALUES (?, ?, ?, 'other workspace', 'upload', ?, ?, ?)`, "page-z-"+suffix, otherUserID, otherWorkspaceID, externalReferenceIDs[1], createdAt, createdAt); err != nil {
		t.Fatalf("insert other workspace item: %v", err)
	}

	first, err := itemStore.ListPage(ctx, workspaceID, 2, "", nil)
	if err != nil {
		t.Fatalf("ListPage(first): %v", err)
	}
	if got := itemPageIDs(first.Items); len(got) != 2 || got[0] != itemIDs[0] || got[1] != itemIDs[1] {
		t.Fatalf("first page ids = %v, want %v", got, itemIDs[:2])
	}
	if first.Items[0].ImageCount != 250 || first.Items[0].PreviewImage == nil || first.Items[0].PreviewImage.Sequence != 0 {
		t.Fatalf("first summary image data = count %d preview %+v", first.Items[0].ImageCount, first.Items[0].PreviewImage)
	}
	if first.Items[1].ImageCount != 1 || first.Items[1].PreviewImage == nil {
		t.Fatalf("second summary image data = count %d preview %+v", first.Items[1].ImageCount, first.Items[1].PreviewImage)
	}
	complete, err := itemStore.GetForWorkspace(ctx, itemIDs[0], workspaceID)
	if err != nil || len(complete.Images) != 250 {
		t.Fatalf("GetForWorkspace complete images = %d/%v, want 250", len(complete.Images), err)
	}
	if first.NextCursor == nil || first.NextCursor.ID != itemIDs[1] {
		t.Fatalf("first next cursor = %+v, want %s", first.NextCursor, itemIDs[1])
	}

	second, err := itemStore.ListPage(ctx, workspaceID, 2, "", first.NextCursor)
	if err != nil {
		t.Fatalf("ListPage(second): %v", err)
	}
	if got := itemPageIDs(second.Items); len(got) != 1 || got[0] != itemIDs[2] {
		t.Fatalf("second page ids = %v, want [%s]", got, itemIDs[2])
	}
	if second.NextCursor != nil {
		t.Fatalf("last page next cursor = %+v, want nil", second.NextCursor)
	}
	filtered, err := itemStore.ListPage(ctx, workspaceID, 10, itemIDs[2], nil)
	if err != nil || len(filtered.Items) != 1 || filtered.Items[0].ID != itemIDs[2] {
		t.Fatalf("filtered page = %+v/%v, want only %s", filtered.Items, err, itemIDs[2])
	}
	correlated, err := itemStore.ListPage(ctx, workspaceID, 10, "pid-b-"+suffix, nil)
	if err != nil || len(correlated.Items) != 1 || correlated.Items[0].ID != itemIDs[1] || correlated.Items[0].ExternalReferenceID != externalReferenceIDs[1] {
		t.Fatalf("external-reference page = %+v/%v, want only %s", correlated.Items, err, itemIDs[1])
	}
	if _, err := database.Exec("UPDATE items SET name = 'literal 100%_ match' WHERE id = ?", itemIDs[0]); err != nil {
		t.Fatalf("set literal wildcard name: %v", err)
	}
	literal, err := itemStore.ListPage(ctx, workspaceID, 10, "%_", nil)
	if err != nil || len(literal.Items) != 1 || literal.Items[0].ID != itemIDs[0] {
		t.Fatalf("literal wildcard page = %+v/%v, want only %s", literal.Items, err, itemIDs[0])
	}
	if _, err := itemStore.ListPage(ctx, workspaceID, 0, "", nil); !errors.Is(err, store.ErrInvalidItemPage) {
		t.Fatalf("zero-sized store page error = %v, want ErrInvalidItemPage", err)
	}
}

func itemPageIDs(items []store.ItemSummary) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids
}
