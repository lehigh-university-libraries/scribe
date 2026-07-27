package server

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/lehigh-university-libraries/scribe/internal/iiif"
)

// maxWordReconciliationLCSCells bounds the quadratic portion of token diffing.
// Common prefixes and suffixes are removed before this budget is evaluated, so
// ordinary edits in long lines remain cheap while adversarial all-different
// inputs fail instead of consuming unbounded CPU and memory.
const maxWordReconciliationLCSCells = 1_000_000

type tokenMatch struct {
	baseIndex  int
	tokenIndex int
}

// reconcileEditedLineWords keeps the line and word views of an interactive
// editor save coherent without assigning existing words from proposed
// geometry. Ownership is established once from the committed base revision and
// carried by word ID through the proposed draft. Spatial assignment is used
// only for genuinely new words and for validating a complete structural
// replacement of a removed line.
func reconcileEditedLineWords(
	baseItems, proposedItems []any,
	identity iiif.PageIdentity,
	expectedRevision uint64,
) ([]any, error) {
	pageID, err := iiif.AnnotationPageID(identity.PublicBaseURL, identity.ItemImageID)
	if err != nil {
		return nil, err
	}
	baseLines, err := reconciliationAnnotations(baseItems, "line")
	if err != nil {
		return nil, fmt.Errorf("index committed lines: %w", err)
	}
	baseWords, err := reconciliationAnnotations(baseItems, "word")
	if err != nil {
		return nil, fmt.Errorf("index committed words: %w", err)
	}
	proposedLines, err := reconciliationAnnotations(proposedItems, "line")
	if err != nil {
		return nil, fmt.Errorf("index proposed lines: %w", err)
	}
	proposedWords, err := reconciliationAnnotations(proposedItems, "word")
	if err != nil {
		return nil, fmt.Errorf("index proposed words: %w", err)
	}
	encodedProposedItems, err := json.Marshal(proposedItems)
	if err != nil {
		return nil, fmt.Errorf("estimate proposed annotation items: %w", err)
	}
	estimatedPageBytes := len(encodedProposedItems) + 256
	lcsCellsRemaining := maxWordReconciliationLCSCells

	baseWordsByLine := make(map[string][]locatedAnnotation)
	baseWordOwner := make(map[string]string, len(baseWords))
	for lineID, positioned := range assignSpatialWordsToLines(baseItems, nil) {
		words := locatedWords(positioned)
		baseWordsByLine[lineID] = words
		for _, word := range words {
			baseWordOwner[annStringValue(word.annotation, "id")] = lineID
		}
	}

	newLineIDs := make(map[string]struct{})
	removedLineIDs := make(map[string]struct{})
	existingLineIDs := make(map[string]struct{})
	for id := range proposedLines {
		if _, existed := baseLines[id]; existed {
			existingLineIDs[id] = struct{}{}
		} else {
			newLineIDs[id] = struct{}{}
		}
	}
	for id := range baseLines {
		if _, remains := proposedLines[id]; !remains {
			removedLineIDs[id] = struct{}{}
		}
	}

	newLineAssignments := assignSpatialWordsToLines(proposedItems, newLineIDs)
	newLineSynchronized := make(map[string]bool, len(newLineIDs))
	newLineForWord := make(map[string]string)
	newWordsClaimedByNewLine := make(map[string]struct{})
	for lineID := range newLineIDs {
		words := locatedWords(newLineAssignments[lineID])
		synchronized := lineAndWordsSynchronized(proposedLines[lineID], words)
		newLineSynchronized[lineID] = synchronized
		hasNewWord := false
		for _, word := range words {
			wordID := annStringValue(word.annotation, "id")
			newLineForWord[wordID] = lineID
			if _, existed := baseWords[wordID]; !existed {
				hasNewWord = true
				if synchronized {
					newWordsClaimedByNewLine[wordID] = struct{}{}
				}
			}
		}
		if hasNewWord && !synchronized {
			return nil, fmt.Errorf("new line %q and its word annotations must be synchronized", lineID)
		}
	}

	// A structural split/join removes committed line IDs while deliberately
	// retaining a complete, synchronized word set beneath new line IDs. That is
	// the only case in which a new line may adopt committed words. An ordinary
	// line deletion cascades its base-owned word IDs.
	basicReplacement := make(map[string]bool, len(removedLineIDs))
	for lineID := range removedLineIDs {
		words := baseWordsByLine[lineID]
		if len(words) == 0 {
			continue
		}
		complete := true
		for _, word := range words {
			wordID := annStringValue(word.annotation, "id")
			if _, present := proposedWords[wordID]; !present {
				complete = false
				break
			}
			newOwner, assigned := newLineForWord[wordID]
			if !assigned || !newLineSynchronized[newOwner] {
				complete = false
				break
			}
		}
		basicReplacement[lineID] = complete
	}
	allowedReplacementLine := make(map[string]bool, len(newLineIDs))
	for lineID := range newLineIDs {
		if !newLineSynchronized[lineID] {
			continue
		}
		allowed := true
		for _, word := range locatedWords(newLineAssignments[lineID]) {
			owner, hadOwner := baseWordOwner[annStringValue(word.annotation, "id")]
			if !hadOwner {
				continue
			}
			if _, removed := removedLineIDs[owner]; !removed || !basicReplacement[owner] {
				allowed = false
				break
			}
		}
		allowedReplacementLine[lineID] = allowed
	}
	protectedRemovedLine := make(map[string]bool, len(removedLineIDs))
	for lineID := range removedLineIDs {
		if !basicReplacement[lineID] {
			continue
		}
		protected := true
		for _, word := range baseWordsByLine[lineID] {
			if !allowedReplacementLine[newLineForWord[annStringValue(word.annotation, "id")]] {
				protected = false
				break
			}
		}
		protectedRemovedLine[lineID] = protected
	}

	removeWordIDs := make(map[string]struct{})
	for lineID := range removedLineIDs {
		if protectedRemovedLine[lineID] {
			continue
		}
		attemptedStructuralReplacement := false
		for _, word := range baseWordsByLine[lineID] {
			if _, assigned := newLineForWord[annStringValue(word.annotation, "id")]; assigned {
				attemptedStructuralReplacement = true
				break
			}
		}
		if attemptedStructuralReplacement {
			return nil, fmt.Errorf("removed line %q has an incomplete or conflicting structural word replacement", lineID)
		}
		for _, word := range baseWordsByLine[lineID] {
			removeWordIDs[annStringValue(word.annotation, "id")] = struct{}{}
		}
	}

	// Structural word-split RPCs derive stable word IDs from the owning line ID
	// and ordinal. Recover that explicit ownership before the spatial fallback:
	// overlapping line boxes are valid, and geometry alone would otherwise
	// attach every new word to the first line and rewrite its text on save.
	newWordsByExistingLine := make(map[string][]locatedAnnotation)
	deterministicNewWordIDs := make(map[string]struct{})
	for lineID := range existingLineIDs {
		line := proposedLines[lineID]
		for wordIndex := range strings.Fields(extractAnnotationText(line.annotation)) {
			wordID, idErr := iiif.AnnotationID(pageID, lineID+"\x00word\x00"+fmt.Sprintf("%d", wordIndex+1))
			if idErr != nil {
				return nil, fmt.Errorf("derive structural word ownership for line %q: %w", lineID, idErr)
			}
			word, proposed := proposedWords[wordID]
			if !proposed {
				continue
			}
			if _, existed := baseWords[wordID]; existed {
				continue
			}
			if _, claimed := newWordsClaimedByNewLine[wordID]; claimed {
				continue
			}
			deterministicNewWordIDs[wordID] = struct{}{}
			newWordsByExistingLine[lineID] = append(newWordsByExistingLine[lineID], word)
		}
	}
	for lineID, positioned := range assignSpatialWordsToLines(proposedItems, existingLineIDs) {
		for _, word := range locatedWords(positioned) {
			wordID := annStringValue(word.annotation, "id")
			if _, existed := baseWords[wordID]; existed {
				continue
			}
			if _, claimed := newWordsClaimedByNewLine[wordID]; claimed {
				continue
			}
			if _, claimed := deterministicNewWordIDs[wordID]; claimed {
				continue
			}
			newWordsByExistingLine[lineID] = append(newWordsByExistingLine[lineID], word)
		}
	}

	usedIDs := make(map[string]struct{}, len(proposedItems))
	for _, value := range proposedItems {
		annotation, _ := value.(map[string]any)
		if id := strings.TrimSpace(annStringValue(annotation, "id")); id != "" {
			usedIDs[id] = struct{}{}
		}
	}
	generatedByLine := make(map[string][]any)
	for lineID := range existingLineIDs {
		baseLine := baseLines[lineID]
		proposedLine := proposedLines[lineID]
		ownedBaseWords := baseWordsByLine[lineID]
		ownedProposedWords := proposedOwnedWords(ownedBaseWords, proposedWords)
		ownedProposedWords = append(ownedProposedWords, newWordsByExistingLine[lineID]...)
		sortReconciliationWords(ownedProposedWords)

		baseText := extractAnnotationText(baseLine.annotation)
		proposedText := extractAnnotationText(proposedLine.annotation)
		if !sameBox(baseLine, proposedLine) {
			// The client changed the embedded target/selector resource value.
			// Preserve extension properties but do not retain an RDF identity that
			// now denotes conflicting geometry.
			clearMutableTargetResourceIDs(proposedLine.annotation)
		}
		if baseText != proposedText && len(ownedBaseWords) > 0 {
			if !sameWordIDMembership(ownedBaseWords, ownedProposedWords) {
				if !lineAndWordsSynchronized(proposedLine, ownedProposedWords) {
					return nil, fmt.Errorf("line %q changes both line text and its word membership without a complete synchronized word view", lineID)
				}
				// Explicit full-page word CRUD already carries the intended IDs,
				// geometry, and text. Preserve it exactly; LCS generation is for
				// line-editor drafts whose membership did not change.
				continue
			}
			tokens := strings.Fields(proposedText)
			if len(proposedItems)-len(ownedBaseWords)+len(tokens) > iiif.MaxAnnotationsPerPage {
				return nil, fmt.Errorf("line %q token edit would exceed %d annotations", lineID, iiif.MaxAnnotationsPerPage)
			}
			nextEstimatedBytes, estimateErr := estimateReconciledTokenBytes(estimatedPageBytes, proposedLine, ownedProposedWords, tokens)
			if estimateErr != nil {
				return nil, fmt.Errorf("reconcile line %q: %w", lineID, estimateErr)
			}
			generated, removed, generateErr := reconcileLineTokens(
				pageID,
				identity.CanvasURI,
				expectedRevision,
				proposedLine,
				ownedBaseWords,
				proposedWords,
				tokens,
				usedIDs,
				&lcsCellsRemaining,
			)
			if generateErr != nil {
				return nil, fmt.Errorf("reconcile line %q: %w", lineID, generateErr)
			}
			// Every base-owned item is replaced in-place by the reconciled
			// sequence. LCS matches retain the same annotation IDs; only
			// unmatched IDs are absent from generated.
			for _, word := range ownedBaseWords {
				removeWordIDs[annStringValue(word.annotation, "id")] = struct{}{}
			}
			for id := range removed {
				removeWordIDs[id] = struct{}{}
			}
			generatedByLine[lineID] = generated
			estimatedPageBytes = nextEstimatedBytes
			setAnnotationText(proposedLine.annotation, strings.Join(tokens, " "))
			continue
		}

		if wordIdentityTextSequence(ownedBaseWords) != wordIdentityTextSequence(ownedProposedWords) {
			setAnnotationText(proposedLine.annotation, normalizedAnnotationTexts(ownedProposedWords))
			continue
		}
		if sameBox(baseLine, proposedLine) || len(ownedBaseWords) == 0 {
			continue
		}
		if !sameWordsIncludingGeometry(ownedBaseWords, ownedProposedWords) {
			// Simultaneous explicit word geometry edits win over inferred line-box
			// scaling; the save remains lossless and does not guess at intent.
			continue
		}
		if unionMatchesLine(ownedProposedWords, proposedLine) {
			// Structural word joins set the line to the existing word union. The
			// words already carry the intended geometry and must not be scaled.
			continue
		}
		for _, word := range ownedProposedWords {
			baseWord := baseWords[annStringValue(word.annotation, "id")]
			x1, y1, x2, y2, scaleErr := proportionalWordBox(baseLine, proposedLine, baseWord)
			if scaleErr != nil {
				return nil, fmt.Errorf("resize words for line %q: %w", lineID, scaleErr)
			}
			if err := retargetExistingWord(word.annotation, identity.CanvasURI, x1, y1, x2, y2); err != nil {
				return nil, fmt.Errorf("resize word %q: %w", annStringValue(word.annotation, "id"), err)
			}
		}
	}

	result := make([]any, 0, len(proposedItems)+iiif.MaxAnnotationsPerPage)
	for _, value := range proposedItems {
		annotation, _ := value.(map[string]any)
		id := strings.TrimSpace(annStringValue(annotation, "id"))
		if _, remove := removeWordIDs[id]; remove && strings.EqualFold(strings.TrimSpace(annStringValue(annotation, "textGranularity")), "word") {
			continue
		}
		result = append(result, value)
		if generated := generatedByLine[id]; len(generated) > 0 && strings.EqualFold(strings.TrimSpace(annStringValue(annotation, "textGranularity")), "line") {
			result = append(result, generated...)
		}
	}
	if len(result) > iiif.MaxAnnotationsPerPage {
		return nil, fmt.Errorf("reconciled page would exceed %d annotations", iiif.MaxAnnotationsPerPage)
	}
	return preserveCommittedLineOwnershipOrder(result, baseLines), nil
}

// preserveCommittedLineOwnershipOrder makes the ID-based ownership decision
// durable for the next revision. Spatial ownership breaks ties by canonical
// item order, so a client-prepended overlapping line would otherwise become
// the apparent owner after one successful save even though it was forbidden
// from claiming the words during this save. Existing line items keep their
// relative order and occupy the earlier line slots; new structural/drawn lines
// keep their relative order after them. Export reading order is geometry-based.
func preserveCommittedLineOwnershipOrder(items []any, baseLines map[string]locatedAnnotation) []any {
	existing := make([]any, 0)
	created := make([]any, 0)
	lineSlots := make([]int, 0)
	for index, value := range items {
		annotation, ok := value.(map[string]any)
		if !ok || !strings.EqualFold(strings.TrimSpace(annStringValue(annotation, "textGranularity")), "line") {
			continue
		}
		lineSlots = append(lineSlots, index)
		if _, committed := baseLines[annStringValue(annotation, "id")]; committed {
			existing = append(existing, value)
		} else {
			created = append(created, value)
		}
	}
	if len(existing) == 0 || len(created) == 0 {
		return items
	}
	ordered := append(existing, created...)
	for index, slot := range lineSlots {
		items[slot] = ordered[index]
	}
	return items
}

func reconciliationAnnotations(items []any, granularity string) (map[string]locatedAnnotation, error) {
	result := make(map[string]locatedAnnotation)
	for index, value := range items {
		annotation, ok := value.(map[string]any)
		if !ok || !strings.EqualFold(strings.TrimSpace(annStringValue(annotation, "textGranularity")), granularity) {
			continue
		}
		id := strings.TrimSpace(annStringValue(annotation, "id"))
		if id == "" {
			return nil, fmt.Errorf("%s annotation at position %d has no id", granularity, index)
		}
		x1, y1, x2, y2, err := parseXYWH(extractFragment(annotation))
		if err != nil {
			return nil, fmt.Errorf("%s annotation %q has invalid geometry: %w", granularity, id, err)
		}
		result[id] = locatedAnnotation{annotation: annotation, index: index, x1: x1, y1: y1, x2: x2, y2: y2}
	}
	return result, nil
}

func proposedOwnedWords(base []locatedAnnotation, proposed map[string]locatedAnnotation) []locatedAnnotation {
	result := make([]locatedAnnotation, 0, len(base))
	for _, word := range base {
		if current, ok := proposed[annStringValue(word.annotation, "id")]; ok {
			result = append(result, current)
		}
	}
	return result
}

func sortReconciliationWords(words []locatedAnnotation) {
	sort.SliceStable(words, func(i, j int) bool {
		if words[i].x1 != words[j].x1 {
			return words[i].x1 < words[j].x1
		}
		if words[i].y1 != words[j].y1 {
			return words[i].y1 < words[j].y1
		}
		return words[i].index < words[j].index
	})
}

func lineAndWordsSynchronized(line locatedAnnotation, words []locatedAnnotation) bool {
	tokens := strings.Fields(extractAnnotationText(line.annotation))
	if len(tokens) != len(words) {
		return false
	}
	for index, word := range words {
		if strings.TrimSpace(extractAnnotationText(word.annotation)) != tokens[index] {
			return false
		}
	}
	return true
}

func wordIdentityTextSequence(words []locatedAnnotation) string {
	var result strings.Builder
	for _, word := range words {
		result.WriteString(annStringValue(word.annotation, "id"))
		result.WriteByte(0)
		result.WriteString(extractAnnotationText(word.annotation))
		result.WriteByte(0)
	}
	return result.String()
}

func sameBox(left, right locatedAnnotation) bool {
	return left.x1 == right.x1 && left.y1 == right.y1 && left.x2 == right.x2 && left.y2 == right.y2
}

func sameWordsIncludingGeometry(base, proposed []locatedAnnotation) bool {
	if len(base) != len(proposed) {
		return false
	}
	baseByID := make(map[string]locatedAnnotation, len(base))
	for _, word := range base {
		baseByID[annStringValue(word.annotation, "id")] = word
	}
	for _, word := range proposed {
		committed, ok := baseByID[annStringValue(word.annotation, "id")]
		if !ok || extractAnnotationText(committed.annotation) != extractAnnotationText(word.annotation) || !sameBox(committed, word) {
			return false
		}
	}
	return true
}

func sameWordIDMembership(base, proposed []locatedAnnotation) bool {
	if len(base) != len(proposed) {
		return false
	}
	ids := make(map[string]struct{}, len(base))
	for _, word := range base {
		ids[annStringValue(word.annotation, "id")] = struct{}{}
	}
	for _, word := range proposed {
		if _, ok := ids[annStringValue(word.annotation, "id")]; !ok {
			return false
		}
	}
	return true
}

func unionMatchesLine(words []locatedAnnotation, line locatedAnnotation) bool {
	if len(words) == 0 {
		return false
	}
	x1, y1, x2, y2 := annotationUnion(words)
	return x1 == line.x1 && y1 == line.y1 && x2 == line.x2 && y2 == line.y2
}

func reconcileLineTokens(
	pageID, canvasURI string,
	expectedRevision uint64,
	line locatedAnnotation,
	baseWords []locatedAnnotation,
	proposedWords map[string]locatedAnnotation,
	tokens []string,
	usedIDs map[string]struct{},
	lcsCellsRemaining *int,
) ([]any, map[string]struct{}, error) {
	if len(tokens) > 0 && line.x2-line.x1 < len(tokens) {
		return nil, nil, fmt.Errorf("line geometry is too narrow for %d distinct word boxes", len(tokens))
	}
	baseTokens := make([]string, len(baseWords))
	for index, word := range baseWords {
		baseTokens[index] = strings.TrimSpace(extractAnnotationText(word.annotation))
	}
	matches, err := longestCommonTokenMatches(baseTokens, tokens, lcsCellsRemaining)
	if err != nil {
		return nil, nil, err
	}
	matchByToken := make(map[int]int, len(matches))
	for _, match := range matches {
		matchByToken[match.tokenIndex] = match.baseIndex
	}

	removed := make(map[string]struct{}, len(baseWords))
	for _, word := range baseWords {
		removed[annStringValue(word.annotation, "id")] = struct{}{}
	}
	result := make([]any, 0, len(tokens))
	lineID := annStringValue(line.annotation, "id")
	for tokenIndex, token := range tokens {
		x1, y1, x2, y2 := dividedWordBox(line, tokenIndex, len(tokens))
		if baseIndex, matched := matchByToken[tokenIndex]; matched {
			baseWord := baseWords[baseIndex]
			wordID := annStringValue(baseWord.annotation, "id")
			template := baseWord.annotation
			if proposed, ok := proposedWords[wordID]; ok {
				template = proposed.annotation
			}
			word, updateErr := updatedRetainedWord(template, wordID, canvasURI, x1, y1, x2, y2, token)
			if updateErr != nil {
				return nil, nil, updateErr
			}
			delete(removed, wordID)
			result = append(result, word)
			continue
		}

		wordID, idErr := availableReconciledWordID(pageID, lineID, expectedRevision, tokenIndex, token, usedIDs)
		if idErr != nil {
			return nil, nil, idErr
		}
		word, deriveErr := deriveTextAnnotation(line.annotation, wordID, "word", canvasURI, x1, y1, x2, y2, token)
		if deriveErr != nil {
			return nil, nil, deriveErr
		}
		usedIDs[wordID] = struct{}{}
		result = append(result, word)
	}
	return result, removed, nil
}

func updatedRetainedWord(template map[string]any, id, canvasURI string, x1, y1, x2, y2 int, text string) (map[string]any, error) {
	word, err := cloneAnnotation(template)
	if err != nil {
		return nil, err
	}
	word["id"] = id
	word["type"] = "Annotation"
	word["textGranularity"] = "word"
	if word["motivation"] == nil {
		word["motivation"] = "supplementing"
	}
	if extractAnnotationText(word) != strings.TrimSpace(text) {
		word["body"] = textBodyWithValue(word["body"], strings.TrimSpace(text))
		clearFirstTextualBodyIdentity(word["body"])
	}
	if err := retargetExistingWord(word, canvasURI, x1, y1, x2, y2); err != nil {
		return nil, err
	}
	return word, nil
}

func retargetExistingWord(word map[string]any, canvasURI string, x1, y1, x2, y2 int) error {
	currentX1, currentY1, currentX2, currentY2, err := parseXYWH(extractFragment(word))
	if err == nil && currentX1 == x1 && currentY1 == y1 && currentX2 == x2 && currentY2 == y2 && extractCanvasURI(word) == strings.TrimSpace(canvasURI) {
		return nil
	}
	target, err := targetWithFragment(word["target"], canvasURI, x1, y1, x2, y2)
	if err != nil {
		return err
	}
	word["target"] = target
	clearMutableTargetResourceIDs(word)
	return nil
}

func dividedWordBox(line locatedAnnotation, index, count int) (int, int, int, int) {
	if count <= 0 {
		return line.x1, line.y1, line.x2, line.y2
	}
	width := int64(line.x2 - line.x1)
	x1 := int64(line.x1) + width*int64(index)/int64(count)
	x2 := int64(line.x1) + width*int64(index+1)/int64(count)
	return int(x1), line.y1, int(x2), line.y2
}

func availableReconciledWordID(pageID, lineID string, expectedRevision uint64, tokenIndex int, token string, used map[string]struct{}) (string, error) {
	for nonce := 0; nonce <= iiif.MaxAnnotationsPerPage; nonce++ {
		seed := fmt.Sprintf("%s\x00word-edit\x00%d\x00%d\x00%s\x00%d", lineID, expectedRevision, tokenIndex, token, nonce)
		id, err := iiif.AnnotationID(pageID, seed)
		if err != nil {
			return "", err
		}
		if _, collision := used[id]; !collision {
			return id, nil
		}
	}
	return "", fmt.Errorf("could not allocate a unique canonical word id")
}

func longestCommonTokenMatches(base, tokens []string, cellsRemaining *int) ([]tokenMatch, error) {
	prefix := 0
	for prefix < len(base) && prefix < len(tokens) && base[prefix] == tokens[prefix] {
		prefix++
	}
	baseEnd, tokenEnd := len(base), len(tokens)
	for baseEnd > prefix && tokenEnd > prefix && base[baseEnd-1] == tokens[tokenEnd-1] {
		baseEnd--
		tokenEnd--
	}
	middleBase := base[prefix:baseEnd]
	middleTokens := tokens[prefix:tokenEnd]
	cells := 0
	if len(middleBase) > 0 {
		if len(middleTokens) > maxWordReconciliationLCSCells/len(middleBase) {
			return nil, fmt.Errorf("token reconciliation exceeds %d comparison cells", maxWordReconciliationLCSCells)
		}
		cells = len(middleBase) * len(middleTokens)
	}
	if cellsRemaining != nil {
		if cells > *cellsRemaining {
			return nil, fmt.Errorf("page token reconciliation exceeds %d comparison cells", maxWordReconciliationLCSCells)
		}
		*cellsRemaining -= cells
	}
	columns := len(middleTokens) + 1
	dp := make([]uint16, (len(middleBase)+1)*columns)
	for i := len(middleBase) - 1; i >= 0; i-- {
		for j := len(middleTokens) - 1; j >= 0; j-- {
			cell := i*columns + j
			if middleBase[i] == middleTokens[j] {
				dp[cell] = dp[(i+1)*columns+j+1] + 1
			} else if dp[(i+1)*columns+j] >= dp[i*columns+j+1] {
				dp[cell] = dp[(i+1)*columns+j]
			} else {
				dp[cell] = dp[i*columns+j+1]
			}
		}
	}
	matches := make([]tokenMatch, 0, prefix+int(dp[0])+(len(base)-baseEnd))
	for index := 0; index < prefix; index++ {
		matches = append(matches, tokenMatch{baseIndex: index, tokenIndex: index})
	}
	for i, j := 0, 0; i < len(middleBase) && j < len(middleTokens); {
		if middleBase[i] == middleTokens[j] {
			matches = append(matches, tokenMatch{baseIndex: prefix + i, tokenIndex: prefix + j})
			i++
			j++
		} else if dp[(i+1)*columns+j] >= dp[i*columns+j+1] {
			i++
		} else {
			j++
		}
	}
	for offset := 0; baseEnd+offset < len(base); offset++ {
		matches = append(matches, tokenMatch{baseIndex: baseEnd + offset, tokenIndex: tokenEnd + offset})
	}
	return matches, nil
}

func estimateReconciledTokenBytes(current int, line locatedAnnotation, replaced []locatedAnnotation, tokens []string) (int, error) {
	replacedBytes := 0
	maxTemplateBytes := 0
	templates := make([]map[string]any, 0, len(replaced)+1)
	templates = append(templates, line.annotation)
	for _, word := range replaced {
		templates = append(templates, word.annotation)
		encoded, err := json.Marshal(word.annotation)
		if err != nil {
			return 0, fmt.Errorf("estimate replaced word: %w", err)
		}
		replacedBytes += len(encoded) + 1
	}
	for _, template := range templates {
		encoded, err := json.Marshal(template)
		if err != nil {
			return 0, fmt.Errorf("estimate word template: %w", err)
		}
		if len(encoded) > maxTemplateBytes {
			maxTemplateBytes = len(encoded)
		}
	}
	estimated := current - replacedBytes
	if estimated < 0 {
		estimated = 0
	}
	for _, token := range tokens {
		cost := maxTemplateBytes + 6*len(token) + 256
		if cost > iiif.MaxAnnotationPageBytes-estimated {
			return 0, fmt.Errorf("token edit would exceed %d encoded bytes", iiif.MaxAnnotationPageBytes)
		}
		estimated += cost
	}
	return estimated, nil
}

func proportionalWordBox(baseLine, proposedLine, baseWord locatedAnnotation) (int, int, int, int, error) {
	baseWidth, baseHeight := baseLine.x2-baseLine.x1, baseLine.y2-baseLine.y1
	newWidth, newHeight := proposedLine.x2-proposedLine.x1, proposedLine.y2-proposedLine.y1
	if baseWidth <= 0 || baseHeight <= 0 || newWidth <= 0 || newHeight <= 0 {
		return 0, 0, 0, 0, fmt.Errorf("line geometry must have positive extent")
	}
	scale := func(value, oldOrigin, oldExtent, newOrigin, newExtent int) int {
		numerator := int64(value-oldOrigin) * int64(newExtent)
		if numerator >= 0 {
			numerator += int64(oldExtent) / 2
		} else {
			numerator -= int64(oldExtent) / 2
		}
		return newOrigin + int(numerator/int64(oldExtent))
	}
	x1 := scale(baseWord.x1, baseLine.x1, baseWidth, proposedLine.x1, newWidth)
	x2 := scale(baseWord.x2, baseLine.x1, baseWidth, proposedLine.x1, newWidth)
	y1 := scale(baseWord.y1, baseLine.y1, baseHeight, proposedLine.y1, newHeight)
	y2 := scale(baseWord.y2, baseLine.y1, baseHeight, proposedLine.y1, newHeight)
	x1 = maxInt(proposedLine.x1, minInt(x1, proposedLine.x2-1))
	x2 = maxInt(x1+1, minInt(x2, proposedLine.x2))
	y1 = maxInt(proposedLine.y1, minInt(y1, proposedLine.y2-1))
	y2 = maxInt(y1+1, minInt(y2, proposedLine.y2))
	return x1, y1, x2, y2, nil
}
