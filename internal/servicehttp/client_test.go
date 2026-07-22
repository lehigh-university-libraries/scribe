package servicehttp

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientNeverFollowsRedirectOrForwardsAuthorization(t *testing.T) {
	var redirectedRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectedRequests.Add(1)
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("redirect received Authorization header %q", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/stolen", http.StatusTemporaryRedirect)
	}))
	defer origin.Close()

	req, err := http.NewRequest(http.MethodPost, origin.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer sentinel-token")
	_, err = NewClient(time.Second).Do(req)
	if !errors.Is(err, ErrRedirectBlocked) {
		t.Fatalf("Do error = %v, want ErrRedirectBlocked", err)
	}
	if got := redirectedRequests.Load(); got != 0 {
		t.Fatalf("redirect target received %d requests, want 0", got)
	}
}
