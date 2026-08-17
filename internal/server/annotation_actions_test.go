package server

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/models"
)

// lineAnno builds a minimal IIIF line annotation JSON string.
func lineAnno(id, canvasURI, text string, x, y, w, h int) string {
	id = testAnnotationID(strings.TrimPrefix(id, testAnnotationPage+"/items/"))
	return fmt.Sprintf(`{
		"id": %q,
		"type": "Annotation",
		"textGranularity": "line",
		"motivation": "supplementing",
		"body": [{"type":"TextualBody","purpose":"supplementing","format":"text/plain","value":%q}],
		"target": {
			"source": {"id":%q,"type":"Canvas"},
			"selector": {"type":"FragmentSelector","conformsTo":"http://www.w3.org/TR/media-frags/","value":"xywh=%d,%d,%d,%d"}
		}
	}`, id, text, canvasURI, x, y, w, h)
}

func testAnnotationID(seed string) string {
	id, err := iiif.AnnotationID(testAnnotationPage, seed)
	if err != nil {
		panic(err)
	}
	return id
}

const (
	testCanvas         = "https://example.org/canvas/1"
	testAnnotationPage = "https://scribe.example/item-image-1/canvas/page-1/annotations"
)

// --- parseLineAnnotation ---

func TestParseLineAnnotation(t *testing.T) {
	raw := lineAnno("anno-1", testCanvas, "Hello World", 10, 20, 200, 30)
	anno, text, x1, y1, x2, y2, canvas, err := parseLineAnnotation(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "Hello World" {
		t.Errorf("text: got %q, want %q", text, "Hello World")
	}
	if x1 != 10 || y1 != 20 || x2 != 210 || y2 != 50 {
		t.Errorf("bbox: got (%d,%d,%d,%d), want (10,20,210,50)", x1, y1, x2, y2)
	}
	if canvas != testCanvas {
		t.Errorf("canvas: got %q, want %q", canvas, testCanvas)
	}
	if annStringValue(anno, "id") != testAnnotationID("anno-1") {
		t.Errorf("id not preserved in parsed annotation")
	}
}

func TestParseLineAnnotation_SelectorArray(t *testing.T) {
	raw := `{
		"id": "anno-1",
		"type": "Annotation",
		"textGranularity": "line",
		"motivation": "supplementing",
		"body": [{"type":"TextualBody","purpose":"supplementing","format":"text/plain","value":"Hello World"}],
		"target": {
			"source": {"id":"https://example.org/canvas/1","type":"Canvas"},
			"selector": [
				{"type":"CssSelector","value":".ignored"},
				{"type":"FragmentSelector","conformsTo":"http://www.w3.org/TR/media-frags/","value":"xywh=10,20,200,30"}
			]
		}
	}`
	_, text, x1, y1, x2, y2, canvas, err := parseLineAnnotation(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "Hello World" {
		t.Errorf("text: got %q, want %q", text, "Hello World")
	}
	if x1 != 10 || y1 != 20 || x2 != 210 || y2 != 50 {
		t.Errorf("bbox: got (%d,%d,%d,%d), want (10,20,210,50)", x1, y1, x2, y2)
	}
	if canvas != testCanvas {
		t.Errorf("canvas: got %q, want %q", canvas, testCanvas)
	}
}

func TestParseLineAnnotation_Errors(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{"invalid json", `not json`},
		{"missing canvas", `{"id":"x","type":"Annotation","body":[{"type":"TextualBody","value":"hi"}],"target":{"source":{},"selector":{"type":"FragmentSelector","value":"xywh=0,0,10,10"}}}`},
		{"missing fragment", `{"id":"x","type":"Annotation","body":[{"type":"TextualBody","value":"hi"}],"target":{"source":{"id":"https://example.org/canvas/1","type":"Canvas"}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, _, _, _, _, err := parseLineAnnotation(tt.raw)
			if err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

// --- buildLineAnnotation ---

func TestBuildLineAnnotation(t *testing.T) {
	anno := buildLineAnnotation("built-1", testCanvas, 5, 10, 105, 40, "some text")
	if annStringValue(anno, "id") != "built-1" {
		t.Errorf("id: got %q", annStringValue(anno, "id"))
	}
	if annStringValue(anno, "textGranularity") != "line" {
		t.Errorf("granularity: got %q", annStringValue(anno, "textGranularity"))
	}
	text := extractAnnotationText(anno)
	if text != "some text" {
		t.Errorf("text: got %q", text)
	}
	// Round-trip through parseLineAnnotation to verify bbox is correct.
	b, _ := json.Marshal(anno)
	_, _, x1, y1, x2, y2, _, err := parseLineAnnotation(string(b))
	if err != nil {
		t.Fatalf("round-trip parse: %v", err)
	}
	// xywh=5,10,100,30 → x1=5,y1=10,x2=105,y2=40
	if x1 != 5 || y1 != 10 || x2 != 105 || y2 != 40 {
		t.Errorf("round-trip bbox: got (%d,%d,%d,%d), want (5,10,105,40)", x1, y1, x2, y2)
	}
}

// --- Complete-page structural transforms ---

var testPageIdentity = iiif.PageIdentity{
	PublicBaseURL: "https://scribe.example",
	ItemImageID:   1,
	CanvasURI:     testCanvas,
}

func TestSplitDraftLineIntoWordsRetainsLine(t *testing.T) {
	t.Parallel()
	line := structuralAnnotation(t, "line-retained", "line", "Hello World", 0, 0, 200, 30)
	lineID := annStringValue(line, "id")
	draft := canonicalAnnotationDraft(t, nil, line)

	if err := splitDraftLineIntoWords(draft, lineID, []string{"Hello", "World"}); err != nil {
		t.Fatalf("split line into words: %v", err)
	}
	assertCanonicalDraft(t, draft)

	retained := draftAnnotationByID(t, draft, lineID)
	if got := extractAnnotationText(retained); got != "Hello World" {
		t.Fatalf("retained line text = %q", got)
	}
	words := draftAnnotationsByGranularity(draft, "word")
	if len(words) != 2 || len(draft.items) != 3 {
		t.Fatalf("items = %d, words = %d; want retained line plus two words", len(draft.items), len(words))
	}
	for index, word := range words {
		if _, err := iiif.PageIdentityFromAnnotationID(annStringValue(word, "id"), testCanvas); err != nil {
			t.Errorf("word %d has noncanonical id: %v", index, err)
		}
		_, text, x1, y1, x2, y2, canvas, err := parseLineAnnotation(mustMarshal(t, word))
		if err != nil {
			t.Fatalf("parse word %d: %v", index, err)
		}
		wantText := []string{"Hello", "World"}[index]
		if text != wantText || x1 != index*100 || y1 != 0 || x2 != (index+1)*100 || y2 != 30 || canvas != testCanvas {
			t.Errorf("word %d = %q (%d,%d,%d,%d) on %q", index, text, x1, y1, x2, y2, canvas)
		}
	}
}

func TestSplitDraftPageIntoWordsIsStableAcrossEveryLine(t *testing.T) {
	t.Parallel()
	pageID, err := iiif.CanonicalPageID(testPageIdentity.PublicBaseURL, testPageIdentity.ItemImageID)
	if err != nil {
		t.Fatal(err)
	}
	lineAID, err := iiif.AnnotationID(pageID, "page-split-line-a")
	if err != nil {
		t.Fatal(err)
	}
	lineBID, err := iiif.AnnotationID(pageID, "page-split-line-b")
	if err != nil {
		t.Fatal(err)
	}
	lineA := transcriptionAnnotation(lineAID, "line", "alpha beta", testPageIdentity.CanvasURI, models.BBox{X1: 0, Y1: 0, X2: 300, Y2: 40})
	lineB := transcriptionAnnotation(lineBID, "line", "gamma delta epsilon", testPageIdentity.CanvasURI, models.BBox{X1: 0, Y1: 50, X2: 450, Y2: 90})
	wordLifecycleAddExtensions(lineA)
	wordLifecycleAddExtensions(lineB)
	seed, err := iiif.NewAnnotationPage(testPageIdentity, []any{lineA, lineB})
	if err != nil {
		t.Fatal(err)
	}
	baseDraft, err := decodeAnnotationDraft(string(seed), testPageIdentity)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := decodeAnnotationDraft(string(seed), testPageIdentity)
	if err != nil {
		t.Fatal(err)
	}

	if err := splitDraftPageIntoWords(draft); err != nil {
		t.Fatalf("first page split: %v", err)
	}
	firstWords := draftAnnotationsByGranularity(draft, "word")
	if len(firstWords) != 5 {
		t.Fatalf("first page split word count = %d, want 5", len(firstWords))
	}
	firstIDs := make(map[string]struct{}, len(firstWords))
	for _, word := range firstWords {
		firstIDs[annStringValue(word, "id")] = struct{}{}
	}
	reconciled, err := reconcileEditedLineWords(baseDraft.items, draft.items, testPageIdentity, 1)
	if err != nil {
		t.Fatalf("reconcile first page split: %v", err)
	}
	draft.items = reconciled
	draft.reindex()
	if got := extractAnnotationText(draftAnnotationByID(t, draft, lineAID)); got != "alpha beta" {
		t.Fatalf("reconciled first line text = %q, want alpha beta", got)
	}
	if got := extractAnnotationText(draftAnnotationByID(t, draft, lineBID)); got != "gamma delta epsilon" {
		t.Fatalf("reconciled second line text = %q, want gamma delta epsilon", got)
	}
	encoded := assertCanonicalDraft(t, draft)
	draft, err = decodeAnnotationDraft(encoded, testPageIdentity)
	if err != nil {
		t.Fatalf("reload first page split: %v", err)
	}

	if err := splitDraftPageIntoWords(draft); err != nil {
		t.Fatalf("repeated page split: %v", err)
	}
	repeatedWords := draftAnnotationsByGranularity(draft, "word")
	if len(repeatedWords) != len(firstWords) {
		t.Fatalf("repeated page split word count = %d, want %d", len(repeatedWords), len(firstWords))
	}
	for _, word := range repeatedWords {
		if _, ok := firstIDs[annStringValue(word, "id")]; !ok {
			t.Fatalf("repeated page split generated unstable word ID %q", annStringValue(word, "id"))
		}
	}
	assertCanonicalDraft(t, draft)
}

func TestSplitDraftLineIntoWordsTokenizesAndRejectsInvalidFanout(t *testing.T) {
	t.Parallel()
	t.Run("tokenizes canonical line text", func(t *testing.T) {
		draft := canonicalAnnotationDraft(t, nil, structuralAnnotation(t, "tokenize", "line", "one two three", 0, 0, 300, 20))
		lineID := annStringValue(draft.items[0].(map[string]any), "id")
		if err := splitDraftLineIntoWords(draft, lineID, nil); err != nil {
			t.Fatal(err)
		}
		if got := len(draftAnnotationsByGranularity(draft, "word")); got != 3 {
			t.Fatalf("word count = %d, want 3", got)
		}
		assertCanonicalDraft(t, draft)
	})

	t.Run("width must admit distinct boxes", func(t *testing.T) {
		line := structuralAnnotation(t, "narrow", "line", "one two three", 0, 0, 2, 20)
		draft := canonicalAnnotationDraft(t, nil, line)
		before := assertCanonicalDraft(t, draft)
		err := splitDraftLineIntoWords(draft, annStringValue(line, "id"), nil)
		if err == nil || !strings.Contains(err.Error(), "too narrow") {
			t.Fatalf("narrow line error = %v", err)
		}
		if after := assertCanonicalDraft(t, draft); after != before {
			t.Fatal("narrow-line rejection mutated the draft")
		}
	})

	t.Run("empty line", func(t *testing.T) {
		line := structuralAnnotation(t, "empty", "line", "", 0, 0, 200, 20)
		draft := canonicalAnnotationDraft(t, nil, line)
		if err := splitDraftLineIntoWords(draft, annStringValue(line, "id"), nil); err == nil {
			t.Fatal("empty line was accepted")
		}
	})
}

func TestSplitDraftLineIntoWordsRejectsUnrelatedDeterministicIDCollision(t *testing.T) {
	t.Parallel()
	line := structuralAnnotation(t, "collision-line", "line", "one two", 0, 0, 200, 20)
	lineID := annStringValue(line, "id")
	collisionID, err := iiif.AnnotationID(testAnnotationPage, lineID+"\x00word\x001")
	if err != nil {
		t.Fatal(err)
	}
	unrelated := structuralAnnotation(t, "unrelated", "word", "elsewhere", 500, 500, 20, 20)
	unrelated["id"] = collisionID
	draft := canonicalAnnotationDraft(t, nil, line, unrelated)
	before := assertCanonicalDraft(t, draft)

	err = splitDraftLineIntoWords(draft, lineID, []string{"one", "two"})
	if err == nil || !strings.Contains(err.Error(), "collides with an unrelated") {
		t.Fatalf("collision error = %v", err)
	}
	if after := assertCanonicalDraft(t, draft); after != before {
		t.Fatal("collision rejection mutated the draft")
	}
}

func TestSplitDraftLineIntoWordsPreflightsFanoutBeforeMutation(t *testing.T) {
	t.Parallel()
	line := structuralAnnotation(t, "fanout", "line", "original text", 0, 0, 1000, 20)
	draft := canonicalAnnotationDraft(t, nil, line)
	before := assertCanonicalDraft(t, draft)
	// Simulate a valid page at the admission ceiling. The conservative fanout
	// estimate must reject before changing the line body or adding any clone.
	draft.rawBytes = iiif.MaxAnnotationPageBytes - 1
	err := splitDraftLineIntoWords(draft, annStringValue(line, "id"), []string{"replacement", "tokens"})
	if err == nil || !strings.Contains(err.Error(), "would exceed") {
		t.Fatalf("fanout error = %v", err)
	}
	if after := assertCanonicalDraft(t, draft); after != before {
		t.Fatal("fanout preflight rejection mutated or cloned the draft")
	}
}

func TestStructuralDerivationPreservesExtensionsAndClearsChangedResourceIDs(t *testing.T) {
	t.Parallel()
	line := structuralAnnotation(t, "rich-line", "line", "one two", 0, 0, 200, 40)
	line["motivation"] = []any{"supplementing", "tagging"}
	line["scribe:annotationCounter"] = json.Number("9007199254740993")
	body := line["body"].([]any)[0].(map[string]any)
	body["id"] = "https://example.org/bodies/original"
	body["language"] = "en"
	body["scribe:bodyCounter"] = json.Number("9007199254740993")
	target := line["target"].(map[string]any)
	target["id"] = "https://example.org/targets/original"
	target["scribe:targetCounter"] = json.Number("9007199254740993")
	source := target["source"].(map[string]any)
	source["service"] = []any{map[string]any{
		"id": "https://example.org/iiif/image/1", "type": "ImageService3", "profile": "level2",
	}}
	nonspatialID := "https://example.org/selectors/time"
	spatialID := "https://example.org/selectors/space"
	target["selector"] = []any{
		map[string]any{
			"id": nonspatialID, "type": "FragmentSelector",
			"conformsTo": "http://www.w3.org/TR/media-frags/", "value": "t=7,8",
		},
		map[string]any{
			"id": spatialID, "type": "FragmentSelector",
			"conformsTo":             "http://www.w3.org/TR/media-frags/",
			"value":                  "t=1,2&xywh=pixel:0,0,200,40&track=audio",
			"scribe:selectorCounter": json.Number("9007199254740993"),
		},
	}
	draft := canonicalAnnotationDraft(t, map[string]any{
		"service": []any{map[string]any{
			"id": "https://example.org/services/page-extension", "type": "ScribePageExtensionService",
			"scribe:pageCounter": json.Number("9007199254740993"),
		}},
	}, line)
	lineID := annStringValue(line, "id")

	if err := splitDraftLineIntoWords(draft, lineID, []string{"one", "two"}); err != nil {
		t.Fatalf("split rich line: %v", err)
	}
	encoded := assertCanonicalDraft(t, draft)
	if strings.Count(encoded, "9007199254740993") < 7 {
		t.Fatalf("large JSON numbers were rounded or dropped: %s", encoded)
	}
	pageServices := draft.document["service"].([]any)
	pageService := pageServices[0].(map[string]any)
	if got := pageService["scribe:pageCounter"]; !reflect.DeepEqual(got, json.Number("9007199254740993")) {
		t.Fatalf("page service extension counter = %#v (%T)", got, got)
	}

	retained := draftAnnotationByID(t, draft, lineID)
	if !reflect.DeepEqual(retained["motivation"], []any{"supplementing", "tagging"}) {
		t.Fatalf("retained motivation = %#v", retained["motivation"])
	}
	if got := retained["scribe:annotationCounter"]; !reflect.DeepEqual(got, json.Number("9007199254740993")) {
		t.Fatalf("retained annotation extension = %#v", got)
	}

	for _, word := range draftAnnotationsByGranularity(draft, "word") {
		if !reflect.DeepEqual(word["motivation"], []any{"supplementing", "tagging"}) {
			t.Errorf("derived motivation = %#v", word["motivation"])
		}
		derivedBody := word["body"].([]any)[0].(map[string]any)
		if _, exists := derivedBody["id"]; exists {
			t.Errorf("changed textual body retained its resource id: %#v", derivedBody)
		}
		if derivedBody["language"] != "en" || !reflect.DeepEqual(derivedBody["scribe:bodyCounter"], json.Number("9007199254740993")) {
			t.Errorf("body extensions were not preserved: %#v", derivedBody)
		}
		derivedTarget := word["target"].(map[string]any)
		if _, exists := derivedTarget["id"]; exists {
			t.Errorf("changed target retained its resource id: %#v", derivedTarget)
		}
		if !reflect.DeepEqual(derivedTarget["scribe:targetCounter"], json.Number("9007199254740993")) {
			t.Errorf("target extension was not preserved: %#v", derivedTarget)
		}
		derivedSource := derivedTarget["source"].(map[string]any)
		if derivedSource["id"] != testCanvas || len(derivedSource["service"].([]any)) != 1 {
			t.Errorf("Canvas source properties were not preserved: %#v", derivedSource)
		}
		selectors := derivedTarget["selector"].([]any)
		nonspatial := selectors[0].(map[string]any)
		spatial := selectors[1].(map[string]any)
		if nonspatial["id"] != nonspatialID || nonspatial["value"] != "t=7,8" {
			t.Errorf("nonspatial selector identity changed: %#v", nonspatial)
		}
		if _, exists := spatial["id"]; exists {
			t.Errorf("changed spatial selector retained id %q: %#v", spatialID, spatial)
		}
		if !strings.HasPrefix(spatial["value"].(string), "t=1,2&xywh=pixel:") || !strings.HasSuffix(spatial["value"].(string), "&track=audio") {
			t.Errorf("spatial fragment dimensions were not preserved: %#v", spatial)
		}
		if !reflect.DeepEqual(spatial["scribe:selectorCounter"], json.Number("9007199254740993")) {
			t.Errorf("selector extension was not preserved: %#v", spatial)
		}
	}
}

func TestStructuralDerivationPreservesCompactTargetDimensions(t *testing.T) {
	t.Parallel()
	line := structuralAnnotation(t, "compact-target", "line", "left right", 0, 0, 200, 20)
	line["target"] = testCanvas + "#t=10,20&xywh=pixel:0,0,200,20&track=video"
	draft := canonicalAnnotationDraft(t, nil, line)

	if err := splitDraftLineIntoWords(draft, annStringValue(line, "id"), nil); err != nil {
		t.Fatal(err)
	}
	assertCanonicalDraft(t, draft)
	words := draftAnnotationsByGranularity(draft, "word")
	for index, word := range words {
		target := word["target"].(map[string]any)
		if got := iiif.TargetCanvas(target); got != testCanvas {
			t.Errorf("word %d canvas = %q", index, got)
		}
		selector := target["selector"].(map[string]any)
		want := fmt.Sprintf("t=10,20&xywh=pixel:%d,0,100,20&track=video", index*100)
		if selector["value"] != want {
			t.Errorf("word %d selector = %q, want %q", index, selector["value"], want)
		}
	}
}

func TestSplitDraftLineIntoTwoRetainsAndRetargetsWords(t *testing.T) {
	t.Parallel()
	line := structuralAnnotation(t, "split-with-words", "line", "one two three four", 10, 20, 400, 40)
	lineID := annStringValue(line, "id")
	items := []map[string]any{line}
	texts := []string{"one", "two", "three", "four"}
	wordIDs := make([]string, 0, len(texts))
	for index, text := range texts {
		word := structuralAnnotation(t, fmt.Sprintf("split-word-%d", index), "word", text, 10+index*100, 20, 100, 40)
		word["scribe:wordState"] = "reviewed"
		wordIDs = append(wordIDs, annStringValue(word, "id"))
		items = append(items, word)
	}
	draft := canonicalAnnotationDraft(t, nil, items...)

	if err := splitDraftLineIntoTwo(draft, lineID, 2); err != nil {
		t.Fatalf("split line into two: %v", err)
	}
	assertCanonicalDraft(t, draft)
	if _, found := findDraftAnnotation(draft, lineID); found {
		t.Fatal("original line was retained after replacement split")
	}
	lines := draftAnnotationsByGranularity(draft, "line")
	if len(lines) != 2 {
		t.Fatalf("line count = %d, want 2", len(lines))
	}
	if extractAnnotationText(lines[0]) != "one two" || extractAnnotationText(lines[1]) != "three four" {
		t.Fatalf("split texts = %q / %q", extractAnnotationText(lines[0]), extractAnnotationText(lines[1]))
	}
	for index, id := range wordIDs {
		word := draftAnnotationByID(t, draft, id)
		if extractAnnotationText(word) != texts[index] || word["scribe:wordState"] != "reviewed" {
			t.Errorf("word %d changed: %#v", index, word)
		}
		_, _, _, y1, _, y2, _, err := parseLineAnnotation(mustMarshal(t, word))
		if err != nil {
			t.Fatal(err)
		}
		wantY1, wantY2 := 20, 40
		if index >= 2 {
			wantY1, wantY2 = 40, 60
		}
		if y1 != wantY1 || y2 != wantY2 {
			t.Errorf("word %d y span = (%d,%d), want (%d,%d)", index, y1, y2, wantY1, wantY2)
		}
	}
}

func TestSplitDraftLineIntoTwoRejectsShortOrUnsynchronizedLine(t *testing.T) {
	t.Parallel()
	t.Run("line height", func(t *testing.T) {
		line := structuralAnnotation(t, "short-line", "line", "one two", 0, 0, 100, 1)
		draft := canonicalAnnotationDraft(t, nil, line)
		if err := splitDraftLineIntoTwo(draft, annStringValue(line, "id"), 1); err == nil || !strings.Contains(err.Error(), "too short") {
			t.Fatalf("short-line error = %v", err)
		}
	})
	t.Run("line and words", func(t *testing.T) {
		line := structuralAnnotation(t, "unsynchronized-line", "line", "one two", 0, 0, 200, 20)
		word := structuralAnnotation(t, "unsynchronized-word", "word", "different", 0, 0, 100, 20)
		draft := canonicalAnnotationDraft(t, nil, line, word)
		if err := splitDraftLineIntoTwo(draft, annStringValue(line, "id"), 1); err == nil || !strings.Contains(err.Error(), "synchronized") {
			t.Fatalf("unsynchronized error = %v", err)
		}
	})
}

func TestJoinDraftLinesUsesReadingOrderAndRetainsWords(t *testing.T) {
	t.Parallel()
	draft, lineIDs, wordIDs := readingOrderDraft(t)
	wordSnapshots := snapshotDraftAnnotations(t, draft, wordIDs)

	if err := joinDraftLines(draft, []string{lineIDs[1], lineIDs[0]}); err != nil {
		t.Fatalf("join reversed lines: %v", err)
	}
	assertCanonicalDraft(t, draft)
	lines := draftAnnotationsByGranularity(draft, "line")
	if len(lines) != 1 || extractAnnotationText(lines[0]) != "one two three four" {
		t.Fatalf("joined lines = %#v", lines)
	}
	expectedID, err := iiif.AnnotationID(testAnnotationPage, strings.Join(lineIDs, "\x00"))
	if err != nil {
		t.Fatal(err)
	}
	if annStringValue(lines[0], "id") != expectedID {
		t.Fatalf("joined line id = %q, want deterministic reading-order id %q", annStringValue(lines[0], "id"), expectedID)
	}
	assertDraftAnnotationsUnchanged(t, draft, wordSnapshots)
}

func TestJoinDraftLinesRejectsMixedWordCoverageWithoutMutation(t *testing.T) {
	t.Parallel()
	withWords := structuralAnnotation(t, "mixed-covered-line", "line", "one two", 0, 0, 200, 20)
	lineOnly := structuralAnnotation(t, "mixed-line-only", "line", "three four", 0, 30, 200, 20)
	one := structuralAnnotation(t, "mixed-word-one", "word", "one", 0, 0, 100, 20)
	two := structuralAnnotation(t, "mixed-word-two", "word", "two", 100, 0, 100, 20)
	draft := canonicalAnnotationDraft(t, nil, withWords, one, two, lineOnly)
	before := assertCanonicalDraft(t, draft)

	err := joinDraftLines(draft, []string{annStringValue(lineOnly, "id"), annStringValue(withWords, "id")})
	if err == nil || !strings.Contains(err.Error(), "either all have word annotations or none") {
		t.Fatalf("mixed word coverage error = %v", err)
	}
	if after := assertCanonicalDraft(t, draft); after != before {
		t.Fatal("mixed word coverage rejection mutated the draft")
	}
}

func TestJoinDraftWordsUsesFinalLineOwnershipOrderAndRetainsWords(t *testing.T) {
	t.Parallel()
	draft, _, wordIDs := readingOrderDraft(t)
	wordSnapshots := snapshotDraftAnnotations(t, draft, wordIDs)
	reversed := []string{wordIDs[3], wordIDs[2], wordIDs[1], wordIDs[0]}

	if err := joinDraftWordsIntoLine(draft, reversed); err != nil {
		t.Fatalf("join reversed uneven words: %v", err)
	}
	assertCanonicalDraft(t, draft)
	lines := draftAnnotationsByGranularity(draft, "line")
	if len(lines) != 1 || extractAnnotationText(lines[0]) != "one three two four" {
		t.Fatalf("joined word line = %#v", lines)
	}
	finalOrderIDs := []string{wordIDs[0], wordIDs[2], wordIDs[1], wordIDs[3]}
	expectedID, err := iiif.AnnotationID(testAnnotationPage, strings.Join(finalOrderIDs, "\x00"))
	if err != nil {
		t.Fatal(err)
	}
	if annStringValue(lines[0], "id") != expectedID {
		t.Fatalf("joined word line id = %q, want %q", annStringValue(lines[0], "id"), expectedID)
	}
	ownedWords := locatedWords(assignSpatialWordsToLines(draft.items, nil)[annStringValue(lines[0], "id")])
	if got := normalizedAnnotationTexts(ownedWords); got != extractAnnotationText(lines[0]) {
		t.Fatalf("joined line text %q does not match word ownership %q", extractAnnotationText(lines[0]), got)
	}
	assertDraftAnnotationsUnchanged(t, draft, wordSnapshots)
}

func TestJoinDraftWordsAcceptsSelectedLooseWordAfterCompleteLine(t *testing.T) {
	t.Parallel()
	line := structuralAnnotation(t, "append-line", "line", "alpha beta gamma", 0, 0, 300, 20)
	alpha := structuralAnnotation(t, "append-word-alpha", "word", "alpha", 0, 0, 100, 20)
	beta := structuralAnnotation(t, "append-word-beta", "word", "beta", 100, 0, 100, 20)
	gamma := structuralAnnotation(t, "append-word-gamma", "word", "gamma", 200, 0, 100, 20)
	epsilon := structuralAnnotation(t, "append-word-epsilon", "word", "epsilon", 300, 0, 100, 20)
	words := []map[string]any{alpha, beta, gamma, epsilon}
	wordIDs := make([]string, 0, len(words))
	for _, word := range words {
		wordIDs = append(wordIDs, annStringValue(word, "id"))
	}
	draft := canonicalAnnotationDraft(t, nil, line, alpha, beta, gamma, epsilon)
	wordSnapshots := snapshotDraftAnnotations(t, draft, wordIDs)
	lineID := annStringValue(line, "id")

	if err := joinDraftWordsIntoLine(draft, wordIDs); err != nil {
		t.Fatalf("join complete line with appended loose word: %v", err)
	}
	assertCanonicalDraft(t, draft)
	lines := draftAnnotationsByGranularity(draft, "line")
	if len(lines) != 1 || annStringValue(lines[0], "id") != lineID || extractAnnotationText(lines[0]) != "alpha beta gamma epsilon" {
		t.Fatalf("joined appended word line = %#v", lines)
	}
	x1, y1, x2, y2, err := parseXYWH(extractFragment(lines[0]))
	if err != nil {
		t.Fatalf("parse joined line geometry: %v", err)
	}
	if x1 != 0 || y1 != 0 || x2 != 400 || y2 != 20 {
		t.Fatalf("joined line geometry = (%d,%d)-(%d,%d), want (0,0)-(400,20)", x1, y1, x2, y2)
	}
	ownedWords := locatedWords(assignSpatialWordsToLines(draft.items, nil)[lineID])
	if got := normalizedAnnotationTexts(ownedWords); got != "alpha beta gamma epsilon" {
		t.Fatalf("joined line word ownership = %q", got)
	}
	assertDraftAnnotationsUnchanged(t, draft, wordSnapshots)
}

func TestJoinDraftWordsOrdersSelectedLoosePrefixAfterLineExpansion(t *testing.T) {
	t.Parallel()
	line := structuralAnnotation(t, "prefix-line", "line", "alpha beta", 100, 0, 200, 20)
	alpha := structuralAnnotation(t, "prefix-word-alpha", "word", "alpha", 100, 0, 100, 20)
	beta := structuralAnnotation(t, "prefix-word-beta", "word", "beta", 200, 0, 100, 20)
	prefix := structuralAnnotation(t, "prefix-word-prefix", "word", "prefix", 0, 0, 100, 20)
	wordIDs := []string{
		annStringValue(alpha, "id"),
		annStringValue(beta, "id"),
		annStringValue(prefix, "id"),
	}
	draft := canonicalAnnotationDraft(t, nil, line, alpha, beta, prefix)
	wordSnapshots := snapshotDraftAnnotations(t, draft, wordIDs)
	lineID := annStringValue(line, "id")

	if err := joinDraftWordsIntoLine(draft, wordIDs); err != nil {
		t.Fatalf("join complete line with loose prefix: %v", err)
	}
	assertCanonicalDraft(t, draft)
	lines := draftAnnotationsByGranularity(draft, "line")
	if len(lines) != 1 || annStringValue(lines[0], "id") != lineID || extractAnnotationText(lines[0]) != "prefix alpha beta" {
		t.Fatalf("joined prefixed word line = %#v", lines)
	}
	ownedWords := locatedWords(assignSpatialWordsToLines(draft.items, nil)[lineID])
	if got := normalizedAnnotationTexts(ownedWords); got != extractAnnotationText(lines[0]) {
		t.Fatalf("joined line text %q does not match word ownership %q", extractAnnotationText(lines[0]), got)
	}
	assertDraftAnnotationsUnchanged(t, draft, wordSnapshots)
}

func TestJoinDraftWordsRejectsUnselectedWordCapturedByExpansion(t *testing.T) {
	t.Parallel()
	line := structuralAnnotation(t, "capture-line", "line", "alpha", 0, 0, 100, 20)
	alpha := structuralAnnotation(t, "capture-word-alpha", "word", "alpha", 0, 0, 100, 20)
	unselected := structuralAnnotation(t, "capture-word-unselected", "word", "unselected", 120, 0, 60, 20)
	suffix := structuralAnnotation(t, "capture-word-suffix", "word", "suffix", 200, 0, 100, 20)
	draft := canonicalAnnotationDraft(t, nil, line, alpha, unselected, suffix)
	before := assertCanonicalDraft(t, draft)

	err := joinDraftWordsIntoLine(draft, []string{
		annStringValue(alpha, "id"),
		annStringValue(suffix, "id"),
	})
	if err == nil || !strings.Contains(err.Error(), "exactly the selected words") {
		t.Fatalf("captured unselected word error = %v", err)
	}
	if after := assertCanonicalDraft(t, draft); after != before {
		t.Fatal("captured unselected word rejection mutated the draft")
	}
}

func TestJoinDraftLooseWordsWithIdenticalGeometryUsesCanonicalItemOrder(t *testing.T) {
	t.Parallel()
	alpha := structuralAnnotation(t, "tie-word-alpha", "word", "alpha", 0, 0, 100, 20)
	beta := structuralAnnotation(t, "tie-word-beta", "word", "beta", 0, 0, 100, 20)
	alphaID := annStringValue(alpha, "id")
	betaID := annStringValue(beta, "id")
	draft := canonicalAnnotationDraft(t, nil, alpha, beta)
	wordSnapshots := snapshotDraftAnnotations(t, draft, []string{alphaID, betaID})

	if err := joinDraftWordsIntoLine(draft, []string{betaID, alphaID}); err != nil {
		t.Fatalf("join reversed identical loose words: %v", err)
	}
	assertCanonicalDraft(t, draft)
	lines := draftAnnotationsByGranularity(draft, "line")
	expectedID, err := iiif.AnnotationID(testAnnotationPage, alphaID+"\x00"+betaID)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || annStringValue(lines[0], "id") != expectedID || extractAnnotationText(lines[0]) != "alpha beta" {
		t.Fatalf("joined identical loose words = %#v, want id %q and canonical text", lines, expectedID)
	}
	assertDraftAnnotationsUnchanged(t, draft, wordSnapshots)
}

func TestJoinDraftWordsRejectsNonTokenTextWithoutMutation(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		text string
	}{
		{name: "blank", text: ""},
		{name: "multiple tokens", text: "two tokens"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			alpha := structuralAnnotation(t, "token-word-alpha-"+test.name, "word", "alpha", 0, 0, 100, 20)
			invalid := structuralAnnotation(t, "token-word-invalid-"+test.name, "word", test.text, 100, 0, 100, 20)
			draft := canonicalAnnotationDraft(t, nil, alpha, invalid)
			before := assertCanonicalDraft(t, draft)

			err := joinDraftWordsIntoLine(draft, []string{
				annStringValue(alpha, "id"),
				annStringValue(invalid, "id"),
			})
			if err == nil || !strings.Contains(err.Error(), "exactly one text token") {
				t.Fatalf("non-token word error = %v", err)
			}
			if after := assertCanonicalDraft(t, draft); after != before {
				t.Fatal("non-token word rejection mutated the draft")
			}
		})
	}
}

func TestJoinDraftWordsRejectsIncompleteAffectedLineWithLooseWord(t *testing.T) {
	t.Parallel()
	line := structuralAnnotation(t, "partial-line", "line", "alpha beta", 0, 0, 200, 20)
	alpha := structuralAnnotation(t, "partial-word-alpha", "word", "alpha", 0, 0, 100, 20)
	beta := structuralAnnotation(t, "partial-word-beta", "word", "beta", 100, 0, 100, 20)
	epsilon := structuralAnnotation(t, "partial-word-epsilon", "word", "epsilon", 200, 0, 100, 20)
	draft := canonicalAnnotationDraft(t, nil, line, alpha, beta, epsilon)
	before := assertCanonicalDraft(t, draft)

	err := joinDraftWordsIntoLine(draft, []string{
		annStringValue(alpha, "id"),
		annStringValue(epsilon, "id"),
	})
	if err == nil || !strings.Contains(err.Error(), "all words in an affected line must be selected") {
		t.Fatalf("incomplete affected line error = %v", err)
	}
	if after := assertCanonicalDraft(t, draft); after != before {
		t.Fatal("incomplete affected line rejection mutated the draft")
	}
}

func TestJoinDraftLinesRejectsConflictingNonspatialFragments(t *testing.T) {
	t.Parallel()
	first := structuralAnnotation(t, "fragment-a", "line", "Hello", 0, 0, 100, 20)
	second := structuralAnnotation(t, "fragment-b", "line", "World", 100, 0, 100, 20)
	first["target"].(map[string]any)["selector"].(map[string]any)["value"] = "t=1,2&xywh=pixel:0,0,100,20&track=audio"
	second["target"].(map[string]any)["selector"].(map[string]any)["value"] = "t=9,10&xywh=pixel:100,0,100,20&track=audio"
	draft := canonicalAnnotationDraft(t, nil, first, second)
	before := assertCanonicalDraft(t, draft)

	err := joinDraftLines(draft, []string{annStringValue(second, "id"), annStringValue(first, "id")})
	if err == nil || !strings.Contains(err.Error(), "conflicting IIIF properties") {
		t.Fatalf("nonspatial conflict error = %v", err)
	}
	if after := assertCanonicalDraft(t, draft); after != before {
		t.Fatal("conflicting join mutated the draft")
	}
}

func TestCompletePageTransformsRejectInvalidSelections(t *testing.T) {
	t.Parallel()
	line := structuralAnnotation(t, "selection", "line", "one two", 0, 0, 100, 20)
	draft := canonicalAnnotationDraft(t, nil, line)
	lineID := annStringValue(line, "id")
	if err := joinDraftLines(draft, []string{lineID}); err == nil {
		t.Fatal("join accepted one annotation")
	}
	if err := joinDraftLines(draft, []string{lineID, lineID}); err == nil || !strings.Contains(err.Error(), "unique") {
		t.Fatalf("duplicate selection error = %v", err)
	}
	if err := splitDraftLineIntoWords(draft, testAnnotationID("missing"), nil); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing selection error = %v", err)
	}
}

func TestRenderAnnotationExportUsesCompleteCanonicalPage(t *testing.T) {
	pageJSON := `{
		"type": "AnnotationPage",
		"items": [{
			"id": "line-1",
			"type": "Annotation",
			"textGranularity": "line",
			"motivation": "supplementing",
			"body": [{"type":"TextualBody","purpose":"supplementing","value":"Hello World"}],
			"target": {
				"source": {"id":"https://example.org/canvas/1","type":"Canvas"},
				"selector": {"type":"FragmentSelector","value":"xywh=10,20,200,30"}
			}
		}]
	}`

	for _, test := range []struct {
		format    string
		mediaType string
	}{
		{format: "txt", mediaType: "text/plain; charset=utf-8"},
		{format: "hocr", mediaType: "text/vnd.hocr+html; charset=utf-8"},
		{format: "pagexml", mediaType: "application/vnd.prima.page+xml; charset=utf-8"},
		{format: "alto", mediaType: "application/alto+xml; charset=utf-8"},
	} {
		t.Run(test.format, func(t *testing.T) {
			content, mediaType, extension, err := renderAnnotationExport(pageJSON, 400, 300, test.format)
			if err != nil {
				t.Fatalf("render canonical export: %v", err)
			}
			if mediaType != test.mediaType || extension == "" {
				t.Fatalf("metadata = %q/%q, want %q/non-empty", mediaType, extension, test.mediaType)
			}
			if !strings.Contains(content, "Hello") || !strings.Contains(content, "World") {
				t.Fatalf("export missing canonical text: %s", content)
			}
		})
	}
}

// mustMarshal marshals v to JSON or fails the test.
func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func structuralAnnotation(t *testing.T, seed, granularity, text string, x, y, w, h int) map[string]any {
	t.Helper()
	var annotation map[string]any
	if err := iiif.DecodeJSON([]byte(lineAnno(seed, testCanvas, text, x, y, w, h)), &annotation); err != nil {
		t.Fatalf("decode annotation fixture: %v", err)
	}
	annotation["textGranularity"] = granularity
	return annotation
}

func canonicalAnnotationDraft(t *testing.T, pageProperties map[string]any, annotations ...map[string]any) *annotationDraft {
	t.Helper()
	items := make([]any, 0, len(annotations))
	for _, annotation := range annotations {
		items = append(items, annotation)
	}
	document := map[string]any{
		"@context": []any{iiif.TextGranularityContext, iiif.PresentationContext},
		"id":       testAnnotationPage,
		"type":     "AnnotationPage",
		"items":    items,
	}
	for key, value := range pageProperties {
		document[key] = value
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("encode canonical draft fixture: %v", err)
	}
	// ValidateCanonicalAnnotationPage invokes libops/iiif-spec before applying
	// the stricter Scribe ownership and Text Granularity invariants.
	if err := iiif.ValidateCanonicalAnnotationPage(raw, testPageIdentity); err != nil {
		t.Fatalf("fixture is not a valid canonical IIIF page: %v\n%s", err, raw)
	}
	draft, err := decodeAnnotationDraft(string(raw), testPageIdentity)
	if err != nil {
		t.Fatalf("decode canonical annotation draft: %v", err)
	}
	return draft
}

func assertCanonicalDraft(t *testing.T, draft *annotationDraft) string {
	t.Helper()
	encoded, err := draft.encode()
	if err != nil {
		t.Fatalf("encode transformed draft: %v", err)
	}
	if err := iiif.ValidateCanonicalAnnotationPage([]byte(encoded), testPageIdentity); err != nil {
		t.Fatalf("transformed draft failed libops/canonical validation: %v\n%s", err, encoded)
	}
	return encoded
}

func findDraftAnnotation(draft *annotationDraft, id string) (map[string]any, bool) {
	for _, raw := range draft.items {
		annotation, ok := raw.(map[string]any)
		if ok && annStringValue(annotation, "id") == id {
			return annotation, true
		}
	}
	return nil, false
}

func draftAnnotationByID(t *testing.T, draft *annotationDraft, id string) map[string]any {
	t.Helper()
	annotation, found := findDraftAnnotation(draft, id)
	if !found {
		t.Fatalf("annotation %q not found", id)
	}
	return annotation
}

func draftAnnotationsByGranularity(draft *annotationDraft, granularity string) []map[string]any {
	annotations := make([]map[string]any, 0)
	for _, raw := range draft.items {
		annotation, ok := raw.(map[string]any)
		if ok && strings.EqualFold(annStringValue(annotation, "textGranularity"), granularity) {
			annotations = append(annotations, annotation)
		}
	}
	return annotations
}

func readingOrderDraft(t *testing.T) (*annotationDraft, []string, []string) {
	t.Helper()
	topLine := structuralAnnotation(t, "reading-line-top", "line", "one two", 0, 0, 140, 40)
	bottomLine := structuralAnnotation(t, "reading-line-bottom", "line", "three four", 0, 50, 140, 60)
	one := structuralAnnotation(t, "reading-word-one", "word", "one", 0, 18, 50, 20)
	two := structuralAnnotation(t, "reading-word-two", "word", "two", 70, 1, 50, 10)
	three := structuralAnnotation(t, "reading-word-three", "word", "three", 0, 90, 50, 10)
	four := structuralAnnotation(t, "reading-word-four", "word", "four", 70, 51, 50, 20)
	// Deliberately put each row's words in reverse item order. Their vertical
	// centers and heights also disagree, so only line ownership plus x order
	// yields the stable OCR reading order.
	draft := canonicalAnnotationDraft(t, nil, topLine, two, one, bottomLine, four, three)
	lineIDs := []string{annStringValue(topLine, "id"), annStringValue(bottomLine, "id")}
	wordIDs := []string{annStringValue(one, "id"), annStringValue(two, "id"), annStringValue(three, "id"), annStringValue(four, "id")}
	return draft, lineIDs, wordIDs
}

func snapshotDraftAnnotations(t *testing.T, draft *annotationDraft, ids []string) map[string]map[string]any {
	t.Helper()
	snapshots := make(map[string]map[string]any, len(ids))
	for _, id := range ids {
		annotation := draftAnnotationByID(t, draft, id)
		clone, err := cloneAnnotation(annotation)
		if err != nil {
			t.Fatalf("snapshot annotation %q: %v", id, err)
		}
		snapshots[id] = clone
	}
	return snapshots
}

func assertDraftAnnotationsUnchanged(t *testing.T, draft *annotationDraft, snapshots map[string]map[string]any) {
	t.Helper()
	for id, before := range snapshots {
		after := draftAnnotationByID(t, draft, id)
		if !reflect.DeepEqual(after, before) {
			t.Errorf("retained annotation %q changed\nbefore: %#v\nafter:  %#v", id, before, after)
		}
	}
}
