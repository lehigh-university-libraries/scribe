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
	cfg.LLM.Gemini.Model = "gemini-3.5-configured"
	cfg.LLM.Gemini.Models = []string{"gemini-3.5-configured"}
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
	if len(catalog) != 4 {
		t.Fatalf("system context count = %d; want Gemini Pro, Tesseract, and two Kraken presets", len(catalog))
	}

	byName := make(map[string]struct {
		provider     string
		model        string
		prompt       string
		segmentation string
		isDefault    bool
		temperature  *float64
	}, len(catalog))
	defaultCount := 0
	for _, contextValue := range catalog {
		if contextValue.IsDefault {
			defaultCount++
		}
		byName[contextValue.Name] = struct {
			provider     string
			model        string
			prompt       string
			segmentation string
			isDefault    bool
			temperature  *float64
		}{
			provider:     contextValue.TranscriptionProvider,
			model:        contextValue.TranscriptionModel,
			prompt:       contextValue.SystemPrompt,
			segmentation: contextValue.SegmentationModel,
			isDefault:    contextValue.IsDefault,
			temperature:  contextValue.Temperature,
		}
	}
	if defaultCount != 1 {
		t.Fatalf("system default count = %d; want 1", defaultCount)
	}
	if _, exists := byName["Default"]; exists {
		t.Fatal("retired Default preset remains in the system catalog")
	}
	if _, exists := byName["Scribe Custom"]; exists {
		t.Fatal("retired Scribe Custom preset remains in the system catalog")
	}
	tesseract := byName["Tesseract OCR"]
	if !tesseract.isDefault || tesseract.provider != "tesseract" || tesseract.segmentation != "scribe" || tesseract.model != "tesseract" {
		t.Fatalf("Tesseract OCR default = %+v", tesseract)
	}
	gemini := byName["Gemini Pro"]
	if gemini.isDefault || gemini.provider != "gemini" || gemini.segmentation != "scribe" || gemini.model != cfg.LLM.Gemini.Model || gemini.prompt != cfg.LLM.DefaultSystemPrompt || gemini.temperature != nil {
		t.Fatalf("Gemini Pro preset = %+v; want non-default scribe/gemini/%s", gemini, cfg.LLM.Gemini.Model)
	}
	krakenBLLA := byName["Kraken BLLA"]
	if krakenBLLA.provider != cfg.LLM.Provider || krakenBLLA.model != cfg.LLM.Ollama.Model || krakenBLLA.prompt != cfg.LLM.DefaultSystemPrompt {
		t.Fatalf("Kraken BLLA configured-LLM selection = %+v", krakenBLLA)
	}
	if got := byName["Kraken CATMuS"].prompt; got != "" {
		t.Fatalf("Kraken CATMuS prompt = %q; Kraken does not support prompts", got)
	}
}

func TestDefaultScribeSegmentationAndTesseractTranscriptionRemainIndependent(t *testing.T) {
	cfg := systemContextTestConfig()
	cfg.LLM.Provider = "kraken"
	defaultValue := defaultContext(cfg)
	if defaultValue.Name != "Tesseract OCR" || defaultValue.SegmentationModel != "scribe" || defaultValue.TranscriptionProvider != "tesseract" || defaultValue.TranscriptionModel != "tesseract" || !defaultValue.IsDefault {
		t.Fatalf("default context = %+v; want Scribe segmentation with credential-free Tesseract transcription", defaultValue)
	}
	catalog := systemContexts(cfg)
	var gemini, krakenBLLA storeContextSelection
	for _, value := range catalog {
		switch value.Name {
		case "Gemini Pro":
			gemini = storeContextSelection{provider: value.TranscriptionProvider, model: value.TranscriptionModel, prompt: value.SystemPrompt}
		case "Kraken BLLA":
			krakenBLLA = storeContextSelection{provider: value.TranscriptionProvider, model: value.TranscriptionModel, prompt: value.SystemPrompt}
		}
	}
	if gemini.provider != "gemini" || gemini.model != cfg.LLM.Gemini.Model || gemini.prompt != cfg.LLM.DefaultSystemPrompt {
		t.Fatalf("Gemini Pro selection = %+v", gemini)
	}
	if krakenBLLA.provider != "kraken" || krakenBLLA.model != cfg.LLM.Kraken.Model || krakenBLLA.prompt != "" {
		t.Fatalf("Kraken BLLA configured-LLM selection = %+v", krakenBLLA)
	}
	if err := validateSystemContextCatalog(cfg, catalog); err != nil {
		t.Fatalf("validateSystemContextCatalog() error = %v", err)
	}
}

type storeContextSelection struct {
	provider string
	model    string
	prompt   string
}

func TestSystemContextStartupValidationNamesInvalidSeed(t *testing.T) {
	cfg := systemContextTestConfig()
	catalog := systemContexts(cfg)
	for index := range catalog {
		if catalog[index].IsDefault {
			catalog[index].TranscriptionModel = "not-registered"
		}
	}
	err := validateSystemContextCatalog(cfg, catalog)
	if err == nil || !strings.Contains(err.Error(), `system context "Tesseract OCR"`) {
		t.Fatalf("validation error = %v; want named system context failure", err)
	}
}

func TestRetiredSystemContextNamesCoverSupersededDefaults(t *testing.T) {
	if got := strings.Join(retiredSystemContextNames[:], "|"); got != "Default|Scribe Custom" {
		t.Fatalf("retired system contexts = %q", got)
	}
}

func TestSystemContextStartupValidationRequiresOneDefault(t *testing.T) {
	cfg := systemContextTestConfig()
	catalog := systemContexts(cfg)
	for index := range catalog {
		catalog[index].IsDefault = false
	}
	if err := validateSystemContextCatalog(cfg, catalog); err == nil || !strings.Contains(err.Error(), "exactly one default") {
		t.Fatalf("zero-default validation error = %v", err)
	}
	catalog[0].IsDefault = true
	catalog[1].IsDefault = true
	if err := validateSystemContextCatalog(cfg, catalog); err == nil || !strings.Contains(err.Error(), "exactly one default") {
		t.Fatalf("multiple-default validation error = %v", err)
	}
}
