package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/lehigh-university-libraries/scribe/internal/auth"
	"github.com/lehigh-university-libraries/scribe/internal/config"
	dbstore "github.com/lehigh-university-libraries/scribe/internal/db"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
)

func TestEnrichAnnotationPageIsAtomicAndPreservesPageProperties(t *testing.T) {
	pageJSON := `{
  "@context":["http://iiif.io/api/presentation/3/context.json",{"custom":"https://example.test/custom#"}],
  "id":"https://scribe.test/presentation/v3/item-image-7/canvas/page-1/annotations",
  "type":"AnnotationPage",
  "label":{"en":["OCR corrections"]},
  "custom:workflow":{"state":"review"},
  "items":[{"id":"one","type":"Annotation","textGranularity":"line"},{"id":"two","type":"Annotation","textGranularity":"line"}]
}`

	call := 0
	enrich := func(_ context.Context, raw string, _ store.Context) (string, error) {
		call++
		if call == 2 {
			return "", fmt.Errorf("provider failed")
		}
		var annotation map[string]any
		if err := json.Unmarshal([]byte(raw), &annotation); err != nil {
			return "", err
		}
		annotation["body"] = []any{map[string]any{"type": "TextualBody", "value": "changed"}}
		encoded, err := json.Marshal(annotation)
		return string(encoded), err
	}

	if enriched, err := enrichAnnotationPageWith(context.Background(), pageJSON, store.Context{}, enrich); err == nil || enriched != "" {
		t.Fatalf("partial page result = %q, %v; want an atomic failure", enriched, err)
	}

	enriched, err := enrichAnnotationPageWith(context.Background(), pageJSON, store.Context{}, func(_ context.Context, raw string, _ store.Context) (string, error) {
		return raw, nil
	})
	if err != nil {
		t.Fatalf("enrichAnnotationPageWith: %v", err)
	}
	var page map[string]any
	if err := json.Unmarshal([]byte(enriched), &page); err != nil {
		t.Fatalf("decode enriched page: %v", err)
	}
	label, _ := page["label"].(map[string]any)
	workflow, _ := page["custom:workflow"].(map[string]any)
	contexts, _ := page["@context"].([]any)
	if label == nil || workflow["state"] != "review" || len(contexts) != 2 {
		t.Fatalf("page properties were not preserved: %#v", page)
	}

	nonLinePage := `{"type":"AnnotationPage","items":[{"id":"line","type":"Annotation","textGranularity":"line"},{"id":"word","type":"Annotation","textGranularity":"word","custom":"preserve"},{"id":"page","type":"Annotation","textGranularity":"page"}]}`
	calls := 0
	enriched, err = enrichAnnotationPageWith(context.Background(), nonLinePage, store.Context{}, func(_ context.Context, raw string, _ store.Context) (string, error) {
		calls++
		return raw, nil
	})
	if err != nil {
		t.Fatalf("line-only enrichAnnotationPageWith: %v", err)
	}
	if calls != 1 {
		t.Fatalf("page enrichment provider calls = %d, want one line only", calls)
	}
	var lineOnly map[string]any
	if err := iiif.DecodeJSON([]byte(enriched), &lineOnly); err != nil {
		t.Fatal(err)
	}
	items := lineOnly["items"].([]any)
	if items[1].(map[string]any)["custom"] != "preserve" || annStringValue(items[2].(map[string]any), "textGranularity") != "page" {
		t.Fatalf("non-line annotations changed: %#v", items)
	}
}

func TestEnrichAnnotationPageRejectsFanoutBeforeProviderCall(t *testing.T) {
	page := `{"type":"AnnotationPage","items":[
    {"id":"line-1","type":"Annotation","textGranularity":"line"},
    {"id":"word-1","type":"Annotation","textGranularity":"word"},
    {"id":"line-2","type":"Annotation","textGranularity":"line"},
    {"id":"line-3","type":"Annotation","textGranularity":"line"}
  ]}`
	calls := 0
	result, err := enrichAnnotationPageWithLimit(context.Background(), page, store.Context{}, 2, func(_ context.Context, raw string, _ store.Context) (string, error) {
		calls++
		return raw, nil
	})
	if err == nil || result != "" {
		t.Fatalf("fanout result/error = %q/%v, want atomic rejection", result, err)
	}
	if calls != 0 {
		t.Fatalf("provider calls before fanout rejection = %d, want 0", calls)
	}
}

func TestItemImageIDFromAnnotationPageIDRejectsNonCanonicalIDs(t *testing.T) {
	got, err := itemImageIDFromAnnotationPageID("https://scribe.test/base/presentation/v3/item-image-42/canvas/page-1/annotations")
	if err != nil || got != 42 {
		t.Fatalf("canonical page id = %d, %v; want 42", got, err)
	}
	for _, invalid := range []string{
		"urn:page:42",
		"https://scribe.test/v1/item-images/42/annotations",
		"https://scribe.test/presentation/v3/item-image-42/canvas/page-1/annotations/",
		"https://scribe.test/presentation/v3/item-image-42/canvas/page-1/annotations/items/one",
		"https://scribe.test/presentation/v3/item-image-42/canvas/page-1/annotations?revision=1",
		"https://scribe.test/presentation/v3/item-image-0/canvas/page-1/annotations",
	} {
		if _, err := itemImageIDFromAnnotationPageID(invalid); err == nil {
			t.Errorf("itemImageIDFromAnnotationPageID(%q) succeeded", invalid)
		}
	}
}

func TestNormalizeAnnotationPreservesV3ExtensionProperties(t *testing.T) {
	t.Parallel()

	annotation := map[string]any{
		"@id":             "https://scribe.test/legacy-id",
		"@type":           "Annotation",
		"textGranularity": "line",
		"body": map[string]any{
			"type":       "TextualBody",
			"value":      "hello",
			"confidence": 0.91,
		},
		"target": map[string]any{
			"source": map[string]any{
				"id":        "https://source.example/canvas/1",
				"type":      "Canvas",
				"custom:id": "source-property",
			},
			"selector": map[string]any{
				"type": "FragmentSelector", "value": "xywh=1,2,30,10",
			},
			"custom:target": "target-property",
		},
	}
	normalized := normalizeAnnotation(annotation, "https://source.example/canvas/1")
	if normalized["@id"] != nil || normalized["@type"] != nil {
		t.Fatalf("legacy aliases survived normalization: %#v", normalized)
	}
	if normalized["motivation"] != "supplementing" {
		t.Fatalf("motivation = %#v, want supplementing", normalized["motivation"])
	}
	body, _ := normalized["body"].(map[string]any)
	if body["purpose"] != "supplementing" || body["confidence"] != 0.91 {
		t.Fatalf("body properties were not normalized/preserved: %#v", body)
	}
	target, _ := normalized["target"].(map[string]any)
	source, _ := target["source"].(map[string]any)
	if target["custom:target"] != "target-property" || source["custom:id"] != "source-property" {
		t.Fatalf("target/source properties were not preserved: %#v", target)
	}
}

func TestNormalizeAnnotationPreservesCompactTargetMediaDimensions(t *testing.T) {
	t.Parallel()
	canvas := "https://source.example/canvas/1?view=primary"
	annotation := map[string]any{
		"id":              "https://scribe.example/annotations/one",
		"type":            "Annotation",
		"textGranularity": "line",
		"motivation":      "supplementing",
		"body":            map[string]any{"type": "TextualBody", "value": "hello"},
		"target":          canvas + "#t=1,2&xywh=pixel:10,20,30,40&track=ocr",
	}
	normalized := normalizeAnnotation(annotation, "")
	if got := extractCanvasURI(normalized); got != canvas {
		t.Fatalf("canvas = %q, want %q", got, canvas)
	}
	if got := extractFragment(normalized); got != "pixel:10,20,30,40" {
		t.Fatalf("xywh = %q", got)
	}
	target, _ := normalized["target"].(map[string]any)
	selector, _ := target["selector"].(map[string]any)
	if got := annStringValue(selector, "value"); got != "t=1,2&xywh=pixel:10,20,30,40&track=ocr" {
		t.Fatalf("selector value = %q", got)
	}
}

func TestExternalCanvasResolutionDuringEnrichmentIsTenantScoped(t *testing.T) {
	database := openTestDB(t)
	sharedCanvas := "https://source.example/canvas/" + uuid.NewString()
	imageA := createServerTestItemImage(t, database, store.AnonymousWorkspaceID, store.AnonymousUserID, sharedCanvas)
	workspaceB, userB := createServerTestWorkspace(t, database)
	imageB := createServerTestItemImage(t, database, workspaceB, userB, sharedCanvas)

	fakeOCR := &imageOnlyWorkerOCR{}
	h := &Handler{
		items:   store.NewItemStore(database),
		ocrRuns: store.NewOCRRunStore(database),
		auth:    &auth.Manager{},
		ocr:     fakeOCR,
	}
	h.imageRegionFetcher = func(context.Context, string, int, int, int, int) (string, func(), error) {
		return "region.png", func() {}, nil
	}
	annotation := fmt.Sprintf(`{
  "id":"https://scribe.test/presentation/v3/item-image-%d/canvas/page-1/annotations/items/line",
  "type":"Annotation",
  "textGranularity":"line",
  "motivation":["tagging","supplementing"],
  "body":[{"id":"https://scribe.example/body/original","type":"TextualBody","value":"before","language":"en","service":[{"id":"https://scribe.example/body-service","type":"Service","scribe:counter":9007199254740993}]}],
  "target":{"source":{"id":%q,"type":"Canvas"},"selector":{"type":"FragmentSelector","value":"xywh=0,0,10,10"}}
}`, imageB.ID, sharedCanvas)
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{
		Authenticated: true,
		UserID:        userB,
		WorkspaceID:   workspaceB,
	})
	enriched, err := h.enrichSingleAnnotation(ctx, imageB.ID, annotation, store.Context{})
	if err != nil {
		t.Fatalf("workspace B exact-image enrichment: %v", err)
	}
	var enrichedAnnotation map[string]any
	if err := iiif.DecodeJSON([]byte(enriched), &enrichedAnnotation); err != nil {
		t.Fatal(err)
	}
	body := enrichedAnnotation["body"].([]any)[0].(map[string]any)
	service := body["service"].([]any)[0].(map[string]any)
	if body["id"] != nil || body["language"] != "en" || extractAnnotationText(enrichedAnnotation) != "final corrected text" {
		t.Fatalf("enrichment did not preserve/mutate the TextualBody correctly: %#v", body)
	}
	if counter, ok := service["scribe:counter"].(json.Number); !ok || counter.String() != "9007199254740993" {
		t.Fatalf("enrichment rounded or lost body extension state: %#v", service)
	}
	if fakeOCR.transcriptionCalls != 1 {
		t.Fatalf("transcription calls = %d, want 1", fakeOCR.transcriptionCalls)
	}

	_, err = h.enrichSingleAnnotation(ctx, imageA.ID, annotation, store.Context{})
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("source image %d", imageA.ID)) {
		t.Fatalf("foreign exact-image enrichment error = %v, want hidden image %d", err, imageA.ID)
	}
	if fakeOCR.transcriptionCalls != 1 {
		t.Fatalf("foreign image reached provider: calls=%d", fakeOCR.transcriptionCalls)
	}
}

func TestLocalAnnotationCRUDSavedAsWholePagesPreservesCanonicalProperties(t *testing.T) {
	previous := config.Get()
	configured := previous
	configured.Config.LLM.Ollama.Models = []string{"test-model"}
	config.Init(configured)
	t.Cleanup(func() { config.Init(previous) })

	database := openTestDB(t)
	ctx := context.Background()
	canvasURI := "https://source.example/canvas/" + uuid.NewString()
	image := createServerTestItemImage(t, database, store.AnonymousWorkspaceID, store.AnonymousUserID, canvasURI)
	annotationStore := store.NewAnnotationStore(database)
	contextStore := store.NewContextStore(database)
	h := &Handler{
		items:       store.NewItemStore(database),
		ocrRuns:     store.NewOCRRunStore(database),
		contexts:    contextStore,
		annotations: annotationStore,
	}

	pageID, err := h.annotationPageIDForItemImage(image.ID)
	if err != nil {
		t.Fatalf("annotation page id: %v", err)
	}
	raw, err := iiif.NewAnnotationPage(iiif.PageIdentity{
		PublicBaseURL: h.publicAnnotationBaseURL(),
		ItemImageID:   image.ID,
		CanvasURI:     canvasURI,
	}, []any{})
	if err != nil {
		t.Fatalf("build annotation page: %v", err)
	}
	var initial map[string]any
	if err := json.Unmarshal(raw, &initial); err != nil {
		t.Fatalf("decode annotation page: %v", err)
	}
	initial["label"] = map[string]any{"en": []any{"OCR corrections"}}
	initial["next"] = "https://source.example/annotations/page-2"
	raw, _ = json.Marshal(initial)
	saved, err := annotationStore.SavePage(ctx, store.AnnotationPage{
		WorkspaceID: store.AnonymousWorkspaceID,
		ItemImageID: image.ID,
		PageID:      pageID,
		CanvasURI:   canvasURI,
		Payload:     string(raw),
	}, 0)
	if err != nil {
		t.Fatalf("seed canonical page: %v", err)
	}
	otherImage := createServerTestItemImage(t, database, store.AnonymousWorkspaceID, store.AnonymousUserID, "https://source.example/canvas/"+uuid.NewString())
	_, err = h.EnrichAnnotation(ctx, connect.NewRequest(&scribev1.EnrichAnnotationRequest{
		ItemImageId:    otherImage.ID,
		Scope:          "page",
		AnnotationJson: saved.Payload,
	}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("enrich page through a different item image = %v, want not_found", err)
	}

	ensureServerTestDefaultContext(t, contextStore, store.Context{
		Name:                  "annotation-hardening-default",
		IsDefault:             true,
		SegmentationModel:     "tesseract/auto",
		TranscriptionProvider: "ollama",
		TranscriptionModel:    "test-model",
	})
	enrichResponse, err := h.EnrichAnnotation(ctx, connect.NewRequest(&scribev1.EnrichAnnotationRequest{
		ItemImageId:    image.ID,
		Scope:          "page",
		AnnotationJson: saved.Payload,
	}))
	if err != nil {
		t.Fatalf("page-scope EnrichAnnotation authorization: %v", err)
	}
	assertCanonicalPageProperties(t, enrichResponse.Msg.GetAnnotationJson())

	annotationID, err := iiif.AnnotationID(pageID, "created")
	if err != nil {
		t.Fatalf("annotation id: %v", err)
	}
	annotation := canonicalMutationTestAnnotation(annotationID, canvasURI, "created")
	var draft map[string]any
	if err := json.Unmarshal([]byte(saved.Payload), &draft); err != nil {
		t.Fatalf("decode local draft: %v", err)
	}
	draft["items"] = append(draft["items"].([]any), mustDecodeAnnotation(annotation))
	payload, _ := json.Marshal(draft)
	afterCreate, err := h.SaveAnnotationPage(ctx, connect.NewRequest(&scribev1.SaveAnnotationPageRequest{
		ItemImageId:        image.ID,
		AnnotationPageJson: string(payload),
		ExpectedRevision:   saved.Revision,
	}))
	if err != nil {
		t.Fatalf("save local create: %v", err)
	}
	if afterCreate.Msg.GetRevision() != saved.Revision+1 {
		t.Fatalf("create revision = %d; want %d", afterCreate.Msg.GetRevision(), saved.Revision+1)
	}
	assertCanonicalPageProperties(t, afterCreate.Msg.GetAnnotationPageJson())

	updated := canonicalMutationTestAnnotation(annotationID, canvasURI, "updated")
	if err := json.Unmarshal([]byte(afterCreate.Msg.GetAnnotationPageJson()), &draft); err != nil {
		t.Fatalf("decode draft after create: %v", err)
	}
	draft["items"] = []any{mustDecodeAnnotation(updated)}
	payload, _ = json.Marshal(draft)
	afterUpdate, err := h.SaveAnnotationPage(ctx, connect.NewRequest(&scribev1.SaveAnnotationPageRequest{
		ItemImageId:        image.ID,
		AnnotationPageJson: string(payload),
		ExpectedRevision:   afterCreate.Msg.GetRevision(),
	}))
	if err != nil {
		t.Fatalf("save local update: %v", err)
	}
	assertCanonicalPageProperties(t, afterUpdate.Msg.GetAnnotationPageJson())

	if err := json.Unmarshal([]byte(afterUpdate.Msg.GetAnnotationPageJson()), &draft); err != nil {
		t.Fatalf("decode draft after update: %v", err)
	}
	draft["items"] = []any{}
	payload, _ = json.Marshal(draft)
	afterDelete, err := h.SaveAnnotationPage(ctx, connect.NewRequest(&scribev1.SaveAnnotationPageRequest{
		ItemImageId:        image.ID,
		AnnotationPageJson: string(payload),
		ExpectedRevision:   afterUpdate.Msg.GetRevision(),
	}))
	if err != nil {
		t.Fatalf("save local delete: %v", err)
	}
	assertCanonicalPageProperties(t, afterDelete.Msg.GetAnnotationPageJson())
	items, err := annotationItemsFromPage(afterDelete.Msg.GetAnnotationPageJson())
	if err != nil || len(items) != 0 {
		t.Fatalf("items after delete = %#v, %v; want none", items, err)
	}
}

func canonicalMutationTestAnnotation(id, canvasURI, text string) string {
	payload, _ := json.Marshal(map[string]any{
		"id":              id,
		"type":            "Annotation",
		"motivation":      "supplementing",
		"textGranularity": "line",
		"body": []any{map[string]any{
			"type": "TextualBody", "purpose": "supplementing", "format": "text/plain", "value": text,
		}},
		"target": map[string]any{
			"source": map[string]any{"id": canvasURI, "type": "Canvas"},
			"selector": map[string]any{
				"type": "FragmentSelector", "conformsTo": "http://www.w3.org/TR/media-frags/", "value": "xywh=0,0,10,10",
			},
		},
	})
	return string(payload)
}

func mustDecodeAnnotation(raw string) map[string]any {
	var annotation map[string]any
	if err := json.Unmarshal([]byte(raw), &annotation); err != nil {
		panic(err)
	}
	return annotation
}

func assertCanonicalPageProperties(t *testing.T, raw string) {
	t.Helper()
	var page map[string]any
	if err := json.Unmarshal([]byte(raw), &page); err != nil {
		t.Fatalf("decode canonical page: %v", err)
	}
	label, _ := page["label"].(map[string]any)
	if label == nil || page["next"] != "https://source.example/annotations/page-2" {
		t.Fatalf("canonical page properties were lost: %#v", page)
	}
}

func createServerTestItemImage(t *testing.T, database *sql.DB, workspaceID, userID uint64, canvasURI string) store.ItemImage {
	t.Helper()
	return createServerTestItemImageWithSourceType(t, database, workspaceID, userID, canvasURI, "manifest")
}

func createServerTestUploadItemImage(t *testing.T, database *sql.DB, workspaceID, userID uint64, canvasURI string) store.ItemImage {
	t.Helper()
	return createServerTestItemImageWithSourceType(t, database, workspaceID, userID, canvasURI, "upload")
}

func createServerTestItemImageWithSourceType(t *testing.T, database *sql.DB, workspaceID, userID uint64, canvasURI, sourceType string) store.ItemImage {
	t.Helper()
	itemID := "annotation-test-" + uuid.NewString()
	itemStore := store.NewItemStore(database)
	sourceURL := ""
	if sourceType == "manifest" {
		sourceURL = "https://source.example/manifest"
	}
	if _, err := itemStore.Create(context.Background(), dbstore.CreateItemParams{
		ID: itemID, UserID: userID, WorkspaceID: workspaceID, Name: "annotation hardening",
		SourceType: sourceType, SourceURL: sourceURL,
	}); err != nil {
		t.Fatalf("create test item: %v", err)
	}
	t.Cleanup(func() {
		_ = itemStore.DeleteForWorkspace(context.Background(), itemID, workspaceID)
	})
	image, err := itemStore.AddImage(context.Background(), dbstore.CreateItemImageParams{
		ItemID:    itemID,
		Sequence:  1,
		ImageURL:  "https://source.example/image/" + uuid.NewString() + ".jpg",
		CanvasURI: canvasURI,
		Width:     10000,
		Height:    10000,
	})
	if err != nil {
		t.Fatalf("create test item image: %v", err)
	}
	return image
}

func createServerTestWorkspace(t *testing.T, database *sql.DB) (uint64, uint64) {
	t.Helper()
	ctx := context.Background()
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin test workspace transaction: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	suffix := uuid.NewString()
	result, err := tx.ExecContext(ctx, `INSERT INTO users (name) VALUES (?)`, "annotation-user-"+suffix)
	if err != nil {
		t.Fatalf("create test user: %v", err)
	}
	userID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("load test user ID: %v", err)
	}
	result, err = tx.ExecContext(ctx, `
INSERT INTO workspaces (owner_user_id, name, slug, is_personal, created_by_user_id)
VALUES (?, ?, ?, TRUE, ?)`, userID, "annotation-workspace-"+suffix, "annotation-workspace-"+suffix, userID)
	if err != nil {
		t.Fatalf("create test workspace: %v", err)
	}
	workspaceID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("load test workspace ID: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO storage_quota_usage (workspace_id) VALUES (?)`, workspaceID); err != nil {
		t.Fatalf("create test workspace quota row: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_members (workspace_id, user_id, role) VALUES (?, ?, 'admin')`, workspaceID, userID); err != nil {
		t.Fatalf("create test workspace membership: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit test workspace transaction: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		itemRows, itemErr := database.QueryContext(cleanupCtx, `SELECT id FROM items WHERE workspace_id = ?`, workspaceID)
		if itemErr == nil {
			var itemIDs []string
			for itemRows.Next() {
				var itemID string
				if itemRows.Scan(&itemID) == nil {
					itemIDs = append(itemIDs, itemID)
				}
			}
			_ = itemRows.Close()
			itemStore := store.NewItemStore(database)
			for _, itemID := range itemIDs {
				_ = itemStore.DeleteForWorkspace(cleanupCtx, itemID, uint64(workspaceID))
			}
		}
		contextRows, contextErr := database.QueryContext(cleanupCtx, `SELECT id FROM contexts WHERE workspace_id = ?`, workspaceID)
		if contextErr == nil {
			var contextIDs []uint64
			for contextRows.Next() {
				var contextID uint64
				if contextRows.Scan(&contextID) == nil {
					contextIDs = append(contextIDs, contextID)
				}
			}
			_ = contextRows.Close()
			contextStore := store.NewContextStore(database)
			for _, contextID := range contextIDs {
				_ = contextStore.DeleteForWorkspace(cleanupCtx, contextID, uint64(workspaceID))
			}
		}
		_, _ = database.ExecContext(context.Background(), `DELETE wd FROM webhook_deliveries wd JOIN event_outbox eo ON eo.event_id = wd.event_id WHERE eo.workspace_id = ?`, workspaceID)
		for _, table := range []string{"event_outbox", "provider_call_audits", "provider_secrets", "api_keys", "external_requests", "workspace_storage_reservations", "resource_cleanup_outbox"} {
			_, _ = database.ExecContext(context.Background(), `DELETE FROM `+table+` WHERE workspace_id = ?`, workspaceID) // #nosec G202 -- closed test-only table list.
		}
		_ = store.NewItemStore(database).RebuildStorageQuotaUsage(context.Background())
		cleanupTx, err := database.BeginTx(cleanupCtx, nil)
		if err != nil {
			t.Errorf("begin test workspace cleanup transaction: %v", err)
			return
		}
		defer func() { _ = cleanupTx.Rollback() }()
		for _, statement := range []struct {
			query string
			id    int64
		}{
			{`DELETE FROM workspace_members WHERE workspace_id = ?`, workspaceID},
			{`DELETE FROM storage_quota_usage WHERE workspace_id = ?`, workspaceID},
			{`DELETE FROM workspaces WHERE id = ?`, workspaceID},
			{`DELETE FROM users WHERE id = ?`, userID},
		} {
			if _, err := cleanupTx.ExecContext(cleanupCtx, statement.query, statement.id); err != nil {
				t.Errorf("clean test workspace identity: %v", err)
				return
			}
		}
		if err := cleanupTx.Commit(); err != nil {
			t.Errorf("commit test workspace cleanup transaction: %v", err)
		}
	})
	return uint64(workspaceID), uint64(userID)
}
