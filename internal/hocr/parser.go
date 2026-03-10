package hocr

import (
	"encoding/xml"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/lehigh-university-libraries/scribe/internal/models"
)

type XMLElement struct {
	XMLName  xml.Name
	Attrs    []xml.Attr   `xml:",any,attr"`
	Content  string       `xml:",chardata"`
	Children []XMLElement `xml:",any"`
}

type WordWithGlyphs struct {
	Word   models.HOCRWord    `json:"word"`
	Glyphs []models.HOCRGlyph `json:"glyphs"`
}

func ParseHOCRLines(hocrXML string) ([]models.HOCRLine, error) {
	var doc XMLElement

	decoder := xml.NewDecoder(strings.NewReader(hocrXML))
	if err := decoder.Decode(&doc); err != nil {
		return nil, fmt.Errorf("failed to parse XML: %w", err)
	}

	var lines []models.HOCRLine

	traverseLinesElements(doc, &lines)

	return lines, nil
}

func ParseHOCRWords(hocrXML string) ([]models.HOCRWord, error) {
	var doc XMLElement

	decoder := xml.NewDecoder(strings.NewReader(hocrXML))
	if err := decoder.Decode(&doc); err != nil {
		return nil, fmt.Errorf("failed to parse XML: %w", err)
	}

	var words []models.HOCRWord

	traverseElementsWithLineContext(doc, &words, "")

	return words, nil
}

func ParseHOCRWordGlyphs(hocrXML string) ([]WordWithGlyphs, error) {
	var doc XMLElement

	decoder := xml.NewDecoder(strings.NewReader(hocrXML))
	if err := decoder.Decode(&doc); err != nil {
		return nil, fmt.Errorf("failed to parse XML: %w", err)
	}

	var words []WordWithGlyphs
	traverseWordGlyphElements(doc, &words, "")
	return words, nil
}

func traverseLinesElements(element XMLElement, lines *[]models.HOCRLine) {
	if isLineElement(element) {
		line, err := parseLineElement(element)
		if err == nil && line.ID != "" {
			*lines = append(*lines, line)
		}
	}

	for _, child := range element.Children {
		traverseLinesElements(child, lines)
	}
}

func traverseElementsWithLineContext(element XMLElement, words *[]models.HOCRWord, currentLineID string) {
	// Update line ID if this element is a line element
	if isLineElement(element) {
		for _, attr := range element.Attrs {
			if attr.Name.Local == "id" {
				currentLineID = attr.Value
				break
			}
		}
	}

	// Parse word elements with line context
	if isWordElement(element) {
		word, err := parseWordElement(element)
		if err == nil && word.ID != "" && isValidWordText(word.Text) {
			word.LineID = currentLineID
			*words = append(*words, word)
		}
	}

	// Recursively traverse children with current line context
	for _, child := range element.Children {
		traverseElementsWithLineContext(child, words, currentLineID)
	}
}

func traverseWordGlyphElements(element XMLElement, words *[]WordWithGlyphs, currentLineID string) {
	if isLineElement(element) {
		for _, attr := range element.Attrs {
			if attr.Name.Local == "id" {
				currentLineID = attr.Value
				break
			}
		}
	}

	if isWordElement(element) {
		word, title, err := parseWordElementWithTitle(element)
		if err == nil && word.ID != "" && isValidWordText(word.Text) {
			word.LineID = currentLineID
			*words = append(*words, WordWithGlyphs{
				Word:   word,
				Glyphs: glyphsFromWord(word, title),
			})
		}
	}

	for _, child := range element.Children {
		traverseWordGlyphElements(child, words, currentLineID)
	}
}

func isLineElement(element XMLElement) bool {
	for _, attr := range element.Attrs {
		if attr.Name.Local == "class" && strings.Contains(attr.Value, "ocr_line") {
			return true
		}
	}
	return false
}

func isWordElement(element XMLElement) bool {
	for _, attr := range element.Attrs {
		if attr.Name.Local == "class" && strings.Contains(attr.Value, "ocrx_word") {
			return true
		}
	}
	return false
}

func parseLineElement(element XMLElement) (models.HOCRLine, error) {
	line := models.HOCRLine{}

	for _, attr := range element.Attrs {
		switch attr.Name.Local {
		case "id":
			line.ID = attr.Value
		case "title":
			if err := parseLineTitleAttribute(attr.Value, &line); err != nil {
				return line, fmt.Errorf("failed to parse title attribute: %w", err)
			}
		}
	}

	// Find ALL words in this line
	var words []models.HOCRWord
	findAllWordsInLine(element, &words, line.ID)
	line.Words = words

	return line, nil
}

func findAllWordsInLine(element XMLElement, words *[]models.HOCRWord, lineID string) {
	if isWordElement(element) {
		word, err := parseWordElement(element)
		if err == nil && word.ID != "" && isValidWordText(word.Text) {
			// Ensure line_id is properly set
			if lineID != "" {
				word.LineID = lineID
			} else {
				// Fallback: generate line ID if missing
				word.LineID = "line_" + word.ID
			}
			*words = append(*words, word)
		}
	}

	for _, child := range element.Children {
		findAllWordsInLine(child, words, lineID)
	}
}

func parseLineTitleAttribute(title string, line *models.HOCRLine) error {
	bboxRegex := regexp.MustCompile(`bbox\s+(\d+)\s+(\d+)\s+(\d+)\s+(\d+)`)
	if matches := bboxRegex.FindStringSubmatch(title); len(matches) == 5 {
		var err error
		if line.BBox.X1, err = strconv.Atoi(matches[1]); err != nil {
			return fmt.Errorf("invalid bbox x1: %w", err)
		}
		if line.BBox.Y1, err = strconv.Atoi(matches[2]); err != nil {
			return fmt.Errorf("invalid bbox y1: %w", err)
		}
		if line.BBox.X2, err = strconv.Atoi(matches[3]); err != nil {
			return fmt.Errorf("invalid bbox x2: %w", err)
		}
		if line.BBox.Y2, err = strconv.Atoi(matches[4]); err != nil {
			return fmt.Errorf("invalid bbox y2: %w", err)
		}
	}

	return nil
}

func parseWordElement(element XMLElement) (models.HOCRWord, error) {
	word := models.HOCRWord{}

	for _, attr := range element.Attrs {
		switch attr.Name.Local {
		case "id":
			word.ID = attr.Value
		case "title":
			if err := parseTitleAttribute(attr.Value, &word); err != nil {
				return word, fmt.Errorf("failed to parse title attribute: %w", err)
			}
		}
	}

	word.Text = strings.TrimSpace(element.Content)

	return word, nil
}

func parseWordElementWithTitle(element XMLElement) (models.HOCRWord, string, error) {
	word := models.HOCRWord{}
	title := ""

	for _, attr := range element.Attrs {
		switch attr.Name.Local {
		case "id":
			word.ID = attr.Value
		case "title":
			title = attr.Value
			if err := parseTitleAttribute(attr.Value, &word); err != nil {
				return word, title, fmt.Errorf("failed to parse title attribute: %w", err)
			}
		}
	}

	word.Text = strings.TrimSpace(element.Content)
	return word, title, nil
}

func glyphsFromWord(word models.HOCRWord, title string) []models.HOCRGlyph {
	textRunes := []rune(strings.TrimSpace(word.Text))
	if len(textRunes) == 0 {
		return nil
	}

	x1 := word.BBox.X1
	x2 := word.BBox.X2
	if x2 <= x1 {
		return nil
	}

	cuts := parseCuts(title)
	boundaries := normalizeBoundaries(x1, x2, cuts)
	if len(cuts) == 0 {
		boundaries = evenlySplitBoundaries(x1, x2, len(textRunes))
	}
	if len(boundaries) < 2 {
		return nil
	}

	segments := len(boundaries) - 1
	if segments <= 0 {
		return nil
	}

	glyphs := make([]models.HOCRGlyph, 0, segments)
	for i := 0; i < segments; i++ {
		startX := boundaries[i]
		endX := boundaries[i+1]
		if endX <= startX {
			continue
		}

		startRune := (i * len(textRunes)) / segments
		endRune := ((i + 1) * len(textRunes)) / segments
		if endRune <= startRune {
			endRune = startRune + 1
			if endRune > len(textRunes) {
				endRune = len(textRunes)
			}
		}
		if startRune >= len(textRunes) {
			break
		}
		glyphText := string(textRunes[startRune:endRune])

		glyphs = append(glyphs, models.HOCRGlyph{
			ID:     fmt.Sprintf("%s_g%d", word.ID, i+1),
			Text:   glyphText,
			BBox:   models.BBox{X1: startX, Y1: word.BBox.Y1, X2: endX, Y2: word.BBox.Y2},
			WordID: word.ID,
			LineID: word.LineID,
			Index:  i,
		})
	}
	return glyphs
}

func parseCuts(title string) []int {
	re := regexp.MustCompile(`cuts\s+([0-9,\s]+)`)
	matches := re.FindStringSubmatch(title)
	if len(matches) != 2 {
		return nil
	}

	raw := strings.FieldsFunc(matches[1], func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	out := make([]int, 0, len(raw))
	for _, token := range raw {
		if token == "" {
			continue
		}
		n, err := strconv.Atoi(token)
		if err != nil {
			continue
		}
		out = append(out, n)
	}
	return out
}

func normalizeBoundaries(x1, x2 int, cuts []int) []int {
	width := x2 - x1
	if width <= 0 {
		return nil
	}
	bounds := []int{x1, x2}
	for _, c := range cuts {
		v := c
		if v < x1 || v > x2 {
			v = x1 + c
		}
		if v <= x1 || v >= x2 {
			continue
		}
		bounds = append(bounds, v)
	}
	sort.Ints(bounds)
	dedup := bounds[:0]
	last := -1
	for _, b := range bounds {
		if b == last {
			continue
		}
		dedup = append(dedup, b)
		last = b
	}
	return dedup
}

func evenlySplitBoundaries(x1, x2, segments int) []int {
	if segments <= 0 || x2 <= x1 {
		return nil
	}
	bounds := make([]int, 0, segments+1)
	for i := 0; i <= segments; i++ {
		x := x1 + ((x2-x1)*i)/segments
		bounds = append(bounds, x)
	}
	return bounds
}

func parseTitleAttribute(title string, word *models.HOCRWord) error {
	bboxRegex := regexp.MustCompile(`bbox\s+(\d+)\s+(\d+)\s+(\d+)\s+(\d+)`)
	if matches := bboxRegex.FindStringSubmatch(title); len(matches) == 5 {
		var err error
		if word.BBox.X1, err = strconv.Atoi(matches[1]); err != nil {
			return fmt.Errorf("invalid bbox x1: %w", err)
		}
		if word.BBox.Y1, err = strconv.Atoi(matches[2]); err != nil {
			return fmt.Errorf("invalid bbox y1: %w", err)
		}
		if word.BBox.X2, err = strconv.Atoi(matches[3]); err != nil {
			return fmt.Errorf("invalid bbox x2: %w", err)
		}
		if word.BBox.Y2, err = strconv.Atoi(matches[4]); err != nil {
			return fmt.Errorf("invalid bbox y2: %w", err)
		}
	}

	confRegex := regexp.MustCompile(`x_wconf\s+(\d+(?:\.\d+)?)`)
	if matches := confRegex.FindStringSubmatch(title); len(matches) == 2 {
		var err error
		if word.Confidence, err = strconv.ParseFloat(matches[1], 64); err != nil {
			return fmt.Errorf("invalid confidence: %w", err)
		}
	}

	return nil
}

func isValidWordText(text string) bool {
	trimmed := strings.TrimSpace(text)
	return trimmed != ""
}
