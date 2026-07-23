package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

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
