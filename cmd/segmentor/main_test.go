package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/segmentor"
)

func TestSegmentorTimeoutsLeaveBoundedCleanupAndWriteMargins(t *testing.T) {
	server := newSegmentorHTTPServer(":0")
	if segmentor.InferenceHandlerTimeout <= segmentor.InferenceRequestTimeout {
		t.Fatalf(
			"segmentor handler timeout = %s, must exceed client inference timeout %s",
			segmentor.InferenceHandlerTimeout,
			segmentor.InferenceRequestTimeout,
		)
	}
	if margin := segmentor.InferenceHandlerTimeout - segmentor.InferenceRequestTimeout; margin != 15*time.Second {
		t.Fatalf("segmentor handler cleanup margin = %s, want 15s", margin)
	}
	if server.WriteTimeout != segmentor.InferenceServerWriteTimeout {
		t.Fatalf("segmentor WriteTimeout = %s, want %s", server.WriteTimeout, segmentor.InferenceServerWriteTimeout)
	}
	if margin := server.WriteTimeout - segmentor.InferenceHandlerTimeout; margin != 15*time.Second {
		t.Fatalf("segmentor response write margin = %s, want 15s", margin)
	}
	if server.WriteTimeout >= 300*time.Second {
		t.Fatalf("segmentor WriteTimeout = %s, must remain below the Cloud Run request timeout", server.WriteTimeout)
	}
}

func TestCheckSegmentorHealth(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		wantError  bool
	}{
		{name: "ready", statusCode: http.StatusOK},
		{name: "not ready", statusCode: http.StatusServiceUnavailable, wantError: true},
		{name: "redirect", statusCode: http.StatusTemporaryRedirect, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if test.statusCode >= 300 && test.statusCode < 400 {
					w.Header().Set("Location", "/healthz")
				}
				w.WriteHeader(test.statusCode)
			}))
			defer server.Close()

			err := checkSegmentorHealth(context.Background(), server.URL)
			if test.wantError && err == nil {
				t.Fatal("checkSegmentorHealth unexpectedly succeeded")
			}
			if !test.wantError && err != nil {
				t.Fatalf("checkSegmentorHealth: %v", err)
			}
		})
	}
}
