package hocr

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/lehigh-university-libraries/hOCRedit/internal/models"
	"github.com/otiai10/gosseract/v2"
)

type Service struct{}

func NewService() *Service {
	slog.Info("Initializing hOCR service (Tesseract word detection + ChatGPT transcription)")
	return &Service{}
}

func (s *Service) ProcessImageToHOCR(imagePath string) (string, error) {
	ocrResponse, err := s.detectWordBoundariesCustom(imagePath)
	if err != nil {
		return "", fmt.Errorf("failed to detect word boundaries with both methods: %w", err)
	}

	stitchedImagePath, err := s.createStitchedImageWithHOCRMarkup(imagePath, ocrResponse)
	if err != nil {
		slog.Warn("Failed to create stitched image, using basic hOCR output only", "error", err)
		return s.convertToBasicHOCR(ocrResponse), nil
	}
	defer os.Remove(stitchedImagePath)

	slog.Info("Created stitched image with hOCR markup", "path", stitchedImagePath)

	hocrResult, err := s.transcribeWithChatGPT(stitchedImagePath)
	if err != nil {
		slog.Warn("ChatGPT transcription failed", "err", err)
		return "", err
	}

	slog.Info("ChatGPT transcription completed", "result_length", hocrResult)

	return s.wrapInHOCRDocument(hocrResult), nil
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
