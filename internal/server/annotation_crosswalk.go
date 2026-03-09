package server

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"sort"
	"strings"

	"github.com/lehigh-university-libraries/hOCRedit/internal/hocr"
	"github.com/lehigh-university-libraries/hOCRedit/internal/models"
)

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

type annotationCrosswalkRequest struct {
	AnnotationPageJSON string `json:"annotation_page_json"`
	AnnotationJSON     string `json:"annotation_json"`
}

type annotationCrosswalkResponse struct {
	Format  string `json:"format"`
	Content string `json:"content"`
}

func (h *Handler) handleCrosswalkToPlainText(w http.ResponseWriter, r *http.Request) {
	var req annotationCrosswalkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid json")
		return
	}
	lines, _, _, err := annotationPayloadToHOCRLines(req.AnnotationPageJSON, req.AnnotationJSON)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, annotationCrosswalkResponse{
		Format:  "text/plain",
		Content: linesToPlainText(lines),
	})
}

func (h *Handler) handleCrosswalkToHOCR(w http.ResponseWriter, r *http.Request) {
	var req annotationCrosswalkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid json")
		return
	}
	lines, pageW, pageH, err := annotationPayloadToHOCRLines(req.AnnotationPageJSON, req.AnnotationJSON)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	converter := hocr.NewConverter()
	xml := converter.ConvertHOCRLinesToXML(lines, pageW, pageH)
	writeJSON(w, 200, annotationCrosswalkResponse{
		Format:  "text/vnd.hocr+html",
		Content: xml,
	})
}

func (h *Handler) handleCrosswalkToPageXML(w http.ResponseWriter, r *http.Request) {
	var req annotationCrosswalkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid json")
		return
	}
	lines, pageW, pageH, err := annotationPayloadToHOCRLines(req.AnnotationPageJSON, req.AnnotationJSON)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, annotationCrosswalkResponse{
		Format:  "application/vnd.prima.page+xml",
		Content: linesToPageXML(lines, pageW, pageH),
	})
}

func (h *Handler) handleCrosswalkToALTOXML(w http.ResponseWriter, r *http.Request) {
	var req annotationCrosswalkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid json")
		return
	}
	lines, pageW, pageH, err := annotationPayloadToHOCRLines(req.AnnotationPageJSON, req.AnnotationJSON)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, annotationCrosswalkResponse{
		Format:  "application/alto+xml",
		Content: linesToALTOXML(lines, pageW, pageH),
	})
}

func annotationPageToHOCRLines(pageJSON string) ([]models.HOCRLine, int, int, error) {
	raw := strings.TrimSpace(pageJSON)
	if raw == "" {
		return nil, 0, 0, fmt.Errorf("annotation_page_json is required")
	}
	var page map[string]any
	if err := json.Unmarshal([]byte(raw), &page); err != nil {
		return nil, 0, 0, fmt.Errorf("invalid annotation page json")
	}
	items, _ := page["items"].([]any)
	if len(items) == 0 {
		return nil, 0, 0, fmt.Errorf("annotation page has no items")
	}

	lineByID := map[string]*models.HOCRLine{}
	var looseLines []models.HOCRLine
	var allWords []models.HOCRWord
	pageW, pageH := 1, 1
	lineCounter := 0
	wordCounter := 0

	for _, it := range items {
		anno, ok := it.(map[string]any)
		if !ok {
			continue
		}
		anno = normalizeAnnotation(anno, "")
		fragment := extractFragment(anno)
		if fragment == "" {
			continue
		}
		x1, y1, x2, y2, err := parseXYWH(fragment)
		if err != nil {
			continue
		}
		if x2 > pageW {
			pageW = x2
		}
		if y2 > pageH {
			pageH = y2
		}
		text := strings.TrimSpace(extractAnnotationText(anno))
		if text == "" {
			continue
		}
		granularity := strings.ToLower(strings.TrimSpace(annStringValue(anno, "textGranularity")))
		if granularity == "" {
			granularity = "line"
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
		default: // line/block/page/paragraph => treat as line
			lineID := strings.TrimSpace(annStringValue(anno, "id"))
			if lineID == "" {
				lineCounter++
				lineID = fmt.Sprintf("line_%d", lineCounter)
			}
			words := splitLineTextToWords(text, x1, y1, x2, y2, lineID)
			line := models.HOCRLine{
				ID:    lineID,
				BBox:  models.BBox{X1: x1, Y1: y1, X2: x2, Y2: y2},
				Words: words,
			}
			if existing, ok := lineByID[lineID]; ok {
				existing.Words = append(existing.Words, line.Words...)
			} else {
				copyLine := line
				lineByID[lineID] = &copyLine
				looseLines = append(looseLines, copyLine)
			}
		}
	}

	if len(allWords) > 0 {
		grouped := wordsToLines(allWords)
		for _, ln := range grouped {
			looseLines = append(looseLines, ln)
		}
	}

	if len(looseLines) == 0 {
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

func annotationPayloadToHOCRLines(pageJSON, annotationJSON string) ([]models.HOCRLine, int, int, error) {
	if strings.TrimSpace(pageJSON) != "" {
		return annotationPageToHOCRLines(pageJSON)
	}
	if strings.TrimSpace(annotationJSON) == "" {
		return nil, 0, 0, fmt.Errorf("annotation_page_json or annotation_json is required")
	}
	var anno map[string]any
	if err := json.Unmarshal([]byte(annotationJSON), &anno); err != nil {
		return nil, 0, 0, fmt.Errorf("invalid annotation json")
	}
	page := map[string]any{
		"@context": annotationPageContexts(),
		"type":     "AnnotationPage",
		"items":    []any{anno},
	}
	b, _ := json.Marshal(page)
	return annotationPageToHOCRLines(string(b))
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

func linesToPageXML(lines []models.HOCRLine, pageW, pageH int) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<PcGts xmlns="http://schema.primaresearch.org/PAGE/gts/pagecontent/2019-07-15">` + "\n")
	b.WriteString(fmt.Sprintf(`<Page imageWidth="%d" imageHeight="%d">`+"\n", pageW, pageH))
	b.WriteString(`<TextRegion id="r1">` + "\n")
	for i, line := range lines {
		b.WriteString(fmt.Sprintf(`<TextLine id="l%d">`, i+1))
		b.WriteString(fmt.Sprintf(`<Coords points="%d,%d %d,%d %d,%d %d,%d"/>`,
			line.BBox.X1, line.BBox.Y1, line.BBox.X2, line.BBox.Y1, line.BBox.X2, line.BBox.Y2, line.BBox.X1, line.BBox.Y2))
		b.WriteString(`<TextEquiv><Unicode>` + html.EscapeString(strings.TrimSpace(joinLineWords(line))) + `</Unicode></TextEquiv>`)
		for j, word := range line.Words {
			b.WriteString(fmt.Sprintf(`<Word id="w%d_%d">`, i+1, j+1))
			b.WriteString(fmt.Sprintf(`<Coords points="%d,%d %d,%d %d,%d %d,%d"/>`,
				word.BBox.X1, word.BBox.Y1, word.BBox.X2, word.BBox.Y1, word.BBox.X2, word.BBox.Y2, word.BBox.X1, word.BBox.Y2))
			b.WriteString(`<TextEquiv><Unicode>` + html.EscapeString(strings.TrimSpace(word.Text)) + `</Unicode></TextEquiv>`)
			b.WriteString(`</Word>`)
		}
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
	b.WriteString(`<alto xmlns="http://www.loc.gov/standards/alto/ns-v4#">` + "\n")
	b.WriteString(`<Layout>` + "\n")
	b.WriteString(fmt.Sprintf(`<Page WIDTH="%d" HEIGHT="%d">`+"\n", pageW, pageH))
	b.WriteString(fmt.Sprintf(`<PrintSpace HPOS="0" VPOS="0" WIDTH="%d" HEIGHT="%d">`+"\n", pageW, pageH))
	b.WriteString(`<TextBlock ID="TB1">` + "\n")
	for i, line := range lines {
		w := maxInt(1, line.BBox.X2-line.BBox.X1)
		h := maxInt(1, line.BBox.Y2-line.BBox.Y1)
		b.WriteString(fmt.Sprintf(`<TextLine ID="TL%d" HPOS="%d" VPOS="%d" WIDTH="%d" HEIGHT="%d">`, i+1, line.BBox.X1, line.BBox.Y1, w, h))
		for j, word := range line.Words {
			ww := maxInt(1, word.BBox.X2-word.BBox.X1)
			wh := maxInt(1, word.BBox.Y2-word.BBox.Y1)
			b.WriteString(fmt.Sprintf(`<String ID="S%d_%d" CONTENT="%s" HPOS="%d" VPOS="%d" WIDTH="%d" HEIGHT="%d"/>`,
				i+1, j+1, html.EscapeString(strings.TrimSpace(word.Text)), word.BBox.X1, word.BBox.Y1, ww, wh))
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
