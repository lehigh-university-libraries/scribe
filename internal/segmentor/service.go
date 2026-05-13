package segmentor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/lehigh-university-libraries/scribe/internal/safefile"
	"github.com/lehigh-university-libraries/scribe/internal/worddetection"
)

const maxMultipartBytes int64 = 64 << 20

type SegmentResponse struct {
	Provider string                  `json:"provider"`
	Words    []worddetection.WordBox `json:"words"`
}

type TranscriptionResponse struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Text     string `json:"text"`
}

func DetectWords(ctx context.Context, imagePath, model string) ([]worddetection.WordBox, string, error) {
	provider, normalized, err := providerForModel(model)
	if err != nil {
		return nil, "", err
	}
	words, err := provider.DetectWords(ctx, imagePath)
	if err != nil {
		return nil, normalized, err
	}
	return words, normalized, nil
}

func NewHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("POST /v1/segment", handleSegment)
	mux.HandleFunc("POST /v1/transcribe", handleTranscribe)
	return mux
}

func handleSegment(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxMultipartBytes)
	if err := r.ParseMultipartForm(64 << 20); err != nil { // #nosec G120 -- request body is capped with http.MaxBytesReader immediately above.
		http.Error(w, fmt.Sprintf("parse multipart form: %v", err), http.StatusBadRequest)
		return
	}
	model := strings.TrimSpace(r.FormValue("model"))
	if model == "" {
		model = "auto"
	}

	file, _, err := r.FormFile("image")
	if err != nil {
		http.Error(w, fmt.Sprintf("read image form file: %v", err), http.StatusBadRequest)
		return
	}
	defer file.Close()

	tmp, err := os.CreateTemp("", "segmentor-*.img")
	if err != nil {
		http.Error(w, fmt.Sprintf("create temp image: %v", err), http.StatusInternalServerError)
		return
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()
	if _, err := io.Copy(tmp, file); err != nil {
		http.Error(w, fmt.Sprintf("write temp image: %v", err), http.StatusInternalServerError)
		return
	}
	if err := tmp.Close(); err != nil {
		http.Error(w, fmt.Sprintf("close temp image: %v", err), http.StatusInternalServerError)
		return
	}

	words, provider, err := DetectWords(r.Context(), tmpPath, model)
	if err != nil {
		http.Error(w, fmt.Sprintf("segment image: %v", err), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(SegmentResponse{
		Provider: provider,
		Words:    words,
	})
}

func handleTranscribe(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxMultipartBytes)
	if err := r.ParseMultipartForm(64 << 20); err != nil { // #nosec G120 -- request body is capped with http.MaxBytesReader immediately above.
		http.Error(w, fmt.Sprintf("parse multipart form: %v", err), http.StatusBadRequest)
		return
	}
	model := strings.TrimSpace(r.FormValue("model"))

	file, _, err := r.FormFile("image")
	if err != nil {
		http.Error(w, fmt.Sprintf("read image form file: %v", err), http.StatusBadRequest)
		return
	}
	defer file.Close()

	tmp, err := os.CreateTemp("", "segmentor-transcribe-*.img")
	if err != nil {
		http.Error(w, fmt.Sprintf("create temp image: %v", err), http.StatusInternalServerError)
		return
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()
	if _, err := io.Copy(tmp, file); err != nil {
		http.Error(w, fmt.Sprintf("write temp image: %v", err), http.StatusInternalServerError)
		return
	}
	if err := tmp.Close(); err != nil {
		http.Error(w, fmt.Sprintf("close temp image: %v", err), http.StatusInternalServerError)
		return
	}

	text, resolvedModel, err := TranscribeWithKraken(r.Context(), tmpPath, model)
	if err != nil {
		http.Error(w, fmt.Sprintf("transcribe image: %v", err), http.StatusBadGateway)
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
		return nil, normalized, fmt.Errorf("unsupported segmentation model %q", model)
	}
}

func TranscribeWithKraken(ctx context.Context, imagePath, model string) (string, string, error) {
	resolvedModel := resolveKrakenModelPathWithDefault(model, defaultKrakenTranscriptionModel())
	if resolvedModel == "" {
		return "", "", fmt.Errorf("kraken transcription model is required")
	}

	output, err := os.CreateTemp("", "segmentor-kraken-*.txt")
	if err != nil {
		return "", "", fmt.Errorf("create kraken output: %w", err)
	}
	outputPath := output.Name()
	_ = output.Close()
	defer func() { _ = os.Remove(outputPath) }()

	cmd := exec.CommandContext(ctx, "kraken", // #nosec G204,G702 -- kraken is invoked directly without a shell; model paths are resolved under the configured model directory.
		"-i", imagePath, outputPath,
		"segment", "-bl",
		"ocr", "-m", resolvedModel,
	)
	combined, err := cmd.CombinedOutput()
	if err != nil {
		return "", "", fmt.Errorf("kraken transcription failed (model=%s): %w\noutput: %s", resolvedModel, err, strings.TrimSpace(string(combined)))
	}

	data, err := safefile.ReadFile(outputPath)
	if err != nil {
		return "", "", fmt.Errorf("read kraken transcription: %w", err)
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
