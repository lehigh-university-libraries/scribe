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
	defaultTranscriptionPrompt = "Transcribe the text in this image, preserving its spelling, punctuation, language, and script. Return ONLY the transcribed text with no additional commentary, numbering, or explanation. If the text is not legible or cannot be read, return exactly: not legible."
)

var (
	// ErrNoTranscription means the provider completed normally but the selected
	// image region contained no readable text. Callers may isolate this outcome
	// to one segment without treating provider or image-pipeline failures as
	// equivalent.
	ErrNoTranscription = errors.New("provider returned no transcription")
	// ErrPermanentProviderRequest classifies a redacted provider failure that
	// cannot succeed when the same job input is retried, such as bad
	// credentials or a rejected model request.
	ErrPermanentProviderRequest = errors.New("permanent provider request failure")
	// ErrRetryableProviderRequest classifies a redacted transient provider
	// failure. It carries no remote response body, URL, or credential detail.
	ErrRetryableProviderRequest = errors.New("retryable provider request failure")
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
	permanent bool
}

func (e *providerRequestError) Error() string { return e.message }

func (e *providerRequestError) Is(target error) bool {
	if e == nil {
		return false
	}
	if target == ErrPermanentProviderRequest {
		return e.permanent
	}
	if target == ErrRetryableProviderRequest {
		return e.retryable
	}
	return e.cause != nil && errors.Is(e.cause, target)
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

func NewService(options ...providerregistry.Option) *Service {
	slog.Info("Initializing hOCR service (registered line segmentation + LLM transcription)")
	return &Service{registry: providerregistry.New(config.Get().Config, options...)}
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
		attrs = append(attrs, "category", hocrFailureProvider, "failure", record.ErrorMessage)
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
	SegmentationModel     string // Server-registered line segmentor selection.
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

	// SegmentOnly: return line boxes without any transcription.
	if pctx.SegmentOnly {
		slog.Info("Segment-only mode: skipping transcription", "line_count", len(lines))
		return s.generateHOCRFromDetectedLines(lines, width, height), selectedProvider, "", nil
	}

	llmProvider, providerName, model, err := s.initLLMProvider(pctx.TranscriptionProvider, pctx.TranscriptionModel)
	if err != nil {
		return "", "", "", fmt.Errorf("init LLM provider: %w", err)
	}

	transcribedWords, err := s.transcribeLines(goCtx, imagePath, lines, llmProvider, providerName, model)
	if err != nil {
		return "", "", "", fmt.Errorf("transcribe words: %w", err)
	}

	return s.generateHOCRFromWords(transcribedWords, width, height), providerName, model, nil
}

// detectWithModel selects and runs the appropriate segmentation provider.
// Selection parsing, trusted endpoint routing, and factory construction all
// live in providerregistry so every runtime and catalog consumer agrees.
func (s *Service) detectWithModel(ctx context.Context, imagePath, segModel string, imageWidth, imageHeight int) ([]worddetection.WordBox, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	started := time.Now()
	model := strings.TrimSpace(segModel)
	if model == "" {
		model = s.registry.DefaultSegmentation()
	}
	audit := func(operationErr error) error {
		redactedErr := redactSegmentationError(operationErr)
		record := ProviderCallAuditRecord{
			Provider: "segmentor", Model: model, Operation: "segment_image",
			DurationMS: time.Since(started).Milliseconds(),
		}
		if redactedErr != nil {
			record.ErrorMessage = redactedErr.Error()
			var providerErr *providerRequestError
			if errors.As(redactedErr, &providerErr) && providerErr.status != 0 {
				record.HTTPStatus = &providerErr.status
			}
		}
		s.auditProviderCall(ctx, record)
		return redactedErr
	}
	detector, err := s.registry.NewSegmentor(segModel)
	if err != nil {
		return nil, "", audit(err)
	}
	words, provider, err := detector.DetectWords(ctx, imagePath)
	if err != nil {
		return nil, provider, audit(err)
	}
	// A page contains both line and word annotations. Reserving half of the
	// canonical capacity for lines prevents segmentation from creating an
	// uncommittable page or unbounded transcription fan-out.
	if err := worddetection.ValidateBoxes(words, imageWidth, imageHeight, iiif.MaxAnnotationsPerPage/2); err != nil {
		return nil, provider, audit(err)
	}
	_ = audit(nil)
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
	var htrError *providers.Error
	if errors.As(err, &htrError) {
		return redactProviderError(err, nil)
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

// SafeProviderFailureMessage returns only Scribe's fixed categorical provider
// message. Callers may expose it inside an already-authorized resource without
// leaking upstream bodies, URLs, credentials, paths, or model output.
func SafeProviderFailureMessage(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	var redacted *providerRequestError
	if errors.As(err, &redacted) && redacted != nil {
		return redacted.Error(), true
	}
	var htrError *providers.Error
	if !errors.As(err, &htrError) {
		return "", false
	}
	redactedErr := redactProviderError(htrError, nil)
	if !errors.As(redactedErr, &redacted) || redacted == nil {
		return "", false
	}
	return redacted.Error(), true
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
		return "", ErrNoTranscription
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
		retryable := htrError.Retryable
		permanent := !retryable
		switch htrError.Kind {
		case providers.ErrorInvalidRequest:
			message = "provider request was rejected"
			permanent = true
			retryable = false
		case providers.ErrorAuthentication:
			message = "provider authentication failed"
			permanent = true
			retryable = false
		case providers.ErrorCanceled:
			message, cause = "provider request canceled", context.Canceled
			retryable = false
			permanent = false
		case providers.ErrorTimeout:
			message, cause = "provider request timed out", context.DeadlineExceeded
			retryable = true
			permanent = false
		case providers.ErrorResponseTooLarge:
			message = "provider response exceeded configured limit"
		case providers.ErrorRateLimited:
			message = "provider request was rate limited"
			retryable = true
			permanent = false
		case providers.ErrorInvalidResponse:
			message = "provider returned an invalid response"
		}
		if status != 0 && (htrError.Kind == providers.ErrorUpstream || htrError.Kind == providers.ErrorRateLimited || htrError.Kind == providers.ErrorAuthentication || htrError.Kind == providers.ErrorInvalidRequest || htrError.Kind == providers.ErrorTimeout) {
			message = fmt.Sprintf("provider request failed with HTTP status %d", status)
		}
		return &providerRequestError{message: message, cause: cause, status: status, retryable: retryable, permanent: permanent}
	}
	if status != 0 {
		retryable := status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
		return &providerRequestError{
			message:   fmt.Sprintf("provider request failed with HTTP status %d", status),
			status:    status,
			retryable: retryable,
			permanent: !retryable,
		}
	}

	return &providerRequestError{message: "provider request failed", permanent: true}
}

func isRetriableProviderError(err error) bool {
	return errors.Is(err, ErrRetryableProviderRequest)
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

// transcribeLines transcribes each validated segmentor crop independently,
// with bounded concurrency.
func (s *Service) transcribeLines(ctx context.Context, imagePath string, lines [][]worddetection.WordBox, provider providers.Client, providerName, model string) ([]TranscribedWord, error) {
	if len(lines) == 0 {
		slog.Info("No lines to transcribe")
		return nil, nil
	}

	concurrency := s.getLineTranscriptionConcurrency()
	slog.Info("Transcribing lines", "line_count", len(lines), "concurrency", concurrency)

	transcribed := make([]TranscribedWord, 0, len(lines))
	skippedEmpty := 0

	type lineRegion struct {
		lineID    int
		queueIdx  int
		wordCount int
		x1, x2    int
		y1        int
		y2        int
	}
	type lineResult struct {
		word        TranscribedWord
		hasWord     bool
		skippedText bool
	}
	var regions []lineRegion
	for idx, line := range lines {
		if len(line) == 0 {
			continue
		}
		box := line[0]
		regions = append(regions, lineRegion{lineID: idx, queueIdx: idx, wordCount: len(line), x1: box.X, x2: box.X + box.Width, y1: box.Y, y2: box.Y + box.Height})
	}

	jobs := make(chan lineRegion, len(regions))
	results := make(chan lineResult, len(regions))
	var wg sync.WaitGroup

	worker := func() {
		defer wg.Done()
		for region := range jobs {
			minX := region.x1
			maxX := region.x2
			minY := region.y1
			maxY := region.y2
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

// wrapInHOCRDocument wraps content in a complete hOCR document
func (s *Service) wrapInHOCRDocument(content string, width, height int) string {
	bbox := fmt.Sprintf("bbox 0 0 %d %d", width, height)
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.0 Transitional//EN" "http://www.w3.org/TR/xhtml1/DTD/xhtml1-transitional.dtd">
<html xmlns="http://www.w3.org/1999/xhtml" xml:lang="en" lang="en">
<head>
<title></title>
<meta http-equiv="Content-Type" content="text/html;charset=utf-8" />
<meta name='ocr-system' content='Scribe-segmented-llm' />
<meta name='ocr-capabilities' content='ocr_page ocr_carea ocr_par ocr_line ocrx_word' />
</head>
<body>
<div class='ocr_page' id='page_1' title='%s'>
%s
</div>
</body>
</html>`, bbox, content)
}

// groupWordsIntoLines preserves model-defined chunks and reading order.
func (s *Service) groupWordsIntoLines(boxes []worddetection.WordBox) [][]worddetection.WordBox {
	lines := make([][]worddetection.WordBox, len(boxes))
	for index, box := range boxes {
		lines[index] = []worddetection.WordBox{box}
	}
	return lines
}

func (s *Service) generateHOCRFromDetectedLines(lines [][]worddetection.WordBox, width, height int) string {
	var spans []string
	for index, line := range lines {
		if len(line) == 0 {
			continue
		}
		box := line[0]
		spans = append(spans, fmt.Sprintf("<span class='ocr_line' id='line_%d' title='bbox %d %d %d %d'></span>", index, box.X, box.Y, box.X+box.Width, box.Y+box.Height))
	}
	return s.wrapInHOCRDocument(strings.Join(spans, "\n"), width, height)
}

func (s *Service) generateHOCRFromWords(words []TranscribedWord, width, height int) string {
	sort.SliceStable(words, func(i, j int) bool { return words[i].LineID < words[j].LineID })
	var spans []string
	for _, box := range words {
		bbox := fmt.Sprintf("bbox %d %d %d %d", box.X, box.Y, box.X+box.Width, box.Y+box.Height)
		spans = append(spans, fmt.Sprintf("<span class='ocr_line' id='line_%d' title='%s'><span class='ocrx_word' id='word_%d_0' title='%s; x_wconf %.0f'>%s</span></span>", box.LineID, bbox, box.LineID, bbox, box.Confidence, html.EscapeString(box.Text)))
	}
	return s.wrapInHOCRDocument(strings.Join(spans, "\n"), width, height)
}

// Only the exact requested sentinel means unreadable; document prose can
// legitimately contain words such as "illegible" or "I cannot".
func (*Service) isRefusalOrIllegible(text string) bool {
	return strings.EqualFold(strings.TrimSpace(text), "not legible")
}
