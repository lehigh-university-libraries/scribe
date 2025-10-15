package providers

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"

	"github.com/lehigh-university-libraries/htr/pkg/providers"
	"github.com/lehigh-university-libraries/htr/pkg/azure"
	"github.com/lehigh-university-libraries/htr/pkg/gemini"
	"github.com/lehigh-university-libraries/htr/pkg/ollama"
	"github.com/lehigh-university-libraries/htr/pkg/openai"
)

// Service wraps the HTR provider functionality for use in hOCRedit
type Service struct {
	registry *providers.Registry
}

// NewService creates a new provider service with all available providers
func NewService() *Service {
	registry := providers.NewRegistry()

	// Register all available providers
	registry.Register(openai.New())
	registry.Register(azure.New())
	registry.Register(gemini.New())
	registry.Register(ollama.New())

	return &Service{
		registry: registry,
	}
}

// GetProvider returns a provider by name
func (s *Service) GetProvider(name string) (providers.Provider, error) {
	return s.registry.Get(name)
}

// ListProviders returns all available provider names
func (s *Service) ListProviders() []string {
	return s.registry.List()
}

// HasProvider checks if a provider is available
func (s *Service) HasProvider(name string) bool {
	return s.registry.HasProvider(name)
}

// TranscribeImage transcribes an image using the specified provider
func (s *Service) TranscribeImage(ctx context.Context, providerName, model, imagePath string) (string, error) {
	provider, err := s.GetProvider(providerName)
	if err != nil {
		return "", fmt.Errorf("failed to get provider %s: %w", providerName, err)
	}

	// Read and encode image
	imageData, err := os.ReadFile(imagePath)
	if err != nil {
		return "", fmt.Errorf("failed to read image: %w", err)
	}
	imageBase64 := base64.StdEncoding.EncodeToString(imageData)

	// Configure the provider
	config := providers.Config{
		Provider: providerName,
		Model:    model,
		Prompt: `Read and transcribe all the hOCR markup overlaid on this image.
You will see hOCR tags like:
<span class='ocrx_line' id='line_X' title='bbox x y w h'>
<span class='ocrx_word' id='word_X' title='bbox x y w h'>
[word image that needs transcription]
</span>
</span>

Transcribe BOTH the hOCR tags AND the text content inside them.
For each word image, read the text and include it between the word tags.
If a word image has no legible text, omit that word's span entirely.
IMPORTANT: If the transcribed text contains special characters like &, <, >, ", or ',
please replace them with their XML entities: &amp; &lt; &gt; &quot; &#39;
Return only the hOCR markup with transcribed text content.`,
		Temperature: 0.0,
	}

	// Validate configuration
	if err := provider.ValidateConfig(config); err != nil {
		return "", fmt.Errorf("provider configuration validation failed: %w", err)
	}

	// Extract text using the provider
	result, err := provider.ExtractText(ctx, config, imagePath, imageBase64)
	if err != nil {
		return "", fmt.Errorf("text extraction failed: %w", err)
	}

	return result, nil
}

// GetDefaultProvider returns the default provider name
func (s *Service) GetDefaultProvider() string {
	// Check environment variable first
	if provider := os.Getenv("OCR_PROVIDER"); provider != "" {
		return provider
	}
	// Default to Ollama
	return "ollama"
}

// GetDefaultModel returns the default model for a provider
func (s *Service) GetDefaultModel(providerName string) string {
	switch providerName {
	case "openai":
		if model := os.Getenv("OPENAI_MODEL"); model != "" {
			return model
		}
		return "gpt-4o"
	case "azure":
		if model := os.Getenv("AZURE_MODEL"); model != "" {
			return model
		}
		return "gpt-4o"
	case "gemini":
		if model := os.Getenv("GEMINI_MODEL"); model != "" {
			return model
		}
		return "gemini-1.5-flash"
	case "ollama":
		if model := os.Getenv("OLLAMA_MODEL"); model != "" {
			return model
		}
		return "mistral-small3.2:24b"
	default:
		return ""
	}
}