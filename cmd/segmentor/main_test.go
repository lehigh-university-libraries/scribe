package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
