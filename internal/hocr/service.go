package hocr

import (
	"context"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lehigh-university-libraries/hOCRedit/internal/models"
	"github.com/lehigh-university-libraries/hOCRedit/internal/providers"
)

type Service struct {
	providerService *providers.Service
}

func NewService() *Service {
	slog.Info("Initializing hOCR service (Custom word detection + Multi-provider transcription)")
	return &Service{
		providerService: providers.NewService(),
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

// detectWordBoundariesCustom uses our own image processing algorithm to find word boundaries
func (s *Service) detectWordBoundariesCustom(imagePath string) (models.OCRResponse, error) {
	// Get image dimensions first
	width, height, err := s.getImageDimensions(imagePath)
	if err != nil {
		return models.OCRResponse{}, fmt.Errorf("failed to get image dimensions: %w", err)
	}

	// Step 1: Detect individual words using image processing
	words, err := s.detectWords(imagePath, width, height)
	if err != nil {
		return models.OCRResponse{}, fmt.Errorf("failed to detect words: %w", err)
	}

	slog.Info("Custom word detection completed", "word_count", len(words), "image_size", fmt.Sprintf("%dx%d", width, height))

	// Step 2: Group words into lines based on coordinates
	lines := s.groupWordsIntoLines(words)
	slog.Info("Grouped words into lines", "line_count", len(lines))

	// Step 3: Convert to OCR response format
	return s.convertWordsAndLinesToOCRResponse(lines, width, height), nil
}

// WordBox represents a detected word with its bounding box
type WordBox struct {
	X, Y, Width, Height int
	Text                string // Placeholder text for custom detection
}

// LineBox represents a line of text containing multiple words
type LineBox struct {
	Words               []WordBox
	X, Y, Width, Height int // Bounding box of the entire line
}

// detectWords finds individual word regions using image processing
func (s *Service) detectWords(imagePath string, imgWidth, imgHeight int) ([]WordBox, error) {
	// Preprocess the image
	processedPath, err := s.preprocessImageForWordDetection(imagePath)
	if err != nil {
		return nil, fmt.Errorf("failed to preprocess image: %w", err)
	}
	defer os.Remove(processedPath)

	// Load processed image
	file, err := os.Open(processedPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open processed image: %w", err)
	}
	defer file.Close()

	img, _, err := image.Decode(file)
	if err != nil {
		return nil, fmt.Errorf("failed to decode processed image: %w", err)
	}

	// Find connected components (potential words)
	components := s.findWordComponents(img)

	// Filter and refine components to get word boxes
	wordBoxes := s.refineComponentsToWords(components, imgWidth, imgHeight)

	return wordBoxes, nil
}

// preprocessImageForWordDetection preprocesses the image for better word detection
func (s *Service) preprocessImageForWordDetection(imagePath string) (string, error) {
	tempDir := "/tmp"
	baseName := strings.TrimSuffix(filepath.Base(imagePath), filepath.Ext(imagePath))
	processedPath := filepath.Join(tempDir, fmt.Sprintf("processed_words_%s_%d.jpg", baseName, time.Now().Unix()))

	// Preprocess: grayscale, enhance contrast, sharpen, threshold
	cmd := exec.Command("magick", imagePath,
		"-colorspace", "Gray", // Convert to grayscale
		"-contrast-stretch", "0.15x0.05%", // Enhance contrast
		"-sharpen", "0x1", // Sharpen slightly
		"-morphology", "close", "rectangle:2x1", // Close small gaps horizontally
		"-threshold", "75%", // Apply threshold
		processedPath)

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("imagemagick preprocessing failed: %w", err)
	}

	return processedPath, nil
}

// findWordComponents finds connected components that could be words
func (s *Service) findWordComponents(img image.Image) []WordBox {
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	visited := make([][]bool, height)
	for i := range visited {
		visited[i] = make([]bool, width)
	}

	var components []WordBox

	// Find all connected components using flood fill
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			if !visited[y][x] && s.isTextPixel(img.At(x, y)) {
				minX, minY, maxX, maxY := x, y, x, y
				s.floodFillComponent(img, visited, x, y, &minX, &minY, &maxX, &maxY)

				// Filter by size to get potential words
				w := maxX - minX + 1
				h := maxY - minY + 1
				if s.isValidWordSize(w, h, width, height) {
					components = append(components, WordBox{
						X:      minX,
						Y:      minY,
						Width:  w,
						Height: h,
						Text:   fmt.Sprintf("word_%d", len(components)+1),
					})
				}
			}
		}
	}

	return components
}

// floodFillComponent performs flood fill to find connected text pixels
func (s *Service) floodFillComponent(img image.Image, visited [][]bool, x, y int, minX, minY, maxX, maxY *int) {
	bounds := img.Bounds()
	if x < 0 || x >= bounds.Dx() || y < 0 || y >= bounds.Dy() || visited[y][x] || !s.isTextPixel(img.At(x, y)) {
		return
	}

	visited[y][x] = true

	// Update bounding box
	if x < *minX {
		*minX = x
	}
	if x > *maxX {
		*maxX = x
	}
	if y < *minY {
		*minY = y
	}
	if y > *maxY {
		*maxY = y
	}

	// Check 8 neighbors
	directions := [][]int{{-1, -1}, {-1, 0}, {-1, 1}, {0, -1}, {0, 1}, {1, -1}, {1, 0}, {1, 1}}
	for _, dir := range directions {
		s.floodFillComponent(img, visited, x+dir[0], y+dir[1], minX, minY, maxX, maxY)
	}
}

// isTextPixel determines if a pixel is likely part of text (dark pixel)
func (s *Service) isTextPixel(c color.Color) bool {
	r, g, b, _ := c.RGBA()
	gray := (r + g + b) / 3
	return gray < 32768 // Dark pixels are considered text
}

// isValidWordSize checks if a component size is reasonable for a word
func (s *Service) isValidWordSize(w, h, imgWidth, imgHeight int) bool {
	// Filter by reasonable word dimensions
	minWidth, minHeight := 8, 10 // Minimum size for a word
	maxWidth := imgWidth / 2     // Words shouldn't be more than half the image width
	maxHeight := imgHeight / 5   // Words shouldn't be more than 1/5 the image height

	return w >= minWidth && h >= minHeight && w <= maxWidth && h <= maxHeight
}

// refineComponentsToWords refines detected components into word boxes
func (s *Service) refineComponentsToWords(components []WordBox, imgWidth, imgHeight int) []WordBox {
	if len(components) == 0 {
		return components
	}

	// Sort components for processing (top to bottom, left to right)
	sort.Slice(components, func(i, j int) bool {
		if abs(components[i].Y-components[j].Y) < 10 { // Same line threshold
			return components[i].X < components[j].X
		}
		return components[i].Y < components[j].Y
	})

	// Merge nearby components that likely belong to the same word
	mergedWords := s.mergeNearbyComponents(components)

	return mergedWords
}

// mergeNearbyComponents merges components that are close together into single words
func (s *Service) mergeNearbyComponents(components []WordBox) []WordBox {
	if len(components) <= 1 {
		return components
	}

	var mergedWords []WordBox
	currentGroup := []WordBox{components[0]}

	for i := 1; i < len(components); i++ {
		component := components[i]
		lastInGroup := currentGroup[len(currentGroup)-1]

		// Check if this component should be merged with the current group
		if s.shouldMergeComponents(lastInGroup, component) {
			currentGroup = append(currentGroup, component)
		} else {
			// Finish current group and start new one
			mergedWord := s.mergeComponentGroup(currentGroup)
			mergedWords = append(mergedWords, mergedWord)
			currentGroup = []WordBox{component}
		}
	}

	// Don't forget the last group
	if len(currentGroup) > 0 {
		mergedWord := s.mergeComponentGroup(currentGroup)
		mergedWords = append(mergedWords, mergedWord)
	}

	return mergedWords
}

// shouldMergeComponents determines if two components should be merged into one word
func (s *Service) shouldMergeComponents(a, b WordBox) bool {
	// Calculate horizontal and vertical distances
	horizontalGap := b.X - (a.X + a.Width)
	verticalOverlap := b.Y+b.Height >= a.Y && b.Y <= a.Y+a.Height

	// Merge if components are close horizontally and have vertical overlap
	maxGap := max(a.Height, b.Height) / 3 // Allow gap up to 1/3 of character height
	return horizontalGap >= 0 && horizontalGap <= maxGap && verticalOverlap
}

// mergeComponentGroup merges a group of components into a single word box
func (s *Service) mergeComponentGroup(group []WordBox) WordBox {
	if len(group) == 1 {
		return group[0]
	}

	minX, minY := group[0].X, group[0].Y
	maxX, maxY := group[0].X+group[0].Width, group[0].Y+group[0].Height

	for _, comp := range group[1:] {
		if comp.X < minX {
			minX = comp.X
		}
		if comp.Y < minY {
			minY = comp.Y
		}
		if comp.X+comp.Width > maxX {
			maxX = comp.X + comp.Width
		}
		if comp.Y+comp.Height > maxY {
			maxY = comp.Y + comp.Height
		}
	}

	return WordBox{
		X:      minX,
		Y:      minY,
		Width:  maxX - minX,
		Height: maxY - minY,
		Text:   fmt.Sprintf("merged_word_%d", len(group)),
	}
}

// groupWordsIntoLines groups detected words into text lines based on their coordinates
func (s *Service) groupWordsIntoLines(words []WordBox) []LineBox {
	if len(words) == 0 {
		return nil
	}

	// Sort words by Y coordinate first, then X coordinate
	sort.Slice(words, func(i, j int) bool {
		if abs(words[i].Y-words[j].Y) < words[i].Height/2 { // Same line threshold
			return words[i].X < words[j].X
		}
		return words[i].Y < words[j].Y
	})

	var lines []LineBox
	var currentLineWords []WordBox

	for _, word := range words {
		if len(currentLineWords) == 0 {
			currentLineWords = append(currentLineWords, word)
			continue
		}

		// Check if this word belongs to the current line
		if s.wordsOnSameLine(currentLineWords, word) {
			currentLineWords = append(currentLineWords, word)
		} else {
			// Finish current line and start new one
			if len(currentLineWords) > 0 {
				line := s.createLineFromWords(currentLineWords)
				lines = append(lines, line)
			}
			currentLineWords = []WordBox{word}
		}
	}

	// Don't forget the last line
	if len(currentLineWords) > 0 {
		line := s.createLineFromWords(currentLineWords)
		lines = append(lines, line)
	}

	return lines
}

// wordsOnSameLine determines if a word belongs to the current line
func (s *Service) wordsOnSameLine(currentLineWords []WordBox, newWord WordBox) bool {
	if len(currentLineWords) == 0 {
		return true
	}

	// Calculate average height of current line
	avgHeight := 0
	minY, maxY := currentLineWords[0].Y, currentLineWords[0].Y+currentLineWords[0].Height
	for _, word := range currentLineWords {
		avgHeight += word.Height
		if word.Y < minY {
			minY = word.Y
		}
		if word.Y+word.Height > maxY {
			maxY = word.Y + word.Height
		}
	}
	avgHeight /= len(currentLineWords)

	// Check for Y-coordinate overlap with some tolerance
	tolerance := avgHeight / 3
	currentLineBottom := maxY + tolerance
	currentLineTop := minY - tolerance

	return newWord.Y+newWord.Height >= currentLineTop && newWord.Y <= currentLineBottom
}

// createLineFromWords creates a LineBox from a group of words
func (s *Service) createLineFromWords(words []WordBox) LineBox {
	if len(words) == 0 {
		return LineBox{}
	}

	// Calculate line bounding box
	minX, minY := words[0].X, words[0].Y
	maxX, maxY := words[0].X+words[0].Width, words[0].Y+words[0].Height

	for _, word := range words[1:] {
		if word.X < minX {
			minX = word.X
		}
		if word.Y < minY {
			minY = word.Y
		}
		if word.X+word.Width > maxX {
			maxX = word.X + word.Width
		}
		if word.Y+word.Height > maxY {
			maxY = word.Y + word.Height
		}
	}

	return LineBox{
		Words:  words,
		X:      minX,
		Y:      minY,
		Width:  maxX - minX,
		Height: maxY - minY,
	}
}

// convertWordsAndLinesToOCRResponse converts our custom detection results to OCR response format
// Each line is treated as a single "word" for simplicity
func (s *Service) convertWordsAndLinesToOCRResponse(lines []LineBox, width, height int) models.OCRResponse {
	var paragraphs []models.Paragraph

	// Convert each line to a paragraph containing a single "word" (the entire line)
	for i, line := range lines {
		// Create a single word that represents the entire line
		word := models.Word{
			BoundingBox: models.BoundingPoly{
				Vertices: []models.Vertex{
					{X: line.X, Y: line.Y},
					{X: line.X + line.Width, Y: line.Y},
					{X: line.X + line.Width, Y: line.Y + line.Height},
					{X: line.X, Y: line.Y + line.Height},
				},
			},
			Symbols: []models.Symbol{
				{
					BoundingBox: models.BoundingPoly{
						Vertices: []models.Vertex{
							{X: line.X, Y: line.Y},
							{X: line.X + line.Width, Y: line.Y},
							{X: line.X + line.Width, Y: line.Y + line.Height},
							{X: line.X, Y: line.Y + line.Height},
						},
					},
					Text: fmt.Sprintf("line_%d", i+1), // Placeholder text for the entire line
				},
			},
		}

		paragraph := models.Paragraph{
			BoundingBox: models.BoundingPoly{
				Vertices: []models.Vertex{
					{X: line.X, Y: line.Y},
					{X: line.X + line.Width, Y: line.Y},
					{X: line.X + line.Width, Y: line.Y + line.Height},
					{X: line.X, Y: line.Y + line.Height},
				},
			},
			Words: []models.Word{word}, // Single word per paragraph (line-level detection)
		}
		paragraphs = append(paragraphs, paragraph)
	}

	block := models.Block{
		BoundingBox: models.BoundingPoly{
			Vertices: []models.Vertex{
				{X: 0, Y: 0},
				{X: width, Y: 0},
				{X: width, Y: height},
				{X: 0, Y: height},
			},
		},
		BlockType:  "TEXT",
		Paragraphs: paragraphs,
	}

	page := models.Page{
		Width:  width,
		Height: height,
		Blocks: []models.Block{block},
	}

	return models.OCRResponse{
		Responses: []models.Response{
			{
				FullTextAnnotation: &models.FullTextAnnotation{
					Pages: []models.Page{page},
					Text:  "Custom word detection with line grouping + ChatGPT transcription",
				},
			},
		},
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

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

