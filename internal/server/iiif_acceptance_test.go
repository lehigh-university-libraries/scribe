package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/lehigh-university-libraries/scribe/internal/auth"
	ocrhandlers "github.com/lehigh-university-libraries/scribe/internal/handlers"
	"github.com/lehigh-university-libraries/scribe/internal/hocr"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
	"github.com/lehigh-university-libraries/scribe/proto/scribe/v1/scribev1connect"
)

const testTenantHeader = "X-Scribe-Test-Tenant"

type testTenantIdentity struct {
	workspaceID uint64
	userID      uint64
}

type imageOnlyWorkerOCR struct {
	segmentCalls         int
	transcriptionCalls   int
	failNextSegmentation bool
	lastSegmentation     hocr.ProcessingContext
}

func (*imageOnlyWorkerOCR) SetProviderCallAuditLogger(hocr.ProviderCallAuditLogger) {}

func (*imageOnlyWorkerOCR) ProcessImageURLWithContext(context.Context, string, hocr.ProcessingContext) (*ocrhandlers.ProcessResult, error) {
	return nil, fmt.Errorf("unexpected durable image processing call")
}

func (f *imageOnlyWorkerOCR) ProcessImageURLTransientWithContext(_ context.Context, _ string, pctx hocr.ProcessingContext) (*ocrhandlers.ProcessResult, error) {
	f.segmentCalls++
	f.lastSegmentation = pctx
	if f.failNextSegmentation {
		f.failNextSegmentation = false
		return nil, fmt.Errorf("temporary segmentation failure")
	}
	return &ocrhandlers.ProcessResult{
		SessionID: "image-only-segmentation",
		HOCR:      reprocessServiceTestHOCR,
		PlainText: "new segmented text",
		Provider:  "test-segmentor",
		Model:     "test-layout",
	}, nil
}

func (*imageOnlyWorkerOCR) ProcessImageUploadWithContext(context.Context, string, []byte, hocr.ProcessingContext) (*ocrhandlers.ProcessResult, error) {
	return nil, fmt.Errorf("unexpected upload processing call")
}

func (*imageOnlyWorkerOCR) StoreUploadedImage(context.Context, string, []byte) (string, error) {
	return "", fmt.Errorf("unexpected upload storage call")
}

func (f *imageOnlyWorkerOCR) TranscribeImageFileWithContext(context.Context, string, string, string) (string, error) {
	f.transcriptionCalls++
	return "final corrected text", nil
}

// newTenantScopedServer exercises the generated Connect transport while
// installing an authenticated principal at the same request boundary used by
// the production auth middleware. NewHandler is deliberately constructed
// without an auth manager so this fixture does not need real sessions; setting
// h.auth after route registration makes resource lookups consume that principal
// without replacing the production Connect handlers.
func newTenantScopedServer(t *testing.T, h *Handler, tenants map[string]testTenantIdentity) *httptest.Server {
	t.Helper()
	h.auth = new(auth.Manager)
	// The synthetic tenant header is intentionally not a production credential,
	// so the pre-authentication edge limiter would otherwise classify every RPC
	// in these acceptance scenarios as one anonymous client. Rate-admission has
	// dedicated middleware coverage; keep the authenticated, workspace-keyed
	// limiter active here while removing that fixture-only timing dependency.
	h.edgeRequestLimiter = nil
	h.edgeAggregateLimiter = nil
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, ok := tenants[strings.TrimSpace(r.Header.Get(testTenantHeader))]
		if !ok {
			http.Error(w, "test tenant is required", http.StatusUnauthorized)
			return
		}
		principal := auth.Principal{
			UserID:        identity.userID,
			WorkspaceID:   identity.workspaceID,
			WorkspaceRole: "admin",
			Authenticated: true,
			AuthType:      "test",
		}
		h.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), principal)))
	}))
	t.Cleanup(server.Close)
	return server
}

func tenantConnectRequest[T any](tenant string, message *T) *connect.Request[T] {
	request := connect.NewRequest(message)
	request.Header().Set(testTenantHeader, tenant)
	return request
}

func testAnnotationExportFormat(t *testing.T, format string) scribev1.AnnotationExportFormat {
	t.Helper()
	switch format {
	case "txt":
		return scribev1.AnnotationExportFormat_ANNOTATION_EXPORT_FORMAT_PLAIN_TEXT
	case "hocr":
		return scribev1.AnnotationExportFormat_ANNOTATION_EXPORT_FORMAT_HOCR
	case "pagexml":
		return scribev1.AnnotationExportFormat_ANNOTATION_EXPORT_FORMAT_PAGE_XML
	case "alto":
		return scribev1.AnnotationExportFormat_ANNOTATION_EXPORT_FORMAT_ALTO_XML
	default:
		t.Fatalf("unknown export format %q", format)
		return scribev1.AnnotationExportFormat_ANNOTATION_EXPORT_FORMAT_UNSPECIFIED
	}
}

func tenantAnnotationExport(
	t *testing.T,
	client scribev1connect.AnnotationServiceClient,
	tenant string,
	itemImageID, revision uint64,
	format string,
) (int, string) {
	t.Helper()
	request := connect.NewRequest(&scribev1.ExportAnnotationPageRequest{
		ItemImageId: itemImageID, ExpectedRevision: revision, Format: testAnnotationExportFormat(t, format),
	})
	if tenant != "" {
		request.Header().Set(testTenantHeader, tenant)
	}
	response, err := client.ExportAnnotationPage(context.Background(), request)
	if err == nil {
		return http.StatusOK, string(response.Msg.GetContent())
	}
	switch connect.CodeOf(err) {
	case connect.CodeNotFound:
		return http.StatusNotFound, err.Error()
	case connect.CodeAborted:
		return http.StatusConflict, err.Error()
	case connect.CodeInvalidArgument:
		return http.StatusBadRequest, err.Error()
	default:
		return http.StatusInternalServerError, err.Error()
	}
}

func buildPresentation3ChoiceManifest(t *testing.T, baseURL string) []byte {
	t.Helper()
	manifest := map[string]any{
		"@context": []any{
			"http://iiif.io/api/extension/text-granularity/context.json",
			"http://iiif.io/api/presentation/3/context.json",
		},
		"id":       baseURL + "/manifest",
		"type":     "Manifest",
		"label":    map[string]any{"en": []any{"Choice manuscript"}, "fr": []any{"Manuscrit Choice"}},
		"rights":   "http://creativecommons.org/licenses/by/4.0/",
		"behavior": []any{"paged"},
		"items": []any{map[string]any{
			"id":       baseURL + "/canvas/1",
			"type":     "Canvas",
			"label":    map[string]any{"none": []any{"Leaf 1"}},
			"width":    2160,
			"height":   3632,
			"metadata": []any{map[string]any{"label": map[string]any{"en": []any{"Shelfmark"}}, "value": map[string]any{"none": []any{"MS 1"}}}},
			"items": []any{map[string]any{
				"id":   baseURL + "/canvas/1/page/painting",
				"type": "AnnotationPage",
				"items": []any{map[string]any{
					"id":         baseURL + "/canvas/1/annotation/painting",
					"type":       "Annotation",
					"motivation": "painting",
					"target":     baseURL + "/canvas/1",
					"body": map[string]any{
						"type": "Choice",
						"items": []any{
							map[string]any{
								"id":     baseURL + "/images/color.jpg",
								"type":   "Image",
								"format": "image/jpeg",
								"width":  2160,
								"height": 3632,
							},
							map[string]any{
								"id":     baseURL + "/images/grayscale.png",
								"type":   "Image",
								"format": "image/png",
								"width":  2160,
								"height": 3632,
							},
						},
					},
				}},
			}},
			"seeAlso": []any{map[string]any{
				"id":      baseURL + "/hocr.xml",
				"type":    "Text",
				"format":  "text/vnd.hocr+html",
				"profile": "http://kba.cloud/hocr-spec",
			}},
		}},
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal Presentation 3 Choice manifest: %v", err)
	}
	if err := iiif.ValidateManifest(payload); err != nil {
		t.Fatalf("source Presentation 3 Choice manifest failed libops validation: %v", err)
	}
	return payload
}

func newChoiceManifestSource(t *testing.T) (*httptest.Server, []byte) {
	t.Helper()
	var manifest []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest":
			w.Header().Set("Content-Type", "application/ld+json")
			_, _ = w.Write(manifest)
		case "/hocr.xml":
			w.Header().Set("Content-Type", "text/vnd.hocr+html; charset=utf-8")
			_, _ = io.WriteString(w, minimalHOCR)
		default:
			http.NotFound(w, r)
		}
	}))
	manifest = buildPresentation3ChoiceManifest(t, server.URL)
	t.Cleanup(server.Close)
	return server, manifest
}

func newChoiceManifestSourceWithoutHOCR(t *testing.T) (*httptest.Server, []byte) {
	t.Helper()
	var manifest []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/manifest" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/ld+json")
		_, _ = w.Write(manifest)
	}))
	manifest = buildPresentation3ChoiceManifest(t, server.URL)
	var document map[string]any
	if err := json.Unmarshal(manifest, &document); err != nil {
		t.Fatalf("decode no-hOCR fixture: %v", err)
	}
	canvases, _ := document["items"].([]any)
	canvas, _ := canvases[0].(map[string]any)
	delete(canvas, "seeAlso")
	var err error
	manifest, err = json.Marshal(document)
	if err != nil {
		t.Fatalf("encode no-hOCR fixture: %v", err)
	}
	if err := iiif.ValidateManifest(manifest); err != nil {
		t.Fatalf("no-hOCR Choice manifest failed libops validation: %v", err)
	}
	t.Cleanup(server.Close)
	return server, manifest
}

func replaceFirstAnnotationText(t *testing.T, raw, replacement string) string {
	t.Helper()
	var page map[string]any
	if err := json.Unmarshal([]byte(raw), &page); err != nil {
		t.Fatalf("decode annotation page for edit: %v", err)
	}
	items, _ := page["items"].([]any)
	// Prefer a word annotation because legacy text exports derive line text
	// from words whenever both granularities are present. The second pass keeps
	// this helper useful for valid line-only pages.
	for _, granularity := range []string{"word", ""} {
		for _, rawItem := range items {
			item, _ := rawItem.(map[string]any)
			if granularity != "" && !strings.EqualFold(strings.TrimSpace(fmt.Sprint(item["textGranularity"])), granularity) {
				continue
			}
			bodies := item["body"]
			switch body := bodies.(type) {
			case map[string]any:
				if body["type"] != "TextualBody" {
					continue
				}
				body["value"] = replacement
			case []any:
				var textBody map[string]any
				for _, rawBody := range body {
					candidate, _ := rawBody.(map[string]any)
					if candidate["type"] == "TextualBody" {
						textBody = candidate
						break
					}
				}
				if textBody == nil {
					continue
				}
				textBody["value"] = replacement
			default:
				continue
			}
			encoded, err := json.Marshal(page)
			if err != nil {
				t.Fatalf("encode edited annotation page: %v", err)
			}
			return string(encoded)
		}
	}
	t.Fatal("annotation page contained no TextualBody to edit")
	return ""
}

func firstAnnotationID(t *testing.T, raw string) string {
	t.Helper()
	var page struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &page); err != nil {
		t.Fatalf("decode annotation page ids: %v", err)
	}
	if len(page.Items) == 0 || strings.TrimSpace(page.Items[0].ID) == "" {
		t.Fatal("annotation page contained no identified items")
	}
	return page.Items[0].ID
}

func assertConnectCode(t *testing.T, err error, want connect.Code) {
	t.Helper()
	if err == nil || connect.CodeOf(err) != want {
		t.Fatalf("Connect error = %v/%v, want %v", connect.CodeOf(err), err, want)
	}
}

func tenantGET(t *testing.T, target, tenant string) (int, string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatalf("build tenant GET: %v", err)
	}
	request.Header.Set(testTenantHeader, tenant)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("tenant GET %s: %v", target, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read tenant GET %s: %v", target, err)
	}
	return response.StatusCode, string(body)
}

func TestPresentation3ArrayContextChoiceImportAndLibopsEmission(t *testing.T) {
	database := openTestDB(t)
	source, sourceManifest := newChoiceManifestSource(t)
	workspaceID, userID := createServerTestWorkspace(t, database)

	// Keep an explicit assertion on the source shape in addition to libops schema
	// validation so this cannot regress into a single-body fixture.
	var sourceDocument struct {
		Context []any `json:"@context"`
		Items   []struct {
			Items []struct {
				Items []struct {
					Body struct {
						Type  string `json:"type"`
						Items []any  `json:"items"`
					} `json:"body"`
				} `json:"items"`
			} `json:"items"`
		} `json:"items"`
	}
	if err := json.Unmarshal(sourceManifest, &sourceDocument); err != nil {
		t.Fatalf("decode source Choice manifest: %v", err)
	}
	choice := sourceDocument.Items[0].Items[0].Items[0].Body
	if len(sourceDocument.Context) != 2 || choice.Type != "Choice" || len(choice.Items) != 2 {
		t.Fatalf("source fixture lost array context or multi-entry Choice: context=%#v choice=%#v", sourceDocument.Context, choice)
	}

	ocrRuns := store.NewOCRRunStore(database)
	items := store.NewItemStore(database)
	contexts := store.NewContextStore(database)
	annotations := store.NewAnnotationStore(database)
	jobs := store.NewTranscriptionJobStore(database)
	handler := NewHandler(ocrRuns, items, contexts, annotations, jobs, nil, nil, nil)
	appServer := newTenantScopedServer(t, handler, map[string]testTenantIdentity{
		"workspace": {workspaceID: workspaceID, userID: userID},
	})

	itemClient := scribev1connect.NewItemServiceClient(http.DefaultClient, appServer.URL)
	annotationClient := scribev1connect.NewAnnotationServiceClient(http.DefaultClient, appServer.URL)
	created, err := itemClient.ImportManifest(context.Background(), tenantConnectRequest("workspace", &scribev1.ImportManifestRequest{
		Name:           "Presentation 3 Choice import",
		ManifestUrl:    source.URL + "/manifest",
		IdempotencyKey: "presentation-3-choice-import",
	}))
	if err != nil {
		t.Fatalf("ImportManifest Presentation 3 Choice import: %v", err)
	}
	item := created.Msg.GetItem()
	if item == nil || len(item.GetImages()) != 1 {
		t.Fatalf("ImportManifest images = %#v, want one imported Canvas", item)
	}
	t.Cleanup(func() {
		_ = items.DeleteForWorkspace(context.Background(), item.GetId(), workspaceID)
	})
	image := item.GetImages()[0]
	if image.GetImageUrl() != source.URL+"/images/color.jpg" {
		t.Fatalf("selected Choice image = %q, want first image resource", image.GetImageUrl())
	}
	importedCanvas := source.URL + "/canvas/1"
	if image.GetCanvasUri() != importedCanvas || image.GetWidth() != 2160 || image.GetHeight() != 3632 {
		t.Fatalf("imported Canvas = %#v", image)
	}
	storedItem, err := items.Get(context.Background(), item.GetId())
	if err != nil || !strings.Contains(storedItem.SourceManifest, `"rights":"http://creativecommons.org/licenses/by/4.0/"`) {
		t.Fatalf("bounded raw source Manifest was not retained: %v", err)
	}

	editorManifest, err := itemClient.GetEditorManifest(context.Background(), tenantConnectRequest("workspace", &scribev1.GetEditorManifestRequest{
		ItemImageId: image.GetId(),
	}))
	if err != nil {
		t.Fatalf("GetEditorManifest: %v", err)
	}
	emittedManifest := []byte(editorManifest.Msg.GetManifestJson())
	if err := iiif.ValidateManifest(emittedManifest); err != nil {
		t.Fatalf("emitted manifest failed libops validation: %v", err)
	}
	var emitted map[string]any
	if err := iiif.DecodeJSON(emittedManifest, &emitted); err != nil {
		t.Fatal(err)
	}
	if emitted["rights"] != "http://creativecommons.org/licenses/by/4.0/" || emitted["behavior"] == nil {
		t.Fatalf("emitted Manifest lost supported source properties: %#v", emitted)
	}
	emittedCanvas := emitted["items"].([]any)[0].(map[string]any)
	if emittedCanvas["id"] != importedCanvas || emittedCanvas["metadata"] == nil {
		t.Fatalf("emitted Canvas did not retain canonical identity and source metadata: %#v", emittedCanvas)
	}
	paintingPage := emittedCanvas["items"].([]any)[0].(map[string]any)
	paintingAnnotation := paintingPage["items"].([]any)[0].(map[string]any)
	wantPaintingPage, _ := iiif.PaintingPageID(handler.publicAnnotationBaseURL(), image.GetId())
	wantPaintingAnnotation, _ := iiif.PaintingAnnotationID(handler.publicAnnotationBaseURL(), image.GetId())
	if paintingPage["id"] != wantPaintingPage || paintingAnnotation["id"] != wantPaintingAnnotation || paintingAnnotation["target"] != importedCanvas {
		t.Fatalf("painting resource identity mismatch: page=%#v annotation=%#v", paintingPage, paintingAnnotation)
	}

	// These are embedded JSON-LD nodes, not independently dereferenceable
	// documents: they inherit the Manifest context validated above. Triplet's
	// standalone painting resources retain their own contexts and are validated
	// by the publication graph contract.
	if _, ok := paintingPage["@context"]; ok {
		t.Fatalf("embedded painting page redundantly defines @context: %#v", paintingPage)
	}
	if _, ok := paintingAnnotation["@context"]; ok {
		t.Fatalf("embedded painting Annotation redundantly defines @context: %#v", paintingAnnotation)
	}

	pageResponse, err := annotationClient.GetAnnotationPage(context.Background(), tenantConnectRequest("workspace", &scribev1.GetAnnotationPageRequest{
		ItemImageId: image.GetId(),
	}))
	if err != nil {
		t.Fatalf("GetAnnotationPage after Choice import: %v", err)
	}
	if err := iiif.ValidateAnnotationPage([]byte(pageResponse.Msg.GetAnnotationPageJson())); err != nil {
		t.Fatalf("emitted AnnotationPage failed libops/Text Granularity validation: %v", err)
	}
	if pageResponse.Msg.GetCanvasUri() != importedCanvas {
		t.Fatalf("emitted page Canvas = %q, want imported target/provenance", pageResponse.Msg.GetCanvasUri())
	}
	var canonicalPage map[string]any
	if err := iiif.DecodeJSON([]byte(pageResponse.Msg.GetAnnotationPageJson()), &canonicalPage); err != nil {
		t.Fatal(err)
	}
	pageItems := canonicalPage["items"].([]any)
	if len(pageItems) == 0 {
		t.Fatal("imported hOCR did not create a canonical child Annotation")
	}
	childID := pageItems[0].(map[string]any)["id"].(string)
	childPayload, err := iiif.AnnotationFromPage([]byte(pageResponse.Msg.GetAnnotationPageJson()), childID)
	if err != nil {
		t.Fatalf("materialize canonical child Annotation: %v", err)
	}
	if err := iiif.ValidateAnnotation(childPayload); err != nil {
		t.Fatalf("canonical child failed standalone libops validation: %v", err)
	}
}

func TestImportedCanvasIdentityIsProvenanceNotTenantKey(t *testing.T) {
	database := openTestDB(t)
	source, _ := newChoiceManifestSource(t)
	workspaceA, userA := createServerTestWorkspace(t, database)
	workspaceB, userB := createServerTestWorkspace(t, database)
	items := store.NewItemStore(database)
	handler := NewHandler(
		store.NewOCRRunStore(database), items, store.NewContextStore(database), store.NewAnnotationStore(database),
		store.NewTranscriptionJobStore(database), nil, nil, nil,
	)
	appServer := newTenantScopedServer(t, handler, map[string]testTenantIdentity{
		"a": {workspaceID: workspaceA, userID: userA},
		"b": {workspaceID: workspaceB, userID: userB},
	})
	client := scribev1connect.NewItemServiceClient(http.DefaultClient, appServer.URL)
	annotationClient := scribev1connect.NewAnnotationServiceClient(http.DefaultClient, appServer.URL)
	importFor := func(tenant string) *scribev1.Item {
		t.Helper()
		response, err := client.ImportManifest(context.Background(), tenantConnectRequest(tenant, &scribev1.ImportManifestRequest{
			Name: "Shared source", ManifestUrl: source.URL + "/manifest", IdempotencyKey: "same-key-is-workspace-scoped",
		}))
		if err != nil {
			t.Fatalf("ImportManifest tenant %s: %v", tenant, err)
		}
		return response.Msg.GetItem()
	}
	itemA := importFor("a")
	itemB := importFor("b")
	t.Cleanup(func() {
		_ = items.DeleteForWorkspace(context.Background(), itemA.GetId(), workspaceA)
		_ = items.DeleteForWorkspace(context.Background(), itemB.GetId(), workspaceB)
	})
	imageA, imageB := itemA.GetImages()[0], itemB.GetImages()[0]
	wantCanvas := source.URL + "/canvas/1"
	if imageA.GetId() == imageB.GetId() || imageA.GetCanvasUri() != wantCanvas || imageB.GetCanvasUri() != wantCanvas {
		t.Fatalf("shared Canvas/import identities = A %#v / B %#v", imageA, imageB)
	}
	pageA, err := annotationClient.GetAnnotationPage(context.Background(), tenantConnectRequest("a", &scribev1.GetAnnotationPageRequest{ItemImageId: imageA.GetId()}))
	if err != nil {
		t.Fatal(err)
	}
	pageB, err := annotationClient.GetAnnotationPage(context.Background(), tenantConnectRequest("b", &scribev1.GetAnnotationPageRequest{ItemImageId: imageB.GetId()}))
	if err != nil {
		t.Fatal(err)
	}
	pageIDA, _ := iiif.AnnotationPageID(handler.publicAnnotationBaseURL(), imageA.GetId())
	pageIDB, _ := iiif.AnnotationPageID(handler.publicAnnotationBaseURL(), imageB.GetId())
	if pageIDA == pageIDB || !strings.Contains(pageA.Msg.GetAnnotationPageJson(), pageIDA) || !strings.Contains(pageB.Msg.GetAnnotationPageJson(), pageIDB) || pageA.Msg.GetCanvasUri() != wantCanvas || pageB.Msg.GetCanvasUri() != wantCanvas {
		t.Fatalf("tenant pages did not separate ownership from target: A %#v / B %#v", pageA.Msg, pageB.Msg)
	}
	if _, err := annotationClient.GetAnnotationPage(context.Background(), tenantConnectRequest("a", &scribev1.GetAnnotationPageRequest{ItemImageId: imageB.GetId()})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("tenant A read tenant B page error = %v, want not_found", err)
	}
	initialExport, err := annotationClient.ExportAnnotationPage(context.Background(), tenantConnectRequest("a", &scribev1.ExportAnnotationPageRequest{
		ItemImageId: imageA.GetId(), ExpectedRevision: pageA.Msg.GetRevision(), Format: scribev1.AnnotationExportFormat_ANNOTATION_EXPORT_FORMAT_PLAIN_TEXT,
	}))
	if err != nil || initialExport.Msg.GetRevision() != pageA.Msg.GetRevision() || len(initialExport.Msg.GetContent()) == 0 {
		t.Fatalf("tenant A exact-revision export = %#v/%v", initialExport, err)
	}
	if _, err := annotationClient.ExportAnnotationPage(context.Background(), tenantConnectRequest("a", &scribev1.ExportAnnotationPageRequest{
		ItemImageId: imageB.GetId(), ExpectedRevision: pageB.Msg.GetRevision(), Format: scribev1.AnnotationExportFormat_ANNOTATION_EXPORT_FORMAT_PLAIN_TEXT,
	})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("tenant A export of tenant B page error = %v, want not_found", err)
	}
	const correctedExportText = "tenant A exact canonical export"
	savedA, err := annotationClient.SaveAnnotationPage(context.Background(), tenantConnectRequest("a", &scribev1.SaveAnnotationPageRequest{
		ItemImageId: imageA.GetId(), AnnotationPageJson: replaceFirstAnnotationText(t, pageA.Msg.GetAnnotationPageJson(), correctedExportText), ExpectedRevision: pageA.Msg.GetRevision(),
	}))
	if err != nil {
		t.Fatalf("save tenant A export correction: %v", err)
	}
	if _, err := annotationClient.ExportAnnotationPage(context.Background(), tenantConnectRequest("a", &scribev1.ExportAnnotationPageRequest{
		ItemImageId: imageA.GetId(), ExpectedRevision: pageA.Msg.GetRevision(), Format: scribev1.AnnotationExportFormat_ANNOTATION_EXPORT_FORMAT_PLAIN_TEXT,
	})); connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("stale canonical export error = %v, want aborted", err)
	}
	currentExport, err := annotationClient.ExportAnnotationPage(context.Background(), tenantConnectRequest("a", &scribev1.ExportAnnotationPageRequest{
		ItemImageId: imageA.GetId(), ExpectedRevision: savedA.Msg.GetRevision(), Format: scribev1.AnnotationExportFormat_ANNOTATION_EXPORT_FORMAT_PLAIN_TEXT,
	}))
	if err != nil || currentExport.Msg.GetRevision() != savedA.Msg.GetRevision() || !strings.Contains(string(currentExport.Msg.GetContent()), correctedExportText) {
		t.Fatalf("current canonical export = %#v/%v", currentExport, err)
	}
	tenantBExport, err := annotationClient.ExportAnnotationPage(context.Background(), tenantConnectRequest("b", &scribev1.ExportAnnotationPageRequest{
		ItemImageId: imageB.GetId(), ExpectedRevision: pageB.Msg.GetRevision(), Format: scribev1.AnnotationExportFormat_ANNOTATION_EXPORT_FORMAT_PLAIN_TEXT,
	}))
	if err != nil || strings.Contains(string(tenantBExport.Msg.GetContent()), correctedExportText) {
		t.Fatalf("tenant B canonical export leaked tenant A correction: %#v/%v", tenantBExport, err)
	}
	if _, err := client.GetEditorManifest(context.Background(), tenantConnectRequest("a", &scribev1.GetEditorManifestRequest{
		ItemImageId: imageB.GetId(),
	})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("tenant A editor-manifest access to tenant B error = %v, want not_found", err)
	}
	var duplicates int
	if err := database.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM item_images WHERE canvas_uri = ?`, wantCanvas).Scan(&duplicates); err != nil || duplicates != 2 {
		t.Fatalf("shared Canvas row count = %d/%v, want two workspace-owned images", duplicates, err)
	}
}

func TestPresentation3ChoiceWithoutHOCRCreatesEmptyCanonicalPageAndIdempotentJob(t *testing.T) {
	database := openTestDB(t)
	source, _ := newChoiceManifestSourceWithoutHOCR(t)
	workspaceID, userID := createServerTestWorkspace(t, database)
	contexts := store.NewContextStore(database)
	processingContext, err := contexts.Create(context.Background(), store.Context{
		UserID: &userID, WorkspaceID: &workspaceID, Name: "no-hocr-choice-context",
		SegmentationModel: "layout", TranscriptionProvider: "tesseract", TranscriptionModel: "eng",
	})
	if err != nil {
		t.Fatalf("create no-hOCR context: %v", err)
	}
	ocrRuns := store.NewOCRRunStore(database)
	items := store.NewItemStore(database)
	annotations := store.NewAnnotationStore(database)
	jobs := store.NewTranscriptionJobStore(database)
	handler := NewHandler(ocrRuns, items, contexts, annotations, jobs, nil, nil, nil)
	fakeOCR := &imageOnlyWorkerOCR{}
	handler.ocr = fakeOCR
	handler.imageRegionFetcher = func(context.Context, string, int, int, int, int) (string, func(), error) {
		return "test-image-region", func() {}, nil
	}
	appServer := newTenantScopedServer(t, handler, map[string]testTenantIdentity{
		"workspace": {workspaceID: workspaceID, userID: userID},
	})
	itemClient := scribev1connect.NewItemServiceClient(http.DefaultClient, appServer.URL)
	annotationClient := scribev1connect.NewAnnotationServiceClient(http.DefaultClient, appServer.URL)
	request := &scribev1.ImportManifestRequest{
		Name: "Image-only Choice", ManifestUrl: source.URL + "/manifest",
		ContextId: processingContext.ID, IdempotencyKey: "no-hocr-choice-import",
	}
	created, err := itemClient.ImportManifest(context.Background(), tenantConnectRequest("workspace", request))
	if err != nil {
		t.Fatalf("ImportManifest no-hOCR Choice: %v", err)
	}
	item := created.Msg.GetItem()
	if item == nil || len(item.GetImages()) != 1 {
		t.Fatalf("created no-hOCR item = %#v", item)
	}
	image := item.GetImages()[0]
	pageResponse, err := annotationClient.GetAnnotationPage(context.Background(), tenantConnectRequest("workspace", &scribev1.GetAnnotationPageRequest{ItemImageId: image.GetId()}))
	if err != nil {
		t.Fatalf("GetAnnotationPage no-hOCR Choice: %v", err)
	}
	if err := iiif.ValidateAnnotationPage([]byte(pageResponse.Msg.GetAnnotationPageJson())); err != nil {
		t.Fatalf("empty canonical page failed libops validation: %v", err)
	}
	var page struct {
		Items []any `json:"items"`
	}
	if err := json.Unmarshal([]byte(pageResponse.Msg.GetAnnotationPageJson()), &page); err != nil || len(page.Items) != 0 {
		t.Fatalf("empty canonical page items = %#v/%v", page.Items, err)
	}
	editorManifest, err := itemClient.GetEditorManifest(context.Background(), tenantConnectRequest("workspace", &scribev1.GetEditorManifestRequest{
		ItemImageId: image.GetId(),
	}))
	if err != nil {
		t.Fatalf("GetEditorManifest before OCR: %v", err)
	}
	imageOnlyManifest := editorManifest.Msg.GetManifestJson()
	if strings.Contains(imageOnlyManifest, `"seeAlso"`) {
		t.Fatalf("empty image-only manifest advertised a nonexistent hOCR export: %s", imageOnlyManifest)
	}
	if err := iiif.ValidateManifest([]byte(imageOnlyManifest)); err != nil {
		t.Fatalf("image-only manifest before OCR failed libops validation: %v", err)
	}
	active, err := jobs.GetActiveByItemImage(context.Background(), image.GetId())
	if err != nil || active.Status != store.TranscriptionJobStatusPending || active.InputRevision != pageResponse.Msg.GetRevision() {
		t.Fatalf("no-hOCR transcription job = %+v/%v", active, err)
	}
	if err := handler.processQueuedTranscriptionJob(context.Background(), active.ID); err != nil {
		t.Fatalf("process image-only transcription job: %v", err)
	}
	completedJob, err := jobs.Get(context.Background(), active.ID)
	if err != nil {
		t.Fatalf("reload completed image-only job: %v", err)
	}
	if completedJob.Status != store.TranscriptionJobStatusCompleted || completedJob.TotalSegments != 1 || completedJob.CompletedSegments != 1 || completedJob.FailedSegments != 0 {
		t.Fatalf("completed image-only job = %+v", completedJob)
	}
	if fakeOCR.segmentCalls != 1 || fakeOCR.transcriptionCalls != 1 || !fakeOCR.lastSegmentation.SegmentOnly {
		t.Fatalf("image-only processing calls/context = segment %d transcription %d context %+v", fakeOCR.segmentCalls, fakeOCR.transcriptionCalls, fakeOCR.lastSegmentation)
	}
	processed, err := annotationClient.GetAnnotationPage(context.Background(), tenantConnectRequest("workspace", &scribev1.GetAnnotationPageRequest{ItemImageId: image.GetId()}))
	if err != nil {
		t.Fatalf("reload processed image-only page: %v", err)
	}
	if processed.Msg.GetRevision() != pageResponse.Msg.GetRevision()+1 {
		t.Fatalf("processed page revision = %d, want %d", processed.Msg.GetRevision(), pageResponse.Msg.GetRevision()+1)
	}
	storedProcessed, err := annotations.LoadPage(context.Background(), workspaceID, image.GetId())
	if err != nil {
		t.Fatalf("load worker-saved canonical page: %v", err)
	}
	if storedProcessed.UpdatedByUserID != nil {
		t.Fatalf("worker save attributed to item creator %d; want explicit system attribution", *storedProcessed.UpdatedByUserID)
	}
	if err := iiif.ValidateAnnotationPage([]byte(processed.Msg.GetAnnotationPageJson())); err != nil {
		t.Fatalf("processed image-only page failed libops validation: %v", err)
	}
	var processedDocument struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal([]byte(processed.Msg.GetAnnotationPageJson()), &processedDocument); err != nil {
		t.Fatalf("decode processed image-only page: %v", err)
	}
	wordCount := 0
	for _, annotation := range processedDocument.Items {
		if strings.EqualFold(strings.TrimSpace(fmt.Sprint(annotation["textGranularity"])), "word") {
			wordCount++
		}
	}
	if wordCount != 3 || !strings.Contains(processed.Msg.GetAnnotationPageJson(), "final") || !strings.Contains(processed.Msg.GetAnnotationPageJson(), "corrected") {
		t.Fatalf("processed page words/text = %d/%s", wordCount, processed.Msg.GetAnnotationPageJson())
	}

	editedJSON := replaceFirstAnnotationText(t, processed.Msg.GetAnnotationPageJson(), "reviewed")
	saved, err := annotationClient.SaveAnnotationPage(context.Background(), tenantConnectRequest("workspace", &scribev1.SaveAnnotationPageRequest{
		ItemImageId: image.GetId(), AnnotationPageJson: editedJSON, ExpectedRevision: processed.Msg.GetRevision(),
	}))
	if err != nil {
		t.Fatalf("save processed image-only correction: %v", err)
	}
	reloaded, err := annotationClient.GetAnnotationPage(context.Background(), tenantConnectRequest("workspace", &scribev1.GetAnnotationPageRequest{ItemImageId: image.GetId()}))
	if err != nil || reloaded.Msg.GetRevision() != saved.Msg.GetRevision() || !strings.Contains(reloaded.Msg.GetAnnotationPageJson(), "reviewed") {
		t.Fatalf("save/reload image-only correction = %#v/%v", reloaded, err)
	}
	for _, format := range []string{"txt", "hocr", "pagexml", "alto"} {
		status, exported := tenantAnnotationExport(t, annotationClient, "workspace", image.GetId(), saved.Msg.GetRevision(), format)
		if status != http.StatusOK || !strings.Contains(exported, "reviewed") || !strings.Contains(exported, "corrected") || !strings.Contains(exported, "text") {
			t.Fatalf("processed image-only %s export status/body = %d/%q", format, status, exported)
		}
	}

	segmentCalls, transcriptionCalls := fakeOCR.segmentCalls, fakeOCR.transcriptionCalls
	if err := handler.processQueuedTranscriptionJob(context.Background(), active.ID); err != nil {
		t.Fatalf("replay completed image-only job: %v", err)
	}
	if fakeOCR.segmentCalls != segmentCalls || fakeOCR.transcriptionCalls != transcriptionCalls {
		t.Fatal("completed job replay invoked providers")
	}
	replayed, err := itemClient.ImportManifest(context.Background(), tenantConnectRequest("workspace", request))
	if err != nil || replayed.Msg.GetItem().GetId() != item.GetId() {
		t.Fatalf("idempotent no-hOCR replay = %#v/%v", replayed, err)
	}
	var itemCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM items WHERE workspace_id = ? AND source_url = ?`, workspaceID, source.URL+"/manifest").Scan(&itemCount); err != nil || itemCount != 1 {
		t.Fatalf("manifest item count = %d/%v, want 1", itemCount, err)
	}

	createAnother := func(name, key string) (*scribev1.Item, store.TranscriptionJob) {
		t.Helper()
		response, createErr := itemClient.ImportManifest(context.Background(), tenantConnectRequest("workspace", &scribev1.ImportManifestRequest{
			Name: name, ManifestUrl: source.URL + "/manifest", ContextId: processingContext.ID, IdempotencyKey: key,
		}))
		if createErr != nil {
			t.Fatalf("ImportManifest %s: %v", name, createErr)
		}
		createdItem := response.Msg.GetItem()
		createdJob, jobErr := jobs.GetActiveByItemImage(context.Background(), createdItem.GetImages()[0].GetId())
		if jobErr != nil {
			t.Fatalf("load %s job: %v", name, jobErr)
		}
		return createdItem, createdJob
	}
	_, retryJob := createAnother("Image-only retry", "no-hocr-choice-retry")
	fakeOCR.failNextSegmentation = true
	if err := handler.processQueuedTranscriptionJob(context.Background(), retryJob.ID); err == nil {
		t.Fatal("transient segmentation attempt unexpectedly succeeded")
	}
	retryPending, err := jobs.Get(context.Background(), retryJob.ID)
	if err != nil || retryPending.Status != store.TranscriptionJobStatusPending || retryPending.AttemptCount != 1 {
		t.Fatalf("retryable image-only job = %+v/%v", retryPending, err)
	}
	if _, err := database.ExecContext(context.Background(), `
UPDATE transcription_jobs
SET retry_after = DATE_SUB(NOW(), INTERVAL 1 SECOND)
WHERE id = ?`, retryJob.ID); err != nil {
		t.Fatalf("make image-only retry due: %v", err)
	}
	if err := handler.processQueuedTranscriptionJob(context.Background(), retryJob.ID); err != nil {
		t.Fatalf("retry image-only job: %v", err)
	}
	retryCompleted, err := jobs.Get(context.Background(), retryJob.ID)
	if err != nil || retryCompleted.Status != store.TranscriptionJobStatusCompleted || retryCompleted.AttemptCount != 2 {
		t.Fatalf("retried image-only job = %+v/%v", retryCompleted, err)
	}

	canceledItem, canceledJob := createAnother("Image-only cancel", "no-hocr-choice-cancel")
	providerCallsBeforeCancel := fakeOCR.segmentCalls + fakeOCR.transcriptionCalls
	if err := jobs.Cancel(context.Background(), canceledJob.ID); err != nil {
		t.Fatalf("cancel pending image-only job: %v", err)
	}
	if err := handler.processQueuedTranscriptionJob(context.Background(), canceledJob.ID); err != nil {
		t.Fatalf("process canceled image-only job: %v", err)
	}
	if fakeOCR.segmentCalls+fakeOCR.transcriptionCalls != providerCallsBeforeCancel {
		t.Fatal("canceled image-only job invoked providers")
	}
	canceledPage, err := annotations.LoadPage(context.Background(), workspaceID, canceledItem.GetImages()[0].GetId())
	if err != nil || canceledPage.Revision != 1 || strings.Contains(canceledPage.Payload, "final corrected text") {
		t.Fatalf("canceled image-only page = %+v/%v", canceledPage, err)
	}
}

func TestSameManifestIsIsolatedAcrossWorkspaces(t *testing.T) {
	database := openTestDB(t)
	source, _ := newChoiceManifestSource(t)
	workspaceA, userA := createServerTestWorkspace(t, database)
	workspaceB, userB := createServerTestWorkspace(t, database)

	ocrRuns := store.NewOCRRunStore(database)
	items := store.NewItemStore(database)
	contexts := store.NewContextStore(database)
	annotations := store.NewAnnotationStore(database)
	jobs := store.NewTranscriptionJobStore(database)
	handler := NewHandler(ocrRuns, items, contexts, annotations, jobs, nil, nil, nil)
	appServer := newTenantScopedServer(t, handler, map[string]testTenantIdentity{
		"workspace-a": {workspaceID: workspaceA, userID: userA},
		"workspace-b": {workspaceID: workspaceB, userID: userB},
	})
	itemClient := scribev1connect.NewItemServiceClient(http.DefaultClient, appServer.URL)
	annotationClient := scribev1connect.NewAnnotationServiceClient(http.DefaultClient, appServer.URL)

	createItem := func(tenant, name string) *scribev1.Item {
		t.Helper()
		response, err := itemClient.ImportManifest(context.Background(), tenantConnectRequest(tenant, &scribev1.ImportManifestRequest{
			Name:           name,
			ManifestUrl:    source.URL + "/manifest",
			IdempotencyKey: "shared-manifest-across-workspaces",
		}))
		if err != nil {
			t.Fatalf("%s ImportManifest: %v", tenant, err)
		}
		if response.Msg.GetItem() == nil || len(response.Msg.GetItem().GetImages()) != 1 {
			t.Fatalf("%s ImportManifest response = %#v", tenant, response.Msg)
		}
		return response.Msg.GetItem()
	}
	itemA := createItem("workspace-a", "Workspace A manuscript")
	itemB := createItem("workspace-b", "Workspace B manuscript")
	t.Cleanup(func() {
		_ = items.DeleteForWorkspace(context.Background(), itemA.GetId(), workspaceA)
		_ = items.DeleteForWorkspace(context.Background(), itemB.GetId(), workspaceB)
	})
	imageA := itemA.GetImages()[0]
	imageB := itemB.GetImages()[0]
	if imageA.GetId() == imageB.GetId() || imageA.GetCanvasUri() != imageB.GetCanvasUri() {
		t.Fatalf("same-manifest identities = image %d/%d canvas %q/%q", imageA.GetId(), imageB.GetId(), imageA.GetCanvasUri(), imageB.GetCanvasUri())
	}

	getPage := func(tenant string, itemImageID uint64) (*connect.Response[scribev1.GetAnnotationPageResponse], error) {
		return annotationClient.GetAnnotationPage(context.Background(), tenantConnectRequest(tenant, &scribev1.GetAnnotationPageRequest{ItemImageId: itemImageID}))
	}
	pageA, err := getPage("workspace-a", imageA.GetId())
	if err != nil {
		t.Fatalf("workspace A read own page: %v", err)
	}
	pageB, err := getPage("workspace-b", imageB.GetId())
	if err != nil {
		t.Fatalf("workspace B read own page: %v", err)
	}
	_, err = getPage("workspace-a", imageB.GetId())
	assertConnectCode(t, err, connect.CodeNotFound)
	_, err = getPage("workspace-b", imageA.GetId())
	assertConnectCode(t, err, connect.CodeNotFound)
	if pageA.Msg.GetAnnotationPageJson() == pageB.Msg.GetAnnotationPageJson() {
		t.Fatal("workspace pages unexpectedly share canonical page identity")
	}

	const workspaceAEdit = "Workspace A private correction"
	editedPageA := replaceFirstAnnotationText(t, pageA.Msg.GetAnnotationPageJson(), workspaceAEdit)
	savedA, err := annotationClient.SaveAnnotationPage(context.Background(), tenantConnectRequest("workspace-a", &scribev1.SaveAnnotationPageRequest{
		ItemImageId:        imageA.GetId(),
		AnnotationPageJson: editedPageA,
		ExpectedRevision:   pageA.Msg.GetRevision(),
	}))
	if err != nil {
		t.Fatalf("workspace A save own page: %v", err)
	}
	if savedA.Msg.GetRevision() <= pageA.Msg.GetRevision() || !strings.Contains(savedA.Msg.GetAnnotationPageJson(), workspaceAEdit) {
		t.Fatalf("workspace A save did not commit correction: %#v", savedA.Msg)
	}

	_, err = annotationClient.SaveAnnotationPage(context.Background(), tenantConnectRequest("workspace-b", &scribev1.SaveAnnotationPageRequest{
		ItemImageId:        imageA.GetId(),
		AnnotationPageJson: savedA.Msg.GetAnnotationPageJson(),
		ExpectedRevision:   savedA.Msg.GetRevision(),
	}))
	assertConnectCode(t, err, connect.CodeNotFound)
	_, err = annotationClient.SaveAnnotationPage(context.Background(), tenantConnectRequest("workspace-a", &scribev1.SaveAnnotationPageRequest{
		ItemImageId:        imageB.GetId(),
		AnnotationPageJson: pageB.Msg.GetAnnotationPageJson(),
		ExpectedRevision:   pageB.Msg.GetRevision(),
	}))
	assertConnectCode(t, err, connect.CodeNotFound)
	reloadedA, err := getPage("workspace-a", imageA.GetId())
	if err != nil {
		t.Fatalf("reload workspace A after denied mutation: %v", err)
	}
	if reloadedA.Msg.GetRevision() != savedA.Msg.GetRevision() || !strings.Contains(reloadedA.Msg.GetAnnotationPageJson(), workspaceAEdit) {
		t.Fatal("denied cross-workspace mutation changed workspace A page")
	}
	reloadedB, err := getPage("workspace-b", imageB.GetId())
	if err != nil {
		t.Fatalf("reload workspace B page: %v", err)
	}
	if strings.Contains(reloadedB.Msg.GetAnnotationPageJson(), workspaceAEdit) {
		t.Fatal("workspace A correction leaked into workspace B canonical page")
	}

	searchCanvas := func(tenant string, itemImageID uint64) (*connect.Response[scribev1.SearchAnnotationsResponse], error) {
		return annotationClient.SearchAnnotations(context.Background(), tenantConnectRequest(tenant, &scribev1.SearchAnnotationsRequest{
			ItemImageId: itemImageID,
			CanvasUri:   imageA.GetCanvasUri(),
		}))
	}
	searchA, err := searchCanvas("workspace-a", imageA.GetId())
	if err != nil {
		t.Fatalf("workspace A search shared Canvas: %v", err)
	}
	searchB, err := searchCanvas("workspace-b", imageB.GetId())
	if err != nil {
		t.Fatalf("workspace B search shared Canvas: %v", err)
	}
	if !strings.Contains(searchA.Msg.GetAnnotationPageJson(), workspaceAEdit) || strings.Contains(searchB.Msg.GetAnnotationPageJson(), workspaceAEdit) {
		t.Fatal("shared Canvas search was not scoped to each workspace's canonical page")
	}
	_, err = annotationClient.SearchAnnotations(context.Background(), tenantConnectRequest("workspace-a", &scribev1.SearchAnnotationsRequest{
		ItemImageId: imageA.GetId(),
		CanvasUri:   imageA.GetCanvasUri() + "&mismatch=true",
	}))
	assertConnectCode(t, err, connect.CodeNotFound)
	_, err = annotationClient.SearchAnnotations(context.Background(), tenantConnectRequest("workspace-b", &scribev1.SearchAnnotationsRequest{
		ItemImageId: imageA.GetId(),
	}))
	assertConnectCode(t, err, connect.CodeNotFound)
	_, err = annotationClient.SearchAnnotations(context.Background(), tenantConnectRequest("workspace-a", &scribev1.SearchAnnotationsRequest{
		ItemImageId: imageB.GetId(),
	}))
	assertConnectCode(t, err, connect.CodeNotFound)

	indexA, err := annotations.SearchIndex(context.Background(), workspaceA, imageA.GetId())
	if err != nil || len(indexA) == 0 {
		t.Fatalf("workspace A derived index = %d/%v", len(indexA), err)
	}
	crossIndex, err := annotations.SearchIndex(context.Background(), workspaceB, imageA.GetId())
	if err != nil {
		t.Fatalf("read cross-workspace derived index: %v", err)
	}
	if len(crossIndex) != 0 {
		t.Fatalf("workspace B saw %d workspace A index rows", len(crossIndex))
	}
	crossIndex, err = annotations.SearchIndex(context.Background(), workspaceA, imageB.GetId())
	if err != nil {
		t.Fatalf("read reverse cross-workspace derived index: %v", err)
	}
	if len(crossIndex) != 0 {
		t.Fatalf("workspace A saw %d workspace B index rows", len(crossIndex))
	}
	annotationID := firstAnnotationID(t, savedA.Msg.GetAnnotationPageJson())
	_, err = annotationClient.GetAnnotation(context.Background(), tenantConnectRequest("workspace-b", &scribev1.GetAnnotationRequest{ItemImageId: imageA.GetId(), Id: annotationID}))
	assertConnectCode(t, err, connect.CodeNotFound)
	annotationIDB := firstAnnotationID(t, pageB.Msg.GetAnnotationPageJson())
	_, err = annotationClient.GetAnnotation(context.Background(), tenantConnectRequest("workspace-a", &scribev1.GetAnnotationRequest{ItemImageId: imageB.GetId(), Id: annotationIDB}))
	assertConnectCode(t, err, connect.CodeNotFound)

	for _, format := range []string{"txt", "hocr", "pagexml", "alto"} {
		status, exported := tenantAnnotationExport(t, annotationClient, "workspace-a", imageA.GetId(), savedA.Msg.GetRevision(), format)
		if status != http.StatusOK || !strings.Contains(exported, workspaceAEdit) {
			t.Fatalf("workspace A %s export status/body = %d/%q", format, status, exported)
		}
		status, exported = tenantAnnotationExport(t, annotationClient, "workspace-b", imageA.GetId(), savedA.Msg.GetRevision(), format)
		if status != http.StatusNotFound || strings.Contains(exported, workspaceAEdit) {
			t.Fatalf("workspace B cross %s export status/body = %d/%q", format, status, exported)
		}
	}
	hocrAURL := fmt.Sprintf("%s/v1/item-images/%d/annotations/revisions/%d/hocr", appServer.URL, imageA.GetId(), savedA.Msg.GetRevision())
	status, canonicalHOCR := tenantGET(t, hocrAURL, "workspace-a")
	if status != http.StatusOK || !strings.Contains(canonicalHOCR, workspaceAEdit) {
		t.Fatalf("workspace A canonical hOCR status/body = %d/%q", status, canonicalHOCR)
	}
	status, canonicalHOCR = tenantGET(t, hocrAURL, "workspace-b")
	if status != http.StatusNotFound || strings.Contains(canonicalHOCR, workspaceAEdit) {
		t.Fatalf("workspace B cross hOCR status/body = %d/%q", status, canonicalHOCR)
	}
	status, _ = tenantAnnotationExport(t, annotationClient, "workspace-b", imageB.GetId(), pageB.Msg.GetRevision(), "txt")
	if status != http.StatusOK {
		t.Fatalf("workspace B own export status = %d", status)
	}
	status, _ = tenantAnnotationExport(t, annotationClient, "workspace-a", imageB.GetId(), pageB.Msg.GetRevision(), "txt")
	if status != http.StatusNotFound {
		t.Fatalf("workspace A reverse cross export status = %d", status)
	}

	publishedA, err := annotationClient.PublishItemImageEdits(context.Background(), tenantConnectRequest("workspace-a", &scribev1.PublishItemImageEditsRequest{
		ItemImageId:      imageA.GetId(),
		ExpectedRevision: savedA.Msg.GetRevision(),
	}))
	if err != nil {
		t.Fatalf("workspace A publish own page: %v", err)
	}
	if !strings.Contains(publishedA.Msg.GetAnnotationPageJson(), workspaceAEdit) {
		t.Fatal("workspace A publication did not read its canonical revision")
	}
	resources, err := handler.buildPublishedPresentationResources(context.Background(), imageA.GetId())
	if err != nil {
		t.Fatalf("build published Triplet graph: %v", err)
	}
	var publicPage []byte
	for _, resource := range resources {
		if resource.ID == publishedA.Msg.GetPublicUrl() {
			publicPage = resource.Payload
			break
		}
	}
	if len(publicPage) == 0 || !strings.Contains(string(publicPage), workspaceAEdit) {
		t.Fatalf("published canonical graph page = %q", publicPage)
	}
	_, err = annotationClient.PublishItemImageEdits(context.Background(), tenantConnectRequest("workspace-b", &scribev1.PublishItemImageEditsRequest{
		ItemImageId:      imageA.GetId(),
		ExpectedRevision: savedA.Msg.GetRevision(),
	}))
	assertConnectCode(t, err, connect.CodeNotFound)
	if _, err := annotationClient.PublishItemImageEdits(context.Background(), tenantConnectRequest("workspace-b", &scribev1.PublishItemImageEditsRequest{
		ItemImageId:      imageB.GetId(),
		ExpectedRevision: pageB.Msg.GetRevision(),
	})); err != nil {
		t.Fatalf("workspace B publish own page: %v", err)
	}
	_, err = annotationClient.PublishItemImageEdits(context.Background(), tenantConnectRequest("workspace-a", &scribev1.PublishItemImageEditsRequest{
		ItemImageId:      imageB.GetId(),
		ExpectedRevision: pageB.Msg.GetRevision(),
	}))
	assertConnectCode(t, err, connect.CodeNotFound)
}

func TestCanonicalHOCRAndExportDoNotFallbackToOCRRun(t *testing.T) {
	database := openTestDB(t)
	image := createServerTestItemImage(
		t,
		database,
		store.AnonymousWorkspaceID,
		store.AnonymousUserID,
		"https://source.example/canvas/no-canonical-page-"+strconv.FormatUint(store.AnonymousWorkspaceID, 10),
	)
	const provenanceOnly = "PROVENANCE_ONLY_SENTINEL"
	ocrRuns := store.NewOCRRunStore(database)
	if err := ocrRuns.Create(context.Background(), store.OCRRun{
		SessionID:    "no-canonical-page-" + strconv.FormatUint(image.ID, 10),
		ItemImageID:  &image.ID,
		ImageURL:     image.ImageURL,
		Provider:     "test",
		Model:        "test",
		OriginalHOCR: provenanceOnly,
		OriginalText: provenanceOnly,
	}); err != nil {
		t.Fatalf("create provenance-only OCR run: %v", err)
	}

	items := store.NewItemStore(database)
	contexts := store.NewContextStore(database)
	annotations := store.NewAnnotationStore(database)
	jobs := store.NewTranscriptionJobStore(database)
	handler := NewHandler(ocrRuns, items, contexts, annotations, jobs, nil, nil, nil)
	appServer := httptest.NewServer(handler)
	t.Cleanup(appServer.Close)

	for name, target := range map[string]string{
		"hocr": fmt.Sprintf("%s/v1/item-images/%d/annotations/revisions/1/hocr", appServer.URL, image.ID),
	} {
		t.Run(name, func(t *testing.T) {
			response, err := http.Get(target)
			if err != nil {
				t.Fatalf("GET %s: %v", name, err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatalf("read %s response: %v", name, err)
			}
			if response.StatusCode == http.StatusOK {
				t.Fatalf("%s returned 200 without a canonical AnnotationPage: %s", name, body)
			}
			if strings.Contains(string(body), provenanceOnly) {
				t.Fatalf("%s leaked OCR-run provenance as canonical output: %s", name, body)
			}
		})
	}
	annotationClient := scribev1connect.NewAnnotationServiceClient(http.DefaultClient, appServer.URL)
	status, exportBody := tenantAnnotationExport(t, annotationClient, "", image.ID, 1, "hocr")
	if status == http.StatusOK || strings.Contains(exportBody, provenanceOnly) {
		t.Fatalf("canonical export without a page status/body = %d/%q", status, exportBody)
	}
}

func TestCanonicalHOCRAndExportDoNotRequireOCRRun(t *testing.T) {
	database := openTestDB(t)
	canvasURI := "https://source.example/canvas/canonical-without-run-" + uuid.NewString()
	image := createServerTestItemImage(
		t,
		database,
		store.AnonymousWorkspaceID,
		store.AnonymousUserID,
		canvasURI,
	)
	const canonicalOnly = "CANONICAL_ONLY_SENTINEL"
	pageID, err := iiif.CanonicalPageID("http://localhost:8080", image.ID)
	if err != nil {
		t.Fatalf("build canonical page id: %v", err)
	}
	annotationID, err := iiif.AnnotationID(pageID, "line-1")
	if err != nil {
		t.Fatalf("build canonical Annotation id: %v", err)
	}
	pagePayload, err := iiif.NewAnnotationPage(iiif.PageIdentity{
		PublicBaseURL: "http://localhost:8080",
		ItemImageID:   image.ID,
		CanvasURI:     canvasURI,
	}, []any{map[string]any{
		"id":              annotationID,
		"type":            "Annotation",
		"motivation":      "supplementing",
		"textGranularity": "line",
		"body": []any{map[string]any{
			"type": "TextualBody", "purpose": "supplementing", "format": "text/plain", "value": canonicalOnly,
		}},
		"target": map[string]any{
			"source": map[string]any{"id": canvasURI, "type": "Canvas"},
			"selector": map[string]any{
				"type": "FragmentSelector", "conformsTo": "http://www.w3.org/TR/media-frags/", "value": "xywh=1,2,30,10",
			},
		},
	}})
	if err != nil {
		t.Fatalf("build canonical page without OCR run: %v", err)
	}
	annotations := store.NewAnnotationStore(database)
	savedPage, err := annotations.SavePage(context.Background(), store.AnnotationPage{
		WorkspaceID: store.AnonymousWorkspaceID,
		ItemImageID: image.ID,
		PageID:      pageID,
		CanvasURI:   canvasURI,
		Payload:     string(pagePayload),
	}, 0)
	if err != nil {
		t.Fatalf("save canonical page without OCR run: %v", err)
	}
	var runCount int
	if err := database.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM ocr_runs WHERE item_image_id = ?`, image.ID).Scan(&runCount); err != nil {
		t.Fatalf("count OCR-run provenance: %v", err)
	}
	if runCount != 0 {
		t.Fatalf("OCR-run provenance count = %d, want 0", runCount)
	}

	ocrRuns := store.NewOCRRunStore(database)
	items := store.NewItemStore(database)
	contexts := store.NewContextStore(database)
	jobs := store.NewTranscriptionJobStore(database)
	handler := NewHandler(ocrRuns, items, contexts, annotations, jobs, nil, nil, nil)
	appServer := httptest.NewServer(handler)
	t.Cleanup(appServer.Close)

	for name, target := range map[string]string{
		"hocr": fmt.Sprintf("%s/v1/item-images/%d/annotations/revisions/%d/hocr", appServer.URL, image.ID, savedPage.Revision),
	} {
		t.Run(name, func(t *testing.T) {
			response, err := http.Get(target)
			if err != nil {
				t.Fatalf("GET %s: %v", name, err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatalf("read %s response: %v", name, err)
			}
			if response.StatusCode != http.StatusOK || !strings.Contains(string(body), canonicalOnly) {
				t.Fatalf("%s canonical response status/body = %d/%s", name, response.StatusCode, body)
			}
		})
	}
	hocrURL := fmt.Sprintf("%s/v1/item-images/%d/annotations/revisions/%d/hocr", appServer.URL, image.ID, savedPage.Revision)
	headResponse, err := http.Head(hocrURL)
	if err != nil {
		t.Fatalf("HEAD revisioned hOCR: %v", err)
	}
	defer headResponse.Body.Close()
	headBody, err := io.ReadAll(headResponse.Body)
	if err != nil || headResponse.StatusCode != http.StatusOK || len(headBody) != 0 || headResponse.ContentLength <= 0 {
		t.Fatalf("HEAD revisioned hOCR = %d/%d/%d/%v", headResponse.StatusCode, len(headBody), headResponse.ContentLength, err)
	}
	staleResponse, err := http.Get(fmt.Sprintf("%s/v1/item-images/%d/annotations/revisions/%d/hocr", appServer.URL, image.ID, savedPage.Revision+1))
	if err != nil {
		t.Fatalf("GET stale revisioned hOCR: %v", err)
	}
	defer staleResponse.Body.Close()
	staleBody, err := io.ReadAll(staleResponse.Body)
	if err != nil || staleResponse.StatusCode != http.StatusConflict || strings.Contains(string(staleBody), canonicalOnly) {
		t.Fatalf("stale revisioned hOCR = %d/%q/%v, want conflict without canonical text", staleResponse.StatusCode, staleBody, err)
	}
	annotationClient := scribev1connect.NewAnnotationServiceClient(http.DefaultClient, appServer.URL)
	status, exported := tenantAnnotationExport(t, annotationClient, "", image.ID, savedPage.Revision, "hocr")
	if status != http.StatusOK || !strings.Contains(exported, canonicalOnly) {
		t.Fatalf("canonical export without OCR run status/body = %d/%q", status, exported)
	}
}
