package server

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/lehigh-university-libraries/scribe/internal/iiif"
)

// annotationDraft is a complete, already-authorized canonical page supplied by
// an editor. Structural RPCs operate on this whole resource so every client
// gets exactly the same replacement, retention, and word-geometry semantics.
// Persistence remains the single SaveAnnotationPage compare-and-swap.
type annotationDraft struct {
	identity  iiif.PageIdentity
	pageID    string
	document  map[string]any
	items     []any
	rawBytes  int
	indexByID map[string]int
}

type locatedAnnotation struct {
	annotation     map[string]any
	index          int
	x1, y1, x2, y2 int
}

func decodeAnnotationDraft(raw string, identity iiif.PageIdentity) (*annotationDraft, error) {
	if err := iiif.ValidateCanonicalAnnotationPage([]byte(raw), identity); err != nil {
		return nil, fmt.Errorf("invalid canonical annotation page: %w", err)
	}
	var document map[string]any
	if err := iiif.DecodeJSON([]byte(raw), &document); err != nil {
		return nil, fmt.Errorf("decode canonical annotation page: %w", err)
	}
	items, ok := document["items"].([]any)
	if !ok {
		return nil, fmt.Errorf("canonical annotation page items must be an array")
	}
	pageID, err := iiif.AnnotationPageID(identity.PublicBaseURL, identity.ItemImageID)
	if err != nil {
		return nil, err
	}
	indexByID := make(map[string]int, len(items))
	for index, value := range items {
		annotation, _ := value.(map[string]any)
		indexByID[strings.TrimSpace(annStringValue(annotation, "id"))] = index
	}
	return &annotationDraft{identity: identity, pageID: pageID, document: document, items: items, rawBytes: len(raw), indexByID: indexByID}, nil
}

func (draft *annotationDraft) encode() (string, error) {
	if draft == nil || draft.document == nil {
		return "", fmt.Errorf("annotation draft is not configured")
	}
	draft.document["items"] = draft.items
	raw, err := json.Marshal(draft.document)
	if err != nil {
		return "", fmt.Errorf("encode transformed annotation page: %w", err)
	}
	if err := iiif.ValidateCanonicalAnnotationPage(raw, draft.identity); err != nil {
		return "", fmt.Errorf("invalid transformed annotation page: %w", err)
	}
	return string(raw), nil
}

func (draft *annotationDraft) annotation(id, granularity string) (locatedAnnotation, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return locatedAnnotation{}, fmt.Errorf("annotation id is required")
	}
	index, exists := draft.indexByID[id]
	if !exists || index < 0 || index >= len(draft.items) {
		return locatedAnnotation{}, fmt.Errorf("annotation %q was not found", id)
	}
	annotation, ok := draft.items[index].(map[string]any)
	if !ok {
		return locatedAnnotation{}, fmt.Errorf("annotation %q was not found", id)
	}
	actual := strings.ToLower(strings.TrimSpace(annStringValue(annotation, "textGranularity")))
	if actual != granularity {
		return locatedAnnotation{}, fmt.Errorf("annotation %q must have %s granularity", id, granularity)
	}
	x1, y1, x2, y2, err := parseXYWH(extractFragment(annotation))
	if err != nil {
		return locatedAnnotation{}, fmt.Errorf("annotation %q has invalid geometry: %w", id, err)
	}
	return locatedAnnotation{annotation: annotation, index: index, x1: x1, y1: y1, x2: x2, y2: y2}, nil
}

func (draft *annotationDraft) annotations(ids []string, granularity string) ([]locatedAnnotation, error) {
	if len(ids) < 2 {
		return nil, fmt.Errorf("at least two annotation ids are required")
	}
	seen := make(map[string]struct{}, len(ids))
	located := make([]locatedAnnotation, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if _, duplicate := seen[id]; duplicate {
			return nil, fmt.Errorf("annotation ids must be unique")
		}
		seen[id] = struct{}{}
		annotation, err := draft.annotation(id, granularity)
		if err != nil {
			return nil, err
		}
		located = append(located, annotation)
	}
	if granularity == "word" {
		draft.sortWordsInReadingOrder(located)
	} else {
		sortLocatedAnnotations(located)
	}
	return located, nil
}

func sortLocatedAnnotations(values []locatedAnnotation) {
	sort.SliceStable(values, func(i, j int) bool {
		centerI := values[i].y1 + values[i].y2
		centerJ := values[j].y1 + values[j].y2
		if centerI != centerJ {
			return centerI < centerJ
		}
		if values[i].x1 != values[j].x1 {
			return values[i].x1 < values[j].x1
		}
		return values[i].index < values[j].index
	})
}

func (draft *annotationDraft) sortWordsInReadingOrder(values []locatedAnnotation) {
	type wordOrder struct {
		line int
		x    int
		y    int
	}
	orders := make(map[string]wordOrder, len(values))
	lines := make([]locatedAnnotation, 0)
	for index, value := range draft.items {
		annotation, ok := value.(map[string]any)
		if !ok || !strings.EqualFold(strings.TrimSpace(annStringValue(annotation, "textGranularity")), "line") {
			continue
		}
		x1, y1, x2, y2, err := parseXYWH(extractFragment(annotation))
		if err == nil {
			lines = append(lines, locatedAnnotation{annotation: annotation, index: index, x1: x1, y1: y1, x2: x2, y2: y2})
		}
	}
	sortLocatedAnnotations(lines)
	assigned := assignSpatialWordsToLines(draft.items, nil)
	for lineRank, line := range lines {
		for _, word := range assigned[annStringValue(line.annotation, "id")] {
			x1, y1, _, _, err := parseXYWH(extractFragment(word.annotation))
			if err == nil {
				orders[annStringValue(word.annotation, "id")] = wordOrder{line: lineRank, x: x1, y: y1}
			}
		}
	}
	sort.SliceStable(values, func(i, j int) bool {
		left, leftAssigned := orders[annStringValue(values[i].annotation, "id")]
		right, rightAssigned := orders[annStringValue(values[j].annotation, "id")]
		if leftAssigned && rightAssigned {
			if left.line != right.line {
				return left.line < right.line
			}
			if left.x != right.x {
				return left.x < right.x
			}
			return left.y < right.y
		}
		if leftAssigned != rightAssigned {
			return leftAssigned
		}
		centerI := values[i].y1 + values[i].y2
		centerJ := values[j].y1 + values[j].y2
		if centerI != centerJ {
			return centerI < centerJ
		}
		return values[i].x1 < values[j].x1
	})
}

func (draft *annotationDraft) wordsForLine(lineID string) []locatedAnnotation {
	positioned := assignSpatialWordsToLines(draft.items, nil)[strings.TrimSpace(lineID)]
	return locatedWords(positioned)
}

func locatedWords(positioned []positionedAnnotationWord) []locatedAnnotation {
	words := make([]locatedAnnotation, 0, len(positioned))
	for _, word := range positioned {
		x1, y1, x2, y2, err := parseXYWH(extractFragment(word.annotation))
		if err != nil {
			continue
		}
		words = append(words, locatedAnnotation{
			annotation: word.annotation,
			index:      word.index,
			x1:         x1,
			y1:         y1,
			x2:         x2,
			y2:         y2,
		})
	}
	sort.SliceStable(words, func(i, j int) bool {
		if words[i].x1 != words[j].x1 {
			return words[i].x1 < words[j].x1
		}
		return words[i].y1 < words[j].y1
	})
	return words
}

func normalizedAnnotationTexts(values []locatedAnnotation) string {
	texts := make([]string, 0, len(values))
	for _, value := range values {
		text := strings.TrimSpace(extractAnnotationText(value.annotation))
		if text != "" {
			texts = append(texts, text)
		}
	}
	return strings.Join(texts, " ")
}

func splitDraftLineIntoWords(draft *annotationDraft, annotationID string, requestedWords []string) error {
	line, err := draft.annotation(annotationID, "line")
	if err != nil {
		return err
	}
	words := make([]string, 0, len(requestedWords))
	for _, word := range requestedWords {
		word = strings.TrimSpace(word)
		if word == "" {
			return fmt.Errorf("words must not contain empty values")
		}
		words = append(words, word)
	}
	if len(words) == 0 {
		words = strings.Fields(extractAnnotationText(line.annotation))
	}
	if len(words) == 0 {
		return fmt.Errorf("line has no words")
	}
	if len(words) > iiif.MaxAnnotationsPerPage {
		return fmt.Errorf("word count exceeds %d", iiif.MaxAnnotationsPerPage)
	}
	for _, word := range words {
		if len(word) > maxStructuralWordBytes {
			return fmt.Errorf("word exceeds %d encoded bytes", maxStructuralWordBytes)
		}
	}

	width := maxInt(1, line.x2-line.x1)
	if width < len(words) {
		return fmt.Errorf("line geometry is too narrow for %d distinct word boxes", len(words))
	}
	ownedWords := draft.wordsForLine(annotationID)
	if len(draft.items)-len(ownedWords)+len(words) > iiif.MaxAnnotationsPerPage {
		return fmt.Errorf("transformed page would exceed %d annotations", iiif.MaxAnnotationsPerPage)
	}
	template, err := json.Marshal(line.annotation)
	if err != nil {
		return fmt.Errorf("encode line template: %w", err)
	}
	if err := preflightWordFanoutBytes(draft.rawBytes, len(template), words); err != nil {
		return err
	}

	ownedIndices := make(map[int]struct{}, len(ownedWords))
	for _, existing := range ownedWords {
		ownedIndices[existing.index] = struct{}{}
	}
	existingByID := make(map[string]int, len(draft.items))
	for index, value := range draft.items {
		annotation, ok := value.(map[string]any)
		if ok {
			existingByID[strings.TrimSpace(annStringValue(annotation, "id"))] = index
		}
	}
	wordIDs := make([]string, len(words))
	for index := range words {
		wordID, err := iiif.AnnotationID(draft.pageID, annotationID+"\x00word\x00"+fmt.Sprintf("%d", index+1))
		if err != nil {
			return err
		}
		if existingIndex, exists := existingByID[wordID]; exists {
			existing, _ := draft.items[existingIndex].(map[string]any)
			if _, owned := ownedIndices[existingIndex]; !owned || !strings.EqualFold(strings.TrimSpace(annStringValue(existing, "textGranularity")), "word") {
				return fmt.Errorf("generated word annotation id %q collides with an unrelated page item", wordID)
			}
		}
		wordIDs[index] = wordID
	}

	setAnnotationText(line.annotation, strings.Join(words, " "))
	wordWidth := maxInt(1, width/len(words))
	generated := make([]any, 0, len(words))
	for index, word := range words {
		wordX1 := line.x1 + index*wordWidth
		wordX2 := wordX1 + wordWidth
		if index == len(words)-1 {
			wordX2 = line.x2
		}
		wordAnnotation, err := deriveTextAnnotation(
			line.annotation,
			wordIDs[index],
			"word",
			draft.identity.CanvasURI,
			wordX1,
			line.y1,
			wordX2,
			line.y2,
			word,
		)
		if err != nil {
			return err
		}
		generated = append(generated, wordAnnotation)
	}

	remove := make(map[int]struct{})
	for _, existing := range ownedWords {
		remove[existing.index] = struct{}{}
	}
	draft.items = replaceDraftItems(draft.items, remove, line.index+1, generated)
	return nil
}

const maxStructuralWordBytes = 16 << 10

func preflightWordFanoutBytes(currentPageBytes, templateBytes int, words []string) error {
	// Each derived annotation retains the template's extension graph. JSON can
	// expand an arbitrary input byte to a six-byte escape, so include that
	// worst-case text cost plus small object/array framing before allocating any
	// clones. This intentionally errs on the safe side at the admission edge.
	remaining := iiif.MaxAnnotationPageBytes - currentPageBytes
	if remaining < 0 {
		return fmt.Errorf("annotation page exceeds %d bytes", iiif.MaxAnnotationPageBytes)
	}
	for _, word := range words {
		cost := templateBytes + 6*len(word) + 256
		if cost > remaining {
			return fmt.Errorf("transformed page would exceed %d bytes", iiif.MaxAnnotationPageBytes)
		}
		remaining -= cost
	}
	return nil
}

func splitDraftLineIntoTwo(draft *annotationDraft, annotationID string, requestedSplit int) error {
	line, err := draft.annotation(annotationID, "line")
	if err != nil {
		return err
	}
	lineTokens := strings.Fields(extractAnnotationText(line.annotation))
	if len(lineTokens) < 2 {
		return fmt.Errorf("line needs at least two words to split")
	}
	if line.y2-line.y1 < 2 {
		return fmt.Errorf("line geometry is too short to split vertically")
	}
	words := draft.wordsForLine(annotationID)
	if len(words) > 0 {
		if len(words) != len(lineTokens) || normalizedAnnotationTexts(words) != strings.Join(lineTokens, " ") {
			return fmt.Errorf("line and word annotations must be synchronized before splitting")
		}
	}
	splitAt := requestedSplit
	if splitAt <= 0 || splitAt >= len(lineTokens) {
		splitAt = len(lineTokens) / 2
	}

	heightA := (line.y2 - line.y1) / 2
	boundary := line.y1 + heightA
	idA, err := iiif.AnnotationID(draft.pageID, annotationID+"\x00line\x00a")
	if err != nil {
		return err
	}
	idB, err := iiif.AnnotationID(draft.pageID, annotationID+"\x00line\x00b")
	if err != nil {
		return err
	}
	lineA, err := deriveTextAnnotation(line.annotation, idA, "line", draft.identity.CanvasURI, line.x1, line.y1, line.x2, boundary, strings.Join(lineTokens[:splitAt], " "))
	if err != nil {
		return err
	}
	lineB, err := deriveTextAnnotation(line.annotation, idB, "line", draft.identity.CanvasURI, line.x1, boundary, line.x2, line.y2, strings.Join(lineTokens[splitAt:], " "))
	if err != nil {
		return err
	}

	for index, word := range words {
		y1, y2 := line.y1, boundary
		if index >= splitAt {
			y1, y2 = boundary, line.y2
		}
		target, err := targetWithFragment(word.annotation["target"], draft.identity.CanvasURI, word.x1, y1, word.x2, y2)
		if err != nil {
			return err
		}
		word.annotation["target"] = target
		clearMutableTargetResourceIDs(word.annotation)
	}
	draft.items = replaceDraftItems(draft.items, map[int]struct{}{line.index: {}}, line.index, []any{lineA, lineB})
	return nil
}

func joinDraftLines(draft *annotationDraft, annotationIDs []string) error {
	lines, err := draft.annotations(annotationIDs, "line")
	if err != nil {
		return err
	}
	wordsByLine := assignSpatialWordsToLines(draft.items, nil)
	hasWordView := false
	hasLineOnlyView := false
	for _, line := range lines {
		words := locatedWords(wordsByLine[annStringValue(line.annotation, "id")])
		if len(words) == 0 {
			hasLineOnlyView = true
			continue
		}
		hasWordView = true
		tokens := strings.Fields(extractAnnotationText(line.annotation))
		if len(words) != len(tokens) || normalizedAnnotationTexts(words) != strings.Join(tokens, " ") {
			return fmt.Errorf("line and word annotations must be synchronized before joining")
		}
	}
	if hasWordView && hasLineOnlyView {
		return fmt.Errorf("selected lines must either all have word annotations or none")
	}
	merged, err := mergeLocatedAnnotations(draft, lines, "line")
	if err != nil {
		return err
	}
	remove := make(map[int]struct{}, len(lines))
	insertAt := len(draft.items)
	for _, line := range lines {
		remove[line.index] = struct{}{}
		if line.index < insertAt {
			insertAt = line.index
		}
	}
	if draft.hasIDOutside(annStringValue(merged, "id"), remove) {
		return fmt.Errorf("joined annotation id already exists")
	}
	draft.items = replaceDraftItems(draft.items, remove, insertAt, []any{merged})
	return nil
}

func joinDraftWordsIntoLine(draft *annotationDraft, annotationIDs []string) error {
	words, err := draft.annotations(annotationIDs, "word")
	if err != nil {
		return err
	}
	selected := make(map[string]struct{}, len(words))
	for _, word := range words {
		selected[annStringValue(word.annotation, "id")] = struct{}{}
	}

	wordsByLine := assignSpatialWordsToLines(draft.items, nil)
	affectedIDs := make(map[string]struct{})
	assignedSelected := make(map[string]struct{}, len(words))
	for lineID, assigned := range wordsByLine {
		containsSelected := false
		for _, word := range assigned {
			wordID := annStringValue(word.annotation, "id")
			if _, ok := selected[wordID]; ok {
				containsSelected = true
				assignedSelected[wordID] = struct{}{}
			}
		}
		if !containsSelected {
			continue
		}
		for _, word := range assigned {
			if _, ok := selected[annStringValue(word.annotation, "id")]; !ok {
				return fmt.Errorf("all words in an affected line must be selected")
			}
		}
		affectedIDs[lineID] = struct{}{}
	}
	if len(affectedIDs) > 0 && len(assignedSelected) != len(selected) {
		return fmt.Errorf("selected words must belong to the same affected line set")
	}

	remove := make(map[int]struct{})
	insertAt := words[0].index
	var merged map[string]any
	if len(affectedIDs) == 1 {
		var lineID string
		for id := range affectedIDs {
			lineID = id
		}
		line, err := draft.annotation(lineID, "line")
		if err != nil {
			return err
		}
		remove[line.index] = struct{}{}
		insertAt = line.index
		minX, minY, maxX, maxY := annotationUnion(words)
		merged, err = deriveTextAnnotation(line.annotation, lineID, "line", draft.identity.CanvasURI, minX, minY, maxX, maxY, normalizedAnnotationTexts(words))
		if err != nil {
			return err
		}
	} else {
		for lineID := range affectedIDs {
			line, err := draft.annotation(lineID, "line")
			if err != nil {
				return err
			}
			remove[line.index] = struct{}{}
			if line.index < insertAt {
				insertAt = line.index
			}
		}
		merged, err = mergeLocatedAnnotations(draft, words, "line")
		if err != nil {
			return err
		}
	}
	if draft.hasIDOutside(annStringValue(merged, "id"), remove) {
		return fmt.Errorf("joined annotation id already exists")
	}
	draft.items = replaceDraftItems(draft.items, remove, insertAt, []any{merged})
	return nil
}

func mergeLocatedAnnotations(draft *annotationDraft, values []locatedAnnotation, granularity string) (map[string]any, error) {
	if len(values) < 2 {
		return nil, fmt.Errorf("at least two annotations are required")
	}
	template := values[0].annotation
	templateProjection, err := structuralPropertyProjection(template)
	if err != nil {
		return nil, err
	}
	texts := make([]string, 0, len(values))
	inputIDs := make([]string, 0, len(values))
	for index, value := range values {
		if index > 0 {
			projection, err := structuralPropertyProjection(value.annotation)
			if err != nil {
				return nil, err
			}
			if !reflect.DeepEqual(templateProjection, projection) {
				return nil, fmt.Errorf("annotation %d has conflicting IIIF properties", index+1)
			}
		}
		texts = append(texts, strings.TrimSpace(extractAnnotationText(value.annotation)))
		inputIDs = append(inputIDs, strings.TrimSpace(annStringValue(value.annotation, "id")))
	}
	minX, minY, maxX, maxY := annotationUnion(values)
	mergedID, err := iiif.AnnotationID(draft.pageID, strings.Join(inputIDs, "\x00"))
	if err != nil {
		return nil, err
	}
	return deriveTextAnnotation(template, mergedID, granularity, draft.identity.CanvasURI, minX, minY, maxX, maxY, strings.Join(texts, " "))
}

func annotationUnion(values []locatedAnnotation) (int, int, int, int) {
	minX, minY := values[0].x1, values[0].y1
	maxX, maxY := values[0].x2, values[0].y2
	for _, value := range values[1:] {
		if value.x1 < minX {
			minX = value.x1
		}
		if value.y1 < minY {
			minY = value.y1
		}
		if value.x2 > maxX {
			maxX = value.x2
		}
		if value.y2 > maxY {
			maxY = value.y2
		}
	}
	return minX, minY, maxX, maxY
}

func (draft *annotationDraft) hasIDOutside(id string, removed map[int]struct{}) bool {
	for index, value := range draft.items {
		if _, ok := removed[index]; ok {
			continue
		}
		annotation, ok := value.(map[string]any)
		if ok && strings.TrimSpace(annStringValue(annotation, "id")) == strings.TrimSpace(id) {
			return true
		}
	}
	return false
}

func replaceDraftItems(items []any, remove map[int]struct{}, insertAt int, replacements []any) []any {
	if insertAt < 0 {
		insertAt = 0
	}
	if insertAt > len(items) {
		insertAt = len(items)
	}
	result := make([]any, 0, len(items)-len(remove)+len(replacements))
	inserted := false
	for index, value := range items {
		if !inserted && index >= insertAt {
			result = append(result, replacements...)
			inserted = true
		}
		if _, skip := remove[index]; !skip {
			result = append(result, value)
		}
	}
	if !inserted {
		result = append(result, replacements...)
	}
	return result
}
