package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lehigh-university-libraries/scribe/internal/config"
)

func TestCORSAllowsConfiguredOrigin(t *testing.T) {
	config.Init(config.Runtime{Config: config.Config{
		PublicBaseURL: "https://scribe.example",
		CORS: config.CORSConfig{
			AllowedOrigins: []string{"https://plugin.example"},
		},
	}})
	t.Cleanup(func() { config.Init(config.Runtime{}) })

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

func TestCORSRejectsUnknownPreflightOrigin(t *testing.T) {
	config.Init(config.Runtime{Config: config.Config{PublicBaseURL: "https://scribe.example"}})
	t.Cleanup(func() { config.Init(config.Runtime{}) })

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
	config.Init(config.Runtime{})
	t.Cleanup(func() { config.Init(config.Runtime{}) })

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
