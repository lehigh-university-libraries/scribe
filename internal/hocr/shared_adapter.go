package hocr

import (
	"fmt"
	"os"

	"github.com/lehigh-university-libraries/hOCRedit/internal/models"
	"github.com/lehigh-university-libraries/htr/pkg/hocr"
	"github.com/lehigh-university-libraries/htr/pkg/providers"
	"github.com/lehigh-university-libraries/htr/pkg/azure"
	"github.com/lehigh-university-libraries/htr/pkg/gemini"
	"github.com/lehigh-university-libraries/htr/pkg/ollama"
	"github.com/lehigh-university-libraries/htr/pkg/openai"
)

// convertModelsTohtr converts hOCRedit models to htr package models
func convertModelsTohtr(response models.OCRResponse) hocr.OCRResponse {
	var responses []hocr.Response

	for _, resp := range response.Responses {
		var pages []hocr.Page

		if resp.FullTextAnnotation != nil {
			for _, page := range resp.FullTextAnnotation.Pages {
				var blocks []hocr.Block

				for _, block := range page.Blocks {
					var paragraphs []hocr.Paragraph

					for _, para := range block.Paragraphs {
						var words []hocr.Word

						for _, word := range para.Words {
							var symbols []hocr.Symbol

							for _, symbol := range word.Symbols {
								symbols = append(symbols, hocr.Symbol{
									BoundingBox: convertBoundingPoly(symbol.BoundingBox),
									Text:        symbol.Text,
								})
							}

							words = append(words, hocr.Word{
								BoundingBox: convertBoundingPoly(word.BoundingBox),
								Symbols:     symbols,
							})
						}

						paragraphs = append(paragraphs, hocr.Paragraph{
							BoundingBox: convertBoundingPoly(para.BoundingBox),
							Words:       words,
						})
					}

					blocks = append(blocks, hocr.Block{
						BoundingBox: convertBoundingPoly(block.BoundingBox),
						Paragraphs:  paragraphs,
						BlockType:   block.BlockType,
					})
				}

				pages = append(pages, hocr.Page{
					Width:  page.Width,
					Height: page.Height,
					Blocks: blocks,
				})
			}

			responses = append(responses, hocr.Response{
				FullTextAnnotation: &hocr.FullTextAnnotation{
					Pages: pages,
					Text:  resp.FullTextAnnotation.Text,
				},
			})
		}
	}

	return hocr.OCRResponse{
		Responses: responses,
	}
}

// convertBoundingPoly converts models.BoundingPoly to hocr.BoundingPoly
func convertBoundingPoly(bp models.BoundingPoly) hocr.BoundingPoly {
	var vertices []hocr.Vertex
	for _, v := range bp.Vertices {
		vertices = append(vertices, hocr.Vertex{
			X: v.X,
			Y: v.Y,
		})
	}
	return hocr.BoundingPoly{
		Vertices: vertices,
	}
}

// convertFromhtr converts htr package models back to hOCRedit models
func convertFromhtr(response hocr.OCRResponse) models.OCRResponse {
	var responses []models.Response

	for _, resp := range response.Responses {
		var pages []models.Page

		if resp.FullTextAnnotation != nil {
			for _, page := range resp.FullTextAnnotation.Pages {
				var blocks []models.Block

				for _, block := range page.Blocks {
					var paragraphs []models.Paragraph

					for _, para := range block.Paragraphs {
						var words []models.Word

						for _, word := range para.Words {
							var symbols []models.Symbol

							for _, symbol := range word.Symbols {
								symbols = append(symbols, models.Symbol{
									BoundingBox: convertBoundingPolyFromhtr(symbol.BoundingBox),
									Text:        symbol.Text,
								})
							}

							words = append(words, models.Word{
								BoundingBox: convertBoundingPolyFromhtr(word.BoundingBox),
								Symbols:     symbols,
							})
						}

						paragraphs = append(paragraphs, models.Paragraph{
							BoundingBox: convertBoundingPolyFromhtr(para.BoundingBox),
							Words:       words,
						})
					}

					blocks = append(blocks, models.Block{
						BoundingBox: convertBoundingPolyFromhtr(block.BoundingBox),
						Paragraphs:  paragraphs,
						BlockType:   block.BlockType,
					})
				}

				pages = append(pages, models.Page{
					Width:  page.Width,
					Height: page.Height,
					Blocks: blocks,
				})
			}

			responses = append(responses, models.Response{
				FullTextAnnotation: &models.FullTextAnnotation{
					Pages: pages,
					Text:  resp.FullTextAnnotation.Text,
				},
			})
		}
	}

	return models.OCRResponse{
		Responses: responses,
	}
}

// convertBoundingPolyFromhtr converts hocr.BoundingPoly to models.BoundingPoly
func convertBoundingPolyFromhtr(bp hocr.BoundingPoly) models.BoundingPoly {
	var vertices []models.Vertex
	for _, v := range bp.Vertices {
		vertices = append(vertices, models.Vertex{
			X: v.X,
			Y: v.Y,
		})
	}
	return models.BoundingPoly{
		Vertices: vertices,
	}
}

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

