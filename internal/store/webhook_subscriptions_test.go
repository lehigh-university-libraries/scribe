package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

func TestWebhookSubscriptionsAndDeliveriesAreWorkspaceIsolated(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	workspaceA, imageA := createAnnotationTestResource(t, database, uuid.NewString()+"-webhook-a", "https://source.example/canvas/"+uuid.NewString())
	workspaceB, imageB := createAnnotationTestResource(t, database, uuid.NewString()+"-webhook-b", "https://source.example/canvas/"+uuid.NewString())
	subscriptionA := createWebhookTestSubscription(t, database, workspaceA)
	subscriptionB := createWebhookTestSubscription(t, database, workspaceB)
	repository := store.NewWebhookSubscriptionStore(database)

	listedA, err := repository.List(ctx, workspaceA)
	if err != nil {
		t.Fatalf("list workspace A subscriptions: %v", err)
	}
	if len(listedA) != 1 || listedA[0].ID != subscriptionA.ID || listedA[0].WorkspaceID != workspaceA {
		t.Fatalf("workspace A subscriptions = %+v", listedA)
	}
	if len(subscriptionA.SigningSecret) != 0 || len(listedA[0].SigningSecret) != 0 {
		t.Fatal("write-only signing secret escaped the subscription persistence boundary")
	}
	if err := repository.Delete(ctx, workspaceA, subscriptionB.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-workspace webhook delete error = %v, want sql.ErrNoRows", err)
	}

	events := store.NewTranscriptionJobStore(database)
	eventA := "workspace-a-" + uuid.NewString()
	eventB := "workspace-b-" + uuid.NewString()
	if err := events.EnqueueWebhookEvent(ctx, eventA, "dev.scribe.annotation.updated", fmt.Sprintf("item-images/%d", imageA), `{"id":"a"}`); err != nil {
		t.Fatalf("enqueue workspace A event: %v", err)
	}
	if err := events.EnqueueWebhookEvent(ctx, eventB, "dev.scribe.annotation.updated", fmt.Sprintf("item-images/%d", imageB), `{"id":"b"}`); err != nil {
		t.Fatalf("enqueue workspace B event: %v", err)
	}
	t.Cleanup(func() {
		_, _ = database.ExecContext(context.Background(), `DELETE FROM webhook_deliveries WHERE event_id IN (?, ?)`, eventA, eventB)
		_, _ = database.ExecContext(context.Background(), `DELETE FROM event_outbox WHERE event_id IN (?, ?)`, eventA, eventB)
	})

	rows, err := database.QueryContext(ctx, `
SELECT wd.event_id, wd.subscription_id, subscription.workspace_id
FROM webhook_deliveries wd
JOIN webhook_subscriptions subscription ON subscription.id = wd.subscription_id
WHERE wd.event_id IN (?, ?)
ORDER BY wd.event_id`, eventA, eventB)
	if err != nil {
		t.Fatalf("load workspace webhook deliveries: %v", err)
	}
	defer rows.Close()
	got := make(map[string]struct {
		subscriptionID uint64
		workspaceID    uint64
	})
	for rows.Next() {
		var eventID string
		var value struct {
			subscriptionID uint64
			workspaceID    uint64
		}
		if err := rows.Scan(&eventID, &value.subscriptionID, &value.workspaceID); err != nil {
			t.Fatalf("scan workspace webhook delivery: %v", err)
		}
		got[eventID] = value
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate workspace webhook deliveries: %v", err)
	}
	if len(got) != 2 || got[eventA].subscriptionID != subscriptionA.ID || got[eventA].workspaceID != workspaceA {
		t.Fatalf("workspace A delivery routing = %+v", got[eventA])
	}
	if got[eventB].subscriptionID != subscriptionB.ID || got[eventB].workspaceID != workspaceB {
		t.Fatalf("workspace B delivery routing = %+v", got[eventB])
	}
	if got[eventB].subscriptionID == subscriptionA.ID {
		t.Fatal("workspace A webhook was scheduled for workspace B event")
	}

	if _, err := database.ExecContext(ctx, `UPDATE webhook_deliveries SET status = 'delivered' WHERE event_id = ? AND subscription_id = ?`, eventA, subscriptionA.ID); err != nil {
		t.Fatalf("mark subscription deletion fixture delivered: %v", err)
	}
	if err := repository.Delete(ctx, workspaceA, subscriptionA.ID); err != nil {
		t.Fatalf("delete workspace A subscription: %v", err)
	}
	var subscriptionCount, deliveryCount, eventCount int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM webhook_subscriptions WHERE id = ?`, subscriptionA.ID).Scan(&subscriptionCount); err != nil {
		t.Fatalf("count deleted webhook subscription: %v", err)
	}
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM webhook_deliveries WHERE subscription_id = ?`, subscriptionA.ID).Scan(&deliveryCount); err != nil {
		t.Fatalf("count deleted subscription deliveries: %v", err)
	}
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_outbox WHERE event_id = ?`, eventA).Scan(&eventCount); err != nil {
		t.Fatalf("count retained subscription event: %v", err)
	}
	if subscriptionCount != 0 || deliveryCount != 0 || eventCount != 1 {
		t.Fatalf("subscription child-first delete = subscription %d, deliveries %d, event %d; want 0/0/1", subscriptionCount, deliveryCount, eventCount)
	}
	if violations, err := store.AuditRelationshipIntegrity(ctx, database); err != nil {
		t.Fatalf("audit subscription deletion: %v", err)
	} else if len(violations) != 0 {
		t.Fatalf("subscription deletion left relationship violations: %+v", violations)
	}
	if _, err := database.ExecContext(ctx, `UPDATE event_outbox SET created_at = DATE_SUB(NOW(), INTERVAL 2 HOUR) WHERE event_id = ?`, eventA); err != nil {
		t.Fatalf("age parent event for retention: %v", err)
	}
	if err := events.RetainWebhookEvents(ctx, time.Hour); err != nil {
		t.Fatalf("retain orphaned subscription event: %v", err)
	}
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_outbox WHERE event_id = ?`, eventA).Scan(&eventCount); err != nil {
		t.Fatalf("count retained webhook event: %v", err)
	}
	if eventCount != 0 {
		t.Fatalf("retained webhook event count = %d, want 0", eventCount)
	}
}

func TestWebhookEventExpansionSerializesWithSubscriptionLifecycle(t *testing.T) {
	t.Run("delete", func(t *testing.T) {
		database := annotationTestDB(t)
		ctx := context.Background()
		workspaceID, imageID := createAnnotationTestResource(t, database, uuid.NewString()+"-webhook-delete-race", "https://source.example/canvas/"+uuid.NewString())
		subscription := createWebhookTestSubscription(t, database, workspaceID)
		eventID := "webhook-delete-race-" + uuid.NewString()
		t.Cleanup(func() {
			_, _ = database.ExecContext(context.Background(), `DELETE FROM webhook_deliveries WHERE event_id = ?`, eventID)
			_, _ = database.ExecContext(context.Background(), `DELETE FROM event_outbox WHERE event_id = ?`, eventID)
		})

		blocker, err := database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin workspace lock: %v", err)
		}
		defer func() { _ = blocker.Rollback() }()
		if err := blocker.QueryRowContext(ctx, `SELECT id FROM workspaces WHERE id = ? FOR UPDATE`, workspaceID).Scan(&workspaceID); err != nil {
			t.Fatalf("lock webhook workspace: %v", err)
		}

		repository := store.NewWebhookSubscriptionStore(database)
		events := store.NewTranscriptionJobStore(database)
		results := make(chan webhookLifecycleRaceResult, 2)
		go func() {
			results <- webhookLifecycleRaceResult{operation: "event expansion", err: events.EnqueueWebhookEvent(
				context.Background(), eventID, "dev.scribe.annotation.updated", fmt.Sprintf("item-images/%d", imageID), `{"id":"delete-race"}`,
			)}
		}()
		go func() {
			results <- webhookLifecycleRaceResult{operation: "subscription delete", err: repository.Delete(context.Background(), workspaceID, subscription.ID)}
		}()

		assertWebhookLifecycleBlockedThenRelease(t, blocker, results)

		var subscriptionCount, deliveryCount, eventCount int
		if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM webhook_subscriptions WHERE id = ?`, subscription.ID).Scan(&subscriptionCount); err != nil {
			t.Fatalf("count raced subscription: %v", err)
		}
		if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM webhook_deliveries WHERE event_id = ?`, eventID).Scan(&deliveryCount); err != nil {
			t.Fatalf("count raced deliveries: %v", err)
		}
		if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_outbox WHERE event_id = ?`, eventID).Scan(&eventCount); err != nil {
			t.Fatalf("count raced event: %v", err)
		}
		if subscriptionCount != 0 || deliveryCount != 0 || eventCount != 1 {
			t.Fatalf("event/delete race = subscription %d, deliveries %d, event %d; want 0/0/1", subscriptionCount, deliveryCount, eventCount)
		}
		assertWebhookRelationshipsClean(t, database)
	})

	t.Run("create", func(t *testing.T) {
		database := annotationTestDB(t)
		ctx := context.Background()
		workspaceID, imageID := createAnnotationTestResource(t, database, uuid.NewString()+"-webhook-create-race", "https://source.example/canvas/"+uuid.NewString())
		eventID := "webhook-create-race-" + uuid.NewString()
		t.Cleanup(func() {
			_, _ = database.ExecContext(context.Background(), `DELETE FROM webhook_deliveries WHERE event_id = ?`, eventID)
			_, _ = database.ExecContext(context.Background(), `DELETE FROM event_outbox WHERE event_id = ?`, eventID)
		})

		blocker, err := database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin workspace lock: %v", err)
		}
		defer func() { _ = blocker.Rollback() }()
		if err := blocker.QueryRowContext(ctx, `SELECT id FROM workspaces WHERE id = ? FOR UPDATE`, workspaceID).Scan(&workspaceID); err != nil {
			t.Fatalf("lock webhook workspace: %v", err)
		}

		repository := store.NewWebhookSubscriptionStore(database)
		events := store.NewTranscriptionJobStore(database)
		results := make(chan webhookLifecycleRaceResult, 2)
		go func() {
			results <- webhookLifecycleRaceResult{operation: "event expansion", err: events.EnqueueWebhookEvent(
				context.Background(), eventID, "dev.scribe.annotation.updated", fmt.Sprintf("item-images/%d", imageID), `{"id":"create-race"}`,
			)}
		}()
		go func() {
			created, createErr := repository.Create(
				context.Background(), workspaceID, "https://webhook.example/"+uuid.NewString(), "01234567890123456789012345678901",
			)
			results <- webhookLifecycleRaceResult{operation: "subscription create", subscription: created, err: createErr}
		}()

		completed := assertWebhookLifecycleBlockedThenRelease(t, blocker, results)
		var subscription store.WebhookSubscription
		for _, result := range completed {
			if result.operation == "subscription create" {
				subscription = result.subscription
			}
		}
		if subscription.ID == 0 {
			t.Fatal("concurrent subscription create returned no identity")
		}
		t.Cleanup(func() { _ = repository.Delete(context.Background(), workspaceID, subscription.ID) })
		var deliveryCount int
		if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM webhook_deliveries WHERE event_id = ? AND subscription_id = ?`, eventID, subscription.ID).Scan(&deliveryCount); err != nil {
			t.Fatalf("count create-race deliveries: %v", err)
		}
		// Zero means the event committed first; one means the subscription
		// committed first. Either serialized order is valid, but no other result is.
		if deliveryCount > 1 {
			t.Fatalf("create-race delivery count = %d, want 0 or 1", deliveryCount)
		}
		assertWebhookRelationshipsClean(t, database)
	})
}

type webhookLifecycleRaceResult struct {
	operation    string
	subscription store.WebhookSubscription
	err          error
}

func assertWebhookLifecycleBlockedThenRelease(t *testing.T, blocker *sql.Tx, results <-chan webhookLifecycleRaceResult) []webhookLifecycleRaceResult {
	t.Helper()
	var early *webhookLifecycleRaceResult
	select {
	case result := <-results:
		early = &result
	case <-time.After(150 * time.Millisecond):
	}
	if err := blocker.Commit(); err != nil {
		t.Fatalf("release webhook workspace lock: %v", err)
	}

	completed := make([]webhookLifecycleRaceResult, 0, 2)
	if early != nil {
		completed = append(completed, *early)
	}
	for len(completed) < 2 {
		select {
		case result := <-results:
			completed = append(completed, result)
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for serialized webhook lifecycle operations")
		}
	}
	if early != nil {
		t.Fatalf("%s bypassed the owning workspace lock", early.operation)
	}
	for _, result := range completed {
		if result.err != nil {
			t.Fatalf("%s after workspace release: %v", result.operation, result.err)
		}
	}
	return completed
}

func assertWebhookRelationshipsClean(t *testing.T, database *sql.DB) {
	t.Helper()
	violations, err := store.AuditRelationshipIntegrity(context.Background(), database)
	if err != nil {
		t.Fatalf("audit webhook relationships: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("webhook relationship violations: %+v", violations)
	}
}
