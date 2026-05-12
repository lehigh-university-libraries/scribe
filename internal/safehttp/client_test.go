package safehttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestValidateURLRejectsLoopback(t *testing.T) {
	u, err := url.Parse("http://127.0.0.1:8080/image.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateURL(u); err == nil {
		t.Fatal("ValidateURL accepted loopback address")
	}
}

func TestGetAllowsPrivateFetchesWhenConfigured(t *testing.T) {
	t.Setenv(AllowPrivateFetchesEnv, "true")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	resp, err := Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}
