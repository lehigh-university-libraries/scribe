package server

import (
	"fmt"
	"html"
	"log/slog"
	"sort"
	"strings"

	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/models"
)

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func annotationPageToHOCRLines(pageJSON string) ([]models.HOCRLine, int, int, error) {
	return annotationPageToHOCRLinesWithDimensions(pageJSON, 0, 0)
}

func annotationPageToHOCRLinesWithDimensions(pageJSON string, canvasWidth, canvasHeight int) ([]models.HOCRLine, int, int, error) {
	raw := strings.TrimSpace(pageJSON)
	if raw == "" {
		return nil, 0, 0, fmt.Errorf("annotation_page_json is required")
	}
	var page map[string]any
	if err := iiif.DecodeJSON([]byte(raw), &page); err != nil {
		return nil, 0, 0, fmt.Errorf("invalid annotation page json")
	}
	itemsValue, present := page["items"]
	if !present {
		return nil, 0, 0, fmt.Errorf("annotation page items are required")
	}
	items, ok := itemsValue.([]any)
	if !ok {
		return nil, 0, 0, fmt.Errorf("annotation page items must be an array")
	}

	lineByID := map[string]*models.HOCRLine{}
	lineOrder := make([]string, 0)
	type rankedLine struct {
		rank int
		line models.HOCRLine
	}
	lineCandidates := make([]rankedLine, 0)
	bestLineRank := 0
	var allWords []models.HOCRWord
	pageW, pageH := maxInt(1, canvasWidth), maxInt(1, canvasHeight)
	lineCounter := 0
	wordCounter := 0

	for _, it := range items {
		anno, ok := it.(map[string]any)
		if !ok {
			continue
		}
		granularity := strings.ToLower(strings.TrimSpace(annStringValue(anno, "textGranularity")))
		// Canonical pages may also contain ordinary Web Annotations created by
		// Mirador or another editor. Preserve those annotations in the page, but
		// never reinterpret a comment/tag as OCR merely because it has geometry.
		if !iiif.IsTextGranularity(granularity) {
			continue
		}
		anno = normalizeAnnotation(anno, "")
		fragment := extractFragment(anno)
		x1, y1, x2, y2 := 0, 0, pageW, pageH
		if fragment == "" && granularity != "page" {
			slog.Info("Crosswalk skipping annotation without fragment",
				"annotation", annotationDebugSummary(anno),
			)
			continue
		}
		if fragment != "" {
			var err error
			x1, y1, x2, y2, err = parseXYWH(fragment)
			if err != nil {
				slog.Info("Crosswalk skipping annotation with invalid fragment",
					"annotation", annotationDebugSummary(anno),
				)
				continue
			}
		}
		if x2 > pageW {
			pageW = x2
		}
		if y2 > pageH {
			pageH = y2
		}
		text := strings.TrimSpace(extractAnnotationText(anno))
		if text == "" {
			slog.Info("Crosswalk skipping annotation without text",
				"annotation", annotationDebugSummary(anno),
			)
			continue
		}
		switch granularity {
		case "word", "glyph":
			wordCounter++
			allWords = append(allWords, models.HOCRWord{
				ID:     fmt.Sprintf("word_%d", wordCounter),
				LineID: "",
				Text:   text,
				BBox:   models.BBox{X1: x1, Y1: y1, X2: x2, Y2: y2},
			})
		default: // line/block/page/paragraph => ranked line fallback
			lineID := strings.TrimSpace(annStringValue(anno, "id"))
			if lineID == "" {
				lineCounter++
				lineID = fmt.Sprintf("line_%d", lineCounter)
			}
			line := models.HOCRLine{
				ID:   lineID,
				BBox: models.BBox{X1: x1, Y1: y1, X2: x2, Y2: y2},
			}
			rank := 4
			switch granularity {
			case "page":
				rank = 1
			case "block":
				rank = 2
			case "paragraph":
				rank = 3
			}
			if rank > bestLineRank {
				bestLineRank = rank
			}
			lineCandidates = append(lineCandidates, rankedLine{rank: rank, line: line})
		}
	}
	for _, candidate := range lineCandidates {
		if candidate.rank != bestLineRank {
			continue
		}
		line := candidate.line
		if existing, ok := lineByID[line.ID]; ok {
			existing.BBox = line.BBox
		} else {
			lineByID[line.ID] = &line
			lineOrder = append(lineOrder, line.ID)
		}
	}

	if len(allWords) > 0 {
		if len(lineByID) > 0 {
			assignWordsToLines(lineByID, allWords)
		} else {
			grouped := wordsToLines(allWords)
			for _, ln := range grouped {
				copyLine := ln
				lineByID[copyLine.ID] = &copyLine
				lineOrder = append(lineOrder, copyLine.ID)
			}
		}
	}

	looseLines := make([]models.HOCRLine, 0, len(lineOrder))
	for _, lineID := range lineOrder {
		line := lineByID[lineID]
		if line == nil {
			continue
		}
		if len(line.Words) == 0 {
			line.Words = splitLineTextToWords(extractLineTextFromID(page, lineID), line.BBox.X1, line.BBox.Y1, line.BBox.X2, line.BBox.Y2, lineID)
		}
		looseLines = append(looseLines, *line)
	}
	if len(items) > 0 && len(looseLines) == 0 {
		return nil, 0, 0, fmt.Errorf("annotation page has no parseable textual annotations")
	}

	sort.Slice(looseLines, func(i, j int) bool {
		ai := looseLines[i].BBox.Y1 + looseLines[i].BBox.Y2
		aj := looseLines[j].BBox.Y1 + looseLines[j].BBox.Y2
		if ai != aj {
			return ai < aj
		}
		return looseLines[i].BBox.X1 < looseLines[j].BBox.X1
	})
	for i := range looseLines {
		if strings.TrimSpace(looseLines[i].ID) == "" {
			looseLines[i].ID = fmt.Sprintf("line_%d", i+1)
		}
		for wi := range looseLines[i].Words {
			if strings.TrimSpace(looseLines[i].Words[wi].ID) == "" {
				looseLines[i].Words[wi].ID = fmt.Sprintf("%s_word_%d", looseLines[i].ID, wi+1)
			}
			looseLines[i].Words[wi].LineID = looseLines[i].ID
		}
	}
	return looseLines, pageW, pageH, nil
}

func assignWordsToLines(lineByID map[string]*models.HOCRLine, words []models.HOCRWord) {
	lines := make([]*models.HOCRLine, 0, len(lineByID))
	for _, line := range lineByID {
		if line != nil {
			line.Words = nil
			lines = append(lines, line)
		}
	}
	sort.Slice(lines, func(i, j int) bool {
		ai := lines[i].BBox.Y1 + lines[i].BBox.Y2
		aj := lines[j].BBox.Y1 + lines[j].BBox.Y2
		if ai != aj {
			return ai < aj
		}
		return lines[i].BBox.X1 < lines[j].BBox.X1
	})

	for _, word := range words {
		best := nearestLineForWord(lines, word)
		if best == nil {
			continue
		}
		best.Words = append(best.Words, models.HOCRWord{
			ID:     word.ID,
			LineID: best.ID,
			Text:   word.Text,
			BBox:   word.BBox,
		})
	}

	for _, line := range lines {
		sort.Slice(line.Words, func(i, j int) bool {
			if line.Words[i].BBox.X1 != line.Words[j].BBox.X1 {
				return line.Words[i].BBox.X1 < line.Words[j].BBox.X1
			}
			return line.Words[i].BBox.Y1 < line.Words[j].BBox.Y1
		})
	}
}

func nearestLineForWord(lines []*models.HOCRLine, word models.HOCRWord) *models.HOCRLine {
	var best *models.HOCRLine
	bestScore := -1.0
	bestDistance := int(^uint(0) >> 1)
	wordCenterY := (word.BBox.Y1 + word.BBox.Y2) / 2

	for _, line := range lines {
		if line == nil {
			continue
		}
		score := wordLineVerticalOverlap(line.BBox, word.BBox)
		lineCenterY := (line.BBox.Y1 + line.BBox.Y2) / 2
		distance := abs(wordCenterY - lineCenterY)
		if score > bestScore || (score == bestScore && distance < bestDistance) {
			best = line
			bestScore = score
			bestDistance = distance
		}
	}
	return best
}

func wordLineVerticalOverlap(lineBox, wordBox models.BBox) float64 {
	overlapTop := maxInt(lineBox.Y1, wordBox.Y1)
	overlapBottom := minInt(lineBox.Y2, wordBox.Y2)
	overlap := maxInt(0, overlapBottom-overlapTop)
	wordHeight := maxInt(1, wordBox.Y2-wordBox.Y1)
	return float64(overlap) / float64(wordHeight)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func extractLineTextFromID(page map[string]any, lineID string) string {
	items, _ := page["items"].([]any)
	for _, it := range items {
		anno, ok := it.(map[string]any)
		if !ok {
			continue
		}
		id := strings.TrimSpace(annStringValue(anno, "id"))
		if id == lineID {
			return strings.TrimSpace(extractAnnotationText(anno))
		}
	}
	return ""
}

func splitLineTextToWords(text string, x1, y1, x2, y2 int, lineID string) []models.HOCRWord {
	tokens := strings.Fields(strings.TrimSpace(text))
	if len(tokens) == 0 {
		return nil
	}
	width := maxInt(1, x2-x1)
	step := maxInt(1, width/len(tokens))
	words := make([]models.HOCRWord, 0, len(tokens))
	for i, t := range tokens {
		wx1 := x1 + i*step
		wx2 := wx1 + step
		if i == len(tokens)-1 {
			wx2 = x2
		}
		words = append(words, models.HOCRWord{
			ID:     fmt.Sprintf("%s_word_%d", lineID, i+1),
			LineID: lineID,
			Text:   t,
			BBox:   models.BBox{X1: wx1, Y1: y1, X2: wx2, Y2: y2},
		})
	}
	return words
}

func wordsToLines(words []models.HOCRWord) []models.HOCRLine {
	if len(words) == 0 {
		return nil
	}
	sort.Slice(words, func(i, j int) bool {
		yi := (words[i].BBox.Y1 + words[i].BBox.Y2) / 2
		yj := (words[j].BBox.Y1 + words[j].BBox.Y2) / 2
		if yi != yj {
			return yi < yj
		}
		return words[i].BBox.X1 < words[j].BBox.X1
	})
	const threshold = 20
	var groups [][]models.HOCRWord
	current := []models.HOCRWord{words[0]}
	currentY := (words[0].BBox.Y1 + words[0].BBox.Y2) / 2
	for i := 1; i < len(words); i++ {
		w := words[i]
		y := (w.BBox.Y1 + w.BBox.Y2) / 2
		if abs(currentY-y) <= threshold {
			current = append(current, w)
			continue
		}
		groups = append(groups, current)
		current = []models.HOCRWord{w}
		currentY = y
	}
	if len(current) > 0 {
		groups = append(groups, current)
	}
	lines := make([]models.HOCRLine, 0, len(groups))
	for i, group := range groups {
		sort.Slice(group, func(a, b int) bool { return group[a].BBox.X1 < group[b].BBox.X1 })
		lineID := fmt.Sprintf("line_%d", i+1)
		minX := group[0].BBox.X1
		minY := group[0].BBox.Y1
		maxX := group[0].BBox.X2
		maxY := group[0].BBox.Y2
		for wi := range group {
			group[wi].LineID = lineID
			if group[wi].BBox.X1 < minX {
				minX = group[wi].BBox.X1
			}
			if group[wi].BBox.Y1 < minY {
				minY = group[wi].BBox.Y1
			}
			if group[wi].BBox.X2 > maxX {
				maxX = group[wi].BBox.X2
			}
			if group[wi].BBox.Y2 > maxY {
				maxY = group[wi].BBox.Y2
			}
		}
		lines = append(lines, models.HOCRLine{
			ID:    lineID,
			BBox:  models.BBox{X1: minX, Y1: minY, X2: maxX, Y2: maxY},
			Words: group,
		})
	}
	return lines
}

func linesToPlainText(lines []models.HOCRLine) string {
	if len(lines) == 0 {
		return ""
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if len(line.Words) == 0 {
			continue
		}
		parts := make([]string, 0, len(line.Words))
		for _, w := range line.Words {
			t := strings.TrimSpace(w.Text)
			if t != "" {
				parts = append(parts, t)
			}
		}
		if len(parts) > 0 {
			out = append(out, strings.Join(parts, " "))
		}
	}
	return strings.Join(out, "\n")
}

// PAGE requires creation metadata and an image filename, but those values are
// not part of the canonical OCR geometry passed to this derived renderer. Keep
// stable derivation markers here so repeated exports of one revision are byte
// identical; source-image provenance remains in the canonical IIIF page.
const (
	pageXMLCreator       = "Scribe"
	pageXMLDerivedAt     = "1970-01-01T00:00:00Z"
	pageXMLImageFilename = "source-image.png"
)

func escapeXMLContent(value string) string {
	value = strings.Map(func(r rune) rune {
		switch {
		case r == '\t', r == '\n', r == '\r':
			return r
		case r >= 0x20 && r <= 0xD7FF:
			return r
		case r >= 0xE000 && r <= 0xFFFD:
			return r
		case r >= 0x10000 && r <= 0x10FFFF:
			return r
		default:
			return '\uFFFD'
		}
	}, strings.TrimSpace(value))
	return html.EscapeString(value)
}

func linesToPageXML(lines []models.HOCRLine, pageW, pageH int) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<PcGts xmlns="http://schema.primaresearch.org/PAGE/gts/pagecontent/2019-07-15">` + "\n")
	b.WriteString(`<Metadata>` + "\n")
	b.WriteString(`<Creator>` + pageXMLCreator + `</Creator>` + "\n")
	b.WriteString(`<Created>` + pageXMLDerivedAt + `</Created>` + "\n")
	b.WriteString(`<LastChange>` + pageXMLDerivedAt + `</LastChange>` + "\n")
	b.WriteString(`</Metadata>` + "\n")
	fmt.Fprintf(&b, `<Page imageFilename="%s" imageWidth="%d" imageHeight="%d">`+"\n", pageXMLImageFilename, pageW, pageH)
	b.WriteString(`<TextRegion id="r1">` + "\n")
	fmt.Fprintf(&b, `<Coords points="0,0 %d,0 %d,%d 0,%d"/>`+"\n", pageW, pageW, pageH, pageH)
	for i, line := range lines {
		fmt.Fprintf(&b, `<TextLine id="l%d">`, i+1)
		fmt.Fprintf(&b, `<Coords points="%d,%d %d,%d %d,%d %d,%d"/>`,
			line.BBox.X1, line.BBox.Y1, line.BBox.X2, line.BBox.Y1, line.BBox.X2, line.BBox.Y2, line.BBox.X1, line.BBox.Y2)
		for j, word := range line.Words {
			fmt.Fprintf(&b, `<Word id="w%d_%d">`, i+1, j+1)
			fmt.Fprintf(&b, `<Coords points="%d,%d %d,%d %d,%d %d,%d"/>`,
				word.BBox.X1, word.BBox.Y1, word.BBox.X2, word.BBox.Y1, word.BBox.X2, word.BBox.Y2, word.BBox.X1, word.BBox.Y2)
			b.WriteString(`<TextEquiv><Unicode>` + escapeXMLContent(word.Text) + `</Unicode></TextEquiv>`)
			b.WriteString(`</Word>`)
		}
		b.WriteString(`<TextEquiv><Unicode>` + escapeXMLContent(joinLineWords(line)) + `</Unicode></TextEquiv>`)
		b.WriteString(`</TextLine>` + "\n")
	}
	b.WriteString(`</TextRegion>` + "\n")
	b.WriteString(`</Page>` + "\n")
	b.WriteString(`</PcGts>` + "\n")
	return b.String()
}

func linesToALTOXML(lines []models.HOCRLine, pageW, pageH int) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<alto xmlns="http://www.loc.gov/standards/alto/ns-v4#" SCHEMAVERSION="4.4">` + "\n")
	b.WriteString(`<Layout>` + "\n")
	fmt.Fprintf(&b, `<Page ID="P1" PHYSICAL_IMG_NR="1" WIDTH="%d" HEIGHT="%d">`+"\n", pageW, pageH)
	fmt.Fprintf(&b, `<PrintSpace HPOS="0" VPOS="0" WIDTH="%d" HEIGHT="%d">`+"\n", pageW, pageH)
	b.WriteString(`<TextBlock ID="TB1">` + "\n")
	for i, line := range lines {
		w := maxInt(1, line.BBox.X2-line.BBox.X1)
		h := maxInt(1, line.BBox.Y2-line.BBox.Y1)
		fmt.Fprintf(&b, `<TextLine ID="TL%d" HPOS="%d" VPOS="%d" WIDTH="%d" HEIGHT="%d">`, i+1, line.BBox.X1, line.BBox.Y1, w, h)
		for j, word := range line.Words {
			ww := maxInt(1, word.BBox.X2-word.BBox.X1)
			wh := maxInt(1, word.BBox.Y2-word.BBox.Y1)
			fmt.Fprintf(&b, `<String ID="S%d_%d" CONTENT="%s" HPOS="%d" VPOS="%d" WIDTH="%d" HEIGHT="%d"/>`,
				i+1, j+1, escapeXMLContent(word.Text), word.BBox.X1, word.BBox.Y1, ww, wh)
		}
		b.WriteString(`</TextLine>` + "\n")
	}
	b.WriteString(`</TextBlock>` + "\n")
	b.WriteString(`</PrintSpace>` + "\n")
	b.WriteString(`</Page>` + "\n")
	b.WriteString(`</Layout>` + "\n")
	b.WriteString(`</alto>` + "\n")
	return b.String()
}
