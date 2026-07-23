package worklimit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestWrapBoundsConcurrentWorkAndDropsCanceledQueue(t *testing.T) {
	t.Setenv("TEST_WORK_CONCURRENCY", "1")
	limiter := FromEnvironment("TEST_WORK_CONCURRENCY", 2, 4)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	handler := limiter.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
		close(started)
		<-release
	}))

	firstDone := make(chan struct{})
	go func() {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
		close(firstDone)
	}()
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	secondDone := make(chan struct{})
	go func() {
		req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
		handler.ServeHTTP(httptest.NewRecorder(), req)
		close(secondDone)
	}()
	cancel()
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("canceled queued request did not return")
	}
	if calls.Load() != 1 {
		t.Fatalf("handler calls = %d; want one active call", calls.Load())
	}
	close(release)
	<-firstDone
}

func TestFromEnvironmentClampsConcurrency(t *testing.T) {
	t.Setenv("TEST_WORK_CONCURRENCY", "100")
	limiter := FromEnvironment("TEST_WORK_CONCURRENCY", 2, 3)
	if got := cap(limiter.slots); got != 3 {
		t.Fatalf("capacity = %d; want 3", got)
	}
}
