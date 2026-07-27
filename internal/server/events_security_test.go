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
	"github.com/lehigh-university-libraries/scribe/internal/store"
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
	secret := []byte(strings.Repeat("s", store.MinWebhookSigningSecretBytes))
	err := (&Handler{}).deliverWebhook(context.Background(), source.URL+"/hook", secret, secretEvent)
	if err == nil {
		t.Fatal("redirecting webhook unexpectedly succeeded")
	}
	if targetCalls != 0 || targetBody != "" {
		t.Fatalf("redirect target received calls/body = %d/%q", targetCalls, targetBody)
	}
}

func TestWebhookSignatureVerificationDistinguishesGenuinePayload(t *testing.T) {
	t.Setenv(safehttp.AllowPrivateFetchesEnv, "true")
	secret := []byte(strings.Repeat("s", store.MinWebhookSigningSecretBytes))
	body := []byte(`{"specversion":"1.0","data":{"itemId":"item-1"}}`)

	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		if !verifyWebhookSignature(secret, r.Header.Get(webhookTimestampHeader), r.Header.Get(webhookSignatureHeader), receivedBody) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer receiver.Close()

	if err := (&Handler{}).deliverWebhook(context.Background(), receiver.URL, secret, body); err != nil {
		t.Fatalf("deliver signed webhook: %v", err)
	}

	timestamp := "1785123456"
	signature := webhookSignature(secret, timestamp, body)
	if !verifyWebhookSignature(secret, timestamp, signature, body) {
		t.Fatal("genuine webhook signature was rejected")
	}
	for name, candidate := range map[string]bool{
		"unsigned":          verifyWebhookSignature(secret, timestamp, "", body),
		"different secret":  verifyWebhookSignature([]byte(strings.Repeat("x", store.MinWebhookSigningSecretBytes)), timestamp, signature, body),
		"changed body":      verifyWebhookSignature(secret, timestamp, signature, append(body, '\n')),
		"changed timestamp": verifyWebhookSignature(secret, timestamp+"0", signature, body),
		"invalid timestamp": verifyWebhookSignature(secret, "not-a-timestamp", signature, body),
	} {
		if candidate {
			t.Errorf("%s webhook was accepted", name)
		}
	}
}
