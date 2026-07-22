package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/models"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	"github.com/lehigh-university-libraries/scribe/proto/scribe/v1/scribev1connect"
)

func TestAnnotationReconciliationPersistsThroughRealConnectSaveReload(t *testing.T) {
	database := openTestDB(t)
	ctx := context.Background()
	workspaceID, userID := createServerTestWorkspace(t, database)
	items := store.NewItemStore(database)
	annotations := store.NewAnnotationStore(database)
	handler := NewHandler(nil, items, nil, annotations, nil, nil, nil, nil)
	appServer := newTenantScopedServer(t, handler, map[string]testTenantIdentity{
		"owner": {workspaceID: workspaceID, userID: userID},
	})
	client := scribev1connect.NewAnnotationServiceClient(appServer.Client(), appServer.URL)

	seed := func(t *testing.T, name string, build func(string, string) []any) (store.ItemImage, store.AnnotationPage) {
		t.Helper()
		canvasURI := fmt.Sprintf("https://source.example/canvas/reconcile-%s", name)
		image := createServerTestItemImage(t, database, workspaceID, userID, canvasURI)
		identity := iiif.PageIdentity{PublicBaseURL: handler.publicAnnotationBaseURL(), ItemImageID: image.ID, CanvasURI: canvasURI}
		pageID, err := iiif.AnnotationPageID(identity.PublicBaseURL, identity.ItemImageID)
		if err != nil {
			t.Fatal(err)
		}
		payload, err := iiif.NewAnnotationPage(identity, build(pageID, canvasURI))
		if err != nil {
			t.Fatalf("build seed page: %v", err)
		}
		page, err := annotations.SavePage(ctx, store.AnnotationPage{
			WorkspaceID: workspaceID, ItemImageID: image.ID, PageID: pageID, CanvasURI: canvasURI, Payload: string(payload),
		}, 0)
		if err != nil {
			t.Fatalf("save seed page: %v", err)
		}
		return image, page
	}

	t.Run("inline token insertion and deletion retain LCS word identities", func(t *testing.T) {
		var lineID, courseID, catalogID string
		image, seeded := seed(t, "tokens", func(pageID, canvasURI string) []any {
			line := reconciliationAcceptanceAnnotation(t, pageID, "tokens-line", "line", "Course Catalog", canvasURI, models.BBox{X1: 0, Y1: 0, X2: 300, Y2: 30})
			course := reconciliationAcceptanceAnnotation(t, pageID, "tokens-course", "word", "Course", canvasURI, models.BBox{X1: 0, Y1: 0, X2: 140, Y2: 30})
			catalog := reconciliationAcceptanceAnnotation(t, pageID, "tokens-catalog", "word", "Catalog", canvasURI, models.BBox{X1: 160, Y1: 0, X2: 300, Y2: 30})
			lineID, courseID, catalogID = annStringValue(line, "id"), annStringValue(course, "id"), annStringValue(catalog, "id")
			for _, word := range []map[string]any{course, catalog} {
				word["scribe:counter"] = json.Number("9007199254740993")
				word["scribe:reviewState"] = "accepted"
			}
			return []any{line, course, catalog}
		})

		draft := wordLifecycleDocument(t, seeded.Payload)
		draftItems := draft["items"].([]any)
		setAnnotationText(reconciliationAnnotation(t, draftItems, lineID), "Revised Course Catalog")
		setAnnotationText(reconciliationAnnotation(t, draftItems, courseID), "Revised")
		setAnnotationText(reconciliationAnnotation(t, draftItems, catalogID), "Course Catalog")
		current := wordLifecycleSaveAndReload(t, ctx, client, image.ID, wordLifecycleJSON(t, draft), seeded.Revision)
		words := reconciliationWords(t, wordLifecycleDocument(t, current.Msg.GetAnnotationPageJson())["items"].([]any))
		if got := reconciliationWordTexts(words); got != "Revised Course Catalog" {
			t.Fatalf("inserted word sequence = %q", got)
		}
		if annStringValue(words[1], "id") != courseID || annStringValue(words[2], "id") != catalogID {
			t.Fatalf("committed LCS IDs were not retained after reload: %#v", words)
		}
		insertedID := annStringValue(words[0], "id")
		for _, retained := range words[1:] {
			if retained["scribe:counter"] != json.Number("9007199254740993") || retained["scribe:reviewState"] != "accepted" {
				t.Fatalf("retained extensions changed after reload: %#v", retained)
			}
		}

		// Mirror the browser's fewer-token distribution on the persisted
		// three-word base. The inserted ID is now the only unmatched LCS item.
		draft = wordLifecycleDocument(t, current.Msg.GetAnnotationPageJson())
		draftItems = draft["items"].([]any)
		setAnnotationText(reconciliationAnnotation(t, draftItems, lineID), "Course Catalog")
		ordered := reconciliationWords(t, draftItems)
		setAnnotationText(ordered[0], "Course")
		setAnnotationText(ordered[1], "Catalog")
		setAnnotationText(ordered[2], "")
		current = wordLifecycleSaveAndReload(t, ctx, client, image.ID, wordLifecycleJSON(t, draft), current.Msg.GetRevision())
		words = reconciliationWords(t, wordLifecycleDocument(t, current.Msg.GetAnnotationPageJson())["items"].([]any))
		if got := reconciliationWordTexts(words); got != "Course Catalog" || len(words) != 2 {
			t.Fatalf("deleted-token word sequence = %q (%d words)", got, len(words))
		}
		if annStringValue(words[0], "id") != courseID || annStringValue(words[1], "id") != catalogID || reconciliationFind(wordLifecycleDocument(t, current.Msg.GetAnnotationPageJson())["items"].([]any), insertedID) != nil {
			t.Fatalf("token deletion did not remove only the unmatched ID: %#v", words)
		}
	})

	t.Run("bbox-only line resize proportionally retargets owned words", func(t *testing.T) {
		var lineID, leftID, rightID string
		image, seeded := seed(t, "resize", func(pageID, canvasURI string) []any {
			line := reconciliationAcceptanceAnnotation(t, pageID, "resize-line", "line", "left right", canvasURI, models.BBox{X1: 10, Y1: 20, X2: 210, Y2: 60})
			left := reconciliationAcceptanceAnnotation(t, pageID, "resize-left", "word", "left", canvasURI, models.BBox{X1: 10, Y1: 20, X2: 110, Y2: 60})
			right := reconciliationAcceptanceAnnotation(t, pageID, "resize-right", "word", "right", canvasURI, models.BBox{X1: 110, Y1: 20, X2: 210, Y2: 60})
			lineID, leftID, rightID = annStringValue(line, "id"), annStringValue(left, "id"), annStringValue(right, "id")
			for _, word := range []map[string]any{left, right} {
				word["scribe:reviewState"] = "accepted"
				target := word["target"].(map[string]any)
				target["id"] = "https://scribe.example/targets/resize"
				target["scribe:targetState"] = "accepted"
				selector := target["selector"].(map[string]any)
				selector["id"] = "https://scribe.example/selectors/resize"
				selector["scribe:selectorState"] = "accepted"
			}
			return []any{line, left, right}
		})

		draft := wordLifecycleDocument(t, seeded.Payload)
		draftItems := draft["items"].([]any)
		line := reconciliationAnnotation(t, draftItems, lineID)
		target, err := targetWithFragment(line["target"], image.CanvasURI, 30, 60, 430, 140)
		if err != nil {
			t.Fatal(err)
		}
		line["target"] = target
		current := wordLifecycleSaveAndReload(t, ctx, client, image.ID, wordLifecycleJSON(t, draft), seeded.Revision)
		persisted := wordLifecycleDocument(t, current.Msg.GetAnnotationPageJson())["items"].([]any)
		want := map[string]models.BBox{
			leftID:  {X1: 30, Y1: 60, X2: 230, Y2: 140},
			rightID: {X1: 230, Y1: 60, X2: 430, Y2: 140},
		}
		for id, expected := range want {
			word := reconciliationAnnotation(t, persisted, id)
			x1, y1, x2, y2, parseErr := parseXYWH(extractFragment(word))
			if parseErr != nil || (models.BBox{X1: x1, Y1: y1, X2: x2, Y2: y2}) != expected {
				t.Fatalf("word %q geometry = (%d,%d,%d,%d), %v; want %+v", id, x1, y1, x2, y2, parseErr, expected)
			}
			target := word["target"].(map[string]any)
			selector := target["selector"].(map[string]any)
			if word["scribe:reviewState"] != "accepted" || target["scribe:targetState"] != "accepted" || selector["scribe:selectorState"] != "accepted" {
				t.Fatalf("word %q lost unknown properties: %#v", id, word)
			}
			if target["id"] != nil || selector["id"] != nil {
				t.Fatalf("word %q retained stale changed-resource identity: %#v", id, word)
			}
		}
	})

	t.Run("drawn overlapping blank line cannot claim existing words", func(t *testing.T) {
		var existingID, alphaID, betaID string
		image, seeded := seed(t, "overlap", func(pageID, canvasURI string) []any {
			existing := reconciliationAcceptanceAnnotation(t, pageID, "overlap-existing", "line", "alpha beta", canvasURI, models.BBox{X1: 0, Y1: 0, X2: 200, Y2: 20})
			alpha := reconciliationAcceptanceAnnotation(t, pageID, "overlap-alpha", "word", "alpha", canvasURI, models.BBox{X1: 0, Y1: 0, X2: 100, Y2: 20})
			beta := reconciliationAcceptanceAnnotation(t, pageID, "overlap-beta", "word", "beta", canvasURI, models.BBox{X1: 100, Y1: 0, X2: 200, Y2: 20})
			existingID, alphaID, betaID = annStringValue(existing, "id"), annStringValue(alpha, "id"), annStringValue(beta, "id")
			return []any{existing, alpha, beta}
		})

		draft := wordLifecycleDocument(t, seeded.Payload)
		draftItems := draft["items"].([]any)
		pageID := annStringValue(draft, "id")
		blank := reconciliationAcceptanceAnnotation(t, pageID, "overlap-blank", "line", "", image.CanvasURI, models.BBox{X1: 0, Y1: 0, X2: 200, Y2: 20})
		// Prepending is adversarial; the real UI appends. The committed line
		// must still remain the durable owner after save/reload.
		draft["items"] = append([]any{blank}, draftItems...)
		current := wordLifecycleSaveAndReload(t, ctx, client, image.ID, wordLifecycleJSON(t, draft), seeded.Revision)
		persisted := wordLifecycleDocument(t, current.Msg.GetAnnotationPageJson())["items"].([]any)
		if extractAnnotationText(reconciliationAnnotation(t, persisted, existingID)) != "alpha beta" || reconciliationFind(persisted, alphaID) == nil || reconciliationFind(persisted, betaID) == nil {
			t.Fatalf("overlapping line stole canonical words: %s", current.Msg.GetAnnotationPageJson())
		}
		ownership := assignSpatialWordsToLines(persisted, nil)
		if len(ownership[existingID]) != 2 || len(ownership[annStringValue(blank, "id")]) != 0 {
			t.Fatalf("persisted ownership = %#v", ownership)
		}
	})

	t.Run("line deletion cascades only its committed owned word IDs", func(t *testing.T) {
		var deletedLineID, deletedAID, deletedBID, keptLineID, keptWordID string
		image, seeded := seed(t, "delete", func(pageID, canvasURI string) []any {
			deletedLine := reconciliationAcceptanceAnnotation(t, pageID, "delete-line", "line", "remove me", canvasURI, models.BBox{X1: 0, Y1: 0, X2: 200, Y2: 20})
			deletedA := reconciliationAcceptanceAnnotation(t, pageID, "delete-remove", "word", "remove", canvasURI, models.BBox{X1: 0, Y1: 0, X2: 100, Y2: 20})
			deletedB := reconciliationAcceptanceAnnotation(t, pageID, "delete-me", "word", "me", canvasURI, models.BBox{X1: 100, Y1: 0, X2: 200, Y2: 20})
			keptLine := reconciliationAcceptanceAnnotation(t, pageID, "delete-keep-line", "line", "keep", canvasURI, models.BBox{X1: 0, Y1: 40, X2: 200, Y2: 60})
			keptWord := reconciliationAcceptanceAnnotation(t, pageID, "delete-keep-word", "word", "keep", canvasURI, models.BBox{X1: 0, Y1: 40, X2: 200, Y2: 60})
			deletedLineID, deletedAID, deletedBID = annStringValue(deletedLine, "id"), annStringValue(deletedA, "id"), annStringValue(deletedB, "id")
			keptLineID, keptWordID = annStringValue(keptLine, "id"), annStringValue(keptWord, "id")
			return []any{deletedLine, deletedA, deletedB, keptLine, keptWord}
		})

		draft := wordLifecycleDocument(t, seeded.Payload)
		draftItems := draft["items"].([]any)
		filtered := make([]any, 0, len(draftItems)-1)
		for _, value := range draftItems {
			annotation := value.(map[string]any)
			if annStringValue(annotation, "id") != deletedLineID {
				filtered = append(filtered, value)
			}
		}
		draft["items"] = filtered
		current := wordLifecycleSaveAndReload(t, ctx, client, image.ID, wordLifecycleJSON(t, draft), seeded.Revision)
		persisted := wordLifecycleDocument(t, current.Msg.GetAnnotationPageJson())["items"].([]any)
		for _, id := range []string{deletedLineID, deletedAID, deletedBID} {
			if reconciliationFind(persisted, id) != nil {
				t.Fatalf("deleted row resource %q survived reload", id)
			}
		}
		if extractAnnotationText(reconciliationAnnotation(t, persisted, keptLineID)) != "keep" || reconciliationFind(persisted, keptWordID) == nil {
			t.Fatal("line deletion changed an unrelated canonical row")
		}
	})

	t.Run("word CRUD updates an otherwise unchanged line", func(t *testing.T) {
		var lineID, oneID, twoID, threeID string
		image, seeded := seed(t, "word-crud", func(pageID, canvasURI string) []any {
			line := reconciliationAcceptanceAnnotation(t, pageID, "crud-line", "line", "one two three", canvasURI, models.BBox{X1: 0, Y1: 0, X2: 300, Y2: 20})
			one := reconciliationAcceptanceAnnotation(t, pageID, "crud-one", "word", "one", canvasURI, models.BBox{X1: 0, Y1: 0, X2: 100, Y2: 20})
			two := reconciliationAcceptanceAnnotation(t, pageID, "crud-two", "word", "two", canvasURI, models.BBox{X1: 100, Y1: 0, X2: 200, Y2: 20})
			three := reconciliationAcceptanceAnnotation(t, pageID, "crud-three", "word", "three", canvasURI, models.BBox{X1: 200, Y1: 0, X2: 300, Y2: 20})
			lineID, oneID, twoID, threeID = annStringValue(line, "id"), annStringValue(one, "id"), annStringValue(two, "id"), annStringValue(three, "id")
			return []any{line, one, two, three}
		})

		draft := wordLifecycleDocument(t, seeded.Payload)
		draftItems := draft["items"].([]any)
		setAnnotationText(reconciliationAnnotation(t, draftItems, oneID), "ONE")
		filtered := make([]any, 0, len(draftItems))
		for _, value := range draftItems {
			if annStringValue(value.(map[string]any), "id") != twoID {
				filtered = append(filtered, value)
			}
		}
		four := reconciliationAcceptanceAnnotation(t, annStringValue(draft, "id"), "crud-four", "word", "four", image.CanvasURI, models.BBox{X1: 100, Y1: 0, X2: 200, Y2: 20})
		draft["items"] = append(filtered, four)
		current := wordLifecycleSaveAndReload(t, ctx, client, image.ID, wordLifecycleJSON(t, draft), seeded.Revision)
		persisted := wordLifecycleDocument(t, current.Msg.GetAnnotationPageJson())["items"].([]any)
		if text := extractAnnotationText(reconciliationAnnotation(t, persisted, lineID)); text != "ONE four three" {
			t.Fatalf("persisted line text = %q", text)
		}
		if reconciliationFind(persisted, twoID) != nil || reconciliationFind(persisted, oneID) == nil || reconciliationFind(persisted, threeID) == nil || reconciliationFind(persisted, annStringValue(four, "id")) == nil {
			t.Fatalf("persisted word CRUD IDs are incorrect: %s", current.Msg.GetAnnotationPageJson())
		}
	})
}

func reconciliationAcceptanceAnnotation(t *testing.T, pageID, seed, granularity, text, canvasURI string, box models.BBox) map[string]any {
	t.Helper()
	id, err := iiif.AnnotationID(pageID, seed)
	if err != nil {
		t.Fatal(err)
	}
	return transcriptionAnnotation(id, granularity, text, canvasURI, box)
}

func reconciliationWordTexts(words []map[string]any) string {
	texts := make([]string, 0, len(words))
	for _, word := range words {
		texts = append(texts, extractAnnotationText(word))
	}
	return strings.Join(texts, " ")
}
