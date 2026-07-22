package worklimit

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestHierarchicalLimiterWaitsAtomicallyAndHonorsCancellation(t *testing.T) {
	limiter, err := NewHierarchical(2, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	releaseA, err := limiter.Acquire(context.Background(), 1, "provider-a")
	if err != nil {
		t.Fatal(err)
	}
	releaseB, err := limiter.Acquire(context.Background(), 2, "provider-b")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	waiting := make(chan error, 1)
	go func() {
		_, acquireErr := limiter.Acquire(ctx, 3, "provider-c")
		waiting <- acquireErr
	}()
	cancel()
	select {
	case acquireErr := <-waiting:
		if !errors.Is(acquireErr, context.Canceled) {
			t.Fatalf("Acquire error = %v, want context cancellation", acquireErr)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled acquire remained queued")
	}

	releaseA()
	releaseB()
	releaseA()
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if limiter.active != 0 || len(limiter.activeByWorkspace) != 0 || len(limiter.activeByProvider) != 0 {
		t.Fatalf("limiter retained usage after release: active=%d workspaces=%v providers=%v", limiter.active, limiter.activeByWorkspace, limiter.activeByProvider)
	}
}

func TestHierarchicalLimiterDoesNotHoldGlobalQuotaWhileProviderIsBusy(t *testing.T) {
	limiter, err := NewHierarchical(2, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	releaseA, err := limiter.Acquire(context.Background(), 1, "provider-a")
	if err != nil {
		t.Fatal(err)
	}

	providerAStarted := make(chan struct{})
	providerARelease := make(chan func(), 1)
	go func() {
		close(providerAStarted)
		release, acquireErr := limiter.Acquire(context.Background(), 2, "provider-a")
		if acquireErr == nil {
			providerARelease <- release
		}
	}()
	<-providerAStarted

	releaseB, err := limiter.Acquire(context.Background(), 3, "provider-b")
	if err != nil {
		t.Fatalf("unrelated provider was blocked: %v", err)
	}
	releaseB()
	releaseA()
	select {
	case release := <-providerARelease:
		release()
	case <-time.After(time.Second):
		t.Fatal("provider waiter did not start after quota release")
	}
}
