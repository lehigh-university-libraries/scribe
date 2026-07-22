package uploadblob

import (
	"context"
	"errors"
	"testing"

	"cloud.google.com/go/storage"
	"google.golang.org/api/option"
)

func TestGetClientRetriesAfterCanceledInitialization(t *testing.T) {
	clientMu.Lock()
	previousClient := client
	previousFactory := newStorageClient
	client = nil
	calls := 0
	newStorageClient = func(ctx context.Context, opts ...option.ClientOption) (*storage.Client, error) {
		calls++
		if calls == 1 {
			return nil, context.Canceled
		}
		return storage.NewClient(ctx, option.WithoutAuthentication())
	}
	clientMu.Unlock()
	t.Cleanup(func() {
		_ = Close()
		clientMu.Lock()
		client = previousClient
		newStorageClient = previousFactory
		clientMu.Unlock()
	})

	if _, err := getClient(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("first getClient error = %v, want context canceled", err)
	}
	got, err := getClient(context.Background())
	if err != nil {
		t.Fatalf("second getClient: %v", err)
	}
	if got == nil || calls != 2 {
		t.Fatalf("second getClient = %v after %d calls, want initialized client after 2 calls", got, calls)
	}
	if again, err := getClient(context.Background()); err != nil || again != got || calls != 2 {
		t.Fatalf("cached successful client = %v, %v after %d calls", again, err, calls)
	}
}
