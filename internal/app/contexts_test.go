package app

import (
	"strings"
	"testing"

	"github.com/lehigh-university-libraries/scribe/internal/config"
)

func systemContextTestConfig() config.Config {
	cfg := config.Config{}
	cfg.LLM.Provider = "ollama"
	cfg.LLM.SegmentationModel = "auto"
	cfg.LLM.DefaultSystemPrompt = "Preserve the document spelling."
	cfg.LLM.Ollama.Model = "ocr-default"
	cfg.LLM.Ollama.Models = []string{"ocr-default"}
	cfg.LLM.Gemini.Model = "gemini-configured"
	cfg.LLM.Gemini.Models = []string{"gemini-configured"}
	cfg.LLM.Kraken.Model = "catmus-configured.mlmodel"
	cfg.LLM.Kraken.Models = []string{"catmus-configured.mlmodel"}
	return cfg
}

func TestSystemContextsUseConfiguredModelsAndSupportedCapabilities(t *testing.T) {
	cfg := systemContextTestConfig()
	catalog := systemContexts(cfg)
	if err := validateSystemContextCatalog(cfg, catalog); err != nil {
		t.Fatalf("validateSystemContextCatalog() error = %v", err)
	}

	byName := make(map[string]struct {
		model  string
		prompt string
	}, len(catalog))
	for _, contextValue := range catalog {
		byName[contextValue.Name] = struct {
			model  string
			prompt string
		}{model: contextValue.TranscriptionModel, prompt: contextValue.SystemPrompt}
	}
	if got := byName["Gemini Pro"].model; got != cfg.LLM.Gemini.Model {
		t.Fatalf("Gemini Pro model = %q; want configured %q", got, cfg.LLM.Gemini.Model)
	}
	if got := byName["Kraken CATMuS"].prompt; got != "" {
		t.Fatalf("Kraken CATMuS prompt = %q; Kraken does not support prompts", got)
	}
}

func TestDefaultKrakenContextDropsUnsupportedPromptBeforeStartupValidation(t *testing.T) {
	cfg := systemContextTestConfig()
	cfg.LLM.Provider = "kraken"
	defaultValue := defaultContext(cfg)
	if defaultValue.SystemPrompt != "" {
		t.Fatalf("default Kraken system prompt = %q; want empty", defaultValue.SystemPrompt)
	}
	if err := validateSystemContextCatalog(cfg, systemContexts(cfg)); err != nil {
		t.Fatalf("validateSystemContextCatalog() error = %v", err)
	}
}

func TestSystemContextStartupValidationNamesInvalidSeed(t *testing.T) {
	cfg := systemContextTestConfig()
	catalog := systemContexts(cfg)
	catalog[0].TranscriptionModel = "not-registered"
	err := validateSystemContextCatalog(cfg, catalog)
	if err == nil || !strings.Contains(err.Error(), `system context "Default"`) {
		t.Fatalf("validation error = %v; want named system context failure", err)
	}
}
