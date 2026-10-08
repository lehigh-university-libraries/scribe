package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPushWorkerAcknowledgesOnlyCompletedWork(t *testing.T) {
	var draining atomic.Bool
	started, release, responded := make(chan struct{}), make(chan struct{}), make(chan struct{})
	handler := workerRequestHandler(workerHealthHandler(nil, &draining), "projects/project/subscriptions/jobs", &draining,
		func(ctx context.Context, id uint64) error {
			if id != 42 || ctx.Err() != nil {
				t.Errorf("unexpected delivery: id=%d context=%v", id, ctx.Err())
			}
			close(started)
			<-release
			return nil
		}, func(context.Context) error { t.Error("transcription invoked maintenance"); return nil })
	request := httptest.NewRequest(http.MethodPost, "/internal/transcription", strings.NewReader(`{"subscription":"projects/project/subscriptions/jobs","message":{"attributes":{"job_id":"42"}}}`))
	recorder := httptest.NewRecorder()
	go func() { handler.ServeHTTP(recorder, request); close(responded) }()
	<-started
	select {
	case <-responded:
		t.Error("push acknowledged before processing completed")
	default:
	}
	close(release)
	<-responded
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("completed push status = %d", recorder.Code)
	}
}

func TestPushWorkerRejectsInvalidAndRetriesIncompleteRequests(t *testing.T) {
	for _, test := range []struct {
		name, path, body string
		draining, fail   bool
		status, calls    int
	}{
		{"wrong subscription", "/internal/transcription", `{"subscription":"other","message":{"attributes":{"job_id":"42"}}}`, false, false, 400, 0},
		{"oversized", "/internal/transcription", strings.Repeat("x", 65537), false, false, 400, 0},
		{"invalid message", "/internal/transcription", `{"subscription":"projects/project/subscriptions/jobs","message":{}}`, false, false, 400, 0},
		{"retry", "/internal/transcription", `{"subscription":"projects/project/subscriptions/jobs","message":{"attributes":{"job_id":"42"}}}`, false, true, 503, 1},
		{"draining", "/internal/maintenance", "", true, false, 503, 0},
		{"maintenance", "/internal/maintenance", "", false, false, 204, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			var draining atomic.Bool
			draining.Store(test.draining)
			calls := 0
			work := func(context.Context) error {
				calls++
				if test.fail {
					return errors.New("retry")
				}
				return nil
			}
			handler := workerRequestHandler(workerHealthHandler(nil, &draining), "projects/project/subscriptions/jobs", &draining, func(ctx context.Context, _ uint64) error { return work(ctx) }, work)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body)))
			if recorder.Code != test.status || calls != test.calls {
				t.Fatalf("status=%d calls=%d; want %d/%d", recorder.Code, calls, test.status, test.calls)
			}
		})
	}
}

type testReadinessChecker struct{ err error }

func (checker testReadinessChecker) PingContext(context.Context) error { return checker.err }

func TestWorkerHealthSeparatesLivenessAndReadiness(t *testing.T) {
	t.Parallel()

	var draining atomic.Bool
	handler := workerHealthHandler(testReadinessChecker{}, &draining)
	for _, path := range []string{"/livez", "/readyz", "/healthz"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d; want 200", path, recorder.Code)
		}
	}

	unready := workerHealthHandler(testReadinessChecker{err: errors.New("offline")}, &draining)
	for _, path := range []string{"/readyz", "/healthz"} {
		recorder := httptest.NewRecorder()
		unready.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("GET %s with database failure status = %d; want 503", path, recorder.Code)
		}
	}
	recorder := httptest.NewRecorder()
	unready.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/livez", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("liveness status with database failure = %d; want 200", recorder.Code)
	}

	draining.Store(true)
	for _, path := range []string{"/readyz", "/healthz"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("GET %s while draining status = %d; want 503", path, recorder.Code)
		}
	}
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/livez", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /livez while draining status = %d; want process liveness 200", recorder.Code)
	}
}
