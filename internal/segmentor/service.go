package segmentor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	"io"
	"log/slog"
	"math"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/imagemagick"
	"github.com/lehigh-university-libraries/scribe/internal/safefile"
	"github.com/lehigh-university-libraries/scribe/internal/uploadlimits"
	"github.com/lehigh-university-libraries/scribe/internal/worddetection"
	"github.com/lehigh-university-libraries/scribe/internal/worklimit"
)

type SegmentResponse struct {
	Provider string       `json:"provider"`
	Words    []SegmentBox `json:"words"`
}

// SegmentBox is the general remote OCR wire representation. Confidence is a
// probability in [0,1], independent of local detector conventions.
type SegmentBox struct {
	X          int     `json:"X"`
	Y          int     `json:"Y"`
	Width      int     `json:"Width"`
	Height     int     `json:"Height"`
	Text       string  `json:"Text"`
	Confidence float64 `json:"Confidence"`
}

type TranscriptionResponse struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Text     string `json:"text"`
}

type processingFailureCategory string

const (
	processingFailureCanceled processingFailureCategory = "canceled"
	processingFailureTimeout  processingFailureCategory = "timeout"
	processingFailureInternal processingFailureCategory = "internal"
	maxKrakenOutputBytes                                = int64(8 << 20)
)

// processingFailure deliberately does not unwrap cause. Subprocess and image
// library errors can contain document content, model paths, or stderr; callers
// retain cancellation checks through Is without gaining access to those
// diagnostics through Error or errors.Unwrap.
type processingFailure struct {
	operation             string
	category              processingFailureCategory
	cause                 error
	subprocessOutputBytes int
}

func (e *processingFailure) Error() string {
	if e == nil {
		return "processing failed"
	}
	switch e.category {
	case processingFailureCanceled:
		return e.operation + " canceled"
	case processingFailureTimeout:
		return e.operation + " timed out"
	default:
		return e.operation + " failed"
	}
}

func (e *processingFailure) Is(target error) bool {
	return e != nil && e.cause != nil && errors.Is(e.cause, target)
}

func redactProcessingError(operation string, err error) error {
	if err == nil {
		return nil
	}
	failure := &processingFailure{
		operation: strings.TrimSpace(operation),
		category:  processingFailureInternal,
		cause:     err,
	}
	if failure.operation == "" {
		failure.operation = "processing"
	}
	var existing *processingFailure
	if errors.As(err, &existing) {
		failure.category = existing.category
		failure.cause = existing.cause
		failure.subprocessOutputBytes = existing.subprocessOutputBytes
		return failure
	}
	switch {
	case errors.Is(err, context.Canceled):
		failure.category = processingFailureCanceled
	case errors.Is(err, context.DeadlineExceeded):
		failure.category = processingFailureTimeout
	}
	return failure
}

func redactSubprocessError(operation string, err error, output []byte) error {
	failure, _ := redactProcessingError(operation, err).(*processingFailure)
	if failure != nil {
		failure.subprocessOutputBytes = len(output)
	}
	return failure
}

func writeProcessingFailure(w http.ResponseWriter, operation string, err error) {
	failure, _ := redactProcessingError(operation, err).(*processingFailure)
	if failure == nil {
		failure = &processingFailure{operation: "processing", category: processingFailureInternal}
	}
	status := http.StatusBadGateway
	switch failure.category {
	case processingFailureCanceled:
		status = http.StatusRequestTimeout
	case processingFailureTimeout:
		status = http.StatusGatewayTimeout
	}
	attrs := []any{
		"operation", failure.operation,
		"category", failure.category,
		"error_type", fmt.Sprintf("%T", err),
	}
	if failure.subprocessOutputBytes > 0 {
		attrs = append(attrs, "subprocess_output_bytes", failure.subprocessOutputBytes)
	}
	slog.Warn("segmentor request failed", attrs...)
	http.Error(w, failure.Error(), status)
}

func DetectWords(ctx context.Context, imagePath, model string) ([]worddetection.WordBox, string, error) {
	provider, normalized, err := providerForModel(model)
	if err != nil {
		return nil, "", redactProcessingError("segmentation configuration", err)
	}
	words, err := provider.DetectWords(ctx, imagePath)
	if err != nil {
		return nil, normalized, redactProcessingError("segmentation provider", err)
	}
	return words, normalized, nil
}

func NewHandler() http.Handler {
	mux := http.NewServeMux()
	expensive := worklimit.FromEnvironment("SEGMENTOR_MAX_CONCURRENCY", 1, 8)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("POST /v1/segment", expensive.Wrap(http.HandlerFunc(handleSegment)))
	mux.Handle("POST /v1/transcribe", expensive.Wrap(http.HandlerFunc(handleTranscribe)))
	return mux
}

func handleSegment(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, uploadlimits.MaxMultipartBodyBytes)
	if err := r.ParseMultipartForm(uploadlimits.MultipartMemoryBytes); err != nil { // #nosec G120 -- request body is capped with http.MaxBytesReader immediately above.
		http.Error(w, "invalid multipart image request", http.StatusBadRequest)
		return
	}
	if r.MultipartForm != nil {
		defer func() { _ = r.MultipartForm.RemoveAll() }()
	}
	model := strings.TrimSpace(r.FormValue("model"))
	if model == "" {
		model = "auto"
	}

	file, header, err := r.FormFile("image")
	if err != nil {
		http.Error(w, "image form file is required", http.StatusBadRequest)
		return
	}
	defer file.Close()

	tmp, err := os.CreateTemp("", "segmentor-*"+segmentorImageExtension(header, r.Header.Get("Content-Type")))
	if err != nil {
		http.Error(w, "prepare uploaded image failed", http.StatusInternalServerError)
		return
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath) // #nosec G703 -- tmpPath comes directly from os.CreateTemp, not request input.
	}()
	if err := copyMultipartImage(tmp, file, header.Size); err != nil {
		http.Error(w, "prepare uploaded image failed", http.StatusInternalServerError)
		return
	}
	if err := tmp.Close(); err != nil {
		http.Error(w, "prepare uploaded image failed", http.StatusInternalServerError)
		return
	}

	preparedPath, cleanupPrepared, err := normalizeSegmentInput(r.Context(), tmpPath)
	if err != nil {
		writeProcessingFailure(w, "normalize segment image", err)
		return
	}
	defer cleanupPrepared()

	words, provider, err := DetectWords(r.Context(), preparedPath, model)
	if err != nil {
		writeProcessingFailure(w, "segment image", err)
		return
	}
	preparedConfig, err := inspectPreparedImage(preparedPath)
	if err != nil || worddetection.ValidateBoxes(words, preparedConfig.Width, preparedConfig.Height, iiif.MaxAnnotationsPerPage/2) != nil {
		writeProcessingFailure(w, "segment image", redactProcessingError("segmentation provider", errors.New("invalid detector output")))
		return
	}
	wireWords, err := segmentBoxes(words)
	if err != nil {
		writeProcessingFailure(w, "segment image", redactProcessingError("segmentation provider", err))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(SegmentResponse{
		Provider: provider,
		Words:    wireWords,
	})
}

func segmentBoxes(words []worddetection.WordBox) ([]SegmentBox, error) {
	result := make([]SegmentBox, len(words))
	for index, word := range words {
		confidence := word.Confidence
		if confidence > 1 && confidence <= 100 {
			confidence /= 100
		}
		if confidence < 0 || confidence > 1 || math.IsNaN(confidence) || math.IsInf(confidence, 0) {
			return nil, errors.New("detector confidence is outside the supported range")
		}
		result[index] = SegmentBox{
			X: word.X, Y: word.Y, Width: word.Width, Height: word.Height,
			Text: word.Text, Confidence: confidence,
		}
	}
	return result, nil
}

func handleTranscribe(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, uploadlimits.MaxMultipartBodyBytes)
	if err := r.ParseMultipartForm(uploadlimits.MultipartMemoryBytes); err != nil { // #nosec G120 -- request body is capped with http.MaxBytesReader immediately above.
		http.Error(w, "invalid multipart image request", http.StatusBadRequest)
		return
	}
	if r.MultipartForm != nil {
		defer func() { _ = r.MultipartForm.RemoveAll() }()
	}
	model := strings.TrimSpace(r.FormValue("model"))

	file, header, err := r.FormFile("image")
	if err != nil {
		http.Error(w, "image form file is required", http.StatusBadRequest)
		return
	}
	defer file.Close()

	tmp, err := os.CreateTemp("", "segmentor-transcribe-*"+segmentorImageExtension(header, r.Header.Get("Content-Type")))
	if err != nil {
		http.Error(w, "prepare uploaded image failed", http.StatusInternalServerError)
		return
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath) // #nosec G703 -- tmpPath comes directly from os.CreateTemp, not request input.
	}()
	if err := copyMultipartImage(tmp, file, header.Size); err != nil {
		http.Error(w, "prepare uploaded image failed", http.StatusInternalServerError)
		return
	}
	if err := tmp.Close(); err != nil {
		http.Error(w, "prepare uploaded image failed", http.StatusInternalServerError)
		return
	}

	text, resolvedModel, err := TranscribeWithKraken(r.Context(), tmpPath, model)
	if err != nil {
		writeProcessingFailure(w, "transcribe image", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(TranscriptionResponse{
		Provider: "kraken",
		Model:    resolvedModel,
		Text:     text,
	})
}

func providerForModel(model string) (worddetection.Provider, string, error) {
	trimmed := strings.TrimSpace(model)
	normalized := strings.ToLower(trimmed)
	switch {
	case normalized == "", normalized == "auto":
		return worddetection.NewAuto(), "auto", nil
	case normalized == "tesseract":
		return worddetection.NewTesseract(), "tesseract", nil
	case normalized == "scribe", normalized == "custom":
		return worddetection.NewCustom(), "scribe", nil
	case normalized == "kraken":
		return worddetection.NewKraken(resolveKrakenModelPathWithDefault("", defaultKrakenSegmentationModel())), "kraken", nil
	case strings.HasPrefix(normalized, "kraken:"):
		return worddetection.NewKraken(resolveKrakenModelPathWithDefault(strings.TrimSpace(trimmed[len("kraken:"):]), defaultKrakenSegmentationModel())), normalized, nil
	default:
		return nil, normalized, fmt.Errorf("unsupported segmentation model")
	}
}

func TranscribeWithKraken(ctx context.Context, imagePath, model string) (string, string, error) {
	resolvedModel := resolveKrakenModelPathWithDefault(model, defaultKrakenTranscriptionModel())
	if resolvedModel == "" {
		return "", "", redactProcessingError("kraken transcription configuration", fmt.Errorf("invalid model selection"))
	}

	output, err := os.CreateTemp("", "segmentor-kraken-*.txt")
	if err != nil {
		return "", "", redactProcessingError("kraken transcription", err)
	}
	outputPath := output.Name()
	if err := output.Close(); err != nil {
		_ = os.Remove(outputPath)
		return "", "", redactProcessingError("kraken transcription", err)
	}
	defer func() { _ = os.Remove(outputPath) }()

	cmd := exec.CommandContext(ctx, "kraken", // #nosec G204,G702 -- kraken is invoked directly without a shell; model paths are resolved under the configured model directory.
		"-i", imagePath, outputPath,
		"segment", "-bl",
		"ocr", "-m", resolvedModel,
	)
	combined, err := cmd.CombinedOutput()
	if err != nil {
		return "", "", redactSubprocessError("kraken transcription", err, combined)
	}

	data, err := safefile.ReadFileLimit(outputPath, maxKrakenOutputBytes)
	if err != nil {
		return "", "", redactProcessingError("kraken transcription", err)
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return "", "", fmt.Errorf("kraken returned empty transcription")
	}
	return text, filepath.Base(resolvedModel), nil
}

func defaultKrakenSegmentationModel() string {
	return strings.TrimSpace(os.Getenv("KRAKEN_SEGMENTATION_MODEL"))
}

func defaultKrakenTranscriptionModel() string {
	model := strings.TrimSpace(os.Getenv("KRAKEN_TRANSCRIPTION_MODEL"))
	if model != "" {
		return model
	}
	return "catmus-print-fondue-large.mlmodel"
}

func resolveKrakenModelPathWithDefault(model, fallback string) string {
	candidate := strings.TrimSpace(model)
	if candidate == "" {
		candidate = strings.TrimSpace(fallback)
	}
	if candidate == "" {
		return ""
	}
	if filepath.IsAbs(candidate) || strings.ContainsAny(candidate, `/\`) {
		return ""
	}
	resolved := filepath.Join("/models/kraken", candidate)
	if _, err := os.Stat(resolved); err == nil { // #nosec G703 -- candidate rejects absolute paths and separators before joining under /models/kraken.
		return resolved
	}
	return candidate
}

func normalizeSegmentInput(ctx context.Context, imagePath string) (string, func(), error) {
	output, err := os.CreateTemp("", "segmentor-prepared-*.jpg")
	if err != nil {
		return "", func() {}, redactProcessingError("image normalization", err)
	}
	outputPath := output.Name()
	if err := output.Close(); err != nil {
		_ = os.Remove(outputPath)
		return "", func() {}, redactProcessingError("image normalization", err)
	}

	cmd, err := imagemagick.ConvertCommandContext(ctx, imagePath, outputPath)
	if err != nil {
		_ = os.Remove(outputPath)
		return "", func() {}, redactProcessingError("image normalization", err)
	}
	if combined, err := cmd.CombinedOutput(); err != nil {
		_ = os.Remove(outputPath)
		return "", func() {}, redactSubprocessError("image normalization", err, combined)
	}
	if err := validatePreparedImage(outputPath); err != nil {
		_ = os.Remove(outputPath)
		return "", func() {}, redactProcessingError("image normalization", err)
	}

	cleanup := func() {
		_ = os.Remove(outputPath)
	}
	return outputPath, cleanup, nil
}

func validatePreparedImage(path string) error {
	_, err := inspectPreparedImage(path)
	return err
}

func inspectPreparedImage(path string) (image.Config, error) {
	info, err := os.Stat(path) // #nosec G703 -- path is returned by os.CreateTemp in normalizeSegmentInput.
	if err != nil {
		return image.Config{}, err
	}
	if info.Size() <= 0 {
		return image.Config{}, fmt.Errorf("normalized image is empty")
	}
	if err := uploadlimits.ValidateImageSize(info.Size()); err != nil {
		return image.Config{}, err
	}
	file, err := safefile.Open(path)
	if err != nil {
		return image.Config{}, err
	}
	defer file.Close()
	config, _, err := image.DecodeConfig(file)
	if err != nil {
		return image.Config{}, fmt.Errorf("normalized image is invalid")
	}
	if err := uploadlimits.ValidateImageDimensions(config.Width, config.Height); err != nil {
		return image.Config{}, err
	}
	return config, nil
}

func copyMultipartImage(dst io.Writer, src io.Reader, declaredSize int64) error {
	if err := uploadlimits.ValidateImageSize(declaredSize); err != nil {
		return err
	}
	written, err := io.Copy(dst, io.LimitReader(src, uploadlimits.MaxImageBytes+1))
	if err != nil {
		return err
	}
	return uploadlimits.ValidateImageSize(written)
}

func segmentorImageExtension(header *multipart.FileHeader, contentType string) string {
	if header != nil {
		switch strings.ToLower(filepath.Ext(strings.TrimSpace(header.Filename))) {
		case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".jp2", ".j2k", ".jpx", ".tif", ".tiff":
			return filepath.Ext(strings.TrimSpace(header.Filename))
		}
	}
	switch strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0])) {
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/jp2", "image/jpeg2000", "image/jpx":
		return ".jp2"
	case "image/tif", "image/tiff":
		return ".tif"
	default:
		return ".jpg"
	}
}
