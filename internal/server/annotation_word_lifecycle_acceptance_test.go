package server

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/models"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
	"github.com/lehigh-university-libraries/scribe/proto/scribe/v1/scribev1connect"
)

// TestWordStructuralLifecyclePersistsAcrossCanonicalPublicAndExports proves
// that structural transforms, local word CRUD, persistence, publication, and
// every export consume one canonical revision. Every intermediate transform is
// saved and reloaded so a transient split->join shortcut cannot hide word loss.
func TestWordStructuralLifecyclePersistsAcrossCanonicalPublicAndExports(t *testing.T) {
	database := openTestDB(t)
	ctx := context.Background()
	_, triplet := newTripletTestStore(t)
	configureTripletTest(t, triplet.URL, "test-triplet-presentation-write-token-32-bytes-minimum")
	workspaceID, userID := createServerTestWorkspace(t, database)
	otherWorkspaceID, otherUserID := createServerTestWorkspace(t, database)
	canvasURI := "https://source.example/canvas/word-lifecycle?view=primary"
	image := createServerTestItemImage(t, database, workspaceID, userID, canvasURI)
	items := store.NewItemStore(database)
	annotations := store.NewAnnotationStore(database)
	handler := NewHandler(nil, items, nil, annotations, nil, nil, nil, nil)

	identity := iiif.PageIdentity{PublicBaseURL: handler.publicAnnotationBaseURL(), ItemImageID: image.ID, CanvasURI: canvasURI}
	pageID, err := iiif.CanonicalPageID(identity.PublicBaseURL, image.ID)
	if err != nil {
		t.Fatalf("canonical page ID: %v", err)
	}
	lineID, err := iiif.AnnotationID(pageID, "word-lifecycle-line")
	if err != nil {
		t.Fatalf("canonical line ID: %v", err)
	}
	line := transcriptionAnnotation(lineID, "line", "alpha beta gamma delta", canvasURI, models.BBox{X1: 0, Y1: 0, X2: 500, Y2: 40})
	wordLifecycleAddExtensions(line)
	seedPayload, err := iiif.NewAnnotationPage(identity, []any{line})
	if err != nil {
		t.Fatalf("build seed page: %v", err)
	}
	seedDocument := wordLifecycleDocument(t, string(seedPayload))
	seedDocument["service"] = []any{map[string]any{
		"id":             "https://scribe.example/services/page-state",
		"type":           "ScribePageStateService",
		"profile":        "https://scribe.example/profiles/page-state",
		"scribe:counter": json.Number("9007199254740993"),
	}}
	seedPayload = []byte(wordLifecycleJSON(t, seedDocument))
	if err := iiif.ValidateCanonicalAnnotationPage(seedPayload, identity); err != nil {
		t.Fatalf("validate extended seed page: %v", err)
	}
	seeded, err := annotations.SavePage(ctx, store.AnnotationPage{
		WorkspaceID: workspaceID, ItemImageID: image.ID, PageID: pageID, CanvasURI: canvasURI, Payload: string(seedPayload),
	}, 0)
	if err != nil {
		t.Fatalf("save seed page: %v", err)
	}

	appServer := newTenantScopedServer(t, handler, map[string]testTenantIdentity{
		"owner": {workspaceID: workspaceID, userID: userID},
		"other": {workspaceID: otherWorkspaceID, userID: otherUserID},
	})
	client := scribev1connect.NewAnnotationServiceClient(http.DefaultClient, appServer.URL)

	// Ownership is keyed by the requested item image, never by an imported
	// Canvas URI that another tenant can share.
	_, err = client.SplitLineIntoWords(ctx, tenantConnectRequest("other", &scribev1.SplitLineIntoWordsRequest{
		ItemImageId: image.ID, AnnotationPageJson: seeded.Payload, SelectedAnnotationId: lineID,
	}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("cross-workspace structural transform error = %v, want not_found", err)
	}

	// Exploding a line retains the line and adds the finest-granularity words.
	splitWords, err := client.SplitLineIntoWords(ctx, tenantConnectRequest("owner", &scribev1.SplitLineIntoWordsRequest{
		ItemImageId: image.ID, AnnotationPageJson: seeded.Payload, SelectedAnnotationId: lineID,
	}))
	if err != nil {
		t.Fatalf("SplitLineIntoWords: %v", err)
	}
	current := wordLifecycleSaveAndReload(t, ctx, client, image.ID, splitWords.Msg.GetAnnotationPageJson(), seeded.Revision)
	if got := wordLifecycleAnnotations(t, current.Msg.GetAnnotationPageJson(), "line"); len(got) != 1 {
		t.Fatalf("line count after word split = %d, want 1", len(got))
	}
	initialWords := wordLifecycleWordSnapshot(t, current.Msg.GetAnnotationPageJson())
	wordLifecycleAssertTexts(t, initialWords, []string{"alpha", "beta", "gamma", "delta"})
	wordLifecycleAssertExtensions(t, current.Msg.GetAnnotationPageJson())

	// Word CRUD is a local full-page edit committed through the same revision
	// CAS; there is no second per-annotation persistence path.
	draft := wordLifecycleDocument(t, current.Msg.GetAnnotationPageJson())
	draftItems := draft["items"].([]any)
	currentLine := wordLifecycleAnnotationByGranularity(t, current.Msg.GetAnnotationPageJson(), "line")
	setAnnotationText(currentLine, "alpha BETA delta epsilon")
	beta := wordLifecycleAnnotationByText(t, current.Msg.GetAnnotationPageJson(), "word", "beta")
	setAnnotationText(beta, "BETA")
	gammaID := annStringValue(wordLifecycleAnnotationByText(t, current.Msg.GetAnnotationPageJson(), "word", "gamma"), "id")
	extraWordID, err := iiif.AnnotationID(pageID, "word-lifecycle-extra")
	if err != nil {
		t.Fatal(err)
	}
	extraWord, err := deriveTextAnnotation(currentLine, extraWordID, "word", canvasURI, 400, 0, 500, 40, "epsilon")
	if err != nil {
		t.Fatalf("derive local word: %v", err)
	}
	updatedItems := make([]any, 0, len(draftItems))
	for _, value := range draftItems {
		annotation, _ := value.(map[string]any)
		if annStringValue(annotation, "id") == gammaID {
			continue
		}
		switch annStringValue(annotation, "id") {
		case lineID:
			updatedItems = append(updatedItems, currentLine)
		case annStringValue(beta, "id"):
			updatedItems = append(updatedItems, beta)
		default:
			updatedItems = append(updatedItems, value)
		}
	}
	draft["items"] = append(updatedItems, extraWord)
	current = wordLifecycleSaveAndReload(t, ctx, client, image.ID, wordLifecycleJSON(t, draft), current.Msg.GetRevision())
	crudWords := wordLifecycleWordSnapshot(t, current.Msg.GetAnnotationPageJson())
	wordLifecycleAssertTexts(t, crudWords, []string{"alpha", "BETA", "delta", "epsilon"})
	wordLifecycleAssertExtensions(t, current.Msg.GetAnnotationPageJson())

	// Persist and reload the split state before joining. This is the regression
	// boundary for retained words at the two new lines' shared Y coordinate.
	currentLine = wordLifecycleAnnotationByGranularity(t, current.Msg.GetAnnotationPageJson(), "line")
	splitLines, err := client.SplitLineIntoTwoLines(ctx, tenantConnectRequest("owner", &scribev1.SplitLineIntoTwoLinesRequest{
		ItemImageId: image.ID, AnnotationPageJson: current.Msg.GetAnnotationPageJson(), SelectedAnnotationId: annStringValue(currentLine, "id"), SplitAtWord: 2,
	}))
	if err != nil {
		t.Fatalf("SplitLineIntoTwoLines: %v", err)
	}
	current = wordLifecycleSaveAndReload(t, ctx, client, image.ID, splitLines.Msg.GetAnnotationPageJson(), current.Msg.GetRevision())
	lines := wordLifecycleAnnotations(t, current.Msg.GetAnnotationPageJson(), "line")
	if len(lines) != 2 {
		t.Fatalf("persisted split line count = %d, want 2", len(lines))
	}
	splitSnapshot := wordLifecycleWordSnapshot(t, current.Msg.GetAnnotationPageJson())
	wordLifecycleAssertStableWords(t, crudWords, splitSnapshot)
	expectedBoxes := map[string]models.BBox{
		"alpha":   {X1: 0, Y1: 0, X2: 125, Y2: 20},
		"BETA":    {X1: 125, Y1: 0, X2: 250, Y2: 20},
		"delta":   {X1: 375, Y1: 20, X2: 500, Y2: 40},
		"epsilon": {X1: 400, Y1: 20, X2: 500, Y2: 40},
	}
	wordLifecycleAssertBoxes(t, splitSnapshot, expectedBoxes)
	wordLifecycleAssertExtensions(t, current.Msg.GetAnnotationPageJson())

	// Reverse caller order to prove text/ID derivation follows geometry rather
	// than selection order.
	joinedLines, err := client.JoinLines(ctx, tenantConnectRequest("owner", &scribev1.JoinLinesRequest{
		ItemImageId: image.ID, AnnotationPageJson: current.Msg.GetAnnotationPageJson(),
		SelectedAnnotationIds: []string{annStringValue(lines[1], "id"), annStringValue(lines[0], "id")},
	}))
	if err != nil {
		t.Fatalf("JoinLines: %v", err)
	}
	current = wordLifecycleSaveAndReload(t, ctx, client, image.ID, joinedLines.Msg.GetAnnotationPageJson(), current.Msg.GetRevision())
	if got := extractAnnotationText(wordLifecycleAnnotationByGranularity(t, current.Msg.GetAnnotationPageJson(), "line")); got != "alpha BETA delta epsilon" {
		t.Fatalf("joined line text = %q", got)
	}
	wordLifecycleAssertIdenticalWords(t, splitSnapshot, wordLifecycleWordSnapshot(t, current.Msg.GetAnnotationPageJson()))

	wordIDs := wordLifecycleIDs(t, current.Msg.GetAnnotationPageJson(), "word")
	sort.Sort(sort.Reverse(sort.StringSlice(wordIDs)))
	joinedWords, err := client.JoinWordsIntoLine(ctx, tenantConnectRequest("owner", &scribev1.JoinWordsIntoLineRequest{
		ItemImageId: image.ID, AnnotationPageJson: current.Msg.GetAnnotationPageJson(), SelectedAnnotationIds: wordIDs,
	}))
	if err != nil {
		t.Fatalf("JoinWordsIntoLine: %v", err)
	}
	current = wordLifecycleSaveAndReload(t, ctx, client, image.ID, joinedWords.Msg.GetAnnotationPageJson(), current.Msg.GetRevision())
	finalWords := wordLifecycleWordSnapshot(t, current.Msg.GetAnnotationPageJson())
	wordLifecycleAssertIdenticalWords(t, splitSnapshot, finalWords)
	wordLifecycleAssertPageCounter(t, current.Msg.GetAnnotationPageJson())
	finalPage := current.Msg.GetAnnotationPageJson()

	published, err := client.PublishItemImageEdits(ctx, tenantConnectRequest("owner", &scribev1.PublishItemImageEditsRequest{
		ItemImageId: image.ID, ExpectedRevision: current.Msg.GetRevision(),
	}))
	if err != nil {
		t.Fatalf("PublishItemImageEdits: %v", err)
	}
	wordLifecycleAssertIdenticalWords(t, finalWords, wordLifecycleWordSnapshot(t, published.Msg.GetAnnotationPageJson()))
	wordLifecycleAssertSemanticPageEquality(t, finalPage, published.Msg.GetAnnotationPageJson())

	// Publishing atomically enqueues the immutable snapshot. Exercise the same
	// durable delivery worker and HTTP boundary used in production, then
	// dereference the exact public URL returned by the Connect operation.
	handler.dispatchAnnotationMirrors(ctx)
	publicRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, published.Msg.GetPublicUrl(), nil)
	if err != nil {
		t.Fatalf("create public AnnotationPage request: %v", err)
	}
	publicResponse, err := http.DefaultClient.Do(publicRequest)
	if err != nil {
		t.Fatalf("dereference public AnnotationPage: %v", err)
	}
	defer publicResponse.Body.Close()
	publicPayload, err := io.ReadAll(io.LimitReader(publicResponse.Body, tripletMaxResourceBytes+1))
	if err != nil {
		t.Fatalf("read public AnnotationPage: %v", err)
	}
	if publicResponse.StatusCode != http.StatusOK {
		t.Fatalf("dereference public AnnotationPage status/body = %d/%q", publicResponse.StatusCode, publicPayload)
	}
	if len(publicPayload) > tripletMaxResourceBytes {
		t.Fatalf("public AnnotationPage exceeds %d bytes", tripletMaxResourceBytes)
	}
	publicPage := string(publicPayload)
	wordLifecycleAssertIdenticalWords(t, finalWords, wordLifecycleWordSnapshot(t, publicPage))
	wordLifecycleAssertSemanticPageEquality(t, finalPage, publicPage)
	wordLifecycleAssertPageCounter(t, publicPage)

	for _, format := range []string{"txt", "hocr", "pagexml", "alto"} {
		status, exported := tenantAnnotationExport(t, client, "owner", image.ID, current.Msg.GetRevision(), format)
		if status != http.StatusOK {
			t.Fatalf("%s export status/body = %d/%q", format, status, exported)
		}
		switch format {
		case "txt":
			if strings.TrimSpace(exported) != "alpha BETA delta epsilon" {
				t.Fatalf("plain-text export = %q", exported)
			}
		case "hocr":
			wordLifecycleAssertExportWords(t, wordLifecycleParseHOCR(t, exported), expectedBoxes)
		case "pagexml":
			wordLifecycleAssertExportWords(t, wordLifecycleParsePAGE(t, exported), expectedBoxes)
		case "alto":
			wordLifecycleAssertExportWords(t, wordLifecycleParseALTO(t, exported), expectedBoxes)
		}
	}
}

func wordLifecycleSaveAndReload(t *testing.T, ctx context.Context, client scribev1connect.AnnotationServiceClient, itemImageID uint64, page string, revision uint64) *connect.Response[scribev1.GetAnnotationPageResponse] {
	t.Helper()
	saved, err := client.SaveAnnotationPage(ctx, tenantConnectRequest("owner", &scribev1.SaveAnnotationPageRequest{
		ItemImageId: itemImageID, AnnotationPageJson: page, ExpectedRevision: revision,
	}))
	if err != nil {
		t.Fatalf("SaveAnnotationPage(revision=%d): %v", revision, err)
	}
	reloaded, err := client.GetAnnotationPage(ctx, tenantConnectRequest("owner", &scribev1.GetAnnotationPageRequest{ItemImageId: itemImageID}))
	if err != nil {
		t.Fatalf("GetAnnotationPage: %v", err)
	}
	if reloaded.Msg.GetRevision() != saved.Msg.GetRevision() || reloaded.Msg.GetAnnotationPageJson() != saved.Msg.GetAnnotationPageJson() {
		t.Fatalf("save/reload drift at revision %d", saved.Msg.GetRevision())
	}
	return reloaded
}

func wordLifecycleAddExtensions(annotation map[string]any) {
	annotation["motivation"] = []any{"tagging", "supplementing"}
	body := annotation["body"].([]any)[0].(map[string]any)
	body["id"] = "https://scribe.example/bodies/source-line"
	body["language"] = "en"
	body["service"] = []any{map[string]any{
		"id": "https://scribe.example/services/body-state", "type": "ScribeBodyStateService",
		"profile": "https://scribe.example/profiles/body-state", "scribe:counter": json.Number("9007199254740995"),
	}}
	target := annotation["target"].(map[string]any)
	target["id"] = "https://scribe.example/targets/source-line"
	target["renderedVia"] = map[string]any{"id": "https://scribe.example/agents/editor", "type": "Software", "name": "Scribe"}
	spatial := target["selector"].(map[string]any)
	spatial["id"] = "https://scribe.example/selectors/spatial"
	spatial["value"] = "t=0,1&xywh=pixel:0,0,500,40&track=ocr"
	target["selector"] = []any{
		map[string]any{
			"id": "https://scribe.example/selectors/nonspatial", "type": "FragmentSelector",
			"conformsTo": "http://www.w3.org/TR/media-frags/", "value": "t=0,1&track=ocr",
		},
		spatial,
	}
}

func wordLifecycleDocument(t *testing.T, raw string) map[string]any {
	t.Helper()
	var document map[string]any
	if err := iiif.DecodeJSON([]byte(raw), &document); err != nil {
		t.Fatalf("decode AnnotationPage: %v", err)
	}
	return document
}

func wordLifecycleItems(t *testing.T, raw string) []map[string]any {
	t.Helper()
	document := wordLifecycleDocument(t, raw)
	values, ok := document["items"].([]any)
	if !ok {
		t.Fatalf("AnnotationPage items = %T", document["items"])
	}
	items := make([]map[string]any, 0, len(values))
	for index, value := range values {
		annotation, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("item %d = %T", index, value)
		}
		items = append(items, annotation)
	}
	return items
}

func wordLifecycleAnnotations(t *testing.T, raw, granularity string) []map[string]any {
	t.Helper()
	result := make([]map[string]any, 0)
	for _, annotation := range wordLifecycleItems(t, raw) {
		if strings.EqualFold(strings.TrimSpace(annStringValue(annotation, "textGranularity")), granularity) {
			result = append(result, annotation)
		}
	}
	return result
}

func wordLifecycleAnnotationByText(t *testing.T, raw, granularity, text string) map[string]any {
	t.Helper()
	for _, annotation := range wordLifecycleAnnotations(t, raw, granularity) {
		if extractAnnotationText(annotation) == text {
			return annotation
		}
	}
	t.Fatalf("%s annotation with text %q was not found", granularity, text)
	return nil
}

func wordLifecycleAnnotationByGranularity(t *testing.T, raw, granularity string) map[string]any {
	t.Helper()
	annotations := wordLifecycleAnnotations(t, raw, granularity)
	if len(annotations) == 0 {
		t.Fatalf("%s annotation was not found", granularity)
	}
	return annotations[0]
}

func wordLifecycleIDs(t *testing.T, raw, granularity string) []string {
	t.Helper()
	result := make([]string, 0)
	for _, annotation := range wordLifecycleAnnotations(t, raw, granularity) {
		result = append(result, annStringValue(annotation, "id"))
	}
	return result
}

func wordLifecycleJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode JSON: %v", err)
	}
	return string(raw)
}

type wordLifecycleWord struct {
	Text               string
	Box                models.BBox
	BodyCounter        string
	NonSpatialSelector string
	SpatialFragment    string
}

func wordLifecycleWordSnapshot(t *testing.T, raw string) map[string]wordLifecycleWord {
	t.Helper()
	result := make(map[string]wordLifecycleWord)
	for _, annotation := range wordLifecycleAnnotations(t, raw, "word") {
		id := strings.TrimSpace(annStringValue(annotation, "id"))
		if id == "" {
			t.Fatal("word annotation has no ID")
		}
		x1, y1, x2, y2, err := parseXYWH(extractFragment(annotation))
		if err != nil {
			t.Fatalf("word %s geometry: %v", id, err)
		}
		body := annotation["body"].([]any)[0].(map[string]any)
		services := body["service"].([]any)
		counter := fmt.Sprint(services[0].(map[string]any)["scribe:counter"])
		target := annotation["target"].(map[string]any)
		selectors := target["selector"].([]any)
		nonSpatial := selectors[0].(map[string]any)
		spatial := selectors[1].(map[string]any)
		if nonSpatial["id"] != "https://scribe.example/selectors/nonspatial" {
			t.Fatalf("word %s lost untouched selector identity: %#v", id, nonSpatial)
		}
		if spatial["id"] != nil || spatial["@id"] != nil || target["id"] != nil || body["id"] != nil {
			t.Fatalf("word %s retained a changed embedded-resource identity", id)
		}
		result[id] = wordLifecycleWord{
			Text: extractAnnotationText(annotation), Box: models.BBox{X1: x1, Y1: y1, X2: x2, Y2: y2}, BodyCounter: counter,
			NonSpatialSelector: annStringValue(nonSpatial, "value"), SpatialFragment: annStringValue(spatial, "value"),
		}
	}
	return result
}

func wordLifecycleAssertStableWords(t *testing.T, before, after map[string]wordLifecycleWord) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatalf("word count changed: %d != %d", len(before), len(after))
	}
	for id, want := range before {
		got, ok := after[id]
		if !ok {
			t.Fatalf("word ID %q was lost", id)
		}
		if got.Text != want.Text || got.BodyCounter != want.BodyCounter || got.NonSpatialSelector != want.NonSpatialSelector {
			t.Fatalf("word %q changed non-geometric state: got %#v want %#v", id, got, want)
		}
		wantWithoutXYWH, _ := iiif.RemoveMediaFragmentPixelXYWH(want.SpatialFragment)
		gotWithoutXYWH, _ := iiif.RemoveMediaFragmentPixelXYWH(got.SpatialFragment)
		if gotWithoutXYWH != wantWithoutXYWH {
			t.Fatalf("word %q changed non-spatial fragment: %q != %q", id, gotWithoutXYWH, wantWithoutXYWH)
		}
	}
}

func wordLifecycleAssertIdenticalWords(t *testing.T, before, after map[string]wordLifecycleWord) {
	t.Helper()
	wordLifecycleAssertStableWords(t, before, after)
	for id, want := range before {
		if got := after[id]; got != want {
			t.Fatalf("word %q changed: got %#v want %#v", id, got, want)
		}
	}
}

func wordLifecycleAssertSemanticPageEquality(t *testing.T, before, after string) {
	t.Helper()
	beforeDocument := wordLifecycleDocument(t, before)
	afterDocument := wordLifecycleDocument(t, after)
	if !reflect.DeepEqual(beforeDocument, afterDocument) {
		t.Fatalf("canonical AnnotationPage changed across publication or dereference")
	}
}

func wordLifecycleAssertTexts(t *testing.T, snapshot map[string]wordLifecycleWord, want []string) {
	t.Helper()
	got := make([]string, 0, len(snapshot))
	for _, word := range snapshot {
		got = append(got, word.Text)
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("word texts = %v, want %v", got, want)
	}
}

func wordLifecycleAssertBoxes(t *testing.T, snapshot map[string]wordLifecycleWord, want map[string]models.BBox) {
	t.Helper()
	for _, word := range snapshot {
		if expected, ok := want[word.Text]; !ok || word.Box != expected {
			t.Fatalf("word %q bbox = %+v, want %+v", word.Text, word.Box, expected)
		}
	}
}

func wordLifecycleAssertExtensions(t *testing.T, raw string) {
	t.Helper()
	wordLifecycleAssertPageCounter(t, raw)
	for id, word := range wordLifecycleWordSnapshot(t, raw) {
		if word.BodyCounter != "9007199254740995" || word.NonSpatialSelector != "t=0,1&track=ocr" {
			t.Fatalf("word %s extension state = %#v", id, word)
		}
		withoutXYWH, err := iiif.RemoveMediaFragmentPixelXYWH(word.SpatialFragment)
		if err != nil || withoutXYWH != "t=0,1&track=ocr" || !strings.Contains(word.SpatialFragment, "xywh=pixel:") {
			t.Fatalf("word %s spatial selector = %q (%v)", id, word.SpatialFragment, err)
		}
	}
}

func wordLifecycleAssertPageCounter(t *testing.T, raw string) {
	t.Helper()
	document := wordLifecycleDocument(t, raw)
	services := document["service"].([]any)
	value := services[0].(map[string]any)["scribe:counter"]
	if number, ok := value.(json.Number); !ok || number.String() != "9007199254740993" {
		t.Fatalf("page extension counter = %#v (%T)", value, value)
	}
}

type wordLifecycleExportWord struct {
	Text string
	Box  models.BBox
}

func wordLifecycleParseHOCR(t *testing.T, raw string) []wordLifecycleExportWord {
	t.Helper()
	decoder := xml.NewDecoder(strings.NewReader(raw))
	result := make([]wordLifecycleExportWord, 0)
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("parse hOCR: %v", err)
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "span" || xmlAttribute(start, "class") != "ocrx_word" {
			continue
		}
		var text string
		if err := decoder.DecodeElement(&text, &start); err != nil {
			t.Fatal(err)
		}
		parts := strings.Fields(strings.TrimPrefix(strings.Split(xmlAttribute(start, "title"), ";")[0], "bbox "))
		result = append(result, wordLifecycleExportWord{Text: strings.TrimSpace(text), Box: wordLifecycleBoxFromParts(t, parts)})
	}
	return result
}

func wordLifecycleParsePAGE(t *testing.T, raw string) []wordLifecycleExportWord {
	t.Helper()
	type pageWord struct {
		Coords struct {
			Points string `xml:"points,attr"`
		} `xml:"Coords"`
		Text string `xml:"TextEquiv>Unicode"`
	}
	decoder := xml.NewDecoder(strings.NewReader(raw))
	result := make([]wordLifecycleExportWord, 0)
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("parse PAGE XML: %v", err)
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "Word" {
			continue
		}
		var word pageWord
		if err := decoder.DecodeElement(&word, &start); err != nil {
			t.Fatal(err)
		}
		points := strings.Fields(word.Coords.Points)
		if len(points) != 4 {
			t.Fatalf("PAGE points = %q", word.Coords.Points)
		}
		first := strings.Split(points[0], ",")
		third := strings.Split(points[2], ",")
		result = append(result, wordLifecycleExportWord{Text: strings.TrimSpace(word.Text), Box: wordLifecycleBoxFromParts(t, []string{first[0], first[1], third[0], third[1]})})
	}
	return result
}

func wordLifecycleParseALTO(t *testing.T, raw string) []wordLifecycleExportWord {
	t.Helper()
	decoder := xml.NewDecoder(strings.NewReader(raw))
	result := make([]wordLifecycleExportWord, 0)
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("parse ALTO XML: %v", err)
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "String" {
			continue
		}
		x := wordLifecycleInt(t, xmlAttribute(start, "HPOS"))
		y := wordLifecycleInt(t, xmlAttribute(start, "VPOS"))
		width := wordLifecycleInt(t, xmlAttribute(start, "WIDTH"))
		height := wordLifecycleInt(t, xmlAttribute(start, "HEIGHT"))
		result = append(result, wordLifecycleExportWord{Text: xmlAttribute(start, "CONTENT"), Box: models.BBox{X1: x, Y1: y, X2: x + width, Y2: y + height}})
	}
	return result
}

func wordLifecycleAssertExportWords(t *testing.T, got []wordLifecycleExportWord, want map[string]models.BBox) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("export word count = %d, want %d: %#v", len(got), len(want), got)
	}
	seen := make(map[string]bool, len(got))
	for _, word := range got {
		expected, ok := want[word.Text]
		if !ok || seen[word.Text] || word.Box != expected {
			t.Fatalf("export word = %#v, expected bbox = %+v, present=%v duplicate=%v", word, expected, ok, seen[word.Text])
		}
		seen[word.Text] = true
	}
}

func xmlAttribute(element xml.StartElement, name string) string {
	for _, attribute := range element.Attr {
		if attribute.Name.Local == name {
			return attribute.Value
		}
	}
	return ""
}

func wordLifecycleBoxFromParts(t *testing.T, values []string) models.BBox {
	t.Helper()
	if len(values) != 4 {
		t.Fatalf("bbox components = %v", values)
	}
	return models.BBox{X1: wordLifecycleInt(t, values[0]), Y1: wordLifecycleInt(t, values[1]), X2: wordLifecycleInt(t, values[2]), Y2: wordLifecycleInt(t, values[3])}
}

func wordLifecycleInt(t *testing.T, raw string) int {
	t.Helper()
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		t.Fatalf("parse integer %q: %v", raw, err)
	}
	return value
}
