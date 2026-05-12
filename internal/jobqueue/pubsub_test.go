package jobqueue

import (
	"testing"

	"cloud.google.com/go/pubsub/v2"
)

func TestParseTranscriptionJobMessageFromAttribute(t *testing.T) {
	jobID, err := parseTranscriptionJobMessage(&pubsub.Message{
		Attributes: map[string]string{"job_id": "123"},
	})
	if err != nil {
		t.Fatalf("parseTranscriptionJobMessage returned error: %v", err)
	}
	if jobID != 123 {
		t.Fatalf("jobID = %d, want 123", jobID)
	}
}

func TestParseTranscriptionJobMessageFromBody(t *testing.T) {
	jobID, err := parseTranscriptionJobMessage(&pubsub.Message{
		Data: []byte(`{"type":"scribe.transcription_job","job_id":456}`),
	})
	if err != nil {
		t.Fatalf("parseTranscriptionJobMessage returned error: %v", err)
	}
	if jobID != 456 {
		t.Fatalf("jobID = %d, want 456", jobID)
	}
}

func TestParseTranscriptionJobMessageRejectsUnexpectedType(t *testing.T) {
	if _, err := parseTranscriptionJobMessage(&pubsub.Message{
		Data: []byte(`{"type":"other","job_id":456}`),
	}); err == nil {
		t.Fatal("expected error")
	}
}
