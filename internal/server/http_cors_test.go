package server

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/lehigh-university-libraries/scribe/internal/config"
)

func TestCORSAllowsConfiguredOrigin(t *testing.T) {
	previous := config.Get()
	configured := previous
	configured.Config.PublicBaseURL = "https://scribe.example"
	configured.Config.CORS.AllowedOrigins = []string{"https://plugin.example"}
	config.Init(configured)
	t.Cleanup(func() { config.Init(previous) })

	handler := &Handler{mux: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})}
	req := httptest.NewRequest(http.MethodOptions, "https://scribe.example/v1/items", nil)
	req.Header.Set("Origin", "https://plugin.example")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://plugin.example" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want configured origin", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("Access-Control-Allow-Credentials = %q, want true", got)
	}
}

func TestCORSRejectsUnknownOriginBeforeDispatch(t *testing.T) {
	previous := config.Get()
	configured := previous
	configured.Config.PublicBaseURL = "https://scribe.example"
	configured.Config.CORS.AllowedOrigins = nil
	config.Init(configured)
	t.Cleanup(func() { config.Init(previous) })

	var called atomic.Bool
	handler := &Handler{mux: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called.Store(true)
		w.WriteHeader(http.StatusOK)
	})}
	req := httptest.NewRequest(http.MethodPost, "https://scribe.example/v1/items", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	if called.Load() {
		t.Fatal("request reached the application handler")
	}
}

func TestCORSRejectsUnknownPreflightOrigin(t *testing.T) {
	previous := config.Get()
	configured := previous
	configured.Config.PublicBaseURL = "https://scribe.example"
	configured.Config.CORS.AllowedOrigins = nil
	config.Init(configured)
	t.Cleanup(func() { config.Init(previous) })

	handler := &Handler{mux: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})}
	req := httptest.NewRequest(http.MethodOptions, "https://scribe.example/v1/items", nil)
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want empty", got)
	}
}

func TestCORSAllowsSameOriginWithPort(t *testing.T) {
	previous := config.Get()
	config.Init(config.Runtime{})
	t.Cleanup(func() { config.Init(previous) })

	handler := &Handler{mux: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})}
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/v1/items", nil)
	req.Host = "127.0.0.1:8080"
	req.Header.Set("Origin", "http://127.0.0.1:8080")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://127.0.0.1:8080" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want same origin with port", got)
	}
}
