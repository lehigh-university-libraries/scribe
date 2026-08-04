package server

import (
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/lehigh-university-libraries/scribe/internal/hocr"
)

func TestAnnotationEnrichmentConnectErrorMapsProviderFailures(t *testing.T) {
	t.Parallel()

	const sensitiveDetail = "provider-secret-or-response-body"
	tests := []struct {
		name        string
		cause       error
		wantCode    connect.Code
		wantMessage string
	}{
		{
			name:        "permanent rejection",
			cause:       hocr.ErrPermanentProviderRequest,
			wantCode:    connect.CodeFailedPrecondition,
			wantMessage: "transcription provider rejected the configured credential or context",
		},
		{
			name:        "retryable failure",
			cause:       hocr.ErrRetryableProviderRequest,
			wantCode:    connect.CodeUnavailable,
			wantMessage: "transcription provider is temporarily unavailable",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			mapped := annotationEnrichmentConnectError(fmt.Errorf("%s: %w", sensitiveDetail, test.cause))
			if got := connect.CodeOf(mapped); got != test.wantCode {
				t.Fatalf("code = %s; want %s", got, test.wantCode)
			}
			if got := mapped.Error(); !strings.Contains(got, test.wantMessage) {
				t.Fatalf("message = %q; want fixed message %q", got, test.wantMessage)
			}
			if strings.Contains(mapped.Error(), sensitiveDetail) {
				t.Fatalf("mapped error exposed sensitive provider detail: %q", mapped)
			}
		})
	}
}
