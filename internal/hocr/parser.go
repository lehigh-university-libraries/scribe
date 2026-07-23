package hocr

import (
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/models"
)

const (
	maxHOCRBytes                = 10 << 20
	maxHOCRDepth                = 128
	maxHOCRElements             = iiif.MaxAnnotationsPerPage * 8
	maxHOCRAttributesPerElement = 64
	maxHOCRAttributeBytes       = 64 << 10
	maxHOCRWordTextBytes        = 4 << 10
)

var (
	bboxPattern       = regexp.MustCompile(`bbox\s+(\d+)\s+(\d+)\s+(\d+)\s+(\d+)`)
	confidencePattern = regexp.MustCompile(`x_wconf\s+(\d+(?:\.\d+)?)`)
	cutsPattern       = regexp.MustCompile(`cuts\s+([0-9,\s]+)`)
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

// Document is the bounded, reusable hOCR projection needed by ingest and
// processing use cases. Callers that need lines, words, text, and page bounds
// should parse once and carry this value through the transaction.
type Document struct {
	Lines      []models.HOCRLine
	Words      []models.HOCRWord
	PageWidth  int
	PageHeight int
}

func ParseDocument(hocrXML string) (Document, error) {
	root, err := decodeBoundedHOCR(hocrXML)
	if err != nil {
		return Document{}, err
	}
	var lines []models.HOCRLine
	traverseLinesElements(root, &lines)
	if err := validateParsedLines(lines); err != nil {
		return Document{}, err
	}
	var words []models.HOCRWord
	traverseElementsWithLineContext(root, &words, "")
	if err := validateParsedWords(words); err != nil {
		return Document{}, err
	}
	width, height := findPageDimensions(root)
	return Document{Lines: lines, Words: words, PageWidth: width, PageHeight: height}, nil
}

func PlainText(lines []models.HOCRLine) string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		words := make([]string, 0, len(line.Words))
		for _, word := range line.Words {
			if text := strings.TrimSpace(word.Text); text != "" {
				words = append(words, text)
			}
		}
		if len(words) > 0 {
			out = append(out, strings.Join(words, " "))
		}
	}
	return strings.Join(out, "\n")
}

func ParseHOCRLines(hocrXML string) ([]models.HOCRLine, error) {
	doc, err := decodeBoundedHOCR(hocrXML)
	if err != nil {
		return nil, err
	}

	var lines []models.HOCRLine

	traverseLinesElements(doc, &lines)
	if err := validateParsedLines(lines); err != nil {
		return nil, err
	}

	return lines, nil
}

func validateParsedLines(lines []models.HOCRLine) error {
	totalWords := 0
	for _, line := range lines {
		if len(line.Words) > iiif.MaxAnnotationsPerPage-totalWords {
			return fmt.Errorf("hOCR contains more than %d words", iiif.MaxAnnotationsPerPage)
		}
		totalWords += len(line.Words)
	}
	if len(lines) > iiif.MaxAnnotationsPerPage {
		return fmt.Errorf("hOCR contains more than %d lines", iiif.MaxAnnotationsPerPage)
	}
	return nil
}

func ParseHOCRWords(hocrXML string) ([]models.HOCRWord, error) {
	doc, err := decodeBoundedHOCR(hocrXML)
	if err != nil {
		return nil, err
	}

	var words []models.HOCRWord

	traverseElementsWithLineContext(doc, &words, "")
	if err := validateParsedWords(words); err != nil {
		return nil, err
	}

	return words, nil
}

func validateParsedWords(words []models.HOCRWord) error {
	if len(words) > iiif.MaxAnnotationsPerPage {
		return fmt.Errorf("hOCR contains more than %d words", iiif.MaxAnnotationsPerPage)
	}
	return nil
}

func findPageDimensions(element XMLElement) (int, int) {
	if hasClass(element, "ocr_page") {
		for _, attribute := range element.Attrs {
			if attribute.Name.Local != "title" {
				continue
			}
			matches := bboxPattern.FindStringSubmatch(attribute.Value)
			if len(matches) != 5 {
				break
			}
			x2, xErr := strconv.Atoi(matches[3])
			y2, yErr := strconv.Atoi(matches[4])
			if xErr == nil && yErr == nil && x2 > 0 && y2 > 0 {
				return x2, y2
			}
			break
		}
	}
	for _, child := range element.Children {
		if width, height := findPageDimensions(child); width > 0 && height > 0 {
			return width, height
		}
	}
	return 0, 0
}

func ParseHOCRWordGlyphs(hocrXML string) ([]WordWithGlyphs, error) {
	doc, err := decodeBoundedHOCR(hocrXML)
	if err != nil {
		return nil, err
	}

	var words []WordWithGlyphs
	totalGlyphs := 0
	if err := traverseWordGlyphElements(doc, &words, "", &totalGlyphs); err != nil {
		return nil, err
	}
	return words, nil
}

type hocrElementFrame struct {
	element     *XMLElement
	captureText bool
	isLine      bool
	content     strings.Builder
}

// decodeBoundedHOCR enforces the input envelope at the parser boundary. This
// covers imported and provider-produced hOCR as well as validated RPC input,
// and avoids handing an unrestricted recursive tree to encoding/xml.
func decodeBoundedHOCR(hocrXML string) (XMLElement, error) {
	if len(hocrXML) > maxHOCRBytes {
		return XMLElement{}, fmt.Errorf("hOCR exceeds %d bytes", maxHOCRBytes)
	}
	decoder := xml.NewDecoder(strings.NewReader(hocrXML))
	decoder.Strict = true
	var root XMLElement
	frames := make([]hocrElementFrame, 0, 16)
	elementCount := 0
	haveRoot := false
	rootComplete := false

	for {
		token, err := decoder.Token()
		if err == io.EOF {
			if len(frames) != 0 {
				return XMLElement{}, fmt.Errorf("failed to parse XML: unexpected end of input")
			}
			return root, nil
		}
		if err != nil {
			return XMLElement{}, fmt.Errorf("failed to parse XML: %w", err)
		}
		switch value := token.(type) {
		case xml.StartElement:
			if rootComplete || len(frames)+1 > maxHOCRDepth {
				return XMLElement{}, fmt.Errorf("hOCR XML exceeds structural limits")
			}
			elementCount++
			if elementCount > maxHOCRElements || len(value.Attr) > maxHOCRAttributesPerElement {
				return XMLElement{}, fmt.Errorf("hOCR XML exceeds structural limits")
			}
			attributeBytes := 0
			for _, attribute := range value.Attr {
				attributeBytes += len(attribute.Name.Space) + len(attribute.Name.Local) + len(attribute.Value)
			}
			if attributeBytes > maxHOCRAttributeBytes {
				return XMLElement{}, fmt.Errorf("hOCR XML exceeds attribute limits")
			}
			element := XMLElement{XMLName: value.Name, Attrs: append([]xml.Attr(nil), value.Attr...)}
			var elementPointer *XMLElement
			if len(frames) == 0 {
				if haveRoot {
					return XMLElement{}, fmt.Errorf("failed to parse XML: multiple root elements")
				}
				root = element
				elementPointer = &root
				haveRoot = true
			} else {
				parent := frames[len(frames)-1].element
				parent.Children = append(parent.Children, element)
				elementPointer = &parent.Children[len(parent.Children)-1]
			}
			captureText := hasClass(*elementPointer, "ocrx_word")
			lineElement := hasClass(*elementPointer, "ocr_line")
			if captureText {
				for index := range frames {
					if frames[index].captureText {
						return XMLElement{}, fmt.Errorf("hOCR contains nested word elements")
					}
				}
			}
			if lineElement {
				for index := range frames {
					if frames[index].isLine {
						return XMLElement{}, fmt.Errorf("hOCR contains nested line elements")
					}
				}
			}
			frames = append(frames, hocrElementFrame{element: elementPointer, captureText: captureText, isLine: lineElement})
		case xml.CharData:
			if len(frames) == 0 {
				if strings.TrimSpace(string(value)) != "" {
					return XMLElement{}, fmt.Errorf("failed to parse XML: text outside root element")
				}
				continue
			}
			for index := range frames {
				if !frames[index].captureText {
					continue
				}
				if frames[index].content.Len()+len(value) > maxHOCRWordTextBytes {
					return XMLElement{}, fmt.Errorf("hOCR word text exceeds %d bytes", maxHOCRWordTextBytes)
				}
				_, _ = frames[index].content.Write(value)
			}
		case xml.EndElement:
			if len(frames) == 0 {
				return XMLElement{}, fmt.Errorf("failed to parse XML: unexpected closing element")
			}
			frame := &frames[len(frames)-1]
			if frame.captureText {
				frame.element.Content = frame.content.String()
			}
			frames = frames[:len(frames)-1]
			if len(frames) == 0 {
				rootComplete = true
			}
		}
	}
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

func traverseWordGlyphElements(element XMLElement, words *[]WordWithGlyphs, currentLineID string, totalGlyphs *int) error {
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
			glyphs := glyphsFromWord(word, title)
			usedAnnotations := len(*words) + *totalGlyphs
			if usedAnnotations >= iiif.MaxAnnotationsPerPage || len(glyphs) > iiif.MaxAnnotationsPerPage-usedAnnotations-1 {
				return fmt.Errorf("hOCR text granularity exceeds %d annotations", iiif.MaxAnnotationsPerPage)
			}
			*totalGlyphs += len(glyphs)
			*words = append(*words, WordWithGlyphs{
				Word:   word,
				Glyphs: glyphs,
			})
		}
	}

	for _, child := range element.Children {
		if err := traverseWordGlyphElements(child, words, currentLineID, totalGlyphs); err != nil {
			return err
		}
	}
	return nil
}

func isLineElement(element XMLElement) bool {
	return hasClass(element, "ocr_line")
}

func isWordElement(element XMLElement) bool {
	return hasClass(element, "ocrx_word")
}

func hasClass(element XMLElement, className string) bool {
	for _, attr := range element.Attrs {
		if attr.Name.Local != "class" {
			continue
		}
		for _, candidate := range strings.Fields(attr.Value) {
			if candidate == className {
				return true
			}
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
	if matches := bboxPattern.FindStringSubmatch(title); len(matches) == 5 {
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
	matches := cutsPattern.FindStringSubmatch(title)
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
	if matches := bboxPattern.FindStringSubmatch(title); len(matches) == 5 {
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

	if matches := confidencePattern.FindStringSubmatch(title); len(matches) == 2 {
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
