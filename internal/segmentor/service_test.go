package segmentor

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/uploadlimits"
	"github.com/lehigh-university-libraries/scribe/internal/worddetection"
)

func TestRequestDeadlineCancelsAbandonedInference(t *testing.T) {
	const timeout = 20 * time.Millisecond
	observed := make(chan error, 1)
	handler := withRequestDeadline(timeout, http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
		observed <- request.Context().Err()
	}))

	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/segment", nil))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("request deadline did not cancel the abandoned inference")
	}
	if err := <-observed; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("request deadline error = %v, want context deadline exceeded", err)
	}
}

func TestReadinessSmokeFixtureFullyDecodes(t *testing.T) {
	encoded, err := os.ReadFile("../../config/readiness-smoke.png.base64")
	if err != nil {
		t.Fatalf("read readiness fixture: %v", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil {
		t.Fatalf("decode readiness fixture base64: %v", err)
	}
	fixtureImage, err := png.Decode(bytes.NewReader(decoded))
	if err != nil {
		t.Fatalf("fully decode readiness fixture PNG: %v", err)
	}
	if bounds := fixtureImage.Bounds(); bounds.Dx() != 640 || bounds.Dy() != 160 {
		t.Fatalf("readiness fixture bounds = %v, want 640x160", bounds)
	}

	darkPixels := 0
	for y := fixtureImage.Bounds().Min.Y; y < fixtureImage.Bounds().Max.Y; y++ {
		for x := fixtureImage.Bounds().Min.X; x < fixtureImage.Bounds().Max.X; x++ {
			red, green, blue, _ := fixtureImage.At(x, y).RGBA()
			if red+green+blue < 3*0xffff/2 {
				darkPixels++
			}
		}
	}
	if darkPixels < 100 {
		t.Fatalf("readiness fixture has only %d dark pixels; visible OCR text is required", darkPixels)
	}
}

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

func TestKrakenPublicRoutesResolveToBakedModelFiles(t *testing.T) {
	modelDirectory := t.TempDir()
	transcriptionPath := filepath.Join(modelDirectory, "transcription-engine.mlmodel")
	segmentationPath := filepath.Join(modelDirectory, "segmentation-engine.mlmodel")
	for _, path := range []string{transcriptionPath, segmentationPath} {
		if err := os.WriteFile(path, []byte("reviewed model fixture"), 0o600); err != nil {
			t.Fatalf("write model fixture: %v", err)
		}
	}

	binDirectory := t.TempDir()
	krakenPath := filepath.Join(binDirectory, "kraken")
	fakeKraken := `#!/bin/sh
set -eu
if [ "$1" = "-i" ]; then
  [ "$4" = "segment" ]
  [ "$5" = "-bl" ]
  [ "$6" = "-i" ]
  [ "$7" = "$EXPECTED_SEGMENTATION_MODEL_PATH" ]
  printf '{"image_size":[100,100],"lines":[{"baseline":[[10,20],[80,20]],"boundary":[],"tags":{"type":[{"type":"default"}]}}]}' > "$3"
  exit 0
fi
[ "$1" = "--raise-on-error" ]
[ "$2" = "-i" ]
[ "$5" = "ocr" ]
[ "$6" = "--no-segmentation" ]
[ "$7" = "-m" ]
[ "$8" = "$EXPECTED_TRANSCRIPTION_MODEL_PATH" ]
printf 'transcribed text\n' > "$4"
`
	if err := os.WriteFile(krakenPath, []byte(fakeKraken), 0o700); err != nil {
		t.Fatalf("write fake kraken: %v", err)
	}
	imagePath := filepath.Join(t.TempDir(), "image.png")
	if err := os.WriteFile(imagePath, []byte("image fixture"), 0o600); err != nil {
		t.Fatalf("write image fixture: %v", err)
	}

	t.Setenv("PATH", binDirectory)
	t.Setenv("KRAKEN_MODEL_DIR", modelDirectory)
	t.Setenv("KRAKEN_SEGMENTATION_MODEL_ID", "layout-lines-v2")
	t.Setenv("KRAKEN_SEGMENTATION_MODEL", filepath.Base(segmentationPath))
	t.Setenv("KRAKEN_TRANSCRIPTION_MODEL_ID", "latin-handwriting-v2")
	t.Setenv("KRAKEN_TRANSCRIPTION_MODEL", filepath.Base(transcriptionPath))
	t.Setenv("EXPECTED_SEGMENTATION_MODEL_PATH", segmentationPath)
	t.Setenv("EXPECTED_TRANSCRIPTION_MODEL_PATH", transcriptionPath)

	words, segmentationID, err := DetectWords(context.Background(), imagePath, "layout-lines-v2")
	if err != nil {
		t.Fatalf("DetectWords() error = %v", err)
	}
	if segmentationID != "layout-lines-v2" || len(words) != 1 {
		t.Fatalf("DetectWords() = %q, %#v", segmentationID, words)
	}

	for _, requestedID := range []string{"latin-handwriting-v2", ""} {
		text, transcriptionID, transcribeErr := TranscribeWithKraken(context.Background(), imagePath, requestedID)
		if transcribeErr != nil {
			t.Fatalf("TranscribeWithKraken(%q) error = %v", requestedID, transcribeErr)
		}
		if text != "transcribed text" || transcriptionID != "latin-handwriting-v2" {
			t.Fatalf("TranscribeWithKraken(%q) = %q, %q", requestedID, text, transcriptionID)
		}
	}

	if _, _, err := DetectWords(context.Background(), imagePath, "unbaked-layout"); err == nil {
		t.Fatal("DetectWords() accepted an unbaked public model ID")
	}
	if _, _, err := TranscribeWithKraken(context.Background(), imagePath, "unbaked-transcription"); err == nil {
		t.Fatal("TranscribeWithKraken() accepted an unbaked public model ID")
	}
}

func TestTranscribeWithKrakenPreservesContextDeadline(t *testing.T) {
	modelDirectory := t.TempDir()
	modelPath := filepath.Join(modelDirectory, "transcription-engine.mlmodel")
	if err := os.WriteFile(modelPath, []byte("reviewed model fixture"), 0o600); err != nil {
		t.Fatalf("write model fixture: %v", err)
	}

	binDirectory := t.TempDir()
	krakenPath := filepath.Join(binDirectory, "kraken")
	if err := os.WriteFile(krakenPath, []byte("#!/bin/sh\nexec /bin/sleep 10\n"), 0o700); err != nil {
		t.Fatalf("write fake kraken: %v", err)
	}

	t.Setenv("PATH", binDirectory)
	t.Setenv("KRAKEN_MODEL_DIR", modelDirectory)
	t.Setenv("KRAKEN_TRANSCRIPTION_MODEL_ID", "latin-handwriting-v2")
	t.Setenv("KRAKEN_TRANSCRIPTION_MODEL", filepath.Base(modelPath))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, _, err := TranscribeWithKraken(ctx, "image.png", "latin-handwriting-v2")
	if err == nil {
		t.Fatal("TranscribeWithKraken() error = nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("TranscribeWithKraken() error = %v, want context deadline exceeded", err)
	}
	var failure *processingFailure
	if !errors.As(err, &failure) || failure.category != processingFailureTimeout {
		t.Fatalf("TranscribeWithKraken() failure = %#v, want timeout category", failure)
	}
}

func TestKrakenModelRoutesRejectUntrustedFilesystemPaths(t *testing.T) {
	modelDirectory := t.TempDir()
	outsideDirectory := t.TempDir()
	outsideModel := filepath.Join(outsideDirectory, "outside.mlmodel")
	if err := os.WriteFile(outsideModel, []byte("untrusted model fixture"), 0o600); err != nil {
		t.Fatalf("write outside model fixture: %v", err)
	}
	if err := os.Symlink(outsideModel, filepath.Join(modelDirectory, "linked.mlmodel")); err != nil {
		t.Fatalf("link outside model fixture: %v", err)
	}

	t.Setenv("KRAKEN_TRANSCRIPTION_MODEL_ID", "reviewed-model")
	t.Setenv("KRAKEN_MODEL_DIR", modelDirectory)
	for _, filename := range []string{
		"../outside.mlmodel",
		"/outside.mlmodel",
		"nested/outside.mlmodel",
		"linked.mlmodel",
	} {
		t.Run(filename, func(t *testing.T) {
			t.Setenv("KRAKEN_TRANSCRIPTION_MODEL", filename)
			if _, err := configuredKrakenModelRoute(
				"KRAKEN_TRANSCRIPTION_MODEL_ID",
				"KRAKEN_TRANSCRIPTION_MODEL",
			); err == nil {
				t.Fatalf("configuredKrakenModelRoute() accepted %q", filename)
			}
		})
	}

	t.Setenv("KRAKEN_TRANSCRIPTION_MODEL", "outside.mlmodel")
	t.Setenv("KRAKEN_MODEL_DIR", "relative-model-directory")
	if _, err := configuredKrakenModelRoute(
		"KRAKEN_TRANSCRIPTION_MODEL_ID",
		"KRAKEN_TRANSCRIPTION_MODEL",
	); err == nil {
		t.Fatal("configuredKrakenModelRoute() accepted a relative model directory")
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
