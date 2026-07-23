package app

import (
	"context"
	"fmt"

	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/providerregistry"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

// defaultContext returns the Default system context derived from the loaded
// LLM config. It uses the configured provider's default model.
func defaultContext(cfg config.Config) store.Context {
	registry := providerregistry.New(cfg)
	descriptor, _ := registry.ResolveProvider("") // built-in default is always installed
	systemPrompt := cfg.LLM.DefaultSystemPrompt
	if !descriptor.Capabilities.SystemPrompt {
		systemPrompt = ""
	}
	return store.Context{
		Name:                  "Default",
		Description:           "System default context. Runs both Tesseract and the Scribe segmentor, then keeps whichever finds more words.",
		IsDefault:             true,
		SegmentationModel:     registry.DefaultSegmentation(),
		TranscriptionProvider: descriptor.ID,
		TranscriptionModel:    descriptor.DefaultModel(),
		SystemPrompt:          systemPrompt,
	}
}

func systemContexts(cfg config.Config) []store.Context {
	defaultCtx := defaultContext(cfg)
	registry := providerregistry.New(cfg)
	geminiModel, _ := registry.EffectiveModel("gemini", "")
	krakenModel, _ := registry.EffectiveModel("kraken", "")
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
			Description:           "Uses the configured Gemini model for transcription with Scribe segmentation.",
			IsDefault:             false,
			SegmentationModel:     "scribe",
			TranscriptionProvider: "gemini",
			TranscriptionModel:    geminiModel,
			SystemPrompt:          defaultCtx.SystemPrompt,
		},
		{
			Name:                  "Kraken CATMuS",
			Description:           "Uses Scribe segmentation with Kraken transcription through the shared OCR service.",
			IsDefault:             false,
			SegmentationModel:     "scribe",
			TranscriptionProvider: "kraken",
			TranscriptionModel:    krakenModel,
		},
	}
}

// EnsureSystemContexts upserts the Default and built-in system contexts.
func EnsureSystemContexts(ctx context.Context, cfg config.Config, contextStore *store.ContextStore) error {
	catalog := systemContexts(cfg)
	if err := validateSystemContextCatalog(cfg, catalog); err != nil {
		return err
	}
	if contextStore == nil {
		return fmt.Errorf("seed system contexts: context store is not configured")
	}
	if err := contextStore.EnsureDefault(ctx, catalog[0]); err != nil {
		return err
	}
	for _, systemCtx := range catalog {
		if err := contextStore.EnsureSystemContext(ctx, systemCtx); err != nil {
			return err
		}
	}
	return nil
}

func validateSystemContextCatalog(cfg config.Config, catalog []store.Context) error {
	registry := providerregistry.New(cfg)
	for _, systemCtx := range catalog {
		if err := registry.ValidateSegmentation(systemCtx.SegmentationModel); err != nil {
			return fmt.Errorf("system context %q: %w", systemCtx.Name, err)
		}
		if err := registry.ValidateSelection(
			systemCtx.TranscriptionProvider,
			systemCtx.TranscriptionModel,
			systemCtx.SystemPrompt,
			systemCtx.Temperature,
		); err != nil {
			return fmt.Errorf("system context %q: %w", systemCtx.Name, err)
		}
	}
	return nil
}
