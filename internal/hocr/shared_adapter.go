package hocr

import (
	"fmt"

	"github.com/lehigh-university-libraries/htr/pkg/azure"
	"github.com/lehigh-university-libraries/htr/pkg/gemini"
	"github.com/lehigh-university-libraries/htr/pkg/hocr"
	"github.com/lehigh-university-libraries/htr/pkg/ollama"
	"github.com/lehigh-university-libraries/htr/pkg/openai"
	"github.com/lehigh-university-libraries/htr/pkg/providers"
)

// processImageToHOCRUsingSharedPackage uses the shared htr/pkg/hocr package
func (s *Service) processImageToHOCRUsingSharedPackage(imagePath string) (string, error) {
	// Step 1: Use shared package for word detection
	hocrResponse, err := hocr.DetectWordBoundariesCustom(imagePath)
	if err != nil {
		return "", fmt.Errorf("failed to detect word boundaries: %w", err)
	}

	// Step 2: Get provider and create config
	providerName := s.providerService.GetDefaultProvider()
	model := s.providerService.GetDefaultModel(providerName)

	provider, err := s.providerService.GetProvider(providerName)
	if err != nil {
		return "", fmt.Errorf("failed to get provider: %w", err)
	}

	// Convert to htr provider interface
	htrProvider, err := convertToHTRProvider(provider, providerName)
	if err != nil {
		return "", fmt.Errorf("failed to convert provider: %w", err)
	}

	// Create provider config
	config := createProviderConfig(providerName, model)

	// Step 3: Use individual word transcription
	hocrContent, err := hocr.TranscribeWordsIndividually(imagePath, hocrResponse, htrProvider, config)
	if err != nil {
		// Fall back to basic hOCR
		return hocr.ConvertToBasicHOCR(hocrResponse), nil
	}

	// Step 4: Wrap in hOCR document
	return hocr.WrapInHOCRDocument(hocrContent), nil
}

// processImageToHOCRWithConfigUsingSharedPackage uses the shared package with custom provider/model
func (s *Service) processImageToHOCRWithConfigUsingSharedPackage(imagePath, providerName, model string) (string, error) {
	// Step 1: Use shared package for word detection
	hocrResponse, err := hocr.DetectWordBoundariesCustom(imagePath)
	if err != nil {
		return "", fmt.Errorf("failed to detect word boundaries: %w", err)
	}

	// Step 2: Get provider instance
	htrProvider, err := convertToHTRProvider(nil, providerName)
	if err != nil {
		return "", fmt.Errorf("failed to get provider: %w", err)
	}

	// Create provider config
	config := createProviderConfig(providerName, model)

	// Step 3: Use individual word transcription
	hocrContent, err := hocr.TranscribeWordsIndividually(imagePath, hocrResponse, htrProvider, config)
	if err != nil {
		// Fall back to basic hOCR
		return hocr.ConvertToBasicHOCR(hocrResponse), nil
	}

	// Step 4: Wrap in hOCR document
	return hocr.WrapInHOCRDocument(hocrContent), nil
}

// convertToHTRProvider creates an HTR provider instance
func convertToHTRProvider(provider interface{}, providerName string) (providers.Provider, error) {
	switch providerName {
	case "openai":
		return openai.New(), nil
	case "azure":
		return azure.New(), nil
	case "gemini":
		return gemini.New(), nil
	case "ollama":
		return ollama.New(), nil
	default:
		return nil, fmt.Errorf("unsupported provider: %s", providerName)
	}
}

// createProviderConfig creates a provider config for the htr package
func createProviderConfig(providerName, model string) providers.Config {
	return providers.Config{
		Provider:    providerName,
		Model:       model,
		Temperature: 0.0,
	}
}
