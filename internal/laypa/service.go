package laypa

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/lehigh-university-libraries/hOCRedit/internal/models"
	"github.com/lehigh-university-libraries/hOCRedit/internal/pagexml"
)

type Service struct {
	apiURL     string
	modelName  string
	httpClient *http.Client
}

// NewService creates a new Laypa service that communicates via API
func NewService() *Service {
	apiURL := os.Getenv("LAYPA_API_URL")
	if apiURL == "" {
		apiURL = "http://localhost:5000" // Default local development
	}

	modelName := os.Getenv("LAYPA_MODEL_NAME")
	if modelName == "" {
		modelName = "default" // Default model folder name
	}

	slog.Info("Initializing Laypa service",
		"api_url", apiURL,
		"model_name", modelName,
	)

	return &Service{
		apiURL:    apiURL,
		modelName: modelName,
		httpClient: &http.Client{
			Timeout: 5 * time.Minute, // Laypa can take time for inference
		},
	}
}

// DetectBoundaries runs Laypa inference on an image via API and returns bounding boxes
func (s *Service) DetectBoundaries(imagePath string) (models.OCRResponse, error) {
	// Generate a unique identifier for this request
	identifier := fmt.Sprintf("hocredit_%d", time.Now().UnixNano())

	// Open the image file
	imageFile, err := os.Open(imagePath)
	if err != nil {
		return models.OCRResponse{}, fmt.Errorf("failed to open image: %w", err)
	}
	defer imageFile.Close()

	// Create multipart form
	var requestBody bytes.Buffer
	writer := multipart.NewWriter(&requestBody)

	// Add image file
	part, err := writer.CreateFormFile("image", filepath.Base(imagePath))
	if err != nil {
		return models.OCRResponse{}, fmt.Errorf("failed to create form file: %w", err)
	}
	if _, err := io.Copy(part, imageFile); err != nil {
		return models.OCRResponse{}, fmt.Errorf("failed to copy image data: %w", err)
	}

	// Add identifier
	if err := writer.WriteField("identifier", identifier); err != nil {
		return models.OCRResponse{}, fmt.Errorf("failed to write identifier: %w", err)
	}

	// Add model name
	if err := writer.WriteField("model", s.modelName); err != nil {
		return models.OCRResponse{}, fmt.Errorf("failed to write model: %w", err)
	}

	writer.Close()

	// Send request to Laypa API
	predictURL := fmt.Sprintf("%s/predict", s.apiURL)
	slog.Info("Sending request to Laypa API", "url", predictURL, "identifier", identifier)

	req, err := http.NewRequest("POST", predictURL, &requestBody)
	if err != nil {
		return models.OCRResponse{}, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return models.OCRResponse{}, fmt.Errorf("laypa API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return models.OCRResponse{}, fmt.Errorf("laypa API returned status %d: %s", resp.StatusCode, string(body))
	}

	// Read the response which should be PageXML
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return models.OCRResponse{}, fmt.Errorf("failed to read response: %w", err)
	}

	// Parse the PageXML
	parsedPageXML, err := pagexml.ParsePageXML(bytes.NewReader(bodyBytes))
	if err != nil {
		return models.OCRResponse{}, fmt.Errorf("failed to parse PageXML: %w", err)
	}

	// Convert PageXML to OCRResponse
	ocrResponse, err := pagexml.ConvertToOCRResponse(parsedPageXML)
	if err != nil {
		return models.OCRResponse{}, fmt.Errorf("failed to convert PageXML to OCRResponse: %w", err)
	}

	slog.Info("Successfully converted Laypa output",
		"identifier", identifier,
		"paragraphs", len(ocrResponse.Responses[0].FullTextAnnotation.Pages[0].Blocks[0].Paragraphs),
	)

	return ocrResponse, nil
}

// IsAvailable checks if Laypa API is available
func (s *Service) IsAvailable() bool {
	// Check health endpoint
	healthURL := fmt.Sprintf("%s/health", s.apiURL)
	req, err := http.NewRequest("GET", healthURL, nil)
	if err != nil {
		slog.Warn("Failed to create health check request", "error", err)
		return false
	}

	// Use a shorter timeout for health check
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("Laypa API health check failed", "url", healthURL, "error", err)
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Warn("Laypa API health check returned non-OK status", "status", resp.StatusCode)
		return false
	}

	slog.Info("Laypa API is available", "url", s.apiURL)
	return true
}
