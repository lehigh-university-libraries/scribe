package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

func TestEventSubjectResolutionFailsClosedAndGlobalEventsAreExplicit(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	workspaceID, imageID := createAnnotationTestResource(
		t,
		database,
		uuid.NewString()+"-nested-event",
		"https://source.example/canvas/"+uuid.NewString(),
	)
	events := store.NewTranscriptionJobStore(database)

	nestedEventID := "nested-event-" + uuid.NewString()
	nestedSubject := fmt.Sprintf("item-images/%d/annotations/line-1", imageID)
	if err := events.EnqueueWebhookEvent(ctx, nestedEventID, "dev.scribe.annotation.updated", nestedSubject, `{}`); err != nil {
		t.Fatalf("enqueue nested event: %v", err)
	}
	malformedEventID := "malformed-event-" + uuid.NewString()
	if err := events.EnqueueWebhookEvent(ctx, malformedEventID, "dev.scribe.system.poison", "system/poison", `{}`); err == nil {
		t.Fatal("malformed tenant event subject was accepted")
	}
	missingEventID := "missing-event-" + uuid.NewString()
	if err := events.EnqueueWebhookEvent(ctx, missingEventID, "dev.scribe.annotation.updated", "item-images/18446744073709551615", `{}`); err == nil {
		t.Fatal("missing item image event subject was accepted")
	}
	globalEventID := "global-event-" + uuid.NewString()
	if err := events.EnqueueSystemWebhookEvent(ctx, globalEventID, store.SystemWebhookEventMaintenance, `{}`); err != nil {
		t.Fatalf("enqueue explicit global event: %v", err)
	}
	if err := events.EnqueueSystemWebhookEvent(ctx, "forbidden-"+uuid.NewString(), store.SystemWebhookEventType("dev.scribe.system.poison"), `{}`); err == nil {
		t.Fatal("unallowlisted global event type was accepted")
	}
	t.Cleanup(func() {
		_, _ = database.ExecContext(context.Background(), `DELETE FROM event_outbox WHERE event_id IN (?, ?, ?, ?)`, nestedEventID, malformedEventID, missingEventID, globalEventID)
	})

	var nestedWorkspace sql.NullInt64
	if err := database.QueryRowContext(ctx, `SELECT workspace_id FROM event_outbox WHERE event_id = ?`, nestedEventID).Scan(&nestedWorkspace); err != nil {
		t.Fatalf("load nested event workspace: %v", err)
	}
	if !nestedWorkspace.Valid || uint64(nestedWorkspace.Int64) != workspaceID {
		t.Fatalf("nested event workspace = %+v, want %d", nestedWorkspace, workspaceID)
	}

	var rejectedCount int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_outbox WHERE event_id IN (?, ?)`, malformedEventID, missingEventID).Scan(&rejectedCount); err != nil {
		t.Fatalf("count rejected tenant events: %v", err)
	}
	if rejectedCount != 0 {
		t.Fatalf("rejected tenant events persisted %d rows", rejectedCount)
	}
	var globalWorkspace sql.NullInt64
	if err := database.QueryRowContext(ctx, `SELECT workspace_id FROM event_outbox WHERE event_id = ?`, globalEventID).Scan(&globalWorkspace); err != nil {
		t.Fatalf("load explicit global event workspace: %v", err)
	}
	if globalWorkspace.Valid {
		t.Fatalf("explicit global event workspace = %+v, want NULL", globalWorkspace)
	}
}

func TestWebhookRetentionBatchesDeletesAndKeepsPendingUntilOverallCutoff(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	workspaceID, imageID := createAnnotationTestResource(t, database, uuid.NewString()+"-retention", "https://source.example/canvas/"+uuid.NewString())
	subscription := createWebhookTestSubscription(t, database, workspaceID)
	prefix := "retention-" + uuid.NewString()
	t.Cleanup(func() {
		_, _ = database.ExecContext(context.Background(), `DELETE FROM webhook_deliveries WHERE event_id LIKE ?`, prefix+"%")
		_, _ = database.ExecContext(context.Background(), `DELETE FROM event_outbox WHERE event_id LIKE ?`, prefix+"%")
	})

	old := time.Now().UTC().Add(-2 * time.Hour)
	recent := time.Now().UTC()
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin retention fixture: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	insertEvent, err := tx.PrepareContext(ctx, `INSERT INTO event_outbox (event_id, event_type, workspace_id, subject, body_json, created_at) VALUES (?, 'dev.scribe.test', ?, ?, '{}', ?)`)
	if err != nil {
		t.Fatalf("prepare event fixture: %v", err)
	}
	defer func() { _ = insertEvent.Close() }()
	insertDelivery, err := tx.PrepareContext(ctx, `INSERT INTO webhook_deliveries (event_id, subscription_id, status, updated_at) VALUES (?, ?, ?, ?)`)
	if err != nil {
		t.Fatalf("prepare delivery fixture: %v", err)
	}
	defer func() { _ = insertDelivery.Close() }()

	const deliveredCount = 1001
	for index := 0; index < deliveredCount; index++ {
		eventID := fmt.Sprintf("%s-delivered-%04d", prefix, index)
		if _, err := insertEvent.ExecContext(ctx, eventID, workspaceID, fmt.Sprintf("item-images/%d", imageID), old); err != nil {
			t.Fatalf("insert delivered event %d: %v", index, err)
		}
		if _, err := insertDelivery.ExecContext(ctx, eventID, subscription.ID, "delivered", old); err != nil {
			t.Fatalf("insert delivered delivery %d: %v", index, err)
		}
	}
	pendingRecentID := prefix + "-pending-recent"
	if _, err := insertEvent.ExecContext(ctx, pendingRecentID, workspaceID, fmt.Sprintf("item-images/%d", imageID), recent); err != nil {
		t.Fatalf("insert recent pending event: %v", err)
	}
	if _, err := insertDelivery.ExecContext(ctx, pendingRecentID, subscription.ID, "pending", old); err != nil {
		t.Fatalf("insert recent pending delivery: %v", err)
	}
	pendingExpiredID := prefix + "-pending-expired"
	if _, err := insertEvent.ExecContext(ctx, pendingExpiredID, workspaceID, fmt.Sprintf("item-images/%d", imageID), old); err != nil {
		t.Fatalf("insert expired pending event: %v", err)
	}
	if _, err := insertDelivery.ExecContext(ctx, pendingExpiredID, subscription.ID, "pending", old); err != nil {
		t.Fatalf("insert expired pending delivery: %v", err)
	}
	failedExpiredID := prefix + "-failed-expired"
	if _, err := insertEvent.ExecContext(ctx, failedExpiredID, workspaceID, fmt.Sprintf("item-images/%d", imageID), old); err != nil {
		t.Fatalf("insert expired failed event: %v", err)
	}
	if _, err := insertDelivery.ExecContext(ctx, failedExpiredID, subscription.ID, "failed", old); err != nil {
		t.Fatalf("insert expired failed delivery: %v", err)
	}
	processingExpiredID := prefix + "-processing-expired"
	if _, err := insertEvent.ExecContext(ctx, processingExpiredID, workspaceID, fmt.Sprintf("item-images/%d", imageID), old); err != nil {
		t.Fatalf("insert expired processing event: %v", err)
	}
	if _, err := insertDelivery.ExecContext(ctx, processingExpiredID, subscription.ID, "processing", old); err != nil {
		t.Fatalf("insert expired processing delivery: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit retention fixture: %v", err)
	}

	if err := store.NewTranscriptionJobStore(database).RetainWebhookEvents(ctx, time.Hour); err != nil {
		t.Fatalf("retain webhook events: %v", err)
	}

	var deliveredRemaining int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_outbox WHERE event_id LIKE ?`, prefix+"-delivered-%").Scan(&deliveredRemaining); err != nil {
		t.Fatalf("count retained delivered events: %v", err)
	}
	if deliveredRemaining != 0 {
		t.Fatalf("retained delivered events = %d, want 0", deliveredRemaining)
	}
	var recentPendingRemaining int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM webhook_deliveries WHERE event_id = ? AND status = 'pending'`, pendingRecentID).Scan(&recentPendingRemaining); err != nil {
		t.Fatalf("count recent pending delivery: %v", err)
	}
	if recentPendingRemaining != 1 {
		t.Fatalf("recent pending deliveries = %d, want 1", recentPendingRemaining)
	}
	var expiredPendingRemaining int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_outbox WHERE event_id = ?`, pendingExpiredID).Scan(&expiredPendingRemaining); err != nil {
		t.Fatalf("count expired pending event: %v", err)
	}
	if expiredPendingRemaining != 0 {
		t.Fatalf("expired pending events = %d, want 0 after overall cutoff", expiredPendingRemaining)
	}
	var otherExpiredRemaining int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_outbox WHERE event_id IN (?, ?)`, failedExpiredID, processingExpiredID).Scan(&otherExpiredRemaining); err != nil {
		t.Fatalf("count other expired event states: %v", err)
	}
	if otherExpiredRemaining != 0 {
		t.Fatalf("expired failed/processing events = %d, want 0", otherExpiredRemaining)
	}
}
