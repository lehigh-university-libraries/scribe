package hocr

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"html"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/lehigh-university-libraries/htr/pkg/providers"
	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/imageservice"
	"github.com/lehigh-university-libraries/scribe/internal/providerregistry"
	"github.com/lehigh-university-libraries/scribe/internal/safefile"
	"github.com/lehigh-university-libraries/scribe/internal/uploadlimits"
	"github.com/lehigh-university-libraries/scribe/internal/worddetection"
)

type Service struct {
	auditLogger ProviderCallAuditLogger
	registry    providerregistry.Registry
}

type ProviderCallAuditRecord struct {
	WorkspaceID  uint64
	SessionID    string
	ItemImageID  *uint64
	ContextID    *uint64
	Provider     string
	Model        string
	Operation    string
	ErrorMessage string
	HTTPStatus   *int
	DurationMS   int64
}

type ProviderCallAuditLogger func(context.Context, ProviderCallAuditRecord)

const (
	maxProviderAuditErrorBytes = 2 << 10
)

type providerCallMetadata struct {
	WorkspaceID uint64
	SessionID   string
	ItemImageID *uint64
	ContextID   *uint64
}

type providerCallMetadataKey struct{}
type transcriptionOptionsKey struct{}

const (
	defaultTranscriptionPrompt = "Transcribe the handwritten text in this image. Return ONLY the transcribed text with no additional commentary, numbering, or explanation. If the text is not legible or cannot be read, return exactly: not legible."
)

// providerRequestError deliberately does not unwrap its cause. Provider
// libraries may include response bodies, URLs, or credentials in error text;
// allowing that error to escape would expose it through worker logs and audit
// rows. Is preserves cancellation/deadline checks without exposing the cause.
type providerRequestError struct {
	message   string
	cause     error
	status    int
	retryable bool
}

func (e *providerRequestError) Error() string { return e.message }

func (e *providerRequestError) Is(target error) bool {
	return e != nil && e.cause != nil && errors.Is(e.cause, target)
}

type hocrFailureCategory string

const (
	hocrFailureCanceled hocrFailureCategory = "canceled"
	hocrFailureTimeout  hocrFailureCategory = "timeout"
	hocrFailureProvider hocrFailureCategory = "provider"
	hocrFailureInternal hocrFailureCategory = "internal"
)

// logHOCRFailure is the only hOCR processing-error logging boundary. Provider,
// HTTP, filesystem, image-decoder, and subprocess errors can contain document
// text, credentials, response bodies, URLs, and temporary paths, so their Error
// strings are never attached to logs.
func logHOCRFailure(message string, err error, attrs ...any) {
	category := hocrFailureInternal
	switch {
	case errors.Is(err, context.Canceled):
		category = hocrFailureCanceled
	case errors.Is(err, context.DeadlineExceeded):
		category = hocrFailureTimeout
	default:
		var providerFailure *providerRequestError
		if errors.As(err, &providerFailure) {
			category = hocrFailureProvider
			if providerFailure.status != 0 {
				attrs = append(attrs, "http_status", providerFailure.status)
			}
		}
	}
	attrs = append(attrs,
		"category", category,
		"error_type", fmt.Sprintf("%T", err),
	)
	slog.Warn(message, attrs...)
}

type transcriptionOptions struct {
	SystemPrompt string
	Temperature  *float64
}

func NewService() *Service {
	slog.Info("Initializing hOCR service (Tesseract word detection + LLM transcription)")
	return &Service{registry: providerregistry.New(config.Get().Config)}
}

func (s *Service) SetProviderCallAuditLogger(logger ProviderCallAuditLogger) {
	s.auditLogger = logger
}

func (s *Service) auditProviderCall(ctx context.Context, record ProviderCallAuditRecord) {
	if s == nil {
		return
	}
	if record.ErrorMessage != "" {
		record.ErrorMessage = redactProviderError(errors.New(record.ErrorMessage), record.HTTPStatus).Error()
	}
	meta := providerCallMetadataFromContext(ctx)
	if record.WorkspaceID == 0 {
		record.WorkspaceID = meta.WorkspaceID
	}
	if record.SessionID == "" {
		record.SessionID = meta.SessionID
	}
	if record.ItemImageID == nil {
		record.ItemImageID = meta.ItemImageID
	}
	if record.ContextID == nil {
		record.ContextID = meta.ContextID
	}
	for _, secret := range providerAPIKeysFromContext(ctx) {
		if secret == "" {
			continue
		}
		record.ErrorMessage = strings.ReplaceAll(record.ErrorMessage, secret, "[REDACTED]")
	}
	record.ErrorMessage = boundedProviderAuditError(record.ErrorMessage)
	attrs := []any{
		"workspace_id", record.WorkspaceID,
		"session_id", record.SessionID,
		"provider", record.Provider,
		"model", record.Model,
		"operation", record.Operation,
		"duration_ms", record.DurationMS,
	}
	if record.ItemImageID != nil {
		attrs = append(attrs, "item_image_id", *record.ItemImageID)
	}
	if record.ContextID != nil {
		attrs = append(attrs, "context_id", *record.ContextID)
	}
	if record.HTTPStatus != nil {
		attrs = append(attrs, "http_status", *record.HTTPStatus)
	}
	if record.ErrorMessage != "" {
		attrs = append(attrs, "failure", record.ErrorMessage)
		slog.Warn("provider call", attrs...)
	} else {
		slog.Info("provider call", attrs...)
	}
	if s.auditLogger != nil {
		s.auditLogger(ctx, record)
	}
}

func boundedProviderAuditError(value string) string {
	if len(value) <= maxProviderAuditErrorBytes {
		return value
	}
	digest := sha256.Sum256([]byte(value))
	return fmt.Sprintf("[TRUNCATED original_bytes=%d sha256=%x]", len(value), digest[:])
}

func WithProviderCallMetadata(ctx context.Context, workspaceID uint64, sessionID string, itemImageID, contextID *uint64) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	metadata := providerCallMetadataFromContext(ctx)
	if workspaceID != 0 {
		metadata.WorkspaceID = workspaceID
	}
	if strings.TrimSpace(sessionID) != "" {
		metadata.SessionID = strings.TrimSpace(sessionID)
	}
	if itemImageID != nil {
		metadata.ItemImageID = itemImageID
	}
	if contextID != nil {
		metadata.ContextID = contextID
	}
	return context.WithValue(ctx, providerCallMetadataKey{}, providerCallMetadata{
		WorkspaceID: metadata.WorkspaceID,
		SessionID:   metadata.SessionID,
		ItemImageID: metadata.ItemImageID,
		ContextID:   metadata.ContextID,
	})
}

func providerCallMetadataFromContext(ctx context.Context) providerCallMetadata {
	if ctx == nil {
		return providerCallMetadata{}
	}
	meta, _ := ctx.Value(providerCallMetadataKey{}).(providerCallMetadata)
	return meta
}

func providerAPIKeysFromContext(ctx context.Context) []string {
	keys := providerregistry.ContextCredentialValues(ctx)
	runtime := config.Get().Secrets
	for _, value := range []string{runtime.OpenAIAPIKey, runtime.GeminiAPIKey} {
		if value = strings.TrimSpace(value); value != "" {
			keys = append(keys, value)
		}
	}
	return keys
}

// WithTranscriptionOptions attaches context-owned prompt and sampling options
// to every provider call made by an OCR operation.
func WithTranscriptionOptions(ctx context.Context, systemPrompt string, temperature *float64) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	var copiedTemperature *float64
	if temperature != nil {
		value := *temperature
		copiedTemperature = &value
	}
	return context.WithValue(ctx, transcriptionOptionsKey{}, transcriptionOptions{
		SystemPrompt: strings.TrimSpace(systemPrompt),
		Temperature:  copiedTemperature,
	})
}

func promptFromContext(ctx context.Context, taskPrompt string) string {
	taskPrompt = strings.TrimSpace(taskPrompt)
	if ctx == nil {
		return taskPrompt
	}
	options, _ := ctx.Value(transcriptionOptionsKey{}).(transcriptionOptions)
	if options.SystemPrompt == "" {
		return taskPrompt
	}
	if taskPrompt == "" {
		return options.SystemPrompt
	}
	return options.SystemPrompt + "\n\nTask instructions:\n" + taskPrompt
}

func temperatureFromContext(ctx context.Context) float64 {
	if ctx == nil {
		return 0
	}
	options, _ := ctx.Value(transcriptionOptionsKey{}).(transcriptionOptions)
	if options.Temperature == nil {
		return 0
	}
	return *options.Temperature
}

// ProcessingContext carries the parameters from a store.Context into the
// processing pipeline without importing the store package (avoids cycles).
type ProcessingContext struct {
	SegmentationModel     string // "tesseract" | "scribe" | "kraken" | "kraken:<model>"
	TranscriptionProvider string
	TranscriptionModel    string
	Temperature           *float64
	SystemPrompt          string
	// SegmentOnly skips LLM transcription and returns hOCR with line bounding
	// boxes only. Used when the client will handle transcription via a batch job.
	SegmentOnly bool
}

// ProcessImageWithContext runs the full pipeline using the supplied context and
// returns the generated hOCR plus the effective provider/model used.
func (s *Service) ProcessImageWithContext(ctx context.Context, imagePath string, pctx ProcessingContext) (string, string, string, error) {
	goCtx := ctx
	if goCtx == nil {
		goCtx = context.Background()
	}
	goCtx = WithTranscriptionOptions(goCtx, pctx.SystemPrompt, pctx.Temperature)

	width, height, err := s.getImageDimensions(goCtx, imagePath)
	if err != nil {
		return "", "", "", fmt.Errorf("get image dimensions: %w", err)
	}

	selectedWords, selectedProvider, err := s.detectWithModel(goCtx, imagePath, pctx.SegmentationModel, width, height)
	if err != nil {
		return "", "", "", fmt.Errorf("segmentation failed (model=%s): %w", pctx.SegmentationModel, err)
	}
	slog.Info("Word detection complete",
		"segmentation_model", pctx.SegmentationModel,
		"selected_provider", selectedProvider,
		"word_count", len(selectedWords))

	lines := s.groupWordsIntoLines(selectedWords)
	if selectedProvider == "custom" || selectedProvider == "kraken" {
		lines = s.filterValidLines(lines, width)
		lines = s.removeOverlappingLines(lines)
	}

	// SegmentOnly: return line boxes without any transcription.
	if pctx.SegmentOnly {
		slog.Info("Segment-only mode: skipping transcription", "line_count", len(lines))
		return s.generateHOCRFromDetectedLines(lines, width, height), selectedProvider, "", nil
	}

	// For explicit tesseract segmentation or "auto" when tesseract wins, use the
	// detected tesseract text directly without an LLM pass.
	seg := strings.ToLower(strings.TrimSpace(pctx.SegmentationModel))
	if seg == "tesseract" || ((seg == "auto" || seg == "") && selectedProvider == "tesseract") {
		slog.Info("Using tesseract text directly (no LLM)",
			"segmentation_model", seg,
			"line_count", len(lines))
		transcribedWords := s.transcribeTesseractDirect(lines, width)
		// Pass "custom" so generateHOCRFromWords emits one line-span per entry.
		return s.generateHOCRFromWords(transcribedWords, lines, width, height, "custom"), "tesseract", "tesseract", nil
	}

	llmProvider, providerName, model, err := s.initLLMProvider(pctx.TranscriptionProvider, pctx.TranscriptionModel)
	if err != nil {
		return "", "", "", fmt.Errorf("init LLM provider: %w", err)
	}

	transcribedWords, err := s.transcribeWords(goCtx, imagePath, selectedWords, width, height,
		llmProvider, providerName, selectedProvider, lines, model)
	if err != nil {
		return "", "", "", fmt.Errorf("transcribe words: %w", err)
	}

	return s.generateHOCRFromWords(transcribedWords, lines, width, height, selectedProvider), providerName, model, nil
}

// transcribeTesseractDirect converts already-grouped lines of tesseract WordBoxes into
// line-level TranscribedWords by joining the text that tesseract detected in each line.
// This is used by auto mode when tesseract wins the competitive segmentation race,
// letting us skip the LLM transcription step entirely.
func (s *Service) transcribeTesseractDirect(lines [][]worddetection.WordBox, imageWidth int) []TranscribedWord {
	result := make([]TranscribedWord, 0, len(lines))
	for i, line := range lines {
		if len(line) == 0 {
			continue
		}
		minY := line[0].Y
		maxY := line[0].Y + line[0].Height
		var texts []string
		for _, w := range line {
			if w.Y < minY {
				minY = w.Y
			}
			if w.Y+w.Height > maxY {
				maxY = w.Y + w.Height
			}
			if t := strings.TrimSpace(w.Text); t != "" {
				texts = append(texts, t)
			}
		}
		if len(texts) == 0 {
			continue
		}
		result = append(result, TranscribedWord{
			X:          0,
			Y:          minY,
			Width:      imageWidth,
			Height:     maxY - minY,
			Text:       strings.Join(texts, " "),
			Confidence: 90.0,
			LineID:     i,
		})
	}
	return result
}

// detectWithModel selects and runs the appropriate segmentation provider.
// Selection parsing, trusted endpoint routing, and factory construction all
// live in providerregistry so every runtime and catalog consumer agrees.
func (s *Service) detectWithModel(ctx context.Context, imagePath, segModel string, imageWidth, imageHeight int) ([]worddetection.WordBox, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	detector, err := s.registry.NewSegmentor(segModel)
	if err != nil {
		return nil, "", err
	}
	words, provider, err := detector.DetectWords(ctx, imagePath)
	if err != nil {
		return nil, provider, redactSegmentationError(err)
	}
	// A page contains both line and word annotations. Reserving half of the
	// canonical capacity for lines prevents segmentation from creating an
	// uncommittable page or unbounded transcription fan-out.
	if err := worddetection.ValidateBoxes(words, imageWidth, imageHeight, iiif.MaxAnnotationsPerPage/2); err != nil {
		return nil, provider, err
	}
	return words, provider, nil
}

// redactSegmentationError prevents subprocess output, local paths, remote
// response content, and other provider diagnostics from crossing the hOCR
// service boundary. The cause is deliberately not unwrapped; Is retains only
// the cancellation semantics needed by workers and request handlers.
func redactSegmentationError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, context.Canceled):
		return &providerRequestError{message: "segmentation provider request canceled", cause: err}
	case errors.Is(err, context.DeadlineExceeded):
		return &providerRequestError{message: "segmentation provider request timed out", cause: err}
	default:
		return &providerRequestError{message: "segmentation provider request failed", cause: err}
	}
}

func (s *Service) ProcessImageToHOCR(imagePath string) (string, error) {
	return s.processImageToHOCR(context.Background(), imagePath, "", "")
}

func (s *Service) ProcessImageToHOCRWithModel(imagePath, modelOverride string) (string, error) {
	return s.processImageToHOCR(context.Background(), imagePath, "", modelOverride)
}

func (s *Service) ProcessImageToHOCRWithProviderAndModel(imagePath, providerOverride, modelOverride string) (string, error) {
	return s.processImageToHOCR(context.Background(), imagePath, providerOverride, modelOverride)
}

func (s *Service) ProcessImageToHOCRWithContext(ctx context.Context, imagePath, providerOverride, modelOverride string) (string, error) {
	return s.processImageToHOCR(ctx, imagePath, providerOverride, modelOverride)
}

func (s *Service) DetectLinesToHOCR(imagePath string) (string, error) {
	ctx := context.Background()

	width, height, err := s.getImageDimensions(ctx, imagePath)
	if err != nil {
		return "", fmt.Errorf("failed to get image dimensions: %w", err)
	}

	selectedWords, selectedProvider, err := s.detectWithModel(ctx, imagePath, "auto", width, height)
	if err != nil {
		return "", fmt.Errorf("detect lines: %w", err)
	}

	lines := s.groupWordsIntoLines(selectedWords)
	if selectedProvider == "custom" || selectedProvider == "kraken" {
		lines = s.filterValidLines(lines, width)
		lines = s.removeOverlappingLines(lines)
	}

	return s.generateHOCRFromDetectedLines(lines, width, height), nil
}

func (s *Service) generateHOCRFromDetectedLines(lines [][]worddetection.WordBox, width, height int) string {
	boxes := make([]lineVerticalBox, 0, len(lines))
	for i, line := range lines {
		if len(line) == 0 {
			continue
		}
		minY := line[0].Y
		maxY := line[0].Y + line[0].Height
		for _, word := range line {
			if word.Y < minY {
				minY = word.Y
			}
			if word.Y+word.Height > maxY {
				maxY = word.Y + word.Height
			}
		}
		boxes = append(boxes, lineVerticalBox{lineID: i, y1: minY, y2: maxY})
	}
	boxes = normalizeLineVerticalBoxes(boxes, height)

	var out []string
	for _, box := range boxes {
		lineBBox := fmt.Sprintf("bbox %d %d %d %d", 0, box.y1, width, box.y2)
		out = append(out, fmt.Sprintf("<span class='ocr_line' id='line_%d' title='%s'></span>", box.lineID, lineBBox))
	}
	return s.wrapInHOCRDocument(strings.Join(out, "\n"), width, height)
}

func (s *Service) TranscribeRegion(imagePath string, minX, minY, maxX, maxY int, providerOverride, modelOverride string) (string, error) {
	return s.TranscribeRegionWithContext(context.Background(), imagePath, minX, minY, maxX, maxY, providerOverride, modelOverride)
}

func (s *Service) TranscribeRegionWithContext(ctx context.Context, imagePath string, minX, minY, maxX, maxY int, providerOverride, modelOverride string) (string, error) {
	if maxX <= minX || maxY <= minY {
		return "", fmt.Errorf("invalid bbox")
	}
	if minX == 0 && minY == 0 {
		return s.transcribeImageFile(ctx, imagePath, providerOverride, modelOverride, "transcribe_region")
	}
	return s.transcribeRegionFromPath(ctx, imagePath, minX, minY, maxX, maxY, providerOverride, modelOverride)
}

func (s *Service) TranscribeImage(imagePath, providerOverride, modelOverride string) (string, error) {
	return s.transcribeImageFile(context.Background(), imagePath, providerOverride, modelOverride, "transcribe_image")
}

func (s *Service) TranscribeImageWithContext(ctx context.Context, imagePath, providerOverride, modelOverride string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return s.transcribeImageFile(ctx, imagePath, providerOverride, modelOverride, "transcribe_image")
}

func (s *Service) transcribeRegionFromPath(ctx context.Context, imagePath string, minX, minY, maxX, maxY int, providerOverride, modelOverride string) (string, error) {
	if maxX <= minX || maxY <= minY {
		return "", fmt.Errorf("invalid bbox")
	}
	llmProvider, providerName, model, err := s.initLLMProvider(providerOverride, modelOverride)
	if err != nil {
		return "", fmt.Errorf("failed to initialize LLM provider: %w", err)
	}

	lineImagePath, err := s.extractLineImage(ctx, imagePath, minX, minY, maxX, maxY, 0)
	if err != nil {
		return "", fmt.Errorf("failed to extract region image: %w", err)
	}
	defer os.Remove(lineImagePath)

	return s.extractTranscriptionFromImageWithOperation(ctx, llmProvider, providerName, model, lineImagePath, "transcribe_region")
}

func (s *Service) transcribeImageFile(ctx context.Context, imagePath, providerOverride, modelOverride, operation string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	llmProvider, providerName, model, err := s.initLLMProvider(providerOverride, modelOverride)
	if err != nil {
		return "", fmt.Errorf("failed to initialize LLM provider: %w", err)
	}

	return s.extractTranscriptionFromImageWithOperation(ctx, llmProvider, providerName, model, imagePath, operation)
}

func (s *Service) extractTranscriptionFromImageWithOperation(ctx context.Context, llmProvider providers.Client, providerName, model, imagePath, operation string) (string, error) {
	imageData, err := safefile.ReadFileLimit(imagePath, uploadlimits.MaxImageBytes)
	if err != nil {
		return "", fmt.Errorf("failed to read image for transcription: %w", err)
	}
	image := providerImage(imagePath, imageData)

	prompt := promptFromContext(ctx, defaultTranscriptionPrompt)
	config, err := s.providerConfig(providerName, model, prompt, temperatureFromContext(ctx))
	if err != nil {
		return "", err
	}

	text, err := s.extractTextWithRetry(ctx, llmProvider, providerName, config, imagePath, image, operation)
	if err != nil {
		return "", fmt.Errorf("failed to transcribe image: %w", err)
	}

	text = strings.TrimSpace(text)
	if text == "" || s.isRefusalOrIllegible(text) {
		return "", fmt.Errorf("region is not legible")
	}
	return text, nil
}

func (s *Service) extractTextWithRetry(
	ctx context.Context,
	llmProvider providers.Client,
	providerName string,
	providerConfig providers.Config,
	imagePath string,
	image providers.Image,
	operation string,
) (string, error) {
	descriptor, err := s.registry.ResolveProvider(providerName)
	if err != nil {
		return "", err
	}
	retry := descriptor.Limits.Retry
	if retry.MaxAttempts < 1 {
		retry.MaxAttempts = 1
	}

	var lastErr error
	for attempt := 1; attempt <= retry.MaxAttempts; attempt++ {
		text, err := s.executeProvider(ctx, llmProvider, descriptor.ID, providerConfig, imagePath, image, operation)
		if err == nil {
			return text, nil
		}
		err = redactProviderError(err, nil)
		lastErr = err

		if !isRetriableProviderError(err) || attempt == retry.MaxAttempts {
			break
		}

		delay := retry.BaseDelay * time.Duration(1<<(attempt-1))
		if retry.MaxDelay > 0 && delay > retry.MaxDelay {
			delay = retry.MaxDelay
		}
		logHOCRFailure(
			"provider request failed; retrying with backoff",
			err,
			"provider", descriptor.ID,
			"attempt", attempt,
			"max_attempts", retry.MaxAttempts,
			"delay_ms", delay.Milliseconds(),
		)
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(delay):
		}
	}
	return "", lastErr
}

func (s *Service) executeProvider(
	ctx context.Context,
	client providers.Client,
	providerName string,
	cfg providers.Config,
	imagePath string,
	image providers.Image,
	operation string,
) (string, error) {
	descriptor, err := s.registry.ResolveProvider(providerName)
	if err != nil {
		return "", redactProviderError(err, nil)
	}
	var text string
	switch descriptor.Execution {
	case providerregistry.ExecutionTesseract:
		text, err = s.extractTextWithTesseract(ctx, imagePath, operation)
	case providerregistry.ExecutionAdapter:
		if client == nil {
			err = providers.NewError(providers.ErrorInvalidRequest, 0, false, nil)
			break
		}
		text, err = s.extractTextWithProvider(ctx, client, descriptor.ID, cfg, image, operation)
	default:
		err = fmt.Errorf("provider execution mode is not installed")
	}
	return text, redactProviderError(err, nil)
}

func (s *Service) providerConfig(providerName, model, prompt string, temperature float64) (providers.Config, error) {
	return s.registry.ProviderConfig(providerName, model, prompt, temperature)
}

func (s *Service) extractTextWithProvider(
	ctx context.Context,
	client providers.Client,
	providerName string,
	config providers.Config,
	image providers.Image,
	operation string,
) (string, error) {
	started := time.Now()
	result, err := client.Extract(ctx, providers.Request{
		Model:       config.Model,
		Prompt:      config.Prompt,
		Temperature: config.Temperature,
		Image:       image,
	})
	redactedErr := redactProviderError(err, nil)
	record := ProviderCallAuditRecord{
		Provider: providerName, Model: config.Model, Operation: operation,
		DurationMS: time.Since(started).Milliseconds(),
	}
	if strings.TrimSpace(result.EffectiveModel) != "" {
		record.Model = result.EffectiveModel
	}
	if redactedErr != nil {
		record.ErrorMessage = redactedErr.Error()
		if providerErr, ok := redactedErr.(*providerRequestError); ok && providerErr.status != 0 {
			record.HTTPStatus = &providerErr.status
		}
	}
	s.auditProviderCall(ctx, record)
	return result.Text, redactedErr
}

func providerImage(imagePath string, data []byte) providers.Image {
	return providers.Image{
		Data:      data,
		MediaType: detectImageContentType(imagePath, data),
		Filename:  filepath.Base(imagePath),
	}
}

// redactProviderError converts an untrusted provider error into a categorical
// error before it can reach logs, audit persistence, job state, or an API
// response. HTR errors are typed and already redacted; Scribe maps those types
// to its stable job/audit vocabulary without inspecting untrusted text.
func redactProviderError(err error, explicitStatus *int) error {
	if err == nil {
		return nil
	}
	if alreadyRedacted, ok := err.(*providerRequestError); ok {
		return alreadyRedacted
	}
	if errors.Is(err, context.Canceled) {
		return &providerRequestError{message: "provider request canceled", cause: context.Canceled}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &providerRequestError{message: "provider request timed out", cause: context.DeadlineExceeded, retryable: true}
	}

	status := 0
	if explicitStatus != nil && *explicitStatus >= 300 && *explicitStatus <= 599 {
		status = *explicitStatus
	}
	var htrError *providers.Error
	if errors.As(err, &htrError) {
		status = htrError.StatusCode
		message := "provider request failed"
		cause := error(nil)
		switch htrError.Kind {
		case providers.ErrorInvalidRequest:
			message = "provider request was rejected"
		case providers.ErrorAuthentication:
			message = "provider authentication failed"
		case providers.ErrorCanceled:
			message, cause = "provider request canceled", context.Canceled
		case providers.ErrorTimeout:
			message, cause = "provider request timed out", context.DeadlineExceeded
		case providers.ErrorResponseTooLarge:
			message = "provider response exceeded configured limit"
		case providers.ErrorRateLimited:
			message = "provider request was rate limited"
		case providers.ErrorInvalidResponse:
			message = "provider returned an invalid response"
		}
		if status != 0 && (htrError.Kind == providers.ErrorUpstream || htrError.Kind == providers.ErrorRateLimited || htrError.Kind == providers.ErrorAuthentication || htrError.Kind == providers.ErrorInvalidRequest || htrError.Kind == providers.ErrorTimeout) {
			message = fmt.Sprintf("provider request failed with HTTP status %d", status)
		}
		return &providerRequestError{message: message, cause: cause, status: status, retryable: htrError.Retryable}
	}
	if status != 0 {
		return &providerRequestError{
			message:   fmt.Sprintf("provider request failed with HTTP status %d", status),
			status:    status,
			retryable: status == http.StatusTooManyRequests || status >= http.StatusInternalServerError,
		}
	}

	return &providerRequestError{message: "provider request failed"}
}

func isRetriableProviderError(err error) bool {
	if err == nil {
		return false
	}
	if providerErr, ok := err.(*providerRequestError); ok {
		return providerErr.retryable
	}
	return false
}

func (s *Service) processImageToHOCR(ctx context.Context, imagePath, providerOverride, modelOverride string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	// Step 1: Get image dimensions
	width, height, err := s.getImageDimensions(ctx, imagePath)
	if err != nil {
		return "", fmt.Errorf("failed to get image dimensions: %w", err)
	}

	selectedWords, selectedProvider, err := s.detectWithModel(ctx, imagePath, "auto", width, height)
	if err != nil {
		return "", fmt.Errorf("word detection failed: %w", err)
	}

	slog.Info("Selected word detection provider",
		"provider", selectedProvider,
		"word_count", len(selectedWords))

	// Step 3: Group words into lines
	lines := s.groupWordsIntoLines(selectedWords)
	slog.Info("Grouped words into lines", "line_count", len(lines))

	// Step 3b: For custom and kraken line providers, filter out anomalously small lines.
	if selectedProvider == "custom" || selectedProvider == "kraken" {
		originalLineCount := len(lines)
		lines = s.filterValidLines(lines, width)
		slog.Info("Filtered lines for custom provider",
			"original_count", originalLineCount,
			"filtered_count", len(lines),
			"removed", originalLineCount-len(lines))

		// Step 3c: Remove overlapping lines, keeping the largest
		linesBeforeOverlap := len(lines)
		lines = s.removeOverlappingLines(lines)
		slog.Info("Removed overlapping lines",
			"before", linesBeforeOverlap,
			"after", len(lines),
			"removed", linesBeforeOverlap-len(lines))
	}

	// Step 4: Initialize LLM provider
	llmProvider, providerName, model, err := s.initLLMProvider(providerOverride, modelOverride)
	if err != nil {
		return "", fmt.Errorf("failed to initialize LLM provider: %w", err)
	}

	// Step 5: Transcribe words/lines using LLM (line-based if custom provider selected)
	transcribedWords, err := s.transcribeWords(ctx, imagePath, selectedWords, width, height, llmProvider, providerName, selectedProvider, lines, model)
	if err != nil {
		return "", fmt.Errorf("failed to transcribe words: %w", err)
	}

	slog.Info("Word transcription completed", "transcribed_count", len(transcribedWords))

	// Step 6: Generate hOCR
	hocr := s.generateHOCRFromWords(transcribedWords, lines, width, height, selectedProvider)

	return hocr, nil
}

func (s *Service) getImageDimensions(ctx context.Context, imagePath string) (int, int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	f, err := safefile.Open(imagePath)
	if err != nil {
		return 0, 0, fmt.Errorf("open image for dimension lookup: %w", err)
	}
	defer f.Close()
	cfg, format, err := image.DecodeConfig(f)
	if err != nil {
		data, readErr := safefile.ReadFileLimit(imagePath, uploadlimits.MaxImageBytes)
		if readErr != nil {
			return 0, 0, fmt.Errorf("decode image config: %w", err)
		}
		client := imageservice.New()
		if !client.Enabled() {
			return 0, 0, fmt.Errorf("decode image config: %w", err)
		}
		normalized, normalizeErr := client.Normalize(ctx, data, detectImageContentType(imagePath, data))
		if normalizeErr != nil {
			return 0, 0, fmt.Errorf("decode image config: %w", err)
		}
		cfg, format, err = image.DecodeConfig(bytes.NewReader(normalized))
		if err != nil {
			return 0, 0, fmt.Errorf("decode image config: %w", err)
		}
	}
	if err := uploadlimits.ValidateImageDimensions(cfg.Width, cfg.Height); err != nil {
		return 0, 0, fmt.Errorf("invalid %s: %w", format, err)
	}
	return cfg.Width, cfg.Height, nil
}

func detectImageContentType(imagePath string, data []byte) string {
	contentType := http.DetectContentType(data)
	if contentType != "application/octet-stream" {
		return contentType
	}
	switch strings.ToLower(filepath.Ext(imagePath)) {
	case ".jp2", ".j2k", ".jpx":
		return "image/jp2"
	case ".tif", ".tiff":
		return "image/tiff"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		return "image/jpeg"
	}
}

// TranscribedWord represents a word with its bounding box and transcribed text
type TranscribedWord struct {
	X, Y, Width, Height int
	Text                string
	Confidence          float64
	LineID              int
}

// initLLMProvider resolves a registered model and constructs its HTR client.
func (s *Service) initLLMProvider(providerOverride, modelOverride string) (providers.Client, string, string, error) {
	descriptor, err := s.registry.ResolveProvider(providerOverride)
	if err != nil {
		return nil, "", "", err
	}
	model, err := s.registry.EffectiveModel(descriptor.ID, modelOverride)
	if err != nil {
		return nil, "", "", err
	}
	client, err := descriptor.NewClient(model)
	if err != nil {
		return nil, "", "", err
	}
	slog.Info("Initializing transcription provider", "provider", descriptor.ID, "model", model)
	return client, descriptor.ID, model, nil
}

// groupWordsIntoLines groups detected words into text lines based on coordinates
func (s *Service) groupWordsIntoLines(words []worddetection.WordBox) [][]worddetection.WordBox {
	if len(words) == 0 {
		return nil
	}

	// Sort words by Y then X
	sortedWords := make([]worddetection.WordBox, len(words))
	copy(sortedWords, words)
	sort.Slice(sortedWords, func(i, j int) bool {
		yi := sortedWords[i].Y + sortedWords[i].Height/2
		yj := sortedWords[j].Y + sortedWords[j].Height/2
		if abs(yi-yj) <= 20 { // Same line threshold
			return sortedWords[i].X < sortedWords[j].X
		}
		return yi < yj
	})

	var lines [][]worddetection.WordBox
	var currentLine []worddetection.WordBox

	for _, word := range sortedWords {
		if len(currentLine) == 0 {
			currentLine = append(currentLine, word)
			continue
		}

		// Check if this word belongs to current line
		lastWord := currentLine[len(currentLine)-1]
		lastY := lastWord.Y + lastWord.Height/2
		currentY := word.Y + word.Height/2

		if abs(lastY-currentY) <= 20 {
			currentLine = append(currentLine, word)
		} else {
			lines = append(lines, currentLine)
			currentLine = []worddetection.WordBox{word}
		}
	}

	if len(currentLine) > 0 {
		lines = append(lines, currentLine)
	}

	return lines
}

// transcribeWords extracts and transcribes words in batches using the configured
// transcription provider. For line-level detectors such as the custom Scribe
// segmentor and Kraken, it transcribes whole lines instead of individual words.
// The lines parameter contains pre-filtered lines.
func (s *Service) transcribeWords(ctx context.Context, imagePath string, words []worddetection.WordBox, imageWidth, imageHeight int, provider providers.Client, providerName, detectionProvider string, lines [][]worddetection.WordBox, model string) ([]TranscribedWord, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	transcribed := make([]TranscribedWord, 0, len(words))

	batchSize := s.getBatchSize()

	// For line-level detectors, transcribe pre-filtered lines instead of individual words.
	if detectionProvider == "custom" || detectionProvider == "kraken" {
		slog.Info("Using line-based transcription for line detector", "provider", providerName, "model", model, "line_count", len(lines), "detection_provider", detectionProvider)
		return s.transcribeLinesForCustomProvider(ctx, imagePath, lines, imageWidth, imageHeight, provider, providerName, model, batchSize)
	}

	slog.Info("Starting batch word transcription", "provider", providerName, "model", model, "word_count", len(words), "batch_size", batchSize)

	// Filter valid words first
	validWords := make([]worddetection.WordBox, 0, len(words))
	skippedCount := 0
	for i, word := range words {
		// Skip empty words
		if strings.TrimSpace(word.Text) == "" {
			skippedCount++
			continue
		}

		// Validate that this is likely a real word
		if !s.isLikelyWordBox(word, imageWidth, imageHeight) {
			slog.Debug("Skipping non-word detection", "index", i,
				"width", word.Width,
				"height", word.Height)
			skippedCount++
			continue
		}

		validWords = append(validWords, word)
	}

	slog.Info("Filtered words for transcription", "valid", len(validWords), "skipped", skippedCount, "total", len(words))

	// Process words in batches
	for batchStart := 0; batchStart < len(validWords); batchStart += batchSize {
		batchEnd := batchStart + batchSize
		if batchEnd > len(validWords) {
			batchEnd = len(validWords)
		}

		batch := validWords[batchStart:batchEnd]
		batchNum := (batchStart / batchSize) + 1
		totalBatches := (len(validWords) + batchSize - 1) / batchSize

		slog.Info("Processing batch", "batch", batchNum, "total_batches", totalBatches, "words_in_batch", len(batch))

		// Stitch word images together
		stitchedImagePath, err := s.stitchWordImages(ctx, imagePath, batch)
		if err != nil {
			logHOCRFailure("Failed to stitch word images", err, "batch", batchNum)
			continue
		}
		defer os.Remove(stitchedImagePath)

		imageData, err := safefile.ReadFileLimit(stitchedImagePath, uploadlimits.MaxImageBytes)
		if err != nil {
			logHOCRFailure("Failed to read stitched image", err, "batch", batchNum)
			continue
		}
		image := providerImage(stitchedImagePath, imageData)

		// Create prompt for batch transcription
		prompt := promptFromContext(ctx, fmt.Sprintf("There are %d words in this image arranged horizontally. Transcribe each word on a separate line. Return ONLY the words, one per line, with no additional text, numbering, or explanation. If a word is not legible, use an empty line for that position.", len(batch)))

		config, err := s.providerConfig(providerName, model, prompt, temperatureFromContext(ctx))
		if err != nil {
			return nil, err
		}

		text, err := s.executeProvider(ctx, provider, providerName, config, stitchedImagePath, image, "transcribe_word_batch")
		if err != nil {
			logHOCRFailure("Failed to transcribe batch", err, "batch", batchNum)
			continue
		}

		// Parse response - split by newlines
		lines := strings.Split(strings.TrimSpace(text), "\n")

		slog.Debug("Batch transcription result", "batch", batchNum, "expected_words", len(batch), "received_lines", len(lines))

		// Map transcribed words back to their original positions
		for i, word := range batch {
			var transcribedText string
			if i < len(lines) {
				transcribedText = strings.TrimSpace(lines[i])
			}

			// Skip empty transcriptions
			if transcribedText == "" {
				continue
			}

			transcribed = append(transcribed, TranscribedWord{
				X:          word.X,
				Y:          word.Y,
				Width:      word.Width,
				Height:     word.Height,
				Text:       transcribedText,
				Confidence: 90.0, // Slightly lower confidence for batch processing
			})
		}
	}

	slog.Info("Batch transcription completed", "transcribed", len(transcribed), "skipped", skippedCount, "total", len(words))
	return transcribed, nil
}

// transcribeLinesForCustomProvider transcribes whole detected lines for
// line-level detectors such as the custom Scribe segmentor and Kraken. The
// lines parameter should be pre-filtered. Lines are processed independently
// with bounded concurrency.
func (s *Service) transcribeLinesForCustomProvider(ctx context.Context, imagePath string, lines [][]worddetection.WordBox, imageWidth, imageHeight int, provider providers.Client, providerName, model string, batchSize int) ([]TranscribedWord, error) {
	if len(lines) == 0 {
		slog.Info("No lines to transcribe for custom provider")
		return nil, nil
	}
	_ = batchSize

	concurrency := s.getLineTranscriptionConcurrency()
	slog.Info("Transcribing lines for custom provider", "line_count", len(lines), "concurrency", concurrency)

	transcribed := make([]TranscribedWord, 0, len(lines))
	skippedEmpty := 0

	type lineRegion struct {
		lineID    int
		queueIdx  int
		wordCount int
		y1        int
		y2        int
	}
	type lineResult struct {
		word        TranscribedWord
		hasWord     bool
		skippedText bool
	}
	var regions []lineRegion
	var boxes []lineVerticalBox
	for idx, line := range lines {
		if len(line) == 0 {
			continue
		}
		minY := line[0].Y
		maxY := line[0].Y + line[0].Height
		for _, word := range line {
			if word.Y < minY {
				minY = word.Y
			}
			if word.Y+word.Height > maxY {
				maxY = word.Y + word.Height
			}
		}
		regions = append(regions, lineRegion{
			lineID:    idx,
			queueIdx:  idx,
			wordCount: len(line),
			y1:        minY,
			y2:        maxY,
		})
		boxes = append(boxes, lineVerticalBox{lineID: idx, y1: minY, y2: maxY})
	}

	boxes = normalizeLineVerticalBoxes(boxes, imageHeight)
	boxByID := make(map[int]lineVerticalBox, len(boxes))
	for _, box := range boxes {
		boxByID[box.lineID] = box
	}
	sort.Slice(regions, func(i, j int) bool {
		return regions[i].y1 < regions[j].y1
	})
	for i := range regions {
		regions[i].queueIdx = i
	}

	jobs := make(chan lineRegion, len(regions))
	results := make(chan lineResult, len(regions))
	var wg sync.WaitGroup

	worker := func() {
		defer wg.Done()
		for region := range jobs {
			box, ok := boxByID[region.lineID]
			if !ok {
				continue
			}
			minX := 0
			maxX := imageWidth
			minY := box.y1
			maxY := box.y2
			lineWidth := maxX - minX
			lineHeight := maxY - minY

			slog.Info("Processing line",
				"line_index", region.lineID,
				"progress", fmt.Sprintf("%d/%d", region.queueIdx+1, len(regions)),
				"x", minX, "y", minY,
				"width", lineWidth, "height", lineHeight,
				"word_count", region.wordCount)

			lineImagePath, err := s.extractLineImage(ctx, imagePath, minX, minY, maxX, maxY, region.lineID)
			if err != nil {
				logHOCRFailure("Failed to extract line image", err, "line_index", region.lineID)
				continue
			}

			imageData, err := safefile.ReadFileLimit(lineImagePath, uploadlimits.MaxImageBytes)
			if err != nil {
				_ = os.Remove(lineImagePath)
				logHOCRFailure("Failed to read line image", err, "line_index", region.lineID)
				continue
			}
			image := providerImage(lineImagePath, imageData)

			prompt := promptFromContext(ctx, defaultTranscriptionPrompt)
			config, err := s.providerConfig(providerName, model, prompt, temperatureFromContext(ctx))
			if err != nil {
				logHOCRFailure("Failed to configure transcription provider", err, "line_index", region.lineID)
				_ = os.Remove(lineImagePath)
				continue
			}

			text, err := s.executeProvider(ctx, provider, providerName, config, lineImagePath, image, "transcribe_line")
			_ = os.Remove(lineImagePath)
			if err != nil {
				logHOCRFailure("Failed to transcribe line", err, "line_index", region.lineID)
				continue
			}

			transcribedText := strings.TrimSpace(text)
			if transcribedText == "" {
				slog.Info("Line transcribed as empty, excluding from hOCR", "line_index", region.lineID)
				results <- lineResult{skippedText: true}
				continue
			}

			if s.isRefusalOrIllegible(transcribedText) {
				slog.Info("Line marked as illegible or refusal, excluding from hOCR",
					"line_index", region.lineID,
					"response_length", utf8.RuneCountInString(transcribedText))
				results <- lineResult{skippedText: true}
				continue
			}

			slog.Info("Line transcribed successfully",
				"line_index", region.lineID,
				"text_length", utf8.RuneCountInString(transcribedText))

			results <- lineResult{
				hasWord: true,
				word: TranscribedWord{
					X:          minX,
					Y:          minY,
					Width:      lineWidth,
					Height:     lineHeight,
					Text:       transcribedText,
					Confidence: 85.0,
					LineID:     region.lineID,
				},
			}
		}
	}

	workerCount := concurrency
	if workerCount > len(regions) {
		workerCount = len(regions)
	}
	if workerCount < 1 {
		workerCount = 1
	}
	wg.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		go worker()
	}
	for _, region := range regions {
		jobs <- region
	}
	close(jobs)
	wg.Wait()
	close(results)

	for result := range results {
		if result.skippedText {
			skippedEmpty++
		}
		if result.hasWord {
			transcribed = append(transcribed, result.word)
		}
	}

	slog.Info("Line transcription completed",
		"total_lines", len(lines),
		"transcribed_lines", len(transcribed),
		"skipped_empty", skippedEmpty)
	return transcribed, nil
}

func (s *Service) getLineTranscriptionConcurrency() int {
	if v := config.Get().Config.LLM.LineTranscribeConcurrency; v > 0 {
		return v
	}
	return config.DefaultLineTranscribeConcurrency
}

// isRefusalOrIllegible checks if the LLM response indicates refusal or illegibility
func (s *Service) isRefusalOrIllegible(text string) bool {
	textLower := strings.ToLower(text)

	// Common refusal patterns
	refusalPatterns := []string{
		"not legible",
		"illegible",
		"cannot transcribe",
		"can't transcribe",
		"unable to transcribe",
		"cannot read",
		"can't read",
		"unable to read",
		"i am sorry",
		"i'm sorry",
		"i apologize",
		"as an ai",
		"as a language model",
		"i cannot",
		"i can't",
		"no text visible",
		"no text found",
		"blank image",
		"empty image",
	}

	for _, pattern := range refusalPatterns {
		if strings.Contains(textLower, pattern) {
			return true
		}
	}

	return false
}

// filterValidLines filters out lines that are anomalously small compared to the average
// This removes detection errors that are too small to be real lines of text
func (s *Service) filterValidLines(lines [][]worddetection.WordBox, imageWidth int) [][]worddetection.WordBox {
	if len(lines) == 0 {
		return lines
	}

	// Calculate width of each line
	lineWidths := make([]int, len(lines))
	for i, line := range lines {
		if len(line) == 0 {
			continue
		}

		// Find min and max X coordinates
		minX := line[0].X
		maxX := line[0].X + line[0].Width

		for _, word := range line {
			if word.X < minX {
				minX = word.X
			}
			if word.X+word.Width > maxX {
				maxX = word.X + word.Width
			}
		}

		lineWidths[i] = maxX - minX
	}

	// Calculate average line width
	totalWidth := 0
	validCount := 0
	for _, width := range lineWidths {
		if width > 0 {
			totalWidth += width
			validCount++
		}
	}

	if validCount == 0 {
		return lines
	}

	avgWidth := float64(totalWidth) / float64(validCount)

	// Calculate median for more robust filtering
	sortedWidths := make([]int, len(lineWidths))
	copy(sortedWidths, lineWidths)
	sort.Ints(sortedWidths)
	medianWidth := float64(sortedWidths[len(sortedWidths)/2])

	// Use the larger of average or median as reference
	referenceWidth := avgWidth
	if medianWidth > avgWidth {
		referenceWidth = medianWidth
	}

	slog.Debug("Line width statistics",
		"avg_width", avgWidth,
		"median_width", medianWidth,
		"reference_width", referenceWidth,
		"image_width", imageWidth)

	// Filter lines based on multiple criteria
	var validLines [][]worddetection.WordBox
	minAbsoluteWidth := int(float64(imageWidth) * 0.15) // At least 15% of image width
	minRelativeWidth := int(referenceWidth * 0.35)      // At least 35% of reference width

	for i, line := range lines {
		width := lineWidths[i]

		// Skip empty lines
		if len(line) == 0 || width == 0 {
			slog.Debug("Skipping empty line", "line_index", i)
			continue
		}

		// Check if line meets minimum width requirements
		if width < minAbsoluteWidth {
			slog.Debug("Skipping line - too narrow (absolute)",
				"line_index", i,
				"width", width,
				"min_absolute", minAbsoluteWidth,
				"percent_of_image", float64(width)/float64(imageWidth)*100)
			continue
		}

		if width < minRelativeWidth {
			slog.Debug("Skipping line - too narrow (relative)",
				"line_index", i,
				"width", width,
				"min_relative", minRelativeWidth,
				"percent_of_reference", float64(width)/referenceWidth*100)
			continue
		}

		validLines = append(validLines, line)
	}

	return validLines
}

// removeOverlappingLines removes overlapping lines, keeping the one with the largest dimension
func (s *Service) removeOverlappingLines(lines [][]worddetection.WordBox) [][]worddetection.WordBox {
	if len(lines) <= 1 {
		return lines
	}

	// Calculate bounding boxes for all lines
	type lineBBox struct {
		minX, minY, maxX, maxY int
		width, height, area    int
		index                  int
	}

	lineBBoxes := make([]lineBBox, len(lines))
	for i, line := range lines {
		if len(line) == 0 {
			continue
		}

		minX, minY := line[0].X, line[0].Y
		maxX, maxY := line[0].X+line[0].Width, line[0].Y+line[0].Height

		for _, word := range line {
			if word.X < minX {
				minX = word.X
			}
			if word.Y < minY {
				minY = word.Y
			}
			if word.X+word.Width > maxX {
				maxX = word.X + word.Width
			}
			if word.Y+word.Height > maxY {
				maxY = word.Y + word.Height
			}
		}

		width := maxX - minX
		height := maxY - minY
		lineBBoxes[i] = lineBBox{
			minX:   minX,
			minY:   minY,
			maxX:   maxX,
			maxY:   maxY,
			width:  width,
			height: height,
			area:   width * height,
			index:  i,
		}
	}

	// Track which lines to keep
	keep := make([]bool, len(lines))
	for i := range keep {
		keep[i] = true
	}

	// Check all pairs for overlaps
	for i := 0; i < len(lineBBoxes); i++ {
		if !keep[i] {
			continue
		}

		for j := i + 1; j < len(lineBBoxes); j++ {
			if !keep[j] {
				continue
			}

			bbox1 := lineBBoxes[i]
			bbox2 := lineBBoxes[j]

			// Check if bounding boxes overlap
			if s.boundingBoxesOverlap(bbox1.minX, bbox1.minY, bbox1.maxX, bbox1.maxY,
				bbox2.minX, bbox2.minY, bbox2.maxX, bbox2.maxY) {

				// Calculate overlap area
				overlapMinX := max(bbox1.minX, bbox2.minX)
				overlapMinY := max(bbox1.minY, bbox2.minY)
				overlapMaxX := min(bbox1.maxX, bbox2.maxX)
				overlapMaxY := min(bbox1.maxY, bbox2.maxY)

				overlapWidth := overlapMaxX - overlapMinX
				overlapHeight := overlapMaxY - overlapMinY
				overlapArea := overlapWidth * overlapHeight

				// Calculate overlap percentage relative to smaller box
				smallerArea := min(bbox1.area, bbox2.area)
				overlapPercent := float64(overlapArea) / float64(smallerArea) * 100

				// If overlap is significant (>30%), keep only the larger box
				if overlapPercent > 30 {
					if bbox1.area >= bbox2.area {
						keep[j] = false
						slog.Debug("Removing overlapping line (keeping larger)",
							"kept_line", i,
							"kept_area", bbox1.area,
							"removed_line", j,
							"removed_area", bbox2.area,
							"overlap_percent", overlapPercent)
					} else {
						keep[i] = false
						slog.Debug("Removing overlapping line (keeping larger)",
							"kept_line", j,
							"kept_area", bbox2.area,
							"removed_line", i,
							"removed_area", bbox1.area,
							"overlap_percent", overlapPercent)
						break // Exit inner loop since line i is removed
					}
				}
			}
		}
	}

	// Build result with only kept lines
	var result [][]worddetection.WordBox
	for i, shouldKeep := range keep {
		if shouldKeep {
			result = append(result, lines[i])
		}
	}

	return result
}

// boundingBoxesOverlap checks if two bounding boxes overlap
func (s *Service) boundingBoxesOverlap(x1min, y1min, x1max, y1max, x2min, y2min, x2max, y2max int) bool {
	// Boxes don't overlap if one is completely to the left/right/above/below the other
	if x1max <= x2min || x2max <= x1min {
		return false
	}
	if y1max <= y2min || y2max <= y1min {
		return false
	}
	return true
}

// min returns the minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// isLikelyWordBox validates whether a detected region is likely to be a real word
// Uses relative sizing based on image dimensions to adapt to different image resolutions
func (s *Service) isLikelyWordBox(box worddetection.WordBox, imageWidth, imageHeight int) bool {
	// Check 1: Minimum size - too small is likely noise
	// Use relative sizing: min 0.5% of image width and 0.8% of image height.
	minWidth := int(float64(imageWidth) * 0.005)
	minHeight := int(float64(imageHeight) * 0.01)

	// Ensure absolute minimums for very small images
	if minWidth < 10 {
		minWidth = 10
	}
	if minHeight < 10 {
		minHeight = 10
	}

	if box.Width < minWidth || box.Height < minHeight {
		return false
	}

	// Check 2: Maximum size - too large is likely not a single word
	// Use relative sizing: max 25% of image width and 10% of image height
	maxWidth := int(float64(imageWidth) * 0.25)
	maxHeight := int(float64(imageHeight) * 0.10)

	// Cap absolute maximums for very large images
	if maxWidth > 500 {
		maxWidth = 500
	}
	if maxHeight > 200 {
		maxHeight = 200
	}

	if box.Width > maxWidth || box.Height > maxHeight {
		return false
	}

	// Check 3: Aspect ratio - words are typically wider than tall
	// Reject very tall/narrow regions (like vertical lines or borders)
	aspectRatio := float64(box.Width) / float64(box.Height)
	if aspectRatio < 0.3 || aspectRatio > 15 {
		return false
	}

	// Check 4: Detected text should have reasonable characters
	// Accept all Unicode writing systems and numeric-only tokens. OCR input is
	// not assumed to be English or Latin-script text.
	word := strings.TrimSpace(box.Text)
	if word == "" {
		return false
	}

	hasLetterOrNumber := false
	specialCharCount := 0
	runeCount := 0
	for _, char := range word {
		runeCount++
		if unicode.IsLetter(char) || unicode.IsNumber(char) {
			hasLetterOrNumber = true
		} else if !unicode.IsMark(char) {
			specialCharCount++
		}
	}

	if !hasLetterOrNumber || runeCount == 0 || float64(specialCharCount)/float64(runeCount) > 0.5 {
		return false
	}

	return true
}

func (s *Service) extractTextWithTesseract(ctx context.Context, imagePath, operation string) (string, error) {
	started := time.Now()
	width, height, err := s.getImageDimensions(ctx, imagePath)
	if err != nil {
		return "", err
	}
	words, _, err := s.detectWithModel(ctx, imagePath, "tesseract", width, height)
	record := ProviderCallAuditRecord{
		Provider: "tesseract", Model: "tesseract", Operation: operation,
		DurationMS: time.Since(started).Milliseconds(),
	}
	if err != nil {
		record.ErrorMessage = err.Error()
		s.auditProviderCall(ctx, record)
		return "", err
	}

	lines := s.groupWordsIntoLines(words)
	textLines := make([]string, 0, len(lines))
	for _, line := range lines {
		parts := make([]string, 0, len(line))
		for _, word := range line {
			if value := strings.TrimSpace(word.Text); value != "" {
				parts = append(parts, value)
			}
		}
		if len(parts) > 0 {
			textLines = append(textLines, strings.Join(parts, " "))
		}
	}
	text := strings.Join(textLines, "\n")
	s.auditProviderCall(ctx, record)
	return text, nil
}

// getBatchSize returns the batch size for word transcription.
func (s *Service) getBatchSize() int {
	if v := config.Get().Config.LLM.BatchSize; v > 0 {
		return v
	}
	return config.DefaultLLMBatchSize
}

// generateHOCRFromWords generates hOCR output from transcribed words and
// detected lines. For line-level detectors, each TranscribedWord represents a
// full line.
func (s *Service) generateHOCRFromWords(transcribedWords []TranscribedWord, lines [][]worddetection.WordBox, width, height int, detectionProvider string) string {
	var hocrLines []string

	// For line-level detectors, each TranscribedWord is a full line.
	if detectionProvider == "custom" || detectionProvider == "kraken" {
		type customLine struct {
			text       string
			confidence float64
			y1         int
			y2         int
		}

		customLines := make([]customLine, 0, len(transcribedWords))
		for _, line := range transcribedWords {
			customLines = append(customLines, customLine{
				text:       line.Text,
				confidence: line.Confidence,
				y1:         line.Y,
				y2:         line.Y + line.Height,
			})
		}

		sort.Slice(customLines, func(i, j int) bool {
			return customLines[i].y1 < customLines[j].y1
		})
		customBoxes := make([]lineVerticalBox, 0, len(customLines))
		for i, line := range customLines {
			customBoxes = append(customBoxes, lineVerticalBox{
				lineID: i,
				y1:     line.y1,
				y2:     line.y2,
			})
		}
		customBoxes = normalizeLineVerticalBoxes(customBoxes, height)

		for i := range customBoxes {
			lineID := customBoxes[i].lineID
			lineBBox := fmt.Sprintf("bbox %d %d %d %d", 0, customBoxes[i].y1, width, customBoxes[i].y2)
			lineSpan := fmt.Sprintf("<span class='ocr_line' id='line_%d' title='%s'>", lineID, lineBBox)

			wordBBox := lineBBox
			wordSpan := fmt.Sprintf("<span class='ocrx_word' id='word_%d_0' title='%s; x_wconf %.0f'>%s</span>",
				lineID, wordBBox, customLines[i].confidence, html.EscapeString(customLines[i].text))

			lineSpan += wordSpan + "</span>"
			hocrLines = append(hocrLines, lineSpan)
		}

		return s.wrapInHOCRDocument(strings.Join(hocrLines, "\n"), width, height)
	}

	// For tesseract provider, use the original word-based grouping logic
	// Group transcribed words by line based on Y-coordinate proximity
	lineWords := make([][]TranscribedWord, len(lines))

	// For each transcribed word, find which line it belongs to
	for _, word := range transcribedWords {
		wordCenterY := word.Y + word.Height/2
		bestLineIdx := -1
		minDistance := int(^uint(0) >> 1) // Max int

		for lineIdx, line := range lines {
			if len(line) == 0 {
				continue
			}
			// Calculate line center Y
			lineCenterY := line[0].Y + line[0].Height/2
			distance := abs(wordCenterY - lineCenterY)
			if distance < minDistance {
				minDistance = distance
				bestLineIdx = lineIdx
			}
		}

		if bestLineIdx >= 0 && minDistance <= 20 {
			lineWords[bestLineIdx] = append(lineWords[bestLineIdx], word)
		}
	}

	type lineWithWords struct {
		lineID int
		words  []TranscribedWord
	}

	lineGroups := make([]lineWithWords, 0, len(lineWords))
	lineBoxes := make([]lineVerticalBox, 0, len(lineWords))
	for lineID, lineWordList := range lineWords {
		if len(lineWordList) == 0 {
			continue
		}
		minY := lineWordList[0].Y
		maxY := lineWordList[0].Y + lineWordList[0].Height
		for _, word := range lineWordList {
			if word.Y < minY {
				minY = word.Y
			}
			if word.Y+word.Height > maxY {
				maxY = word.Y + word.Height
			}
		}
		lineGroups = append(lineGroups, lineWithWords{lineID: lineID, words: lineWordList})
		lineBoxes = append(lineBoxes, lineVerticalBox{lineID: lineID, y1: minY, y2: maxY})
	}

	lineBoxes = normalizeLineVerticalBoxes(lineBoxes, height)
	boxByID := make(map[int]lineVerticalBox, len(lineBoxes))
	for _, box := range lineBoxes {
		boxByID[box.lineID] = box
	}

	for _, group := range lineGroups {
		box, ok := boxByID[group.lineID]
		if !ok {
			continue
		}

		filtered := make([]TranscribedWord, 0, len(group.words))
		for _, word := range group.words {
			centerY := word.Y + word.Height/2
			if centerY >= box.y1 && centerY <= box.y2 {
				filtered = append(filtered, word)
			}
		}
		if len(filtered) == 0 {
			continue
		}

		sort.Slice(filtered, func(i, j int) bool {
			return filtered[i].X < filtered[j].X
		})

		lineBBox := fmt.Sprintf("bbox %d %d %d %d", 0, box.y1, width, box.y2)
		lineSpan := fmt.Sprintf("<span class='ocr_line' id='line_%d' title='%s'>", group.lineID, lineBBox)

		var wordSpans []string
		for i, word := range filtered {
			wordBBox := fmt.Sprintf("bbox %d %d %d %d", word.X, word.Y, word.X+word.Width, word.Y+word.Height)
			wordSpan := fmt.Sprintf("<span class='ocrx_word' id='word_%d_%d' title='%s; x_wconf %.0f'>%s</span>",
				group.lineID, i, wordBBox, word.Confidence, html.EscapeString(word.Text))
			wordSpans = append(wordSpans, wordSpan)
		}

		lineSpan += strings.Join(wordSpans, " ") + "</span>"
		hocrLines = append(hocrLines, lineSpan)
	}

	return s.wrapInHOCRDocument(strings.Join(hocrLines, "\n"), width, height)
}

type lineVerticalBox struct {
	lineID int
	y1     int
	y2     int
}

func normalizeLineVerticalBoxes(boxes []lineVerticalBox, imageHeight int) []lineVerticalBox {
	if len(boxes) == 0 {
		return boxes
	}

	for i := range boxes {
		if boxes[i].y1 < 0 {
			boxes[i].y1 = 0
		}
		if boxes[i].y2 > imageHeight {
			boxes[i].y2 = imageHeight
		}
		if boxes[i].y2 < boxes[i].y1 {
			boxes[i].y2 = boxes[i].y1
		}
	}

	sort.Slice(boxes, func(i, j int) bool {
		return boxes[i].y1 < boxes[j].y1
	})

	for i := 0; i < len(boxes)-1; i++ {
		boundary := (boxes[i].y2 + boxes[i+1].y1) / 2
		if boundary < boxes[i].y1 {
			boundary = boxes[i].y1
		}
		if boundary > boxes[i+1].y2 {
			boundary = boxes[i+1].y2
		}

		boxes[i].y2 = boundary
		nextStart := boundary + 1
		if nextStart > boxes[i+1].y2 {
			nextStart = boxes[i+1].y2
		}
		if nextStart < boxes[i+1].y1 {
			nextStart = boxes[i+1].y1
		}
		boxes[i+1].y1 = nextStart
	}

	return boxes
}

// wrapInHOCRDocument wraps content in a complete hOCR document
func (s *Service) wrapInHOCRDocument(content string, width, height int) string {
	bbox := fmt.Sprintf("bbox 0 0 %d %d", width, height)
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.0 Transitional//EN" "http://www.w3.org/TR/xhtml1/DTD/xhtml1-transitional.dtd">
<html xmlns="http://www.w3.org/1999/xhtml" xml:lang="en" lang="en">
<head>
<title></title>
<meta http-equiv="Content-Type" content="text/html;charset=utf-8" />
<meta name='ocr-system' content='Scribe-tesseract-llm' />
<meta name='ocr-capabilities' content='ocr_page ocr_carea ocr_par ocr_line ocrx_word' />
</head>
<body>
<div class='ocr_page' id='page_1' title='%s'>
%s
</div>
</body>
</html>`, bbox, content)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
