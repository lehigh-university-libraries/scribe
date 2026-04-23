package hocr

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lehigh-university-libraries/htr/pkg/ollama"
	"github.com/lehigh-university-libraries/htr/pkg/openai"
	"github.com/lehigh-university-libraries/htr/pkg/providers"
	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/segmentor"
	"github.com/lehigh-university-libraries/scribe/internal/worddetection"
)

type Service struct {
	auditLogger ProviderCallAuditLogger
}

type ProviderCallAuditRecord struct {
	SessionID    string
	ItemImageID  *uint64
	ContextID    *uint64
	Provider     string
	Model        string
	Operation    string
	Prompt       string
	RequestJSON  string
	ResponseJSON string
	ErrorMessage string
	HTTPStatus   *int
}

type ProviderCallAuditLogger func(context.Context, ProviderCallAuditRecord)

type providerCallMetadata struct {
	SessionID   string
	ItemImageID *uint64
	ContextID   *uint64
}

type providerCallMetadataKey struct{}
type providerAPIKeyKey struct{}

func NewService() *Service {
	slog.Info("Initializing hOCR service (Tesseract word detection + LLM transcription)")
	return &Service{}
}

func (s *Service) SetProviderCallAuditLogger(logger ProviderCallAuditLogger) {
	s.auditLogger = logger
}

func (s *Service) auditProviderCall(ctx context.Context, record ProviderCallAuditRecord) {
	if s == nil || s.auditLogger == nil {
		return
	}
	meta := providerCallMetadataFromContext(ctx)
	if record.SessionID == "" {
		record.SessionID = meta.SessionID
	}
	if record.ItemImageID == nil {
		record.ItemImageID = meta.ItemImageID
	}
	if record.ContextID == nil {
		record.ContextID = meta.ContextID
	}
	s.auditLogger(ctx, record)
}

func WithProviderCallMetadata(ctx context.Context, sessionID string, itemImageID, contextID *uint64) context.Context {
	return context.WithValue(ctx, providerCallMetadataKey{}, providerCallMetadata{
		SessionID:   sessionID,
		ItemImageID: itemImageID,
		ContextID:   contextID,
	})
}

func providerCallMetadataFromContext(ctx context.Context) providerCallMetadata {
	if ctx == nil {
		return providerCallMetadata{}
	}
	meta, _ := ctx.Value(providerCallMetadataKey{}).(providerCallMetadata)
	return meta
}

func WithProviderAPIKey(ctx context.Context, provider, apiKey string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	apiKey = strings.TrimSpace(apiKey)
	if provider == "" || apiKey == "" {
		return ctx
	}
	current, _ := ctx.Value(providerAPIKeyKey{}).(map[string]string)
	next := make(map[string]string, len(current)+1)
	for key, value := range current {
		next[key] = value
	}
	next[provider] = apiKey
	return context.WithValue(ctx, providerAPIKeyKey{}, next)
}

func providerAPIKeyFromContext(ctx context.Context, provider string) string {
	if ctx == nil {
		return ""
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	values, _ := ctx.Value(providerAPIKeyKey{}).(map[string]string)
	return strings.TrimSpace(values[provider])
}

// ProcessingContext carries the parameters from a store.Context into the
// processing pipeline without importing the store package (avoids cycles).
type ProcessingContext struct {
	SegmentationModel     string // "tesseract" | "scribe" | "kraken" | "kraken:<model>"
	TranscriptionProvider string
	TranscriptionModel    string
	TranscriptionBaseURL  string
	TranscriptionAudience string
	Temperature           *float64
	SystemPrompt          string
	// SegmentOnly skips LLM transcription and returns hOCR with line bounding
	// boxes only. Used when the client will handle transcription via a batch job.
	SegmentOnly bool
}

type providerConfigOverridesKey struct{}

type providerConfigOverrides struct {
	BaseURL  string
	Audience string
}

// WithProviderConfigOverrides stores optional provider config overrides, such
// as a per-context Ollama endpoint and Cloud Run audience.
func WithProviderConfigOverrides(ctx context.Context, baseURL, audience string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	baseURL = strings.TrimSpace(baseURL)
	audience = strings.TrimSpace(audience)
	if baseURL == "" && audience == "" {
		return ctx
	}
	return context.WithValue(ctx, providerConfigOverridesKey{}, providerConfigOverrides{
		BaseURL:  baseURL,
		Audience: audience,
	})
}

func providerConfigOverridesFromContext(ctx context.Context) providerConfigOverrides {
	if ctx == nil {
		return providerConfigOverrides{}
	}
	overrides, _ := ctx.Value(providerConfigOverridesKey{}).(providerConfigOverrides)
	overrides.BaseURL = strings.TrimSpace(overrides.BaseURL)
	overrides.Audience = strings.TrimSpace(overrides.Audience)
	return overrides
}

// ProcessImageWithContext runs the full pipeline using the supplied context and
// returns the generated hOCR plus the effective provider/model used.
func (s *Service) ProcessImageWithContext(ctx context.Context, imagePath string, pctx ProcessingContext) (string, string, string, error) {
	goCtx := ctx
	if goCtx == nil {
		goCtx = context.Background()
	}
	goCtx = WithProviderConfigOverrides(goCtx, pctx.TranscriptionBaseURL, pctx.TranscriptionAudience)

	width, height, err := s.getImageDimensions(imagePath)
	if err != nil {
		return "", "", "", fmt.Errorf("get image dimensions: %w", err)
	}

	selectedWords, selectedProvider, err := s.detectWithModel(goCtx, imagePath, pctx.SegmentationModel)
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

	llmProvider, providerName, err := s.initLLMProvider(pctx.TranscriptionProvider)
	if err != nil {
		return "", "", "", fmt.Errorf("init LLM provider: %w", err)
	}
	model := strings.TrimSpace(pctx.TranscriptionModel)
	if model == "" {
		model = s.getModelForProvider(providerName)
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

// parseSegmentationModel normalizes the configured segmentation model and
// preserves the Kraken model identifier suffix exactly as configured.
func parseSegmentationModel(segModel string) (string, string) {
	trimmed := strings.TrimSpace(segModel)
	lower := strings.ToLower(trimmed)

	switch {
	case lower == "", lower == "auto":
		return "auto", ""
	case lower == "tesseract":
		return "tesseract", ""
	case lower == "scribe":
		return "scribe", ""
	case lower == "kraken":
		return "kraken", ""
	case strings.HasPrefix(lower, "kraken:"):
		return "kraken", strings.TrimSpace(trimmed[len("kraken:"):])
	default:
		return "auto", ""
	}
}

func normalizeDetectionProvider(provider string) string {
	normalized := strings.ToLower(strings.TrimSpace(provider))
	switch {
	case normalized == "scribe" || normalized == "custom":
		return "custom"
	case normalized == "kraken" || strings.HasPrefix(normalized, "kraken:"):
		return "kraken"
	case normalized == "tesseract":
		return "tesseract"
	default:
		return normalized
	}
}

// detectWithModel selects and runs the appropriate segmentation provider.
// segModel values: "tesseract", "scribe", "kraken", "kraken:<model-id>", "auto", ""
// "auto" (or empty string) runs both providers in parallel and picks the one that
// detects more words; the winning provider name is returned for downstream use.
func (s *Service) detectWithModel(ctx context.Context, imagePath, segModel string) ([]worddetection.WordBox, string, error) {
	kind, modelID := parseSegmentationModel(segModel)
	requested := strings.TrimSpace(segModel)
	if requested == "" {
		requested = kind
	}
	if kind == "kraken" && modelID != "" {
		requested = "kraken:" + modelID
	}

	if kind == "kraken" {
		if client := segmentor.NewSegmentationModelClient(requested); client.Enabled() {
			words, provider, err := client.DetectWords(ctx, imagePath, requested)
			return words, normalizeDetectionProvider(provider), err
		}
		if client := segmentor.NewClient(); client.Enabled() {
			words, provider, err := client.DetectWords(ctx, imagePath, requested)
			return words, normalizeDetectionProvider(provider), err
		}
	} else if client := segmentor.NewClient(); client.Enabled() {
		words, provider, err := client.DetectWords(ctx, imagePath, requested)
		return words, normalizeDetectionProvider(provider), err
	}

	switch kind {
	case "tesseract":
		p := worddetection.NewTesseract()
		words, err := p.DetectWords(ctx, imagePath)
		return words, "tesseract", err

	case "scribe":
		p := worddetection.NewCustom()
		words, err := p.DetectWords(ctx, imagePath)
		return words, "custom", err

	case "kraken":
		p := worddetection.NewKraken(modelID)
		words, err := p.DetectWords(ctx, imagePath)
		return words, "kraken", err

	default:
		// "auto" or empty: run both providers, pick the one with more detections.
		tesseractProvider := worddetection.NewTesseract()
		customProvider := worddetection.NewCustom()
		tesseractWords, tesseractErr := tesseractProvider.DetectWords(ctx, imagePath)
		customWords, customErr := customProvider.DetectWords(ctx, imagePath)

		if tesseractErr != nil && customErr != nil {
			return nil, "", fmt.Errorf("both detection methods failed - tesseract: %v, custom: %v",
				tesseractErr, customErr)
		}
		if tesseractErr != nil {
			return customWords, "custom", nil
		}
		if customErr != nil {
			return tesseractWords, "tesseract", nil
		}
		if len(tesseractWords) >= len(customWords) {
			return tesseractWords, "tesseract", nil
		}
		return customWords, "custom", nil
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

	width, height, err := s.getImageDimensions(imagePath)
	if err != nil {
		return "", fmt.Errorf("failed to get image dimensions: %w", err)
	}

	selectedWords, selectedProvider, err := s.detectWithModel(ctx, imagePath, "auto")
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
	type lineRange struct {
		lineID int
		y1     int
		y2     int
	}
	ranges := make([]lineRange, 0, len(lines))
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
		ranges = append(ranges, lineRange{lineID: i, y1: minY, y2: maxY})
	}

	boxes := make([]lineVerticalBox, 0, len(ranges))
	for _, r := range ranges {
		boxes = append(boxes, lineVerticalBox{lineID: r.lineID, y1: r.y1, y2: r.y2})
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
	llmProvider, providerName, err := s.initLLMProvider(providerOverride)
	if err != nil {
		return "", fmt.Errorf("failed to initialize LLM provider: %w", err)
	}

	model := strings.TrimSpace(modelOverride)
	if model == "" {
		model = s.getModelForProvider(providerName)
	}

	lineImagePath, err := s.extractLineImage(imagePath, minX, minY, maxX, maxY, 0)
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
	llmProvider, providerName, err := s.initLLMProvider(providerOverride)
	if err != nil {
		return "", fmt.Errorf("failed to initialize LLM provider: %w", err)
	}

	model := strings.TrimSpace(modelOverride)
	if model == "" {
		model = s.getModelForProvider(providerName)
	}

	return s.extractTranscriptionFromImageWithOperation(ctx, llmProvider, providerName, model, imagePath, operation)
}

func (s *Service) extractTranscriptionFromImage(ctx context.Context, llmProvider providers.Provider, providerName, model, imagePath string) (string, error) {
	return s.extractTranscriptionFromImageWithOperation(ctx, llmProvider, providerName, model, imagePath, "transcribe_image")
}

func (s *Service) extractTranscriptionFromImageWithOperation(ctx context.Context, llmProvider providers.Provider, providerName, model, imagePath, operation string) (string, error) {
	imageData, err := os.ReadFile(imagePath)
	if err != nil {
		return "", fmt.Errorf("failed to read image for transcription: %w", err)
	}
	imageBase64 := base64.StdEncoding.EncodeToString(imageData)

	prompt := "Transcribe the handwritten text in this image. Return ONLY the transcribed text with no additional commentary, numbering, or explanation. If the text is not legible or cannot be read, return exactly: not legible."
	config := s.providerConfigWithContext(ctx, providerName, model, prompt, 0.0)

	text, err := s.extractTextWithRetry(ctx, llmProvider, providerName, config, imagePath, imageBase64, prompt, operation)
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
	llmProvider providers.Provider,
	providerName string,
	config providers.Config,
	imagePath, imageBase64, prompt, operation string,
) (string, error) {
	attempts := 1
	baseDelay := 0 * time.Millisecond
	maxDelay := 0 * time.Millisecond
	if providerName == "ollama" {
		attempts = 6
		baseDelay = 1000 * time.Millisecond
		maxDelay = 30000 * time.Millisecond
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		var text string
		var err error
		if providerName == "gemini" {
			text, err = s.extractTextWithGemini(ctx, config.Model, prompt, imageBase64, operation)
		} else if providerName == "kraken" {
			text, err = s.extractTextWithKraken(ctx, config.Model, imagePath, operation)
		} else {
			text, err = s.extractTextWithProvider(ctx, llmProvider, providerName, config, imagePath, imageBase64, operation)
		}
		if err == nil {
			return text, nil
		}
		lastErr = err

		if providerName != "ollama" || !isRetriableOllamaError(err) || attempt == attempts {
			break
		}

		delay := baseDelay * time.Duration(1<<(attempt-1))
		if maxDelay > 0 && delay > maxDelay {
			delay = maxDelay
		}
		slog.Warn(
			"Ollama request failed; retrying with backoff",
			"attempt", attempt,
			"max_attempts", attempts,
			"delay_ms", delay.Milliseconds(),
			"error", err,
		)
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(delay):
		}
	}
	return "", lastErr
}

func (s *Service) providerConfig(providerName, model, prompt string, temperature float64) providers.Config {
	cfg := providers.Config{
		Provider:    providerName,
		Model:       model,
		Prompt:      prompt,
		Temperature: temperature,
	}
	if strings.EqualFold(providerName, "ollama") {
		runtime := config.Get()
		cfg.BaseURL, cfg.Audience = runtime.Config.LLM.Ollama.ResolveForModel(model)
		if cfg.BaseURL == "" {
			cfg.BaseURL = strings.TrimSpace(runtime.Config.LLM.Ollama.URL)
		}
		if cfg.Audience == "" {
			cfg.Audience = strings.TrimSpace(runtime.Config.LLM.Ollama.Audience)
		}
	} else if strings.EqualFold(providerName, "kraken") {
		runtime := config.Get()
		cfg.BaseURL = strings.TrimSpace(runtime.Config.LLM.Kraken.URL)
		cfg.Audience = strings.TrimSpace(runtime.Config.LLM.Kraken.Audience)
	}
	return cfg
}

func (s *Service) providerConfigWithContext(ctx context.Context, providerName, model, prompt string, temperature float64) providers.Config {
	cfg := s.providerConfig(providerName, model, prompt, temperature)
	if !strings.EqualFold(providerName, "ollama") && !strings.EqualFold(providerName, "kraken") {
		return cfg
	}
	overrides := providerConfigOverridesFromContext(ctx)
	if overrides.BaseURL != "" {
		cfg.BaseURL = overrides.BaseURL
		// A context-scoped base URL override selects a different runtime endpoint.
		// If no audience override is provided, fall back to the provider's runtime
		// default audience rather than any model-routed audience resolved earlier.
		if overrides.Audience == "" {
			runtime := config.Get()
			if strings.EqualFold(providerName, "ollama") {
				cfg.Audience = strings.TrimSpace(runtime.Config.LLM.Ollama.Audience)
			} else {
				cfg.Audience = strings.TrimSpace(runtime.Config.LLM.Kraken.Audience)
			}
		}
	}
	if overrides.Audience != "" {
		cfg.Audience = overrides.Audience
	}
	return cfg
}

func (s *Service) extractTextWithProvider(
	ctx context.Context,
	llmProvider providers.Provider,
	providerName string,
	config providers.Config,
	imagePath, imageBase64, operation string,
) (string, error) {
	text, rawResponse, err := llmProvider.ExtractText(ctx, config, imagePath, imageBase64)
	record := ProviderCallAuditRecord{
		Provider:     providerName,
		Model:        config.Model,
		Operation:    operation,
		Prompt:       config.Prompt,
		RequestJSON:  fmt.Sprintf(`{"model":%q,"prompt":%q,"temperature":%v}`, config.Model, config.Prompt, config.Temperature),
		ResponseJSON: providerResponseJSON(rawResponse),
	}
	if err != nil {
		record.ErrorMessage = err.Error()
	}
	s.auditProviderCall(ctx, record)
	return text, err
}

func providerResponseJSON(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

func isRetriableOllamaError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "503") ||
		strings.Contains(msg, "status 503") ||
		strings.Contains(msg, "service you requested is not available yet") ||
		strings.Contains(msg, "temporarily unavailable")
}

func (s *Service) processImageToHOCR(ctx context.Context, imagePath, providerOverride, modelOverride string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	// Step 1: Get image dimensions
	width, height, err := s.getImageDimensions(imagePath)
	if err != nil {
		return "", fmt.Errorf("failed to get image dimensions: %w", err)
	}

	selectedWords, selectedProvider, err := s.detectWithModel(ctx, imagePath, "auto")
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
	llmProvider, providerName, err := s.initLLMProvider(providerOverride)
	if err != nil {
		return "", fmt.Errorf("failed to initialize LLM provider: %w", err)
	}

	// Step 5: Transcribe words/lines using LLM (line-based if custom provider selected)
	transcribedWords, err := s.transcribeWords(ctx, imagePath, selectedWords, width, height, llmProvider, providerName, selectedProvider, lines, modelOverride)
	if err != nil {
		return "", fmt.Errorf("failed to transcribe words: %w", err)
	}

	slog.Info("Word transcription completed", "transcribed_count", len(transcribedWords))

	// Step 6: Generate hOCR
	hocr := s.generateHOCRFromWords(transcribedWords, lines, width, height, selectedProvider)

	return hocr, nil
}

func (s *Service) getImageDimensions(imagePath string) (int, int, error) {
	f, err := os.Open(imagePath)
	if err != nil {
		return 0, 0, fmt.Errorf("open image for dimension lookup: %w", err)
	}
	defer f.Close()
	cfg, format, err := image.DecodeConfig(f)
	if err != nil {
		return 0, 0, fmt.Errorf("decode image config: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return 0, 0, fmt.Errorf("invalid %s dimensions %d x %d", format, cfg.Width, cfg.Height)
	}
	return cfg.Width, cfg.Height, nil
}

// TranscribedWord represents a word with its bounding box and transcribed text
type TranscribedWord struct {
	X, Y, Width, Height int
	Text                string
	Confidence          float64
	LineID              int
}

// initLLMProvider initializes the appropriate LLM provider based on configuration
func (s *Service) initLLMProvider(providerOverride string) (providers.Provider, string, error) {
	providerType := strings.ToLower(strings.TrimSpace(providerOverride))
	if providerType == "" {
		providerType = strings.ToLower(strings.TrimSpace(config.Get().Config.LLM.Provider))
	}
	if providerType == "" {
		providerType = "ollama" // Default to Ollama
	}

	slog.Info("Initializing LLM provider", "provider", providerType)

	switch providerType {
	case "ollama":
		return ollama.New(), providerType, nil
	case "openai":
		return openai.New(), providerType, nil
	case "gemini":
		return nil, providerType, nil
	case "kraken":
		return nil, providerType, nil
	default:
		return nil, "", fmt.Errorf("unsupported transcription provider: %s (must be 'ollama', 'openai', 'gemini', or 'kraken')", providerType)
	}
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
func (s *Service) transcribeWords(ctx context.Context, imagePath string, words []worddetection.WordBox, imageWidth, imageHeight int, provider providers.Provider, providerName, detectionProvider string, lines [][]worddetection.WordBox, modelOverride string) ([]TranscribedWord, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	transcribed := make([]TranscribedWord, 0, len(words))

	model := strings.TrimSpace(modelOverride)
	if model == "" {
		model = s.getModelForProvider(providerName)
	}
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
				"height", word.Height,
				"detected_text", word.Text)
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
		stitchedImagePath, err := s.stitchWordImages(imagePath, batch)
		if err != nil {
			slog.Warn("Failed to stitch word images", "batch", batchNum, "error", err)
			continue
		}
		defer os.Remove(stitchedImagePath)

		// Convert to base64
		imageData, err := os.ReadFile(stitchedImagePath)
		if err != nil {
			slog.Warn("Failed to read stitched image", "batch", batchNum, "error", err)
			continue
		}
		imageBase64 := base64.StdEncoding.EncodeToString(imageData)

		// Create prompt for batch transcription
		prompt := fmt.Sprintf("There are %d words in this image arranged horizontally. Transcribe each word on a separate line. Return ONLY the words, one per line, with no additional text, numbering, or explanation. If a word is not legible, use an empty line for that position.", len(batch))

		config := s.providerConfigWithContext(ctx, providerName, model, prompt, 0.0)

		var text string
		if providerName == "gemini" {
			text, err = s.extractTextWithGemini(ctx, model, prompt, imageBase64, "transcribe_word_batch")
		} else {
			text, err = s.extractTextWithProvider(ctx, provider, providerName, config, stitchedImagePath, imageBase64, "transcribe_word_batch")
		}
		if err != nil {
			slog.Warn("Failed to transcribe batch", "batch", batchNum, "error", err)
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
func (s *Service) transcribeLinesForCustomProvider(ctx context.Context, imagePath string, lines [][]worddetection.WordBox, imageWidth, imageHeight int, provider providers.Provider, providerName, model string, batchSize int) ([]TranscribedWord, error) {
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

			lineImagePath, err := s.extractLineImage(imagePath, minX, minY, maxX, maxY, region.lineID)
			if err != nil {
				slog.Warn("Failed to extract line image", "line_index", region.lineID, "error", err)
				continue
			}

			imageData, err := os.ReadFile(lineImagePath)
			if err != nil {
				_ = os.Remove(lineImagePath)
				slog.Warn("Failed to read line image", "line_index", region.lineID, "error", err)
				continue
			}
			imageBase64 := base64.StdEncoding.EncodeToString(imageData)

			prompt := "Transcribe the handwritten text in this image. Return ONLY the transcribed text with no additional commentary, numbering, or explanation. If the text is not legible or cannot be read, return exactly: not legible."
			config := s.providerConfigWithContext(ctx, providerName, model, prompt, 0.0)

			var text string
			if providerName == "gemini" {
				text, err = s.extractTextWithGemini(ctx, model, prompt, imageBase64, "transcribe_line")
			} else if providerName == "kraken" {
				text, err = s.extractTextWithKraken(ctx, model, lineImagePath, "transcribe_line")
			} else {
				text, err = s.extractTextWithProvider(ctx, provider, providerName, config, lineImagePath, imageBase64, "transcribe_line")
			}
			_ = os.Remove(lineImagePath)
			if err != nil {
				slog.Warn("Failed to transcribe line", "line_index", region.lineID, "error", err)
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
					"response", transcribedText)
				results <- lineResult{skippedText: true}
				continue
			}

			slog.Info("Line transcribed successfully",
				"line_index", region.lineID,
				"text_length", len(transcribedText),
				"text_preview", truncateString(transcribedText, 50))

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
	return 5
}

// truncateString truncates a string to maxLen characters, adding "..." if truncated
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
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
	// Use relative sizing: min 0.5% of image width and 0.8% of image height
	minWidth := int(float64(imageWidth) * 0.02)
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
	// Filter out detections with only special characters or numbers
	word := strings.TrimSpace(box.Text)
	if len(word) == 0 {
		return false
	}

	// Check if word contains at least some letters
	hasLetter := false
	specialCharCount := 0
	for _, char := range word {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') {
			hasLetter = true
		}
		// Count excessive special characters
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') {
			specialCharCount++
		}
	}

	// Reject if no letters or mostly special characters
	if !hasLetter || (float64(specialCharCount)/float64(len(word)) > 0.5) {
		return false
	}

	// Check 5: Very short "words" that are single characters might be noise
	// Unless they're common single-letter words
	if len(word) == 1 {
		validSingleChars := "aAiI" // Common single-letter words in English
		if !strings.Contains(validSingleChars, word) {
			return false
		}
	}

	return true
}

// getModelForProvider returns the appropriate model for the provider
func (s *Service) getModelForProvider(providerName string) string {
	llm := config.Get().Config.LLM
	switch providerName {
	case "ollama":
		if llm.Ollama.Model != "" {
			return llm.Ollama.Model
		}
		return "glm-ocr:bf16"
	case "kraken":
		if llm.Kraken.Model != "" {
			return llm.Kraken.Model
		}
		return "catmus-print-fondue-large.mlmodel"
	case "openai":
		if llm.OpenAI.Model != "" {
			return llm.OpenAI.Model
		}
		return "gpt-4o"
	case "gemini":
		if llm.Gemini.Model != "" {
			return llm.Gemini.Model
		}
		return "gemini-2.0-flash"
	default:
		return ""
	}
}

func (s *Service) extractTextWithKraken(ctx context.Context, model, imagePath, operation string) (string, error) {
	if strings.TrimSpace(model) == "" {
		model = s.getModelForProvider("kraken")
	}
	overrides := providerConfigOverridesFromContext(ctx)
	text, resolvedModel, err := segmentor.NewKrakenClient(model, overrides.BaseURL, overrides.Audience).Transcribe(ctx, imagePath, model)
	record := ProviderCallAuditRecord{
		Provider:    "kraken",
		Model:       model,
		Operation:   operation,
		RequestJSON: fmt.Sprintf(`{"model":%q}`, model),
	}
	if resolvedModel != "" {
		record.Model = resolvedModel
	}
	if err != nil {
		record.ErrorMessage = err.Error()
		s.auditProviderCall(ctx, record)
		return "", err
	}
	record.ResponseJSON = providerResponseJSON(map[string]string{
		"text":  text,
		"model": resolvedModel,
	})
	s.auditProviderCall(ctx, record)
	return text, nil
}

func (s *Service) extractTextWithGemini(ctx context.Context, model, prompt, imageBase64, operation string) (string, error) {
	apiKey := providerAPIKeyFromContext(ctx, "gemini")
	if apiKey == "" {
		apiKey = strings.TrimSpace(config.Get().Secrets.GeminiAPIKey)
	}
	if apiKey == "" {
		return "", fmt.Errorf("gemini api key is required when provider is gemini")
	}
	if strings.TrimSpace(model) == "" {
		model = s.getModelForProvider("gemini")
	}

	urlTemplate := strings.TrimSpace(config.Get().Config.LLM.Gemini.URLTemplate)
	if urlTemplate == "" {
		urlTemplate = "https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s"
	}
	requestURL := fmt.Sprintf(urlTemplate, model, apiKey)

	payload := map[string]any{
		"contents": []any{
			map[string]any{
				"parts": []any{
					map[string]any{"text": prompt},
					map[string]any{
						"inline_data": map[string]any{
							"mime_type": "image/png",
							"data":      imageBase64,
						},
					},
				},
			},
		},
		"generationConfig": map[string]any{
			"temperature": 0,
			"thinkingConfig": map[string]any{
				"includeThoughts": true,
			},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal gemini payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create gemini request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.auditProviderCall(ctx, ProviderCallAuditRecord{
			Provider:     "gemini",
			Model:        model,
			Operation:    operation,
			Prompt:       prompt,
			RequestJSON:  string(body),
			ErrorMessage: err.Error(),
		})
		return "", fmt.Errorf("call gemini: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		statusCode := resp.StatusCode
		s.auditProviderCall(ctx, ProviderCallAuditRecord{
			Provider:     "gemini",
			Model:        model,
			Operation:    operation,
			Prompt:       prompt,
			RequestJSON:  string(body),
			ErrorMessage: err.Error(),
			HTTPStatus:   &statusCode,
		})
		return "", fmt.Errorf("read gemini response: %w", err)
	}
	statusCode := resp.StatusCode
	record := ProviderCallAuditRecord{
		Provider:     "gemini",
		Model:        model,
		Operation:    operation,
		Prompt:       prompt,
		RequestJSON:  string(body),
		ResponseJSON: string(raw),
		HTTPStatus:   &statusCode,
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		record.ErrorMessage = fmt.Sprintf("gemini returned status %d", resp.StatusCode)
		s.auditProviderCall(ctx, record)
		return "", fmt.Errorf("gemini returned status %d: %s", resp.StatusCode, string(raw))
	}

	var parsed struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		record.ErrorMessage = fmt.Sprintf("parse gemini response: %v", err)
		s.auditProviderCall(ctx, record)
		return "", fmt.Errorf("parse gemini response: %w", err)
	}
	if len(parsed.Candidates) == 0 || len(parsed.Candidates[0].Content.Parts) == 0 {
		record.ErrorMessage = "gemini returned no candidates"
		s.auditProviderCall(ctx, record)
		return "", fmt.Errorf("gemini returned no candidates")
	}

	var out strings.Builder
	for _, part := range parsed.Candidates[0].Content.Parts {
		if strings.TrimSpace(part.Text) == "" {
			continue
		}
		if out.Len() > 0 {
			out.WriteByte('\n')
		}
		out.WriteString(part.Text)
	}
	s.auditProviderCall(ctx, record)
	return out.String(), nil
}

// getBatchSize returns the batch size for word transcription.
func (s *Service) getBatchSize() int {
	if v := config.Get().Config.LLM.BatchSize; v > 0 {
		return v
	}
	return 10
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
