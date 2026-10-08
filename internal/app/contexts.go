package app

import (
	"context"
	"fmt"

	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/providerregistry"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

var retiredSystemContextNames = [...]string{"Default", "Scribe Custom", "Tesseract OCR", "Kraken BLLA", "Gemini Pro", "Kraken CATMuS", "Kraken BLLA + GLM-OCR", "Kraken BLLA + Gemini Pro", "Kraken BLLA + Gemini Flash", "Kraken BLLA + CATMuS Medieval", "Kraken BLLA + CATMuS Print"}

func systemContexts(cfg config.Config) []store.Context {
	var catalog []store.Context
	for _, document := range []struct{ name, segmentor string }{
		{"Letters", "kraken"}, {"Medieval manuscripts", "kraken"}, {"Newspapers", "newspapers"},
	} {
		for _, transcription := range []struct{ label, provider, model string }{
			{"GLM-OCR", "ollama", "glm-ocr:bf16"},
			{"Gemini Pro", "gemini", "gemini-3.1-pro-preview"},
			{"Gemini Flash", "gemini", "gemini-3.8-flash"},
			{"OpenAI", "openai", "gpt-4.1"},
		} {
			catalog = append(catalog, store.Context{
				Name:                  document.name + " + " + transcription.label,
				Description:           "Segment " + document.name + " into lines and transcribe each crop with " + transcription.label + ".",
				IsDefault:             len(catalog) == 0,
				SegmentationModel:     document.segmentor,
				TranscriptionProvider: transcription.provider,
				TranscriptionModel:    transcription.model,
				SystemPrompt:          cfg.LLM.DefaultSystemPrompt,
			})
		}
	}
	return catalog
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
