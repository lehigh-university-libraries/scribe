package server

import (
	"context"
	"database/sql"
	"errors"
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

func TestProviderCallAuditRetentionDeletesOldRows(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	audits := store.NewProviderCallAuditStore(db)

	oldSession := uniqueName("old-audit")
	newSession := uniqueName("new-audit")
	if err := audits.Create(ctx, store.ProviderCallAudit{
		SessionID: oldSession,
		Provider:  "gemini",
		Model:     "gemini-test",
		Operation: "transcribe",
		Prompt:    "old prompt",
	}); err != nil {
		t.Fatalf("create old audit: %v", err)
	}
	if err := audits.Create(ctx, store.ProviderCallAudit{
		SessionID: newSession,
		Provider:  "gemini",
		Model:     "gemini-test",
		Operation: "transcribe",
		Prompt:    "new prompt",
	}); err != nil {
		t.Fatalf("create new audit: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM provider_call_audits WHERE session_id IN (?, ?)`, oldSession, newSession)
	})
	if _, err := db.Exec(`UPDATE provider_call_audits SET created_at = ? WHERE session_id = ?`, time.Now().UTC().Add(-48*time.Hour), oldSession); err != nil {
		t.Fatalf("age old audit: %v", err)
	}

	if err := audits.Retain(ctx, 24*time.Hour); err != nil {
		t.Fatalf("Retain() error = %v", err)
	}

	var oldCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM provider_call_audits WHERE session_id = ?`, oldSession).Scan(&oldCount); err != nil {
		t.Fatalf("count old audit: %v", err)
	}
	if oldCount != 0 {
		t.Fatalf("old audit count = %d, want 0", oldCount)
	}
	var newCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM provider_call_audits WHERE session_id = ?`, newSession).Scan(&newCount); err != nil {
		t.Fatalf("count new audit: %v", err)
	}
	if newCount != 1 {
		t.Fatalf("new audit count = %d, want 1", newCount)
	}
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

func TestResumedTranscriptionProgressUsesPersistedCursor(t *testing.T) {
	t.Parallel()

	completed, failed, startIndex := resumedTranscriptionProgress(&store.TranscriptionJob{
		CompletedSegments: 2,
		FailedSegments:    1,
	}, 5)
	if completed != 2 || failed != 1 || startIndex != 3 {
		t.Fatalf("completed=%d failed=%d startIndex=%d, want 2/1/3", completed, failed, startIndex)
	}
}

func TestResumedTranscriptionProgressClampsToTotal(t *testing.T) {
	t.Parallel()

	completed, failed, startIndex := resumedTranscriptionProgress(&store.TranscriptionJob{
		CompletedSegments: 9,
		FailedSegments:    4,
	}, 5)
	if completed != 5 || failed != 0 || startIndex != 5 {
		t.Fatalf("completed=%d failed=%d startIndex=%d, want 5/0/5", completed, failed, startIndex)
	}
}

func TestSeedTranscriptionJobOCRRunUsesActualCompletedCount(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	itemStore := store.NewItemStore(db)
	contextStore := store.NewContextStore(db)
	annotationStore := store.NewAnnotationStore(db)
	ocrRunStore := store.NewOCRRunStore(db)
	handler := &Handler{
		annotations: annotationStore,
		ocrRuns:     ocrRunStore,
	}

	userID := createTestUser(t, db, uniqueName("seed-user"))
	workspaceID := createTestWorkspace(t, db, userID, uniqueName("seed-workspace"))
	contextRow, err := contextStore.Create(ctx, store.Context{
		Name:                  uniqueName("seed-context"),
		SegmentationModel:     "scribe",
		TranscriptionProvider: "ollama",
		TranscriptionModel:    "seed-model",
	})
	if err != nil {
		t.Fatalf("create context: %v", err)
	}
	t.Cleanup(func() {
		_ = contextStore.Delete(context.Background(), contextRow.ID)
	})

	item, err := itemStore.Create(ctx, dbstore.CreateItemParams{
		ID:          uniqueName("seed-item"),
		UserID:      userID,
		WorkspaceID: workspaceID,
		Name:        "Seed Item",
		SourceType:  "upload",
	})
	if err != nil {
		t.Fatalf("create item: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM item_images WHERE item_id = ?`, item.ID)
		_ = itemStore.Delete(context.Background(), item.ID)
	})

	createImageWithAnnotation := func(t *testing.T, sequence int) store.ItemImage {
		t.Helper()
		image, err := itemStore.AddImage(ctx, dbstore.CreateItemImageParams{
			ItemID:   item.ID,
			Sequence: uint32(sequence),
			ImageURL: fmt.Sprintf("https://example.test/seed-%d.jpg", sequence),
		})
		if err != nil {
			t.Fatalf("add image %d: %v", sequence, err)
		}
		annotationID := fmt.Sprintf("%s/annotation/line-%d", image.CanvasURI, sequence)
		payload := fmt.Sprintf(`{
			"id":%q,
			"type":"Annotation",
			"motivation":"supplementing",
			"textGranularity":"line",
			"body":[{"type":"TextualBody","value":"Seeded text %d","format":"text/plain"}],
			"target":{"source":{"id":%q,"type":"Canvas"},"selector":{"type":"FragmentSelector","value":"xywh=10,20,100,30"}}
		}`, annotationID, sequence, image.CanvasURI)
		if err := annotationStore.Upsert(ctx, annotationID, image.CanvasURI, payload); err != nil {
			t.Fatalf("upsert annotation %d: %v", sequence, err)
		}
		t.Cleanup(func() {
			_ = annotationStore.Delete(context.Background(), annotationID)
		})
		return image
	}

	completedImage := createImageWithAnnotation(t, 1)
	job := &store.TranscriptionJob{
		ID:                uint64(time.Now().UnixNano()),
		ItemImageID:       completedImage.ID,
		CompletedSegments: 0,
	}
	if err := handler.seedTranscriptionJobOCRRun(ctx, job, contextRow, completedImage, 1); err != nil {
		t.Fatalf("seed completed OCR run: %v", err)
	}
	run, err := ocrRunStore.GetByItemImageID(ctx, completedImage.ID)
	if err != nil {
		t.Fatalf("get seeded OCR run: %v", err)
	}
	if run.OriginalText != "Seeded text 1" {
		t.Fatalf("OriginalText = %q, want %q", run.OriginalText, "Seeded text 1")
	}

	emptyImage := createImageWithAnnotation(t, 2)
	emptyJob := &store.TranscriptionJob{
		ID:                uint64(time.Now().UnixNano() + 1),
		ItemImageID:       emptyImage.ID,
		CompletedSegments: 1,
	}
	if err := handler.seedTranscriptionJobOCRRun(ctx, emptyJob, contextRow, emptyImage, 0); err != nil {
		t.Fatalf("seed empty OCR run: %v", err)
	}
	if _, err := ocrRunStore.GetByItemImageID(ctx, emptyImage.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("empty job OCR run error = %v, want sql.ErrNoRows", err)
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
