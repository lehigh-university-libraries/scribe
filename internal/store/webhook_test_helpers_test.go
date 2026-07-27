package store_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

func createWebhookTestSubscription(t *testing.T, database *sql.DB, workspaceID uint64) store.WebhookSubscription {
	t.Helper()
	repository := store.NewWebhookSubscriptionStore(database)
	subscription, err := repository.Create(
		context.Background(),
		workspaceID,
		"https://webhook.example/"+uuid.NewString(),
		strings.Repeat("s", store.MinWebhookSigningSecretBytes),
	)
	if err != nil {
		t.Fatalf("create webhook subscription: %v", err)
	}
	t.Cleanup(func() {
		_ = repository.Delete(context.Background(), workspaceID, subscription.ID)
	})
	return subscription
}
