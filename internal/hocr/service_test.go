package hocr

import (
	"context"
	"testing"

	"github.com/lehigh-university-libraries/scribe/internal/config"
)

func TestParseSegmentationModel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		wantKind  string
		wantModel string
	}{
		{name: "empty defaults to auto", input: "", wantKind: "auto", wantModel: ""},
		{name: "auto", input: "auto", wantKind: "auto", wantModel: ""},
		{name: "tesseract", input: "tesseract", wantKind: "tesseract", wantModel: ""},
		{name: "scribe", input: "scribe", wantKind: "scribe", wantModel: ""},
		{name: "kraken shorthand", input: "kraken", wantKind: "kraken", wantModel: ""},
		{name: "kraken explicit model", input: "kraken:blla.mlmodel", wantKind: "kraken", wantModel: "blla.mlmodel"},
		{name: "kraken preserves model case", input: "KRAKEN:Models/Latin.mlmodel", wantKind: "kraken", wantModel: "Models/Latin.mlmodel"},
		{name: "unknown falls back to auto", input: "something-else", wantKind: "auto", wantModel: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotKind, gotModel := parseSegmentationModel(tt.input)
			if gotKind != tt.wantKind || gotModel != tt.wantModel {
				t.Fatalf("parseSegmentationModel(%q) = (%q, %q), want (%q, %q)", tt.input, gotKind, gotModel, tt.wantKind, tt.wantModel)
			}
		})
	}
}

func TestProviderConfigWithContextOverridesOllamaEndpoint(t *testing.T) {
	config.Init(config.Runtime{
		Config: config.Config{
			LLM: config.LLMConfig{
				Ollama: config.OllamaConfig{
					URL:      "http://default-ollama:11434",
					Audience: "https://default.run.app",
				},
			},
		},
	})
	t.Cleanup(func() {
		config.Init(config.Runtime{})
	})

	svc := NewService()
	ctx := WithProviderConfigOverrides(context.Background(), "https://glm-ocr.run.app", "")
	cfg := svc.providerConfigWithContext(ctx, "ollama", "glm-ocr:bf16", "prompt", 0)

	if cfg.BaseURL != "https://glm-ocr.run.app" {
		t.Fatalf("cfg.BaseURL = %q, want override", cfg.BaseURL)
	}
	if cfg.Audience != "https://default.run.app" {
		t.Fatalf("cfg.Audience = %q, want runtime fallback", cfg.Audience)
	}
}
