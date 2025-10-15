package hocr

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"

	"github.com/lehigh-university-libraries/hOCRedit/internal/laypa"
	"github.com/lehigh-university-libraries/hOCRedit/internal/models"
	"github.com/lehigh-university-libraries/hOCRedit/internal/providers"
)

type Service struct {
	providerService *providers.Service
	laypaService    *laypa.Service
}

func NewService() *Service {
	laypaService := laypa.NewService()

	if !laypaService.IsAvailable() {
		slog.Error("Laypa service is not available - this is a required dependency")
		panic("Laypa service is required but not available. Please ensure Laypa API is running.")
	}

	slog.Info("Initializing hOCR service",
		"detection_method", "Laypa segmentation",
		"transcription", "Multi-provider")

	return &Service{
		providerService: providers.NewService(),
		laypaService:    laypaService,
	}
}

func (s *Service) ProcessImageToHOCR(imagePath string) (string, error) {
	// Use the shared package implementation
	return s.processImageToHOCRUsingSharedPackage(imagePath)
}

func (s *Service) ProcessImageToHOCRWithConfig(imagePath, provider, model string) (string, error) {
	// Use the shared package implementation
	return s.processImageToHOCRWithConfigUsingSharedPackage(imagePath, provider, model)
}

func (s *Service) getImageDimensions(imagePath string) (int, int, error) {
	// Use ImageMagick to get dimensions
	cmd := exec.Command("magick", "identify", "-format", "%w %h", imagePath)
	output, err := cmd.Output()
	if err != nil {
		return 0, 0, fmt.Errorf("failed to get image dimensions: %w", err)
	}

	var width, height int
	_, err = fmt.Sscanf(strings.TrimSpace(string(output)), "%d %d", &width, &height)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to parse dimensions: %w", err)
	}

	return width, height, nil
}

// detectWordBoundariesCustom uses Laypa segmentation to find word boundaries
func (s *Service) detectWordBoundariesCustom(imagePath string) (models.OCRResponse, error) {
	slog.Info("Using Laypa for segmentation", "image", imagePath)

	ocrResponse, err := s.laypaService.DetectBoundaries(imagePath)
	if err != nil {
		return models.OCRResponse{}, fmt.Errorf("laypa segmentation failed: %w", err)
	}

	slog.Info("Laypa segmentation completed successfully")
	return ocrResponse, nil
}

// Note: All custom word detection code has been removed.
// We now rely exclusively on Laypa for segmentation.

// transcribeWithProvider uses the provider service to transcribe the image
func (s *Service) transcribeWithProvider(imagePath string) (string, error) {
	// Get provider and model from environment or use defaults
	providerName := s.providerService.GetDefaultProvider()
	model := s.providerService.GetDefaultModel(providerName)

	slog.Info("Using provider for transcription", "provider", providerName, "model", model)

	// Use the provider service to transcribe the image
	ctx := context.Background()
	result, err := s.providerService.TranscribeImage(ctx, providerName, model, imagePath)
	if err != nil {
		return "", fmt.Errorf("transcription failed with provider %s: %w", providerName, err)
	}

	// Apply the same cleaning logic that was used for ChatGPT
	cleanedResult := s.cleanProviderResponse(result)
	return cleanedResult, nil
}

// transcribeWithProviderAndModel uses the specified provider and model to transcribe the image
func (s *Service) transcribeWithProviderAndModel(imagePath, provider, model string) (string, error) {
	slog.Info("Using specific provider for transcription", "provider", provider, "model", model)

	// Use the provider service to transcribe the image
	ctx := context.Background()
	result, err := s.providerService.TranscribeImage(ctx, provider, model, imagePath)
	if err != nil {
		return "", fmt.Errorf("transcription failed with provider %s: %w", provider, err)
	}

	// Apply the same cleaning logic that was used for ChatGPT
	cleanedResult := s.cleanProviderResponse(result)
	return cleanedResult, nil
}

// cleanProviderResponse applies the same cleaning logic as the original ChatGPT implementation
func (s *Service) cleanProviderResponse(content string) string {
	// Clean up the provider response to fix common XML issues
	result := content

	// Handle standalone & characters that aren't part of valid entities
	// Replace & with &amp; unless it's already part of a valid entity
	result = s.fixAmpersands(result)

	// Clean up any other problematic characters in text content
	result = s.escapeTextContent(result)

	return result
}

