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

	// Step 3b: For custom provider (handwritten text), filter out anomalously small lines
	if selectedProvider == "custom" {
		originalLineCount := len(lines)
		lines = s.filterValidLines(lines, width)
		slog.Info("Filtered lines for custom provider",
			"original_count", originalLineCount,
			"filtered_count", len(lines),
			"removed", originalLineCount-len(lines))
	}

	// Step 4: Initialize LLM provider
	llmProvider, err := s.initLLMProvider()
	if err != nil {
		return "", fmt.Errorf("failed to initialize LLM provider: %w", err)
	}

	// Step 5: Transcribe words/lines using LLM (line-based if custom provider selected)
	transcribedWords, err := s.transcribeWords(imagePath, selectedWords, width, height, llmProvider, selectedProvider, lines)
	if err != nil {
		return "", fmt.Errorf("failed to transcribe words: %w", err)
	}

	slog.Info("Word transcription completed", "transcribed_count", len(transcribedWords))

	// Step 6: Generate hOCR
	hocr := s.generateHOCRFromWords(transcribedWords, lines, width, height, selectedProvider)

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

// transcribeWords extracts and transcribes words in batches using the LLM provider
// If detectionProvider is "custom", transcribes entire lines instead of individual words
// The lines parameter contains pre-filtered lines (filtered in ProcessImageToHOCR)
func (s *Service) transcribeWords(imagePath string, words []worddetection.WordBox, imageWidth, imageHeight int, provider providers.Provider, detectionProvider string, lines [][]worddetection.WordBox) ([]TranscribedWord, error) {
	ctx := context.Background()
	transcribed := make([]TranscribedWord, 0, len(words))

	providerName := provider.Name()
	model := s.getModelForProvider(providerName)
	batchSize := s.getBatchSize()

	// For custom provider (handwritten text), transcribe pre-filtered lines
	if detectionProvider == "custom" {
		slog.Info("Using line-based transcription for custom provider", "provider", providerName, "model", model, "line_count", len(lines))
		return s.transcribeLinesForCustomProvider(ctx, imagePath, lines, imageWidth, imageHeight, provider, model, batchSize)
	}

	slog.Info("Starting batch word transcription", "provider", providerName, "model", model, "word_count", len(words), "batch_size", batchSize)

	// Filter valid words first
	validWords := make([]worddetection.WordBox, 0, len(words))
	skippedCount := 0
	for i, word := range words {
		// Skip empty words
		if strings.TrimSpace(word.Text) == "" {
			skippedCount++
			continue
		}

		// Validate that this is likely a real word
		if !s.isLikelyWordBox(word, imageWidth, imageHeight) {
			slog.Debug("Skipping non-word detection", "index", i,
				"width", word.Width,
				"height", word.Height,
				"detected_text", word.Text)
			skippedCount++
			continue
		}

		validWords = append(validWords, word)
	}

	slog.Info("Filtered words for transcription", "valid", len(validWords), "skipped", skippedCount, "total", len(words))

	// Process words in batches
	for batchStart := 0; batchStart < len(validWords); batchStart += batchSize {
		batchEnd := batchStart + batchSize
		if batchEnd > len(validWords) {
			batchEnd = len(validWords)
		}

		batch := validWords[batchStart:batchEnd]
		batchNum := (batchStart / batchSize) + 1
		totalBatches := (len(validWords) + batchSize - 1) / batchSize

		slog.Info("Processing batch", "batch", batchNum, "total_batches", totalBatches, "words_in_batch", len(batch))

		// Stitch word images together
		stitchedImagePath, err := s.stitchWordImages(imagePath, batch)
		if err != nil {
			slog.Warn("Failed to stitch word images", "batch", batchNum, "error", err)
			continue
		}
		defer os.Remove(stitchedImagePath)

		// Convert to base64
		imageData, err := os.ReadFile(stitchedImagePath)
		if err != nil {
			slog.Warn("Failed to read stitched image", "batch", batchNum, "error", err)
			continue
		}
		imageBase64 := base64.StdEncoding.EncodeToString(imageData)

		// Create prompt for batch transcription
		prompt := fmt.Sprintf("There are %d words in this image arranged horizontally. Transcribe each word on a separate line. Return ONLY the words, one per line, with no additional text, numbering, or explanation. If a word is not legible, use an empty line for that position.", len(batch))

		config := providers.Config{
			Model:       model,
			Prompt:      prompt,
			Temperature: 0.0,
		}

		text, _, err := provider.ExtractText(ctx, config, stitchedImagePath, imageBase64)
		if err != nil {
			slog.Warn("Failed to transcribe batch", "batch", batchNum, "error", err)
			continue
		}

		// Parse response - split by newlines
		lines := strings.Split(strings.TrimSpace(text), "\n")

		slog.Debug("Batch transcription result", "batch", batchNum, "expected_words", len(batch), "received_lines", len(lines))

		// Map transcribed words back to their original positions
		for i, word := range batch {
			var transcribedText string
			if i < len(lines) {
				transcribedText = strings.TrimSpace(lines[i])
			}

			// Skip empty transcriptions
			if transcribedText == "" {
				continue
			}

			transcribed = append(transcribed, TranscribedWord{
				X:          word.X,
				Y:          word.Y,
				Width:      word.Width,
				Height:     word.Height,
				Text:       transcribedText,
				Confidence: 90.0, // Slightly lower confidence for batch processing
			})
		}
	}

	slog.Info("Batch transcription completed", "transcribed", len(transcribed), "skipped", skippedCount, "total", len(words))
	return transcribed, nil
}

// transcribeLinesForCustomProvider transcribes entire lines for handwritten text
// This is used when the custom provider is selected (indicating handwritten text)
// The lines parameter should be pre-filtered (filtering happens in ProcessImageToHOCR)
// Each line is processed individually (no batching) for better accuracy
func (s *Service) transcribeLinesForCustomProvider(ctx context.Context, imagePath string, lines [][]worddetection.WordBox, imageWidth, imageHeight int, provider providers.Provider, model string, batchSize int) ([]TranscribedWord, error) {
	if len(lines) == 0 {
		slog.Info("No lines to transcribe for custom provider")
		return nil, nil
	}

	slog.Info("Transcribing lines for custom provider (one at a time)", "line_count", len(lines))
	transcribed := make([]TranscribedWord, 0, len(lines))
	skippedEmpty := 0

	// Process each line individually (no batching)
	for lineIndex, line := range lines {
		// Calculate bounding box for the entire line (not individual words)
		if len(line) == 0 {
			slog.Debug("Skipping empty line", "line_index", lineIndex)
			continue
		}

		minX, minY := line[0].X, line[0].Y
		maxX, maxY := line[0].X+line[0].Width, line[0].Y+line[0].Height

		for _, word := range line {
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

		lineWidth := maxX - minX
		lineHeight := maxY - minY

		slog.Info("Processing line",
			"line_index", lineIndex,
			"progress", fmt.Sprintf("%d/%d", lineIndex+1, len(lines)),
			"x", minX, "y", minY,
			"width", lineWidth, "height", lineHeight,
			"word_count", len(line))

		// Extract the line image region
		lineImagePath, err := s.extractLineImage(imagePath, minX, minY, maxX, maxY, lineIndex)
		if err != nil {
			slog.Warn("Failed to extract line image", "line_index", lineIndex, "error", err)
			continue
		}
		defer os.Remove(lineImagePath)

		// Convert to base64
		imageData, err := os.ReadFile(lineImagePath)
		if err != nil {
			slog.Warn("Failed to read line image", "line_index", lineIndex, "error", err)
			continue
		}
		imageBase64 := base64.StdEncoding.EncodeToString(imageData)

		// Create prompt for single line transcription
		prompt := "Transcribe the handwritten text in this image. Return ONLY the transcribed text with no additional commentary, numbering, or explanation. If the text is not legible or cannot be read, return exactly: not legible."

		config := providers.Config{
			Model:       model,
			Prompt:      prompt,
			Temperature: 0.0,
		}

		text, _, err := provider.ExtractText(ctx, config, lineImagePath, imageBase64)
		if err != nil {
			slog.Warn("Failed to transcribe line", "line_index", lineIndex, "error", err)
			continue
		}

		transcribedText := strings.TrimSpace(text)

		// Skip empty transcriptions - these will not appear in the hOCR
		if transcribedText == "" {
			slog.Info("Line transcribed as empty, excluding from hOCR", "line_index", lineIndex)
			skippedEmpty++
			continue
		}

		// Filter out LLM refusal responses and illegible markers
		if s.isRefusalOrIllegible(transcribedText) {
			slog.Info("Line marked as illegible or refusal, excluding from hOCR",
				"line_index", lineIndex,
				"response", transcribedText)
			skippedEmpty++
			continue
		}

		slog.Info("Line transcribed successfully",
			"line_index", lineIndex,
			"text_length", len(transcribedText),
			"text_preview", truncateString(transcribedText, 50))

		// Create a TranscribedWord for the entire line
		transcribed = append(transcribed, TranscribedWord{
			X:          minX,
			Y:          minY,
			Width:      lineWidth,
			Height:     lineHeight,
			Text:       transcribedText,
			Confidence: 85.0,
			LineID:     lineIndex,
		})
	}

	slog.Info("Line transcription completed",
		"total_lines", len(lines),
		"transcribed_lines", len(transcribed),
		"skipped_empty", skippedEmpty)
	return transcribed, nil
}

// extractLineImage extracts a line region from the image
func (s *Service) extractLineImage(imagePath string, minX, minY, maxX, maxY, lineIndex int) (string, error) {
	width := maxX - minX
	height := maxY - minY

	// Validate dimensions
	if width <= 0 || height <= 0 {
		return "", fmt.Errorf("invalid dimensions: width=%d, height=%d", width, height)
	}

	// Add padding for better context
	padding := 10
	cropX := max(0, minX-padding)
	cropY := max(0, minY-padding)
	cropWidth := width + 2*padding
	cropHeight := height + 2*padding

	outputPath := filepath.Join("/tmp", fmt.Sprintf("line_%d_%d.png", lineIndex, time.Now().UnixNano()))

	// Extract line region
	cmd := exec.Command("magick", imagePath,
		"-crop", fmt.Sprintf("%dx%d+%d+%d", cropWidth, cropHeight, cropX, cropY),
		"+repage",
		outputPath)

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to extract line image: %w", err)
	}

	return outputPath, nil
}

// truncateString truncates a string to maxLen characters, adding "..." if truncated
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// isRefusalOrIllegible checks if the LLM response indicates refusal or illegibility
func (s *Service) isRefusalOrIllegible(text string) bool {
	textLower := strings.ToLower(text)

	// Common refusal patterns
	refusalPatterns := []string{
		"not legible",
		"illegible",
		"cannot transcribe",
		"can't transcribe",
		"unable to transcribe",
		"cannot read",
		"can't read",
		"unable to read",
		"i am sorry",
		"i'm sorry",
		"i apologize",
		"as an ai",
		"as a language model",
		"i cannot",
		"i can't",
		"no text visible",
		"no text found",
		"blank image",
		"empty image",
	}

	for _, pattern := range refusalPatterns {
		if strings.Contains(textLower, pattern) {
			return true
		}
	}

	return false
}

// filterValidLines filters out lines that are anomalously small compared to the average
// This removes detection errors that are too small to be real lines of text
func (s *Service) filterValidLines(lines [][]worddetection.WordBox, imageWidth int) [][]worddetection.WordBox {
	if len(lines) == 0 {
		return lines
	}

	// Calculate width of each line
	lineWidths := make([]int, len(lines))
	for i, line := range lines {
		if len(line) == 0 {
			continue
		}

		// Find min and max X coordinates
		minX := line[0].X
		maxX := line[0].X + line[0].Width

		for _, word := range line {
			if word.X < minX {
				minX = word.X
			}
			if word.X+word.Width > maxX {
				maxX = word.X + word.Width
			}
		}

		lineWidths[i] = maxX - minX
	}

	// Calculate average line width
	totalWidth := 0
	validCount := 0
	for _, width := range lineWidths {
		if width > 0 {
			totalWidth += width
			validCount++
		}
	}

	if validCount == 0 {
		return lines
	}

	avgWidth := float64(totalWidth) / float64(validCount)

	// Calculate median for more robust filtering
	sortedWidths := make([]int, len(lineWidths))
	copy(sortedWidths, lineWidths)
	sort.Ints(sortedWidths)
	medianWidth := float64(sortedWidths[len(sortedWidths)/2])

	// Use the larger of average or median as reference
	referenceWidth := avgWidth
	if medianWidth > avgWidth {
		referenceWidth = medianWidth
	}

	slog.Debug("Line width statistics",
		"avg_width", avgWidth,
		"median_width", medianWidth,
		"reference_width", referenceWidth,
		"image_width", imageWidth)

	// Filter lines based on multiple criteria
	var validLines [][]worddetection.WordBox
	minAbsoluteWidth := int(float64(imageWidth) * 0.15) // At least 15% of image width
	minRelativeWidth := int(referenceWidth * 0.35)      // At least 35% of reference width

	for i, line := range lines {
		width := lineWidths[i]

		// Skip empty lines
		if len(line) == 0 || width == 0 {
			slog.Debug("Skipping empty line", "line_index", i)
			continue
		}

		// Check if line meets minimum width requirements
		if width < minAbsoluteWidth {
			slog.Debug("Skipping line - too narrow (absolute)",
				"line_index", i,
				"width", width,
				"min_absolute", minAbsoluteWidth,
				"percent_of_image", float64(width)/float64(imageWidth)*100)
			continue
		}

		if width < minRelativeWidth {
			slog.Debug("Skipping line - too narrow (relative)",
				"line_index", i,
				"width", width,
				"min_relative", minRelativeWidth,
				"percent_of_reference", float64(width)/referenceWidth*100)
			continue
		}

		validLines = append(validLines, line)
	}

	return validLines
}

// isLikelyWordBox validates whether a detected region is likely to be a real word
// Uses relative sizing based on image dimensions to adapt to different image resolutions
func (s *Service) isLikelyWordBox(box worddetection.WordBox, imageWidth, imageHeight int) bool {
	// Check 1: Minimum size - too small is likely noise
	// Use relative sizing: min 0.5% of image width and 0.8% of image height
	minWidth := int(float64(imageWidth) * 0.02)
	minHeight := int(float64(imageHeight) * 0.01)

	// Ensure absolute minimums for very small images
	if minWidth < 10 {
		minWidth = 10
	}
	if minHeight < 10 {
		minHeight = 10
	}

	if box.Width < minWidth || box.Height < minHeight {
		return false
	}

	// Check 2: Maximum size - too large is likely not a single word
	// Use relative sizing: max 25% of image width and 10% of image height
	maxWidth := int(float64(imageWidth) * 0.25)
	maxHeight := int(float64(imageHeight) * 0.10)

	// Cap absolute maximums for very large images
	if maxWidth > 500 {
		maxWidth = 500
	}
	if maxHeight > 200 {
		maxHeight = 200
	}

	if box.Width > maxWidth || box.Height > maxHeight {
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

// getBatchSize returns the batch size for word transcription from environment
func (s *Service) getBatchSize() int {
	batchSizeStr := os.Getenv("BATCH_SIZE")
	if batchSizeStr == "" {
		return 10 // Default batch size
	}

	var batchSize int
	_, err := fmt.Sscanf(batchSizeStr, "%d", &batchSize)
	if err != nil || batchSize < 1 {
		slog.Warn("Invalid BATCH_SIZE, using default", "value", batchSizeStr, "default", 10)
		return 10
	}

	return batchSize
}

// stitchWordImages combines multiple word images horizontally into a single image
func (s *Service) stitchWordImages(imagePath string, words []worddetection.WordBox) (string, error) {
	if len(words) == 0 {
		return "", fmt.Errorf("no words to stitch")
	}

	// Create output path
	outputPath := filepath.Join("/tmp", fmt.Sprintf("stitched_%d.png", time.Now().UnixNano()))

	// Build ImageMagick command to extract and stitch words horizontally
	// We'll use multiple -crop operations and +append to combine horizontally
	args := []string{imagePath}

	for _, word := range words {
		// Add crop for each word with padding
		padding := 5
		cropX := max(0, word.X-padding)
		cropY := max(0, word.Y-padding)
		cropWidth := word.Width + 2*padding
		cropHeight := word.Height + 2*padding

		args = append(args, "(", "-clone", "0",
			"-crop", fmt.Sprintf("%dx%d+%d+%d", cropWidth, cropHeight, cropX, cropY),
			"+repage", ")")
	}

	// Remove original image and append all cropped images horizontally
	args = append(args, "-delete", "0", "+append", outputPath)

	cmd := exec.Command("magick", args...)
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to stitch word images: %w", err)
	}

	return outputPath, nil
}

// generateHOCRFromWords generates hOCR output from transcribed words and detected lines
// If detectionProvider is "custom", each transcribedWord represents a full line
func (s *Service) generateHOCRFromWords(transcribedWords []TranscribedWord, lines [][]worddetection.WordBox, width, height int, detectionProvider string) string {
	var hocrLines []string

	// For custom provider, each TranscribedWord is a full line
	if detectionProvider == "custom" {
		// Sort by Y coordinate to ensure proper line ordering
		sort.Slice(transcribedWords, func(i, j int) bool {
			return transcribedWords[i].Y < transcribedWords[j].Y
		})

		for lineID, transcribedLine := range transcribedWords {
			// Each transcribedLine represents a full line of text
			lineBBox := fmt.Sprintf("bbox %d %d %d %d",
				transcribedLine.X, transcribedLine.Y,
				transcribedLine.X+transcribedLine.Width, transcribedLine.Y+transcribedLine.Height)

			// Create line span with the transcribed text as a single word
			lineSpan := fmt.Sprintf("<span class='ocr_line' id='line_%d' title='%s'>", lineID, lineBBox)

			// The entire line is treated as one word in the hOCR
			wordBBox := lineBBox
			wordSpan := fmt.Sprintf("<span class='ocrx_word' id='word_%d_0' title='%s; x_wconf %.0f'>%s</span>",
				lineID, wordBBox, transcribedLine.Confidence, html.EscapeString(transcribedLine.Text))

			lineSpan += wordSpan + "</span>"
			hocrLines = append(hocrLines, lineSpan)
		}

		return s.wrapInHOCRDocument(strings.Join(hocrLines, "\n"), width, height)
	}

	// For tesseract provider, use the original word-based grouping logic
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
