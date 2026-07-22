package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lehigh-university-libraries/scribe/internal/safehttp"
)

func TestWebhookAuditFieldsAndFailuresRedactTargetSecrets(t *testing.T) {
	t.Parallel()

	raw := "https://user:secret@hooks.example.edu/path?token=top-secret#fragment"
	origin, targetID := webhookTargetAuditFields(raw)
	if origin != "https://hooks.example.edu" {
		t.Fatalf("origin = %q", origin)
	}
	if len(targetID) != 12 {
		t.Fatalf("target ID = %q", targetID)
	}
	if strings.Contains(origin+targetID, "secret") || strings.Contains(origin+targetID, "token") {
		t.Fatalf("audit fields exposed target secret: %q / %q", origin, targetID)
	}

	failure := safeWebhookFailure(errors.New("Post " + raw + ": connection refused"))
	if failure != "webhook request failed" || strings.Contains(failure, raw) {
		t.Fatalf("safe failure = %q", failure)
	}
	if got := safeWebhookFailure(errors.New("status 503")); got != "status 503" {
		t.Fatalf("HTTP failure = %q", got)
	}
}

func TestWebhookRedirectNeverReceivesCloudEventBody(t *testing.T) {
	t.Setenv(safehttp.AllowPrivateFetchesEnv, "true")
	var targetCalls int
	var targetBody string
	target := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		targetCalls++
		body, _ := io.ReadAll(r.Body)
		targetBody = string(body)
	}))
	defer target.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/capture", http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	secretEvent := []byte(`{"specversion":"1.0","data":{"private":"manuscript text"}}`)
	err := (&Handler{}).deliverWebhook(context.Background(), source.URL+"/hook", secretEvent)
	if err == nil {
		t.Fatal("redirecting webhook unexpectedly succeeded")
	}
	if targetCalls != 0 || targetBody != "" {
		t.Fatalf("redirect target received calls/body = %d/%q", targetCalls, targetBody)
	}
}
