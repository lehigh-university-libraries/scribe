package app

import (
	"github.com/lehigh-university-libraries/scribe/internal/config"
	"strings"
	"testing"
)

func systemContextTestConfig() config.Config {
	cfg := config.Config{}
	cfg.LLM.Gemini.Models = []string{"gemini-3.1-pro-preview", "gemini-3.8-flash"}
	cfg.LLM.OpenAI.Models = []string{"gpt-4.1"}
	cfg.Segmentation.Models = []string{"kraken", "newspapers"}
	return cfg
}
func TestDocumentContextsUseOnlyRegisteredSegmentorsAndLLMTranscription(t *testing.T) {
	cfg := systemContextTestConfig()
	catalog := systemContexts(cfg)
	if len(catalog) != 12 {
		t.Fatalf("catalog size = %d", len(catalog))
	}
	if err := validateSystemContextCatalog(cfg, catalog); err != nil {
		t.Fatal(err)
	}
	for _, preset := range catalog {
		if preset.TranscriptionProvider != "ollama" && preset.TranscriptionProvider != "gemini" && preset.TranscriptionProvider != "openai" {
			t.Fatalf("unsupported transcription: %+v", preset)
		}
		if strings.HasPrefix(preset.Name, "Newspapers") != (preset.SegmentationModel == "newspapers") {
			t.Fatalf("wrong segmentor: %+v", preset)
		}
		if preset.IsDefault != (preset.Name == "Letters + GLM-OCR") {
			t.Fatalf("wrong default: %+v", preset)
		}
	}
}
func TestSystemContextStartupRejectsUnregisteredModelsAndMultipleDefaults(t *testing.T) {
	cfg := systemContextTestConfig()
	catalog := systemContexts(cfg)
	catalog[0].TranscriptionModel = "not-installed"
	if err := validateSystemContextCatalog(cfg, catalog); err == nil {
		t.Fatal("accepted unregistered model")
	}
	catalog = systemContexts(cfg)
	catalog[1].IsDefault = true
	if err := validateSystemContextCatalog(cfg, catalog); err == nil {
		t.Fatal("accepted two defaults")
	}
}
