package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestReadinessRequiresDeployedImageAndOrigin(t *testing.T) {
	const body = `{"status":"ready","api_image":"registry/api@sha256:123","public_origin":"https://scribe.example"}`
	for _, tt := range []struct {
		name, body, image, origin string
		status                    int
		ok                        bool
	}{
		{"ready", body, "registry/api@sha256:123", "https://scribe.example", 200, true},
		{"old image", body, "registry/api@sha256:456", "https://scribe.example", 200, false},
		{"wrong origin", body, "registry/api@sha256:123", "https://other.example", 200, false},
		{"not ready", strings.Replace(body, "ready", "starting", 1), "registry/api@sha256:123", "https://scribe.example", 200, false},
		{"failed status", body, "registry/api@sha256:123", "https://scribe.example", 503, false},
		{"missing expectation", body, "", "https://scribe.example", 200, false},
		{"invalid JSON", "not JSON", "registry/api@sha256:123", "https://scribe.example", 200, false},
		{"bounded response", strings.Repeat(" ", 4096) + body, "registry/api@sha256:123", "https://scribe.example", 200, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			response := &http.Response{StatusCode: tt.status, Body: io.NopCloser(strings.NewReader(tt.body))}
			if err := validate(response, tt.image, tt.origin); (err == nil) != tt.ok {
				t.Fatalf("validate = %v; success want %v", err, tt.ok)
			}
		})
	}
}

func TestProbeRejectsUntrustedOriginsBeforeObtainingCredentials(t *testing.T) {
	for _, origin := range []string{"", "http://service.run.app", "https://169.254.169.254", "https://service.run.app.evil.example", "https://user:password@service.run.app", "https://service.run.app/path", "https://service.run.app:443", "https://service.run.app?redirect=elsewhere"} {
		if err := probe(context.Background(), origin); err == nil || err.Error() != "readiness requires a canonical Cloud Run origin" {
			t.Fatalf("unsafe origin %q: %v", origin, err)
		}
	}
}

func TestProbeRetryRecoversAndStopsOnCancellation(t *testing.T) {
	calls := 0
	if err := retryProbe(context.Background(), time.Nanosecond, func(context.Context) error {
		calls++
		if calls == 1 {
			return errors.New("temporary readiness failure")
		}
		return nil
	}); err != nil || calls != 2 {
		t.Fatalf("retry = %v after %d calls", err, calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := retryProbe(ctx, time.Hour, func(context.Context) error { cancel(); return errors.New("not ready") }); err != context.Canceled {
		t.Fatalf("cancelled probe = %v", err)
	}
}
