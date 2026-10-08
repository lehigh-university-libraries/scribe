package jobqueue

import (
	"encoding/base64"
	"testing"

	"cloud.google.com/go/pubsub/v2"
)

func TestPushJobDecodesWrappedData(t *testing.T) {
	body := []byte(`{"subscription":"projects/project/subscriptions/jobs","message":{"data":"` + base64.StdEncoding.EncodeToString([]byte(`{"type":"scribe.transcription_job","job_id":123}`)) + `"}}`)
	id, err := ParsePushTranscriptionJob(body, "projects/project/subscriptions/jobs")
	if err != nil || id != 123 {
		t.Fatalf("push job = %d/%v; want 123", id, err)
	}
	if _, err := ParsePushTranscriptionJob(body, "projects/project/subscriptions/other"); err == nil {
		t.Fatal("accepted another subscription's push")
	}
}

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
