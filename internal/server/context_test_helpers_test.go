package server

import (
	"context"
	"testing"

	"github.com/lehigh-university-libraries/scribe/internal/store"
)

func ensureServerTestDefaultContext(t *testing.T, contexts *store.ContextStore, desired store.Context) {
	t.Helper()
	desired.Name = uniqueName(desired.Name)
	if err := contexts.EnsureDefault(context.Background(), desired); err != nil {
		t.Fatalf("ensure test default context: %v", err)
	}
	created, err := contexts.GetDefault(context.Background())
	if err != nil {
		t.Fatalf("load test default context: %v", err)
	}
	if created.Name != desired.Name {
		return
	}
	t.Cleanup(func() {
		if err := contexts.Delete(context.Background(), created.ID); err != nil {
			t.Errorf("delete test default context %d: %v", created.ID, err)
		}
	})
}
