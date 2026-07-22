package store

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/db"
)

func TestItemImageIDFromSubject(t *testing.T) {
	for _, subject := range []string{
		"item-images/123",
		"item-images/123/annotations/line-1",
		"  item-images/123/annotations/line-1  ",
	} {
		got, ok := itemImageIDFromSubject(subject)
		if !ok || got != 123 {
			t.Fatalf("itemImageIDFromSubject(%q) = %d/%v, want 123/true", subject, got, ok)
		}
	}

	for _, subject := range []string{
		"",
		"items/123",
		"item-images/",
		"item-images//annotations/line-1",
		"item-images/not-a-number",
		"item-images/123x/annotations/line-1",
		"item-images/0",
	} {
		t.Run(subject, func(t *testing.T) {
			if got, ok := itemImageIDFromSubject(subject); ok || got != 0 {
				t.Fatalf("itemImageIDFromSubject(%q) = %d/%v, want 0/false", subject, got, ok)
			}
		})
	}
}

func TestExternalRequestRetryBudgetIsBounded(t *testing.T) {
	t.Parallel()

	failed := db.SelectExternalRequestForUpdateManualRow{
		Status:       db.ExternalRequestsStatusFailed,
		AttemptCount: 3,
		MaxAttempts:  3,
	}
	if !failedExternalRequestExhausted(failed) {
		t.Fatal("failed request at max_attempts was considered retryable")
	}
	failed.AttemptCount = 2
	if failedExternalRequestExhausted(failed) {
		t.Fatal("failed request below max_attempts was considered exhausted")
	}

	running := db.SelectExternalRequestForUpdateManualRow{
		Status:       db.ExternalRequestsStatusInProgress,
		AttemptCount: 3,
		MaxAttempts:  3,
		LeaseUntil:   sql.NullTime{Time: time.Now().UTC().Add(-time.Minute), Valid: true},
	}
	if !exhaustedExternalRequest(running) {
		t.Fatal("expired in-progress request at max_attempts was not exhausted")
	}
}

func TestSafeTranscriptionFailureMessageRedactsDetectorDiagnostics(t *testing.T) {
	t.Parallel()

	const secret = "PRIVATE_DOCUMENT_TEXT_AND_PROVIDER_OUTPUT"
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "kraken", err: fmt.Errorf("kraken segment failed: exit status 1; output: %s", secret)},
		{name: "tesseract", err: fmt.Errorf("tesseract provider failed on word %s", secret)},
	} {
		t.Run(test.name, func(t *testing.T) {
			safe := SafeTranscriptionFailureMessage(test.err)
			if strings.Contains(safe, secret) || len(safe) > maxDurableFailureMessageBytes {
				t.Fatalf("SafeTranscriptionFailureMessage() = %q; want bounded categorical failure", safe)
			}
			if safe != "transcription attempt failed" && safe != "transcription provider request failed" {
				t.Fatalf("SafeTranscriptionFailureMessage() = %q; want known category", safe)
			}
		})
	}
}
