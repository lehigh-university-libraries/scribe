package segmentor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lehigh-university-libraries/scribe/internal/uploadlimits"
	"github.com/lehigh-university-libraries/scribe/internal/worddetection"
)

func TestProcessingFailuresRedactSubprocessDiagnosticsFromResponsesAndLogs(t *testing.T) {
	const privateDiagnostic = "PRIVATE_DOCUMENT_STDERR_/models/secret/model.mlmodel"

	previousLogger := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	tests := []struct {
		name         string
		cause        error
		wantStatus   int
		wantBody     string
		wantCategory string
		wantTarget   error
	}{
		{
			name:         "internal",
			cause:        errors.New(privateDiagnostic),
			wantStatus:   http.StatusBadGateway,
			wantBody:     "transcribe image failed\n",
			wantCategory: string(processingFailureInternal),
		},
		{
			name:         "canceled",
			cause:        fmt.Errorf("%s: %w", privateDiagnostic, context.Canceled),
			wantStatus:   http.StatusRequestTimeout,
			wantBody:     "transcribe image canceled\n",
			wantCategory: string(processingFailureCanceled),
			wantTarget:   context.Canceled,
		},
		{
			name:         "timeout",
			cause:        fmt.Errorf("%s: %w", privateDiagnostic, context.DeadlineExceeded),
			wantStatus:   http.StatusGatewayTimeout,
			wantBody:     "transcribe image timed out\n",
			wantCategory: string(processingFailureTimeout),
			wantTarget:   context.DeadlineExceeded,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var logs bytes.Buffer
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))

			redacted := redactSubprocessError("kraken transcription", test.cause, []byte(privateDiagnostic))
			if redacted == nil {
				t.Fatal("redactSubprocessError() error = nil")
			}
			if strings.Contains(redacted.Error(), privateDiagnostic) || errors.Unwrap(redacted) != nil {
				t.Fatalf("redacted error exposed its subprocess diagnostic or unwrap cause: %q", redacted)
			}
			if test.wantTarget != nil && !errors.Is(redacted, test.wantTarget) {
				t.Fatalf("redacted error no longer matches %v", test.wantTarget)
			}

			response := httptest.NewRecorder()
			writeProcessingFailure(response, "transcribe image", redacted)
			if response.Code != test.wantStatus || response.Body.String() != test.wantBody {
				t.Fatalf("response = %d %q, want %d %q", response.Code, response.Body.String(), test.wantStatus, test.wantBody)
			}
			combined := response.Body.String() + logs.String()
			if strings.Contains(combined, privateDiagnostic) || strings.Contains(combined, "/models/secret") {
				t.Fatalf("response or log exposed subprocess diagnostics: %s", combined)
			}
			for _, metadata := range []string{
				`"msg":"segmentor request failed"`,
				`"operation":"transcribe image"`,
				`"category":"` + test.wantCategory + `"`,
				`"subprocess_output_bytes":`,
			} {
				if !strings.Contains(logs.String(), metadata) {
					t.Fatalf("safe failure log omitted %s: %s", metadata, logs.String())
				}
			}
		})
	}
}

func TestSegmentBoxesNormalizesLocalDetectorConfidenceForHTRWireContract(t *testing.T) {
	boxes, err := segmentBoxes([]worddetection.WordBox{
		{X: 1, Y: 2, Width: 3, Height: 4, Text: "café 世界", Confidence: 90},
		{X: 5, Y: 6, Width: 7, Height: 8, Confidence: 0.75},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(boxes) != 2 || boxes[0].Confidence != 0.9 || boxes[0].Text != "café 世界" || boxes[1].Confidence != 0.75 {
		t.Fatalf("wire boxes = %#v", boxes)
	}
	if _, err := segmentBoxes([]worddetection.WordBox{{Confidence: 101}}); err == nil {
		t.Fatal("out-of-range confidence was accepted")
	}
}

func TestMalformedMultipartResponsesDoNotReflectRequestContent(t *testing.T) {
	const privateRequestContent = "PRIVATE_MULTIPART_DOCUMENT_CONTENT"

	for _, endpoint := range []string{"/v1/segment", "/v1/transcribe"} {
		t.Run(endpoint, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(privateRequestContent))
			request.Header.Set("Content-Type", "multipart/form-data; boundary=scribe-test-boundary")
			response := httptest.NewRecorder()

			NewHandler().ServeHTTP(response, request)

			if response.Code != http.StatusBadRequest || response.Body.String() != "invalid multipart image request\n" {
				t.Fatalf("response = %d %q", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), privateRequestContent) {
				t.Fatalf("multipart error reflected request content: %q", response.Body.String())
			}
		})
	}
}

func TestDetectWordsRedactsUnsupportedModelValue(t *testing.T) {
	const privateModelValue = "PRIVATE_MODEL_SELECTION_DO_NOT_REFLECT"

	_, _, err := DetectWords(context.Background(), "unused.png", privateModelValue)
	if err == nil {
		t.Fatal("DetectWords() error = nil")
	}
	if got := err.Error(); got != "segmentation configuration failed" {
		t.Fatalf("DetectWords() error = %q", got)
	}
	if strings.Contains(err.Error(), privateModelValue) {
		t.Fatalf("DetectWords() reflected model input: %q", err)
	}
}

func TestPreparedImageInspectionEnforcesByteAndPixelEnvelope(t *testing.T) {
	directory := t.TempDir()
	validPath := filepath.Join(directory, "valid.jpg")
	valid, err := os.OpenFile(validPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatalf("create valid fixture: %v", err)
	}
	if err := jpeg.Encode(valid, image.NewGray(image.Rect(0, 0, 4, 3)), nil); err != nil {
		_ = valid.Close()
		t.Fatalf("encode valid fixture: %v", err)
	}
	if err := valid.Close(); err != nil {
		t.Fatalf("close valid fixture: %v", err)
	}
	config, err := inspectPreparedImage(validPath)
	if err != nil || config.Width != 4 || config.Height != 3 {
		t.Fatalf("inspectPreparedImage(valid) = %#v, %v", config, err)
	}

	oversizedPath := filepath.Join(directory, "oversized.jpg")
	oversized, err := os.OpenFile(oversizedPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatalf("create oversized fixture: %v", err)
	}
	if err := oversized.Truncate(uploadlimits.MaxImageBytes + 1); err != nil {
		_ = oversized.Close()
		t.Fatalf("truncate oversized fixture: %v", err)
	}
	if err := oversized.Close(); err != nil {
		t.Fatalf("close oversized fixture: %v", err)
	}
	if _, err := inspectPreparedImage(oversizedPath); err == nil {
		t.Fatal("inspectPreparedImage accepted an oversized subprocess output")
	}
}
