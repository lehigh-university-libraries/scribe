package app

import (
	"context"

	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

// defaultContext returns the Default system context derived from the loaded
// LLM config. It uses the configured provider's default model.
func defaultContext(cfg config.Config) store.Context {
	provider := cfg.LLM.Provider
	var model string
	switch provider {
	case "kraken":
		model = cfg.LLM.Kraken.Model
	case "openai":
		model = cfg.LLM.OpenAI.Model
	case "gemini":
		model = cfg.LLM.Gemini.Model
	default:
		provider = "ollama"
		model = cfg.LLM.Ollama.Model
	}
	segModel := cfg.LLM.SegmentationModel
	if segModel == "" {
		segModel = "auto"
	}
	return store.Context{
		Name:                  "Default",
		Description:           "System default context. Runs both Tesseract and the Scribe segmentor, then keeps whichever finds more words.",
		IsDefault:             true,
		SegmentationModel:     segModel,
		TranscriptionProvider: provider,
		TranscriptionModel:    model,
		SystemPrompt:          cfg.LLM.DefaultSystemPrompt,
	}
}

func systemContexts(cfg config.Config) []store.Context {
	defaultCtx := defaultContext(cfg)
	return []store.Context{
		defaultCtx,
		{
			Name:                  "Tesseract OCR",
			Description:           "Built-in system context that uses Tesseract segmentation and Tesseract transcription directly.",
			IsDefault:             false,
			SegmentationModel:     "tesseract",
			TranscriptionProvider: "tesseract",
			TranscriptionModel:    "tesseract",
		},
		{
			Name:                  "Scribe Custom",
			Description:           "Built-in system context that uses the Scribe custom segmentor and line-by-line LLM transcription.",
			IsDefault:             false,
			SegmentationModel:     "scribe",
			TranscriptionProvider: defaultCtx.TranscriptionProvider,
			TranscriptionModel:    defaultCtx.TranscriptionModel,
			SystemPrompt:          defaultCtx.SystemPrompt,
		},
		{
			Name:                  "Kraken BLLA",
			Description:           "Built-in system context that uses Kraken page segmentation with the default BLLA model and then transcribes each detected line with the configured LLM provider.",
			IsDefault:             false,
			SegmentationModel:     "kraken",
			TranscriptionProvider: defaultCtx.TranscriptionProvider,
			TranscriptionModel:    defaultCtx.TranscriptionModel,
			SystemPrompt:          defaultCtx.SystemPrompt,
		},
		{
			Name:                  "Gemini Pro",
			Description:           "Uses gemini-3-pro-preview for transcription with Scribe segmentation.",
			IsDefault:             false,
			SegmentationModel:     "scribe",
			TranscriptionProvider: "gemini",
			TranscriptionModel:    "gemini-3-pro-preview",
			SystemPrompt:          defaultCtx.SystemPrompt,
		},
		{
			Name:                  "Kraken CATMuS",
			Description:           "Uses Scribe segmentation with Kraken transcription through the shared OCR service.",
			IsDefault:             false,
			SegmentationModel:     "scribe",
			TranscriptionProvider: "kraken",
			TranscriptionModel:    cfg.LLM.Kraken.Model,
			SystemPrompt:          defaultCtx.SystemPrompt,
		},
	}
}

// EnsureSystemContexts upserts the Default and built-in system contexts.
func EnsureSystemContexts(ctx context.Context, cfg config.Config, contextStore *store.ContextStore) error {
	if err := contextStore.EnsureDefault(ctx, defaultContext(cfg)); err != nil {
		return err
	}
	for _, systemCtx := range systemContexts(cfg) {
		if err := contextStore.EnsureSystemContext(ctx, systemCtx); err != nil {
			return err
		}
	}
	return nil
}
