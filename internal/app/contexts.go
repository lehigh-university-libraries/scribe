package app

import (
	"context"
	"fmt"

	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/providerregistry"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

var retiredSystemContextNames = [...]string{"Default", "Scribe Custom"}

// defaultContext returns the deterministic, credential-free preset used for
// automatic context resolution.
func defaultContext(config.Config) store.Context {
	return store.Context{
		Name:                  "Tesseract OCR",
		Description:           "Built-in system context that uses Tesseract segmentation and Tesseract transcription directly.",
		IsDefault:             true,
		SegmentationModel:     "tesseract",
		TranscriptionProvider: "tesseract",
		TranscriptionModel:    "tesseract",
	}
}

func configuredLLMSelection(cfg config.Config) store.Context {
	registry := providerregistry.New(cfg)
	descriptor, _ := registry.ResolveProvider("") // configured provider is startup-validated
	systemPrompt := cfg.LLM.DefaultSystemPrompt
	if !descriptor.Capabilities.SystemPrompt {
		systemPrompt = ""
	}
	return store.Context{
		TranscriptionProvider: descriptor.ID,
		TranscriptionModel:    descriptor.DefaultModel(),
		SystemPrompt:          systemPrompt,
	}
}

func systemContexts(cfg config.Config) []store.Context {
	defaultCtx := defaultContext(cfg)
	configuredLLM := configuredLLMSelection(cfg)
	registry := providerregistry.New(cfg)
	geminiDescriptor, _ := registry.ResolveProvider("gemini") // built-in provider is always installed
	geminiModel, _ := registry.EffectiveModel(geminiDescriptor.ID, "")
	geminiPrompt := cfg.LLM.DefaultSystemPrompt
	if !geminiDescriptor.Capabilities.SystemPrompt {
		geminiPrompt = ""
	}
	krakenModel, _ := registry.EffectiveModel("kraken", "")
	return []store.Context{
		defaultCtx,
		{
			Name:                  "Kraken BLLA",
			Description:           "Built-in system context that uses Kraken page segmentation with the default BLLA model and then transcribes each detected line with the configured LLM provider.",
			IsDefault:             false,
			SegmentationModel:     "kraken",
			TranscriptionProvider: configuredLLM.TranscriptionProvider,
			TranscriptionModel:    configuredLLM.TranscriptionModel,
			SystemPrompt:          configuredLLM.SystemPrompt,
		},
		{
			Name:                  "Gemini Pro",
			Description:           "Uses Scribe segmentation and the configured Gemini model with model-default sampling.",
			IsDefault:             false,
			SegmentationModel:     "scribe",
			TranscriptionProvider: geminiDescriptor.ID,
			TranscriptionModel:    geminiModel,
			SystemPrompt:          geminiPrompt,
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

// EnsureSystemContexts upserts the built-in catalog, promotes its sole default,
// and explicitly retires superseded presets.
func EnsureSystemContexts(ctx context.Context, cfg config.Config, contextStore *store.ContextStore) error {
	catalog := systemContexts(cfg)
	if err := validateSystemContextCatalog(cfg, catalog); err != nil {
		return err
	}
	if contextStore == nil {
		return fmt.Errorf("seed system contexts: context store is not configured")
	}
	for _, systemCtx := range catalog {
		if err := contextStore.EnsureSystemContext(ctx, systemCtx); err != nil {
			return err
		}
	}
	var desiredDefault store.Context
	for _, systemCtx := range catalog {
		if systemCtx.IsDefault {
			desiredDefault = systemCtx
			break
		}
	}
	return contextStore.ReplaceSystemDefault(ctx, desiredDefault, retiredSystemContextNames[:])
}

func validateSystemContextCatalog(cfg config.Config, catalog []store.Context) error {
	registry := providerregistry.New(cfg)
	defaultCount := 0
	names := make(map[string]struct{}, len(catalog))
	for _, systemCtx := range catalog {
		name := systemCtx.Name
		if _, exists := names[name]; exists {
			return fmt.Errorf("system context %q is duplicated", name)
		}
		names[name] = struct{}{}
		if systemCtx.IsDefault {
			defaultCount++
		}
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
	if defaultCount != 1 {
		return fmt.Errorf("system context catalog must contain exactly one default")
	}
	return nil
}
