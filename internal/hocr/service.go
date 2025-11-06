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

	"github.com/lehigh-university-libraries/hOCRedit/internal/worddetection"
	"github.com/lehigh-university-libraries/htr/pkg/ollama"
	"github.com/lehigh-university-libraries/htr/pkg/openai"
	"github.com/lehigh-university-libraries/htr/pkg/providers"
)

type Service struct{}

func NewService() *Service {
	slog.Info("Initializing hOCR service (Tesseract word detection + LLM transcription)")
	return &Service{}
}

func (s *Service) ProcessImageToHOCR(imagePath string) (string, error) {
	ctx := context.Background()

	// Step 1: Get image dimensions
	width, height, err := s.getImageDimensions(imagePath)
	if err != nil {
		return "", fmt.Errorf("failed to get image dimensions: %w", err)
	}

	// Step 2: Run both word detection providers in parallel
	tesseractProvider := worddetection.NewTesseract()
	customProvider := worddetection.NewCustom()

	tesseractWords, tesseractErr := tesseractProvider.DetectWords(ctx, imagePath)
	customWords, customErr := customProvider.DetectWords(ctx, imagePath)

	// Log results from both providers
	slog.Info("Word detection results",
		"tesseract_count", len(tesseractWords),
		"tesseract_error", tesseractErr,
		"custom_count", len(customWords),
		"custom_error", customErr)

	// Pick the provider with more valid words
	var selectedWords []worddetection.WordBox
	var selectedProvider string

	if tesseractErr != nil && customErr != nil {
		return "", fmt.Errorf("both detection methods failed - tesseract: %v, custom: %v", tesseractErr, customErr)
	}

	if tesseractErr != nil {
		selectedWords = customWords
		selectedProvider = "custom"
	} else if customErr != nil {
		selectedWords = tesseractWords
		selectedProvider = "tesseract"
	} else if len(tesseractWords) >= len(customWords) {
		selectedWords = tesseractWords
		selectedProvider = "tesseract"
	} else {
		selectedWords = customWords
		selectedProvider = "custom"
	}

	slog.Info("Selected word detection provider",
		"provider", selectedProvider,
		"word_count", len(selectedWords))

	// Step 3: Group words into lines
	lines := s.groupWordsIntoLines(selectedWords)
	slog.Info("Grouped words into lines", "line_count", len(lines))

	// Step 4: Initialize LLM provider
	llmProvider, err := s.initLLMProvider()
	if err != nil {
		return "", fmt.Errorf("failed to initialize LLM provider: %w", err)
	}

	// Step 5: Transcribe each word using LLM
	transcribedWords, err := s.transcribeWords(imagePath, selectedWords, llmProvider)
	if err != nil {
		return "", fmt.Errorf("failed to transcribe words: %w", err)
	}

	slog.Info("Word transcription completed", "transcribed_count", len(transcribedWords))

	// Step 6: Generate hOCR
	hocr := s.generateHOCRFromWords(transcribedWords, lines, width, height)

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

// groupWordsIntoLines groups detected words into text lines based on coordinates
func (s *Service) groupWordsIntoLines(words []worddetection.WordBox) [][]worddetection.WordBox {
	if len(words) == 0 {
		return nil
	}

	// Sort words by Y then X
	sortedWords := make([]worddetection.WordBox, len(words))
	copy(sortedWords, words)
	sort.Slice(sortedWords, func(i, j int) bool {
		yi := sortedWords[i].Y + sortedWords[i].Height/2
		yj := sortedWords[j].Y + sortedWords[j].Height/2
		if abs(yi-yj) <= 20 { // Same line threshold
			return sortedWords[i].X < sortedWords[j].X
		}
		return yi < yj
	})

	var lines [][]worddetection.WordBox
	var currentLine []worddetection.WordBox

	for _, word := range sortedWords {
		if len(currentLine) == 0 {
			currentLine = append(currentLine, word)
			continue
		}

		// Check if this word belongs to current line
		lastWord := currentLine[len(currentLine)-1]
		lastY := lastWord.Y + lastWord.Height/2
		currentY := word.Y + word.Height/2

		if abs(lastY-currentY) <= 20 {
			currentLine = append(currentLine, word)
		} else {
			lines = append(lines, currentLine)
			currentLine = []worddetection.WordBox{word}
		}
	}

	if len(currentLine) > 0 {
		lines = append(lines, currentLine)
	}

	return lines
}

// transcribeWords extracts and transcribes each word using the LLM provider
func (s *Service) transcribeWords(imagePath string, words []worddetection.WordBox, provider providers.Provider) ([]TranscribedWord, error) {
	ctx := context.Background()
	transcribed := make([]TranscribedWord, 0, len(words))

	providerName := provider.Name()
	model := s.getModelForProvider(providerName)
	slog.Info("Starting word transcription", "provider", providerName, "model", model, "word_count", len(words))

	skippedCount := 0
	for i, word := range words {
		// Skip empty words
		if strings.TrimSpace(word.Text) == "" {
			skippedCount++
			continue
		}

		// Validate that this is likely a real word
		if !s.isLikelyWordBox(word) {
			slog.Debug("Skipping non-word detection", "index", i,
				"width", word.Width,
				"height", word.Height,
				"detected_text", word.Text)
			skippedCount++
			continue
		}

		// Extract word image
		wordImagePath, err := s.extractWordFromImage(imagePath, word.X, word.Y, word.X+word.Width, word.Y+word.Height, i)
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
			X:          word.X,
			Y:          word.Y,
			Width:      word.Width,
			Height:     word.Height,
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

// isLikelyWordBox validates whether a detected region is likely to be a real word
func (s *Service) isLikelyWordBox(box worddetection.WordBox) bool {
	// Check 1: Minimum size - too small is likely noise
	if box.Width < 10 || box.Height < 10 {
		return false
	}

	// Check 2: Maximum size - too large is likely not a single word
	if box.Width > 500 || box.Height > 200 {
		return false
	}

	// Check 3: Aspect ratio - words are typically wider than tall
	// Reject very tall/narrow regions (like vertical lines or borders)
	aspectRatio := float64(box.Width) / float64(box.Height)
	if aspectRatio < 0.3 || aspectRatio > 15 {
		return false
	}

	// Check 4: Detected text should have reasonable characters
	// Filter out detections with only special characters or numbers
	word := strings.TrimSpace(box.Text)
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

// generateHOCRFromWords generates hOCR output from transcribed words and detected lines
func (s *Service) generateHOCRFromWords(transcribedWords []TranscribedWord, lines [][]worddetection.WordBox, width, height int) string {
	var hocrLines []string

	// Group transcribed words by line based on Y-coordinate proximity
	lineWords := make([][]TranscribedWord, len(lines))

	// For each transcribed word, find which line it belongs to
	for _, word := range transcribedWords {
		wordCenterY := word.Y + word.Height/2
		bestLineIdx := -1
		minDistance := int(^uint(0) >> 1) // Max int

		for lineIdx, line := range lines {
			if len(line) == 0 {
				continue
			}
			// Calculate line center Y
			lineCenterY := line[0].Y + line[0].Height/2
			distance := abs(wordCenterY - lineCenterY)
			if distance < minDistance {
				minDistance = distance
				bestLineIdx = lineIdx
			}
		}

		if bestLineIdx >= 0 && minDistance <= 20 {
			lineWords[bestLineIdx] = append(lineWords[bestLineIdx], word)
		}
	}

	// Generate hOCR for each line
	for lineID, lineWordList := range lineWords {
		if len(lineWordList) == 0 {
			continue
		}

		// Sort words by X coordinate within line
		sort.Slice(lineWordList, func(i, j int) bool {
			return lineWordList[i].X < lineWordList[j].X
		})

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
