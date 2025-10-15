package hocr

import (
	"log/slog"

	"github.com/lehigh-university-libraries/hOCRedit/internal/laypa"
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

// Note: All custom word detection code has been removed.
// We now rely exclusively on Laypa for segmentation.
