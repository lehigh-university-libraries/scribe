package server

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/lehigh-university-libraries/scribe/internal/iiif"
)

func TestReconcileEditedLineWordsUsesCommittedLCSAndPreservesRetainedExtensions(t *testing.T) {
	t.Parallel()
	line := structuralAnnotation(t, "reconcile-lcs-line", "line", "Course Catalog", 0, 0, 300, 30)
	course := structuralAnnotation(t, "reconcile-lcs-course", "word", "Course", 0, 0, 140, 30)
	catalog := structuralAnnotation(t, "reconcile-lcs-catalog", "word", "Catalog", 160, 0, 140, 30)
	for index, word := range []map[string]any{course, catalog} {
		word["scribe:counter"] = json.Number("9007199254740993")
		body := word["body"].([]any)[0].(map[string]any)
		body["id"] = "https://scribe.example/body/original"
		body["scribe:bodyState"] = "reviewed"
		target := word["target"].(map[string]any)
		target["id"] = "https://scribe.example/target/original"
		target["scribe:targetState"] = "reviewed"
		selector := target["selector"].(map[string]any)
		selector["id"] = "https://scribe.example/selector/original"
		selector["scribe:selectorIndex"] = json.Number([]string{"1", "2"}[index])
	}
	base := []any{line, course, catalog}
	proposed := reconciliationCloneItems(t, base)
	proposedLine := reconciliationAnnotation(t, proposed, annStringValue(line, "id"))
	proposedCourse := reconciliationAnnotation(t, proposed, annStringValue(course, "id"))
	proposedCatalog := reconciliationAnnotation(t, proposed, annStringValue(catalog, "id"))
	setAnnotationText(proposedLine, "Revised Course Catalog")
	// Mirror the browser's transient redistribution. The committed texts, not
	// these bodies, are the source for matching retained word identities.
	setAnnotationText(proposedCourse, "Revised")
	setAnnotationText(proposedCatalog, "Course Catalog")

	got, err := reconcileEditedLineWords(base, proposed, testPageIdentity, 7)
	if err != nil {
		t.Fatalf("reconcile inserted token: %v", err)
	}
	words := reconciliationWords(t, got)
	if len(words) != 3 {
		t.Fatalf("word count = %d, want 3", len(words))
	}
	if texts := []string{extractAnnotationText(words[0]), extractAnnotationText(words[1]), extractAnnotationText(words[2])}; !reflect.DeepEqual(texts, []string{"Revised", "Course", "Catalog"}) {
		t.Fatalf("word texts = %v", texts)
	}
	if annStringValue(words[1], "id") != annStringValue(course, "id") || annStringValue(words[2], "id") != annStringValue(catalog, "id") {
		t.Fatalf("LCS-matched IDs were not retained: %q / %q", annStringValue(words[1], "id"), annStringValue(words[2], "id"))
	}
	if annStringValue(words[0], "id") == annStringValue(course, "id") || annStringValue(words[0], "id") == annStringValue(catalog, "id") {
		t.Fatal("inserted token reused an unmatched committed ID")
	}
	if _, err := iiif.PageIdentityFromAnnotationID(annStringValue(words[0], "id"), testCanvas); err != nil {
		t.Fatalf("inserted word ID is not canonical: %v", err)
	}
	for _, retained := range words[1:] {
		if retained["scribe:counter"] != json.Number("9007199254740993") {
			t.Fatalf("retained large extension value changed: %#v", retained["scribe:counter"])
		}
		body := retained["body"].([]any)[0].(map[string]any)
		target := retained["target"].(map[string]any)
		selector := target["selector"].(map[string]any)
		if body["scribe:bodyState"] != "reviewed" || target["scribe:targetState"] != "reviewed" || selector["scribe:selectorIndex"] == nil {
			t.Fatalf("retained extension graph changed: %#v", retained)
		}
		if body["id"] != nil || target["id"] != nil || selector["id"] != nil {
			t.Fatalf("changed embedded resource retained stale identity: %#v", retained)
		}
	}
}

func TestReconcileEditedLineWordsRejectsTokenFanoutBeforeAllocation(t *testing.T) {
	t.Parallel()
	line := structuralAnnotation(t, "reconcile-fanout-line", "line", "base", 0, 0, iiif.MaxAnnotationsPerPage+1, 20)
	word := structuralAnnotation(t, "reconcile-fanout-word", "word", "base", 0, 0, iiif.MaxAnnotationsPerPage+1, 20)
	base := []any{line, word}
	proposed := reconciliationCloneItems(t, base)
	setAnnotationText(reconciliationAnnotation(t, proposed, annStringValue(line, "id")), strings.Repeat("x ", iiif.MaxAnnotationsPerPage+1))

	if _, err := reconcileEditedLineWords(base, proposed, testPageIdentity, 2); err == nil || !strings.Contains(err.Error(), "exceed 10000 annotations") {
		t.Fatalf("token fanout error = %v", err)
	}
}

func TestLongestCommonTokenMatchesIsDeterministicWithDuplicates(t *testing.T) {
	t.Parallel()
	budget := maxWordReconciliationLCSCells
	matches, err := longestCommonTokenMatches(
		[]string{"same", "middle", "same"},
		[]string{"same", "same"},
		&budget,
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []tokenMatch{{baseIndex: 0, tokenIndex: 0}, {baseIndex: 2, tokenIndex: 1}}
	if !reflect.DeepEqual(matches, want) {
		t.Fatalf("duplicate-token matches = %#v, want %#v", matches, want)
	}
}

func TestReconcileEditedLineWordsScalesOnlyBaseOwnedWordsOnLineResize(t *testing.T) {
	t.Parallel()
	line := structuralAnnotation(t, "reconcile-resize-line", "line", "left right", 10, 20, 200, 40)
	left := structuralAnnotation(t, "reconcile-resize-left", "word", "left", 10, 20, 100, 40)
	right := structuralAnnotation(t, "reconcile-resize-right", "word", "right", 110, 20, 100, 40)
	sentinelLine := structuralAnnotation(t, "reconcile-resize-sentinel-line", "line", "sentinel", 500, 500, 100, 20)
	sentinel := structuralAnnotation(t, "reconcile-resize-sentinel", "word", "sentinel", 500, 500, 100, 20)
	for _, word := range []map[string]any{left, right} {
		target := word["target"].(map[string]any)
		target["id"] = "https://scribe.example/target/resize"
		target["scribe:targetState"] = "reviewed"
		selector := target["selector"].(map[string]any)
		selector["id"] = "https://scribe.example/selector/resize"
		selector["scribe:selectorState"] = "reviewed"
	}
	base := []any{line, left, right, sentinelLine, sentinel}
	proposed := reconciliationCloneItems(t, base)
	resized := reconciliationAnnotation(t, proposed, annStringValue(line, "id"))
	resizedTarget := resized["target"].(map[string]any)
	resizedTarget["id"] = "https://scribe.example/targets/resized-line"
	resizedTarget["selector"].(map[string]any)["id"] = "https://scribe.example/selectors/resized-line"
	target, err := targetWithFragment(resized["target"], testCanvas, 30, 60, 430, 140)
	if err != nil {
		t.Fatal(err)
	}
	resized["target"] = target
	sentinelBefore, err := cloneAnnotation(reconciliationAnnotation(t, proposed, annStringValue(sentinel, "id")))
	if err != nil {
		t.Fatal(err)
	}

	got, err := reconcileEditedLineWords(base, proposed, testPageIdentity, 4)
	if err != nil {
		t.Fatalf("reconcile resized line: %v", err)
	}
	persistedLineTarget := reconciliationAnnotation(t, got, annStringValue(line, "id"))["target"].(map[string]any)
	if persistedLineTarget["id"] != nil || persistedLineTarget["selector"].(map[string]any)["id"] != nil {
		t.Fatalf("resized line retained stale changed-resource identity: %#v", persistedLineTarget)
	}
	wantBoxes := map[string][4]int{
		annStringValue(left, "id"):  {30, 60, 230, 140},
		annStringValue(right, "id"): {230, 60, 430, 140},
	}
	for id, want := range wantBoxes {
		word := reconciliationAnnotation(t, got, id)
		x1, y1, x2, y2, parseErr := parseXYWH(extractFragment(word))
		if parseErr != nil || [4]int{x1, y1, x2, y2} != want {
			t.Fatalf("word %q bbox = %v (%v), want %v", id, [4]int{x1, y1, x2, y2}, parseErr, want)
		}
		target := word["target"].(map[string]any)
		selector := target["selector"].(map[string]any)
		if target["scribe:targetState"] != "reviewed" || selector["scribe:selectorState"] != "reviewed" {
			t.Fatalf("word %q lost target extensions: %#v", id, word)
		}
		if target["id"] != nil || selector["id"] != nil {
			t.Fatalf("word %q retained changed selector identity: %#v", id, word)
		}
	}
	if after := reconciliationAnnotation(t, got, annStringValue(sentinel, "id")); !reflect.DeepEqual(after, sentinelBefore) {
		t.Fatalf("unrelated word changed:\n got %#v\nwant %#v", after, sentinelBefore)
	}
}

func TestReconcileEditedLineWordsDoesNotStealWordsForNewOverlappingLine(t *testing.T) {
	t.Parallel()
	existing := structuralAnnotation(t, "reconcile-overlap-existing", "line", "alpha beta", 0, 0, 200, 20)
	alpha := structuralAnnotation(t, "reconcile-overlap-alpha", "word", "alpha", 0, 0, 100, 20)
	beta := structuralAnnotation(t, "reconcile-overlap-beta", "word", "beta", 100, 0, 100, 20)
	base := []any{existing, alpha, beta}
	blank := structuralAnnotation(t, "reconcile-overlap-new", "line", "", 0, 0, 200, 20)
	proposed := append([]any{blank}, reconciliationCloneItems(t, base)...)

	got, err := reconcileEditedLineWords(base, proposed, testPageIdentity, 2)
	if err != nil {
		t.Fatalf("reconcile overlapping new line: %v", err)
	}
	if extractAnnotationText(reconciliationAnnotation(t, got, annStringValue(existing, "id"))) != "alpha beta" {
		t.Fatal("existing line text was changed by overlapping line")
	}
	if extractAnnotationText(reconciliationAnnotation(t, got, annStringValue(blank, "id"))) != "" {
		t.Fatal("blank line claimed existing word text")
	}
	for _, word := range []map[string]any{alpha, beta} {
		retained := reconciliationAnnotation(t, got, annStringValue(word, "id"))
		if extractAnnotationText(retained) != extractAnnotationText(word) || extractFragment(retained) != extractFragment(word) {
			t.Fatalf("existing word was stolen or changed: %#v", retained)
		}
	}
	ownership := assignSpatialWordsToLines(got, nil)
	if len(ownership[annStringValue(existing, "id")]) != 2 || len(ownership[annStringValue(blank, "id")]) != 0 {
		t.Fatalf("overlapping new line changed durable word ownership: %#v", ownership)
	}
}

func TestReconcileEditedLineWordsRejectsUnsynchronizedNewLineWords(t *testing.T) {
	t.Parallel()
	existing := structuralAnnotation(t, "reconcile-new-words-existing", "line", "alpha beta", 0, 0, 200, 20)
	alpha := structuralAnnotation(t, "reconcile-new-words-alpha", "word", "alpha", 0, 0, 100, 20)
	beta := structuralAnnotation(t, "reconcile-new-words-beta", "word", "beta", 100, 0, 100, 20)
	base := []any{existing, alpha, beta}
	created := structuralAnnotation(t, "reconcile-new-words-line", "line", "new", 0, 0, 200, 20)
	createdWord := structuralAnnotation(t, "reconcile-new-words-word", "word", "different", 50, 0, 100, 20)
	proposed := append(reconciliationCloneItems(t, base), created, createdWord)
	before := wordIdentityTextSequence(locatedWords(assignSpatialWordsToLines(base, nil)[annStringValue(existing, "id")]))

	if _, err := reconcileEditedLineWords(base, proposed, testPageIdentity, 2); err == nil || !strings.Contains(err.Error(), "must be synchronized") {
		t.Fatalf("unsynchronized new-line words error = %v", err)
	}
	after := wordIdentityTextSequence(locatedWords(assignSpatialWordsToLines(base, nil)[annStringValue(existing, "id")]))
	if after != before || extractAnnotationText(existing) != "alpha beta" {
		t.Fatal("rejected new-line word set mutated the committed fixture")
	}
}

func TestReconcileEditedLineWordsCascadesRemovedBaseLineByWordID(t *testing.T) {
	t.Parallel()
	removedLine := structuralAnnotation(t, "reconcile-delete-line", "line", "delete these", 0, 0, 200, 20)
	deleteWord := structuralAnnotation(t, "reconcile-delete-word", "word", "delete", 0, 0, 100, 20)
	theseWord := structuralAnnotation(t, "reconcile-these-word", "word", "these", 100, 0, 100, 20)
	keptLine := structuralAnnotation(t, "reconcile-kept-line", "line", "keep", 0, 40, 200, 20)
	keptWord := structuralAnnotation(t, "reconcile-kept-word", "word", "keep", 0, 40, 200, 20)
	base := []any{removedLine, deleteWord, theseWord, keptLine, keptWord}
	proposed := reconciliationCloneItems(t, base[1:])
	// Moving an orphan into another line must not transfer its ownership; the
	// committed owner ID is the cascade boundary.
	moved := reconciliationAnnotation(t, proposed, annStringValue(deleteWord, "id"))
	target, err := targetWithFragment(moved["target"], testCanvas, 0, 40, 100, 60)
	if err != nil {
		t.Fatal(err)
	}
	moved["target"] = target

	got, err := reconcileEditedLineWords(base, proposed, testPageIdentity, 3)
	if err != nil {
		t.Fatalf("reconcile deleted line: %v", err)
	}
	for _, deletedID := range []string{annStringValue(deleteWord, "id"), annStringValue(theseWord, "id")} {
		if reconciliationFind(got, deletedID) != nil {
			t.Fatalf("base-owned word %q survived its line deletion", deletedID)
		}
	}
	if extractAnnotationText(reconciliationAnnotation(t, got, annStringValue(keptLine, "id"))) != "keep" || reconciliationFind(got, annStringValue(keptWord, "id")) == nil {
		t.Fatal("unrelated line/word changed during cascade")
	}
}

func TestReconcileEditedLineWordsWordCRUDUpdatesUnchangedLine(t *testing.T) {
	t.Parallel()
	line := structuralAnnotation(t, "reconcile-crud-line", "line", "one two three", 0, 0, 300, 20)
	one := structuralAnnotation(t, "reconcile-crud-one", "word", "one", 0, 0, 100, 20)
	two := structuralAnnotation(t, "reconcile-crud-two", "word", "two", 100, 0, 100, 20)
	three := structuralAnnotation(t, "reconcile-crud-three", "word", "three", 200, 0, 100, 20)
	base := []any{line, one, two, three}
	proposed := reconciliationCloneItems(t, []any{line, one, three})
	setAnnotationText(reconciliationAnnotation(t, proposed, annStringValue(one, "id")), "ONE")
	four := structuralAnnotation(t, "reconcile-crud-four", "word", "four", 100, 0, 100, 20)
	proposed = append(proposed, four)

	got, err := reconcileEditedLineWords(base, proposed, testPageIdentity, 9)
	if err != nil {
		t.Fatalf("reconcile word CRUD: %v", err)
	}
	if text := extractAnnotationText(reconciliationAnnotation(t, got, annStringValue(line, "id"))); text != "ONE four three" {
		t.Fatalf("line text = %q, want ONE four three", text)
	}
	if reconciliationFind(got, annStringValue(two, "id")) != nil {
		t.Fatal("explicitly deleted word was recreated")
	}
	for _, id := range []string{annStringValue(one, "id"), annStringValue(three, "id"), annStringValue(four, "id")} {
		if reconciliationFind(got, id) == nil {
			t.Fatalf("word %q was not retained", id)
		}
	}
}

func reconciliationCloneItems(t *testing.T, items []any) []any {
	t.Helper()
	raw, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	var clone []any
	if err := iiif.DecodeJSON(raw, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func reconciliationFind(items []any, id string) map[string]any {
	for _, value := range items {
		annotation, _ := value.(map[string]any)
		if annStringValue(annotation, "id") == id {
			return annotation
		}
	}
	return nil
}

func reconciliationAnnotation(t *testing.T, items []any, id string) map[string]any {
	t.Helper()
	annotation := reconciliationFind(items, id)
	if annotation == nil {
		t.Fatalf("annotation %q was not found", id)
	}
	return annotation
}

func reconciliationWords(t *testing.T, items []any) []map[string]any {
	t.Helper()
	located, err := reconciliationAnnotations(items, "word")
	if err != nil {
		t.Fatal(err)
	}
	words := make([]locatedAnnotation, 0, len(located))
	for _, word := range located {
		words = append(words, word)
	}
	sortReconciliationWords(words)
	result := make([]map[string]any, 0, len(words))
	for _, word := range words {
		result = append(result, word.annotation)
	}
	return result
}
