package hocr

import (
	"fmt"
	"html"
	"sort"
	"strings"

	"github.com/lehigh-university-libraries/scribe/internal/models"
)

type Converter struct {
	lineCounter int
	wordCounter int
}

func NewConverter() *Converter {
	return &Converter{
		lineCounter: 1,
		wordCounter: 1,
	}
}

func (h *Converter) ConvertToHOCRLines(ocrResponse models.OCRResponse) ([]models.HOCRLine, error) {
	if len(ocrResponse.Responses) == 0 {
		return nil, fmt.Errorf("no responses found in OCR data")
	}

	response := ocrResponse.Responses[0]
	if response.FullTextAnnotation == nil {
		return nil, fmt.Errorf("no full text annotation found")
	}

	var allLines []models.HOCRLine

	for _, page := range response.FullTextAnnotation.Pages {
		pageLines := h.convertPageToLines(page)
		allLines = append(allLines, pageLines...)
	}

	return allLines, nil
}

func (h *Converter) ConvertHOCRLinesToXML(lines []models.HOCRLine, pageWidth, pageHeight int) string {
	var hocr strings.Builder

	hocr.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	hocr.WriteString("<!DOCTYPE html PUBLIC \"-//W3C//DTD XHTML 1.0 Transitional//EN\"\n")
	hocr.WriteString("    \"http://www.w3.org/TR/xhtml1/DTD/xhtml1-transitional.dtd\">\n")
	hocr.WriteString("<html xmlns=\"http://www.w3.org/1999/xhtml\" xml:lang=\"en\" lang=\"en\">\n")
	hocr.WriteString("<head>\n")
	hocr.WriteString("<title></title>\n")
	hocr.WriteString("<meta http-equiv=\"Content-Type\" content=\"text/html; charset=utf-8\" />\n")
	hocr.WriteString("<meta name='ocr-system' content='custom-word-detection-with-chatgpt' />\n")
	hocr.WriteString("<meta name='ocr-capabilities' content='ocr_page ocr_carea ocr_par ocr_line ocrx_word' />\n")
	hocr.WriteString("</head>\n")
	hocr.WriteString("<body>\n")

	bbox := fmt.Sprintf("bbox 0 0 %d %d", pageWidth, pageHeight)
	fmt.Fprintf(&hocr, "<div class='ocr_page' id='page_1' title='%s'>\n", bbox)

	for _, line := range lines {
		hocr.WriteString(h.convertHOCRLineToXML(line))
	}

	hocr.WriteString("</div>\n")
	hocr.WriteString("</body>\n")
	hocr.WriteString("</html>\n")

	return hocr.String()
}

func (h *Converter) convertHOCRLineToXML(line models.HOCRLine) string {
	bbox := fmt.Sprintf("bbox %d %d %d %d", line.BBox.X1, line.BBox.Y1, line.BBox.X2, line.BBox.Y2)

	var lineBuilder strings.Builder
	fmt.Fprintf(&lineBuilder, "<span class='ocr_line' id='%s' title='%s'>", line.ID, bbox)

	for _, word := range line.Words {
		wordXML := h.convertHOCRWordToXML(word)
		lineBuilder.WriteString(wordXML)
	}

	lineBuilder.WriteString("</span>\n")
	return lineBuilder.String()
}

func (h *Converter) convertHOCRWordToXML(word models.HOCRWord) string {
	bbox := fmt.Sprintf("bbox %d %d %d %d", word.BBox.X1, word.BBox.Y1, word.BBox.X2, word.BBox.Y2)
	confidence := fmt.Sprintf("; x_wconf %.0f", word.Confidence)
	title := bbox + confidence

	return fmt.Sprintf("<span class='ocrx_word' id='%s' title='%s'>%s</span> ",
		word.ID, title, html.EscapeString(word.Text))
}

func (h *Converter) ConvertToHOCR(ocrResponse models.OCRResponse) (string, error) {
	lines, err := h.ConvertToHOCRLines(ocrResponse)
	if err != nil {
		return "", err
	}

	if len(ocrResponse.Responses) == 0 || ocrResponse.Responses[0].FullTextAnnotation == nil || len(ocrResponse.Responses[0].FullTextAnnotation.Pages) == 0 {
		return "", fmt.Errorf("no page data found")
	}

	page := ocrResponse.Responses[0].FullTextAnnotation.Pages[0]
	return h.ConvertHOCRLinesToXML(lines, page.Width, page.Height), nil
}

func (h *Converter) convertPageToLines(page models.Page) []models.HOCRLine {
	var allLines []models.HOCRLine

	for _, block := range page.Blocks {
		if block.BlockType == "TEXT" {
			blockLines := h.convertBlockToLines(block)
			allLines = append(allLines, blockLines...)
		}
	}

	return allLines
}

func (h *Converter) convertBlockToLines(block models.Block) []models.HOCRLine {
	var allLines []models.HOCRLine

	for _, paragraph := range block.Paragraphs {
		paragraphLines := h.convertParagraphToLines(paragraph)
		allLines = append(allLines, paragraphLines...)
	}

	return allLines
}

func (h *Converter) convertParagraphToLines(paragraph models.Paragraph) []models.HOCRLine {
	if len(paragraph.Words) == 0 {
		return []models.HOCRLine{}
	}

	// Group words into lines based on Y-coordinate proximity
	var lineGroups [][]models.Word

	// Sort words by reading order (top to bottom, left to right)
	sortedWords := make([]models.Word, len(paragraph.Words))
	copy(sortedWords, paragraph.Words)
	sort.Slice(sortedWords, func(i, j int) bool {
		// First sort by Y coordinate (top to bottom)
		yDiff := h.getWordCenterY(sortedWords[i]) - h.getWordCenterY(sortedWords[j])
		if abs(yDiff) > 20 { // Same line threshold: 20 pixels
			return yDiff < 0
		}
		// If roughly same Y, sort by X coordinate (left to right)
		return h.getWordCenterX(sortedWords[i]) < h.getWordCenterX(sortedWords[j])
	})

	// Group words into lines
	currentLine := []models.Word{sortedWords[0]}
	currentLineY := h.getWordCenterY(sortedWords[0])

	for i := 1; i < len(sortedWords); i++ {
		word := sortedWords[i]
		wordY := h.getWordCenterY(word)

		// Check if this word belongs to the current line (within 20 pixels vertically)
		if abs(wordY-currentLineY) <= 20 {
			currentLine = append(currentLine, word)
		} else {
			// Start a new line
			lineGroups = append(lineGroups, currentLine)
			currentLine = []models.Word{word}
			currentLineY = wordY
		}
	}
	// Don't forget the last line
	if len(currentLine) > 0 {
		lineGroups = append(lineGroups, currentLine)
	}

	// Convert line groups to HOCRLine objects
	var lines []models.HOCRLine
	for _, lineWords := range lineGroups {
		lineID := fmt.Sprintf("line_%d", h.lineCounter)
		h.lineCounter++

		// Calculate line bounding box from all words in the line
		lineBBox := h.calculateLineBoundingBox(lineWords)

		// Convert all words in this line
		var hocrWords []models.HOCRWord
		for _, ocrWord := range lineWords {
			hocrWord := h.convertOCRWordToHOCRWord(ocrWord, lineID)
			hocrWords = append(hocrWords, hocrWord)
		}

		line := models.HOCRLine{
			ID:    lineID,
			BBox:  lineBBox,
			Words: hocrWords,
		}

		lines = append(lines, line)
	}

	return lines
}

// Helper function to get word center Y coordinate
func (h *Converter) getWordCenterY(word models.Word) int {
	if len(word.BoundingBox.Vertices) < 4 {
		return 0
	}
	return (word.BoundingBox.Vertices[0].Y + word.BoundingBox.Vertices[2].Y) / 2
}

// Helper function to get word center X coordinate
func (h *Converter) getWordCenterX(word models.Word) int {
	if len(word.BoundingBox.Vertices) < 4 {
		return 0
	}
	return (word.BoundingBox.Vertices[0].X + word.BoundingBox.Vertices[2].X) / 2
}

// Helper function to calculate line bounding box from multiple words
func (h *Converter) calculateLineBoundingBox(words []models.Word) models.BBox {
	if len(words) == 0 {
		return models.BBox{X1: 0, Y1: 0, X2: 0, Y2: 0}
	}

	minX, minY := int(^uint(0)>>1), int(^uint(0)>>1) // Max int values
	maxX, maxY := 0, 0

	for _, word := range words {
		wordBBox := h.boundingPolyToBBoxStruct(word.BoundingBox)
		if wordBBox.X1 < minX {
			minX = wordBBox.X1
		}
		if wordBBox.Y1 < minY {
			minY = wordBBox.Y1
		}
		if wordBBox.X2 > maxX {
			maxX = wordBBox.X2
		}
		if wordBBox.Y2 > maxY {
			maxY = wordBBox.Y2
		}
	}

	return models.BBox{X1: minX, Y1: minY, X2: maxX, Y2: maxY}
}

func (h *Converter) convertOCRWordToHOCRWord(ocrWord models.Word, lineID string) models.HOCRWord {
	var text strings.Builder
	for _, symbol := range ocrWord.Symbols {
		text.WriteString(symbol.Text)
	}

	bbox := h.boundingPolyToBBoxStruct(ocrWord.BoundingBox)

	confidence := 95.0
	if ocrWord.Property != nil && len(ocrWord.Property.DetectedLanguages) > 0 {
		confidence = ocrWord.Property.DetectedLanguages[0].Confidence * 100
	}

	wordID := fmt.Sprintf("word_%d", h.wordCounter)
	h.wordCounter++

	return models.HOCRWord{
		ID:         wordID,
		Text:       text.String(),
		BBox:       bbox,
		Confidence: confidence,
		LineID:     lineID,
	}
}

func (h *Converter) boundingPolyToBBoxStruct(boundingPoly models.BoundingPoly) models.BBox {
	if len(boundingPoly.Vertices) == 0 {
		return models.BBox{X1: 0, Y1: 0, X2: 0, Y2: 0}
	}

	minX, minY := int(^uint(0)>>1), int(^uint(0)>>1)
	maxX, maxY := 0, 0

	for _, vertex := range boundingPoly.Vertices {
		if vertex.X < minX {
			minX = vertex.X
		}
		if vertex.X > maxX {
			maxX = vertex.X
		}
		if vertex.Y < minY {
			minY = vertex.Y
		}
		if vertex.Y > maxY {
			maxY = vertex.Y
		}
	}

	return models.BBox{X1: minX, Y1: minY, X2: maxX, Y2: maxY}
}
