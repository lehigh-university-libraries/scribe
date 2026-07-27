package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

func TestRepositoriesOwnRelationshipAdmissionAndIntegrityAudit(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	workspaceA, imageA := createAnnotationTestResource(t, database, suffix+"-graph-a", "https://source.example/canvas/"+suffix+"/a")
	workspaceB, imageB := createAnnotationTestResource(t, database, suffix+"-graph-b", "https://source.example/canvas/"+suffix+"/b")
	subscriptionA := createWebhookTestSubscription(t, database, workspaceA)
	subscriptionB := createWebhookTestSubscription(t, database, workspaceB)

	var foreignKeys, triggers int
	if err := database.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM information_schema.referential_constraints
WHERE constraint_schema = DATABASE()`).Scan(&foreignKeys); err != nil {
		t.Fatalf("count schema foreign keys: %v", err)
	}
	if err := database.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM information_schema.triggers
WHERE trigger_schema = DATABASE()`).Scan(&triggers); err != nil {
		t.Fatalf("count schema triggers: %v", err)
	}
	if foreignKeys != 0 || triggers != 0 {
		t.Fatalf("database relationship enforcement = %d foreign keys, %d triggers; want repositories only", foreignKeys, triggers)
	}

	var ownerA uint64
	if err := database.QueryRowContext(ctx, `SELECT owner_user_id FROM workspaces WHERE id = ?`, workspaceA).Scan(&ownerA); err != nil {
		t.Fatalf("load workspace owner: %v", err)
	}

	annotationStore := store.NewAnnotationStore(database)
	foreignPage := canonicalTestPage(t, workspaceA, imageB, "https://source.example/canvas/"+suffix+"/foreign", "forbidden")
	if _, err := annotationStore.SavePage(ctx, foreignPage, 0); err == nil {
		t.Fatal("annotation repository accepted another workspace's image")
	}

	providerAudits := store.NewProviderCallAuditStore(database)
	if err := providerAudits.Create(ctx, store.ProviderCallAudit{
		WorkspaceID: workspaceA,
		ItemImageID: &imageB,
		Provider:    "tesseract",
		Model:       "tesseract",
		Operation:   "transcribe",
	}); err == nil || !strings.Contains(err.Error(), "item image") {
		t.Fatalf("cross-workspace provider audit error = %v", err)
	}

	providerSecrets := store.NewProviderSecretStore(database)
	if _, err := providerSecrets.Create(ctx, store.ProviderSecret{
		UserID:      &ownerA,
		WorkspaceID: workspaceB,
		Provider:    "openai",
		Name:        "foreign member",
		VaultPath:   "scribe/test/relationship-integrity/" + suffix,
	}); err == nil || !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-workspace provider secret error = %v, want membership rejection", err)
	}

	contexts := store.NewContextStore(database)
	if _, err := contexts.Create(ctx, store.Context{
		UserID:                &ownerA,
		WorkspaceID:           &workspaceB,
		Name:                  "foreign context " + suffix,
		SegmentationModel:     "tesseract",
		TranscriptionProvider: "tesseract",
		TranscriptionModel:    "tesseract",
	}); err == nil || !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-workspace context error = %v, want membership rejection", err)
	}

	if violations, err := store.AuditRelationshipIntegrity(ctx, database); err != nil {
		t.Fatalf("audit clean resource graph: %v", err)
	} else if len(violations) != 0 {
		t.Fatalf("clean resource graph violations = %+v", violations)
	}

	// Raw SQL is deliberately able to create bad relationships: it is useful
	// for restore testing and proves the release audit catches drift independently
	// of the repository code that normally prevents it.
	pageID := "https://scribe.example/v1/relationship-integrity/" + suffix
	if _, err := database.ExecContext(ctx, `
INSERT INTO annotation_pages (workspace_id, item_image_id, page_id, canvas_uri, payload, revision)
VALUES (?, ?, ?, ?, ?, 1)`, workspaceA, imageB, pageID,
		"https://source.example/canvas/"+suffix+"/corrupt",
		fmt.Sprintf(`{"id":%q,"type":"AnnotationPage","items":[]}`, pageID)); err != nil {
		t.Fatalf("inject cross-tenant audit fixture: %v", err)
	}
	t.Cleanup(func() {
		_, _ = database.Exec(`DELETE FROM annotation_pages WHERE workspace_id = ? AND item_image_id = ?`, workspaceA, imageB)
	})

	violations, err := store.AuditRelationshipIntegrity(ctx, database)
	if err != nil {
		t.Fatalf("audit corrupt resource graph: %v", err)
	}
	if !relationshipViolationContains(violations, "annotation_pages.image", 1) {
		t.Fatalf("corrupt resource graph violations = %+v, want annotation_pages.image", violations)
	}
	if _, err := database.ExecContext(ctx, `DELETE FROM annotation_pages WHERE workspace_id = ? AND item_image_id = ?`, workspaceA, imageB); err != nil {
		t.Fatalf("repair cross-tenant audit fixture: %v", err)
	}
	if violations, err := store.AuditRelationshipIntegrity(ctx, database); err != nil {
		t.Fatalf("audit repaired resource graph: %v", err)
	} else if len(violations) != 0 {
		t.Fatalf("repaired resource graph violations = %+v", violations)
	}

	eventID := "relationship-integrity-" + suffix
	if err := store.NewTranscriptionJobStore(database).EnqueueWebhookEvent(
		ctx,
		eventID,
		"dev.scribe.annotation.updated",
		fmt.Sprintf("item-images/%d", imageA),
		`{"id":"relationship-integrity"}`,
	); err != nil {
		t.Fatalf("enqueue webhook relationship fixture: %v", err)
	}
	t.Cleanup(func() {
		_, _ = database.Exec(`DELETE FROM webhook_deliveries WHERE event_id = ?`, eventID)
		_, _ = database.Exec(`DELETE FROM event_outbox WHERE event_id = ?`, eventID)
	})
	if _, err := database.ExecContext(ctx, `UPDATE webhook_deliveries SET subscription_id = ? WHERE event_id = ? AND subscription_id = ?`, subscriptionB.ID, eventID, subscriptionA.ID); err != nil {
		t.Fatalf("inject cross-tenant webhook relationship: %v", err)
	}
	if violations, err := store.AuditRelationshipIntegrity(ctx, database); err != nil {
		t.Fatalf("audit cross-tenant webhook relationship: %v", err)
	} else if !relationshipViolationContains(violations, "webhook_deliveries.event_subscription_workspace", 1) {
		t.Fatalf("cross-tenant webhook relationship violations = %+v", violations)
	}
	if _, err := database.ExecContext(ctx, `UPDATE webhook_deliveries SET subscription_id = ? WHERE event_id = ? AND subscription_id = ?`, subscriptionA.ID, eventID, subscriptionB.ID); err != nil {
		t.Fatalf("repair cross-tenant webhook relationship: %v", err)
	}
	var missingSubscriptionID uint64
	if err := database.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) + 1 FROM webhook_subscriptions`).Scan(&missingSubscriptionID); err != nil {
		t.Fatalf("choose missing webhook subscription identity: %v", err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE webhook_deliveries SET subscription_id = ? WHERE event_id = ? AND subscription_id = ?`, missingSubscriptionID, eventID, subscriptionA.ID); err != nil {
		t.Fatalf("inject missing webhook subscription relationship: %v", err)
	}
	if violations, err := store.AuditRelationshipIntegrity(ctx, database); err != nil {
		t.Fatalf("audit missing webhook subscription relationship: %v", err)
	} else if !relationshipViolationContains(violations, "webhook_deliveries.subscription", 1) {
		t.Fatalf("missing webhook subscription relationship violations = %+v", violations)
	}
	if _, err := database.ExecContext(ctx, `UPDATE webhook_deliveries SET subscription_id = ? WHERE event_id = ? AND subscription_id = ?`, subscriptionA.ID, eventID, missingSubscriptionID); err != nil {
		t.Fatalf("repair missing webhook subscription relationship: %v", err)
	}
	if violations, err := store.AuditRelationshipIntegrity(ctx, database); err != nil {
		t.Fatalf("audit repaired webhook relationship: %v", err)
	} else if len(violations) != 0 {
		t.Fatalf("repaired webhook graph violations = %+v", violations)
	}

	// Keep imageA live until fixture cleanup; this makes accidental graph-wide
	// deletion during a rejected admission visible to the test.
	if _, err := store.NewItemStore(database).GetImageForWorkspace(ctx, imageA, workspaceA); err != nil {
		t.Fatalf("rejected relationship admission mutated the owning workspace: %v", err)
	}
}

func relationshipViolationContains(violations []store.RelationshipIntegrityViolation, relationship string, minimum uint64) bool {
	for _, violation := range violations {
		if violation.Relationship == relationship && violation.Count >= minimum {
			return true
		}
	}
	return false
}
