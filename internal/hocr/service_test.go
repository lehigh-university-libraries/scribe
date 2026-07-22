package hocr

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/lehigh-university-libraries/htr/pkg/providers"
	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/worddetection"
)

type failingClient struct {
	err error
}

func (p failingClient) Extract(context.Context, providers.Request) (providers.Result, error) {
	return providers.Result{}, p.err
}

func (failingClient) Name() string { return "failing" }

func TestLikelyWordBoxAcceptsUnicodeAndNumbers(t *testing.T) {
	service := NewService()
	for _, text := range []string{"Привет", "漢字", "1234", "é"} {
		if !service.isLikelyWordBox(worddetection.WordBox{X: 10, Y: 10, Width: 80, Height: 24, Text: text}, 1000, 1000) {
			t.Errorf("isLikelyWordBox() rejected %q", text)
		}
	}
}

func TestRejectedWordDetectionLogDoesNotExposeDocumentText(t *testing.T) {
	const privateText = "PRIVATE_HANDWRITTEN_TEXT_DO_NOT_LOG"

	var captured bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&captured, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	service := NewService()
	words, err := service.transcribeWords(
		context.Background(),
		"unused.jpg",
		[]worddetection.WordBox{{X: 1, Y: 1, Width: 1, Height: 1, Text: privateText}},
		1000,
		1000,
		nil,
		"ollama",
		"tesseract",
		nil,
		"glm-ocr:bf16",
	)
	if err != nil {
		t.Fatalf("transcribeWords() error = %v", err)
	}
	if len(words) != 0 {
		t.Fatalf("transcribeWords() returned %d rejected words", len(words))
	}

	logs := captured.String()
	if strings.Contains(logs, privateText) {
		t.Fatalf("rejected detection log exposed document text: %s", logs)
	}
	for _, metadata := range []string{`"msg":"Skipping non-word detection"`, `"width":1`, `"height":1`} {
		if !strings.Contains(logs, metadata) {
			t.Fatalf("rejected detection log omitted bounded diagnostic metadata %s: %s", metadata, logs)
		}
	}
}

func TestProviderConfigUsesOllamaModelEndpointMap(t *testing.T) {
	config.Init(config.Runtime{
		Config: config.Config{
			LLM: config.LLMConfig{
				Ollama: config.OllamaConfig{
					URL:      "http://default-ollama:11434",
					Audience: "https://default.run.app",
					ModelEndpoints: map[string]config.ModelEndpoint{
						"glm-ocr:bf16": {
							URL:      "https://ollama-glm.run.app",
							Audience: "https://ollama-glm.run.app",
						},
					},
				},
			},
		},
	})
	t.Cleanup(func() {
		config.Init(config.Runtime{})
	})

	svc := NewService()
	cfg, err := svc.providerConfig("ollama", "glm-ocr:bf16", "prompt", 0)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.BaseURL != "https://ollama-glm.run.app" {
		t.Fatalf("cfg.BaseURL = %q, want model-routed URL", cfg.BaseURL)
	}
	if cfg.Audience != "https://ollama-glm.run.app" {
		t.Fatalf("cfg.Audience = %q, want model-routed audience", cfg.Audience)
	}
}

func TestTranscriptionOptionsApplyPromptAndTemperature(t *testing.T) {
	temperature := 0.35
	ctx := WithTranscriptionOptions(context.Background(), "Preserve abbreviations.", &temperature)
	if got := promptFromContext(ctx, "Transcribe the line."); got != "Preserve abbreviations.\n\nTask instructions:\nTranscribe the line." {
		t.Fatalf("promptFromContext() = %q", got)
	}
	if got := temperatureFromContext(ctx); got != temperature {
		t.Fatalf("temperatureFromContext() = %v, want %v", got, temperature)
	}
}

func TestProviderAuditsAreMetadataOnlyAndErrorsAreBounded(t *testing.T) {
	recordType := reflect.TypeOf(ProviderCallAuditRecord{})
	for _, forbidden := range []string{"Prompt", "RequestJSON", "ResponseJSON"} {
		if _, found := recordType.FieldByName(forbidden); found {
			t.Fatalf("provider audit record still exposes captured body field %q", forbidden)
		}
	}
	oversized := strings.Repeat("x", maxProviderAuditErrorBytes+1)
	bounded := boundedProviderAuditError(oversized)
	if len(bounded) > maxProviderAuditErrorBytes || !strings.Contains(bounded, "sha256=") {
		t.Fatalf("bounded provider audit error = %q (%d bytes)", bounded, len(bounded))
	}
}

func TestProviderResponseContentIsRedactedFromErrorsAndAudit(t *testing.T) {
	const responseSecret = "sentinel-private-provider-response"
	service := NewService()
	var audit ProviderCallAuditRecord
	service.SetProviderCallAuditLogger(func(_ context.Context, record ProviderCallAuditRecord) {
		audit = record
	})

	_, err := service.extractTextWithProvider(
		context.Background(),
		failingClient{err: providers.NewError(providers.ErrorUpstream, http.StatusServiceUnavailable, true, nil)},
		"openai",
		providers.Config{Model: "test-model"},
		providers.Image{Data: []byte("image"), MediaType: "image/png"},
		"test",
	)
	if err == nil {
		t.Fatal("extractTextWithProvider() error = nil")
	}
	if strings.Contains(err.Error(), responseSecret) {
		t.Fatalf("returned provider error leaked response content: %q", err)
	}
	if got := err.Error(); got != "provider request failed with HTTP status 503" {
		t.Fatalf("returned provider error = %q", got)
	}
	if strings.Contains(audit.ErrorMessage, responseSecret) {
		t.Fatalf("provider audit error leaked response content: %q", audit.ErrorMessage)
	}
	if audit.HTTPStatus == nil || *audit.HTTPStatus != http.StatusServiceUnavailable {
		t.Fatalf("audit HTTP status = %v, want %d", audit.HTTPStatus, http.StatusServiceUnavailable)
	}
	if !isRetriableProviderError(err) {
		t.Fatal("redacted 503 error is not retryable")
	}
}

func TestDirectProviderExecutionAndFailureLogsRedactRemoteDiagnostics(t *testing.T) {
	const privateDiagnostic = "PRIVATE_PROVIDER_BODY_https://user:token@example.test/tmp/document.png"

	service := NewService()
	_, err := service.executeProvider(
		context.Background(),
		failingClient{err: providers.NewError(providers.ErrorUpstream, http.StatusServiceUnavailable, true, nil)},
		"openai",
		providers.Config{Model: "test-model"},
		"/tmp/private-input.png",
		providers.Image{Data: []byte(privateDiagnostic), MediaType: "image/png"},
		"transcribe_line",
	)
	if err == nil || strings.Contains(err.Error(), privateDiagnostic) {
		t.Fatalf("direct provider execution error = %q, want categorical redaction", err)
	}

	previousLogger := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	var logs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	logHOCRFailure("provider operation failed", err, "line_index", 7)
	if strings.Contains(logs.String(), privateDiagnostic) || strings.Contains(logs.String(), "user:token") || strings.Contains(logs.String(), "/tmp/") {
		t.Fatalf("hOCR failure log exposed remote diagnostics: %s", logs.String())
	}
	for _, metadata := range []string{
		`"msg":"provider operation failed"`,
		`"line_index":7`,
		`"http_status":503`,
		`"category":"provider"`,
		`"error_type":"*hocr.providerRequestError"`,
	} {
		if !strings.Contains(logs.String(), metadata) {
			t.Fatalf("hOCR failure log omitted %s: %s", metadata, logs.String())
		}
	}
}

func TestProviderErrorRedactionPreservesCancellationWithoutUnwrapping(t *testing.T) {
	redacted := redactProviderError(fmt.Errorf("remote body contained a secret: %w", context.DeadlineExceeded), nil)
	if !errors.Is(redacted, context.DeadlineExceeded) {
		t.Fatal("redacted error no longer matches context deadline")
	}
	if strings.Contains(redacted.Error(), "remote body") || strings.Contains(redacted.Error(), "secret") {
		t.Fatalf("redacted error leaked original text: %q", redacted)
	}
	if got := redacted.Error(); got != "provider request timed out" {
		t.Fatalf("redacted deadline error = %q", got)
	}
}

func TestSegmentationErrorRedactionDoesNotExposeDetectorDiagnostics(t *testing.T) {
	const detectorDiagnostic = "PRIVATE_KRAKEN_STDERR_AND_MODEL_PATH"

	redacted := redactSegmentationError(fmt.Errorf("kraken failed: %s", detectorDiagnostic))
	if redacted == nil {
		t.Fatal("redactSegmentationError() error = nil")
	}
	if got := redacted.Error(); got != "segmentation provider request failed" {
		t.Fatalf("redactSegmentationError() = %q", got)
	}
	if strings.Contains(redacted.Error(), detectorDiagnostic) {
		t.Fatalf("redacted segmentation error exposed detector diagnostics: %q", redacted)
	}

	deadline := redactSegmentationError(fmt.Errorf("%s: %w", detectorDiagnostic, context.DeadlineExceeded))
	if !errors.Is(deadline, context.DeadlineExceeded) {
		t.Fatal("redacted segmentation error no longer matches context deadline")
	}
	if strings.Contains(deadline.Error(), detectorDiagnostic) {
		t.Fatalf("redacted deadline error exposed detector diagnostics: %q", deadline)
	}
}

func TestImageOperationsHonorCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	service := NewService()
	if _, err := service.extractLineImage(ctx, "unused.png", 0, 0, 10, 10, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("extractLineImage error = %v, want context.Canceled", err)
	}
	if _, err := service.stitchWordImages(ctx, "unused.png", []worddetection.WordBox{{X: 0, Y: 0, Width: 10, Height: 10}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("stitchWordImages error = %v, want context.Canceled", err)
	}
}

func TestProviderCallMetadataIsMerged(t *testing.T) {
	contextID := uint64(12)
	itemImageID := uint64(34)
	ctx := WithProviderCallMetadata(context.Background(), 42, "", nil, &contextID)
	ctx = WithProviderCallMetadata(ctx, 0, "processing-id", &itemImageID, nil)
	metadata := providerCallMetadataFromContext(ctx)
	if metadata.WorkspaceID != 42 || metadata.SessionID != "processing-id" || metadata.ItemImageID == nil || *metadata.ItemImageID != itemImageID || metadata.ContextID == nil || *metadata.ContextID != contextID {
		t.Fatalf("merged metadata = %#v", metadata)
	}
}

func TestTesseractIsAFirstClassTranscriptionProvider(t *testing.T) {
	provider, name, model, err := NewService().initLLMProvider("tesseract", "")
	if err != nil {
		t.Fatalf("initLLMProvider(tesseract) error = %v", err)
	}
	if provider != nil {
		t.Fatalf("tesseract unexpectedly returned an LLM provider: %#v", provider)
	}
	if name != "tesseract" {
		t.Fatalf("provider name = %q, want tesseract", name)
	}
	if model != "tesseract" {
		t.Fatalf("model = %q, want tesseract", model)
	}
}
