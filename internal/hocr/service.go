package hocr

import (
	"context"
	"encoding/base64"
	"fmt"
	"html"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lehigh-university-libraries/hOCRedit/internal/models"
	"github.com/lehigh-university-libraries/htr/pkg/ollama"
	"github.com/lehigh-university-libraries/htr/pkg/openai"
	"github.com/lehigh-university-libraries/htr/pkg/providers"
	"github.com/otiai10/gosseract/v2"
)

type Service struct{}

func NewService() *Service {
	slog.Info("Initializing hOCR service (Tesseract word detection + LLM transcription)")
	return &Service{}
}

func (s *Service) ProcessImageToHOCR(imagePath string) (string, error) {
	// Step 1: Use Tesseract to detect word boundaries
	width, height, err := s.getImageDimensions(imagePath)
	if err != nil {
		return "", fmt.Errorf("failed to get image dimensions: %w", err)
	}

	words, lines, err := s.detectWordsWithTesseract(imagePath, width, height)
	if err != nil {
		return "", fmt.Errorf("failed to detect words: %w", err)
	}

	slog.Info("Tesseract word detection completed", "word_count", len(words), "line_count", len(lines))

	// Step 2: Initialize LLM provider
	provider, err := s.initLLMProvider()
	if err != nil {
		return "", fmt.Errorf("failed to initialize LLM provider: %w", err)
	}

	// Step 3: Transcribe each word using LLM
	transcribedWords, err := s.transcribeWords(imagePath, words, provider)
	if err != nil {
		return "", fmt.Errorf("failed to transcribe words: %w", err)
	}

	slog.Info("Word transcription completed", "transcribed_count", len(transcribedWords))

	// Step 4: Generate hOCR with Tesseract boxes + LLM transcriptions
	hocr := s.generateHOCR(transcribedWords, lines, width, height)

	return hocr, nil
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

// TranscribedWord represents a word with its bounding box and transcribed text
type TranscribedWord struct {
	X, Y, Width, Height int
	Text                string
	Confidence          float64
	LineID              int
}

// initLLMProvider initializes the appropriate LLM provider based on configuration
func (s *Service) initLLMProvider() (providers.Provider, error) {
	providerType := os.Getenv("LLM_PROVIDER")
	if providerType == "" {
		providerType = "ollama" // Default to Ollama
	}

	slog.Info("Initializing LLM provider", "provider", providerType)

	switch providerType {
	case "ollama":
		return ollama.New(), nil
	case "openai":
		return openai.New(), nil
	default:
		return nil, fmt.Errorf("unsupported LLM provider: %s (must be 'ollama' or 'openai')", providerType)
	}
}

// detectWordsWithTesseract uses Tesseract to detect word boundaries
func (s *Service) detectWordsWithTesseract(imagePath string, width, height int) ([]gosseract.BoundingBox, [][]gosseract.BoundingBox, error) {
	client := gosseract.NewClient()
	defer client.Close()

	if err := client.SetImage(imagePath); err != nil {
		return nil, nil, fmt.Errorf("failed to set image: %w", err)
	}

	// Get word-level bounding boxes
	words, err := client.GetBoundingBoxes(gosseract.RIL_WORD)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get bounding boxes: %w", err)
	}

	// Group words into lines
	lines := s.groupTesseractWordsIntoLines(words)

	return words, lines, nil
}

// groupTesseractWordsIntoLines groups Tesseract words into lines
func (s *Service) groupTesseractWordsIntoLines(words []gosseract.BoundingBox) [][]gosseract.BoundingBox {
	if len(words) == 0 {
		return nil
	}

	// Sort words by Y then X
	sortedWords := make([]gosseract.BoundingBox, len(words))
	copy(sortedWords, words)
	sort.Slice(sortedWords, func(i, j int) bool {
		yi := (sortedWords[i].Box.Min.Y + sortedWords[i].Box.Max.Y) / 2
		yj := (sortedWords[j].Box.Min.Y + sortedWords[j].Box.Max.Y) / 2
		if abs(yi-yj) <= 20 { // Same line threshold
			return sortedWords[i].Box.Min.X < sortedWords[j].Box.Min.X
		}
		return yi < yj
	})

	var lines [][]gosseract.BoundingBox
	var currentLine []gosseract.BoundingBox

	for _, word := range sortedWords {
		if len(currentLine) == 0 {
			currentLine = append(currentLine, word)
			continue
		}

		// Check if this word belongs to current line
		lastWord := currentLine[len(currentLine)-1]
		lastY := (lastWord.Box.Min.Y + lastWord.Box.Max.Y) / 2
		currentY := (word.Box.Min.Y + word.Box.Max.Y) / 2

		if abs(lastY-currentY) <= 20 {
			currentLine = append(currentLine, word)
		} else {
			lines = append(lines, currentLine)
			currentLine = []gosseract.BoundingBox{word}
		}
	}

	if len(currentLine) > 0 {
		lines = append(lines, currentLine)
	}

	return lines
}

// transcribeWords extracts and transcribes each word using the LLM provider
func (s *Service) transcribeWords(imagePath string, words []gosseract.BoundingBox, provider providers.Provider) ([]TranscribedWord, error) {
	ctx := context.Background()
	transcribed := make([]TranscribedWord, 0, len(words))

	providerName := provider.Name()
	model := s.getModelForProvider(providerName)
	slog.Info("Starting word transcription", "provider", providerName, "model", model, "word_count", len(words))

	skippedCount := 0
	for i, word := range words {
		// Skip empty words
		if strings.TrimSpace(word.Word) == "" {
			skippedCount++
			continue
		}

		// Validate that this is likely a real word
		if !s.isLikelyWord(word) {
			slog.Debug("Skipping non-word detection", "index", i,
				"width", word.Box.Max.X-word.Box.Min.X,
				"height", word.Box.Max.Y-word.Box.Min.Y,
				"detected_text", word.Word)
			skippedCount++
			continue
		}

		// Extract word image
		wordImagePath, err := s.extractWordFromImage(imagePath, word.Box.Min.X, word.Box.Min.Y, word.Box.Max.X, word.Box.Max.Y, i)
		if err != nil {
			slog.Warn("Failed to extract word image", "index", i, "error", err)
			continue
		}
		defer os.Remove(wordImagePath)

		// Convert to base64
		imageData, err := os.ReadFile(wordImagePath)
		if err != nil {
			slog.Warn("Failed to read word image", "index", i, "error", err)
			continue
		}
		imageBase64 := base64.StdEncoding.EncodeToString(imageData)

		// Transcribe using LLM
		config := providers.Config{
			Model:       model,
			Prompt:      "Transcribe the single word in this image. Return ONLY the word with no additional text, punctuation, or explanation. If there is no legible text, return an empty string.",
			Temperature: 0.0,
		}

		text, _, err := provider.ExtractText(ctx, config, wordImagePath, imageBase64)
		if err != nil {
			slog.Warn("Failed to transcribe word", "index", i, "error", err)
			continue
		}

		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}

		transcribed = append(transcribed, TranscribedWord{
			X:          word.Box.Min.X,
			Y:          word.Box.Min.Y,
			Width:      word.Box.Max.X - word.Box.Min.X,
			Height:     word.Box.Max.Y - word.Box.Min.Y,
			Text:       text,
			Confidence: 95.0, // Default confidence
		})

		if (i+1)%10 == 0 {
			slog.Info("Transcription progress", "completed", i+1, "total", len(words))
		}
	}

	slog.Info("Transcription completed", "transcribed", len(transcribed), "skipped", skippedCount, "total", len(words))
	return transcribed, nil
}

// isLikelyWord validates whether a detected region is likely to be a real word
func (s *Service) isLikelyWord(box gosseract.BoundingBox) bool {
	width := box.Box.Max.X - box.Box.Min.X
	height := box.Box.Max.Y - box.Box.Min.Y

	// Check 1: Minimum size - too small is likely noise
	if width < 10 || height < 10 {
		return false
	}

	// Check 2: Maximum size - too large is likely not a single word
	if width > 500 || height > 200 {
		return false
	}

	// Check 3: Aspect ratio - words are typically wider than tall
	// Reject very tall/narrow regions (like vertical lines or borders)
	aspectRatio := float64(width) / float64(height)
	if aspectRatio < 0.3 || aspectRatio > 15 {
		return false
	}

	// Check 4: Tesseract's detected text should have reasonable characters
	// Filter out detections with only special characters or numbers
	word := strings.TrimSpace(box.Word)
	if len(word) == 0 {
		return false
	}

	// Check if word contains at least some letters
	hasLetter := false
	specialCharCount := 0
	for _, char := range word {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') {
			hasLetter = true
		}
		// Count excessive special characters
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9')) {
			specialCharCount++
		}
	}

	// Reject if no letters or mostly special characters
	if !hasLetter || (float64(specialCharCount)/float64(len(word)) > 0.5) {
		return false
	}

	// Check 5: Very short "words" that are single characters might be noise
	// Unless they're common single-letter words
	if len(word) == 1 {
		validSingleChars := "aAiI" // Common single-letter words in English
		if !strings.Contains(validSingleChars, word) {
			return false
		}
	}

	return true
}

// extractWordFromImage extracts a word region from the image
func (s *Service) extractWordFromImage(imagePath string, minX, minY, maxX, maxY, wordIndex int) (string, error) {
	width := maxX - minX
	height := maxY - minY

	// Validate dimensions
	if width <= 0 || height <= 0 {
		return "", fmt.Errorf("invalid dimensions: width=%d, height=%d", width, height)
	}

	// Ensure minimum dimensions for vision models
	if width < 5 || height < 5 {
		return "", fmt.Errorf("word too small: width=%d, height=%d", width, height)
	}

	// Add padding
	padding := 5 // Increased padding for better context
	cropX := max(0, minX-padding)
	cropY := max(0, minY-padding)
	cropWidth := width + 2*padding
	cropHeight := height + 2*padding

	outputPath := filepath.Join("/tmp", fmt.Sprintf("word_%d_%d.png", wordIndex, time.Now().UnixNano()))

	// Extract and ensure minimum size
	cmd := exec.Command("magick", imagePath,
		"-crop", fmt.Sprintf("%dx%d+%d+%d", cropWidth, cropHeight, cropX, cropY),
		"+repage",
		"-resize", "x100>", // Ensure minimum height of 100px for clarity
		outputPath)

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to extract word image: %w", err)
	}

	return outputPath, nil
}

// getModelForProvider returns the appropriate model for the provider
func (s *Service) getModelForProvider(providerName string) string {
	switch providerName {
	case "ollama":
		model := os.Getenv("OLLAMA_MODEL")
		if model == "" {
			model = "mistral-small3.2:24b"
		}
		return model
	case "openai":
		model := os.Getenv("OPENAI_MODEL")
		if model == "" {
			model = "gpt-4o"
		}
		return model
	default:
		return ""
	}
}

// generateHOCR generates hOCR output from transcribed words
func (s *Service) generateHOCR(words []TranscribedWord, lines [][]gosseract.BoundingBox, width, height int) string {
	var hocrLines []string

	// Assign line IDs to words based on their position
	wordToLineID := make(map[int]int)
	wordIndex := 0
	for lineID, line := range lines {
		for range line {
			wordToLineID[wordIndex] = lineID
			wordIndex++
		}
	}

	// Group transcribed words by line
	lineWords := make(map[int][]TranscribedWord)
	for i, word := range words {
		lineID := wordToLineID[i]
		word.LineID = lineID
		lineWords[lineID] = append(lineWords[lineID], word)
	}

	// Generate hOCR for each line
	lineID := 0
	for _, lineWordList := range lineWords {
		if len(lineWordList) == 0 {
			continue
		}

		// Calculate line bounding box
		minX, minY := lineWordList[0].X, lineWordList[0].Y
		maxX, maxY := lineWordList[0].X+lineWordList[0].Width, lineWordList[0].Y+lineWordList[0].Height

		for _, word := range lineWordList {
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

		// Generate line span
		lineBBox := fmt.Sprintf("bbox %d %d %d %d", minX, minY, maxX, maxY)
		lineSpan := fmt.Sprintf("<span class='ocr_line' id='line_%d' title='%s'>", lineID, lineBBox)

		// Generate word spans
		var wordSpans []string
		for i, word := range lineWordList {
			wordBBox := fmt.Sprintf("bbox %d %d %d %d", word.X, word.Y, word.X+word.Width, word.Y+word.Height)
			wordSpan := fmt.Sprintf("<span class='ocrx_word' id='word_%d_%d' title='%s; x_wconf %.0f'>%s</span>",
				lineID, i, wordBBox, word.Confidence, html.EscapeString(word.Text))
			wordSpans = append(wordSpans, wordSpan)
		}

		lineSpan += strings.Join(wordSpans, " ") + "</span>"
		hocrLines = append(hocrLines, lineSpan)
		lineID++
	}

	return s.wrapInHOCRDocument(strings.Join(hocrLines, "\n"), width, height)
}

// wrapInHOCRDocument wraps content in a complete hOCR document
func (s *Service) wrapInHOCRDocument(content string, width, height int) string {
	bbox := fmt.Sprintf("bbox 0 0 %d %d", width, height)
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.0 Transitional//EN" "http://www.w3.org/TR/xhtml1/DTD/xhtml1-transitional.dtd">
<html xmlns="http://www.w3.org/1999/xhtml" xml:lang="en" lang="en">
<head>
<title></title>
<meta http-equiv="Content-Type" content="text/html;charset=utf-8" />
<meta name='ocr-system' content='hOCRedit-tesseract-llm' />
<meta name='ocr-capabilities' content='ocr_page ocr_carea ocr_par ocr_line ocrx_word' />
</head>
<body>
<div class='ocr_page' id='page_1' title='%s'>
%s
</div>
</body>
</html>`, bbox, content)
}

// detectWordBoundariesCustom uses Tesseract to find word boundaries
func (s *Service) detectWordBoundariesCustom(imagePath string) (models.OCRResponse, error) {
	// Get image dimensions first
	width, height, err := s.getImageDimensions(imagePath)
	if err != nil {
		return models.OCRResponse{}, fmt.Errorf("failed to get image dimensions: %w", err)
	}

	// Use Tesseract to detect words and lines
	client := gosseract.NewClient()
	defer client.Close()

	// Set image path
	if err := client.SetImage(imagePath); err != nil {
		return models.OCRResponse{}, fmt.Errorf("failed to set image: %w", err)
	}

	// Get word-level bounding boxes
	boxes, err := client.GetBoundingBoxes(gosseract.RIL_WORD)
	if err != nil {
		return models.OCRResponse{}, fmt.Errorf("failed to get bounding boxes: %w", err)
	}

	slog.Info("Tesseract word detection completed", "word_count", len(boxes), "image_size", fmt.Sprintf("%dx%d", width, height))

	// Convert Tesseract boxes to our WordBox format
	words := make([]WordBox, 0, len(boxes))
	for i, box := range boxes {
		// Skip boxes with no content
		if strings.TrimSpace(box.Word) == "" {
			continue
		}

		words = append(words, WordBox{
			X:      box.Box.Min.X,
			Y:      box.Box.Min.Y,
			Width:  box.Box.Max.X - box.Box.Min.X,
			Height: box.Box.Max.Y - box.Box.Min.Y,
			Text:   fmt.Sprintf("word_%d", i+1),
		})
	}

	// Group words into lines based on coordinates
	lines := s.groupWordsIntoLines(words)
	slog.Info("Grouped words into lines", "line_count", len(lines))

	// Convert to OCR response format
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
// Each line contains individual words, preserving word-level granularity
func (s *Service) convertWordsAndLinesToOCRResponse(lines []LineBox, width, height int) models.OCRResponse {
	var paragraphs []models.Paragraph

	// Convert each line to a paragraph containing individual words
	for _, line := range lines {
		var words []models.Word

		// Create individual Word objects for each WordBox in the line
		for j, wordBox := range line.Words {
			word := models.Word{
				BoundingBox: models.BoundingPoly{
					Vertices: []models.Vertex{
						{X: wordBox.X, Y: wordBox.Y},
						{X: wordBox.X + wordBox.Width, Y: wordBox.Y},
						{X: wordBox.X + wordBox.Width, Y: wordBox.Y + wordBox.Height},
						{X: wordBox.X, Y: wordBox.Y + wordBox.Height},
					},
				},
				Symbols: []models.Symbol{
					{
						BoundingBox: models.BoundingPoly{
							Vertices: []models.Vertex{
								{X: wordBox.X, Y: wordBox.Y},
								{X: wordBox.X + wordBox.Width, Y: wordBox.Y},
								{X: wordBox.X + wordBox.Width, Y: wordBox.Y + wordBox.Height},
								{X: wordBox.X, Y: wordBox.Y + wordBox.Height},
							},
						},
						Text: fmt.Sprintf("word_%d", j+1), // Placeholder text for each word
					},
				},
			}
			words = append(words, word)
		}

		// Create paragraph with all words from this line
		paragraph := models.Paragraph{
			BoundingBox: models.BoundingPoly{
				Vertices: []models.Vertex{
					{X: line.X, Y: line.Y},
					{X: line.X + line.Width, Y: line.Y},
					{X: line.X + line.Width, Y: line.Y + line.Height},
					{X: line.X, Y: line.Y + line.Height},
				},
			},
			Words: words, // Individual words per paragraph (word-level detection)
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
