package server

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

func TestTranscriptionJobConnectErrorMapsQuotaToResourceExhausted(t *testing.T) {
	err := transcriptionJobConnectError("enqueue", &store.TranscriptionJobQuotaExceededError{
		WorkspaceID: 42,
		Limit:       3,
	})
	if got := connect.CodeOf(err); got != connect.CodeResourceExhausted {
		t.Fatalf("Connect code = %v, want %v", got, connect.CodeResourceExhausted)
	}
}
