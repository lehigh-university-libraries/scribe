package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
	"github.com/lehigh-university-libraries/scribe/proto/scribe/v1/scribev1connect"
)

func TestDuplicateCanvasImportsWithinWorkspaceRemainItemImageScoped(t *testing.T) {
	database := openTestDB(t)
	source, _ := newChoiceManifestSource(t)
	ctx := context.Background()

	ocrRuns := store.NewOCRRunStore(database)
	items := store.NewItemStore(database)
	contexts := store.NewContextStore(database)
	annotations := store.NewAnnotationStore(database)
	jobs := store.NewTranscriptionJobStore(database)
	handler := NewHandler(ocrRuns, items, contexts, annotations, jobs, nil, nil, nil)
	appServer := httptest.NewServer(handler)
	t.Cleanup(appServer.Close)

	itemClient := scribev1connect.NewItemServiceClient(http.DefaultClient, appServer.URL)
	annotationClient := scribev1connect.NewAnnotationServiceClient(http.DefaultClient, appServer.URL)
	importItem := func(name, idempotencyKey string) *scribev1.Item {
		t.Helper()
		response, err := itemClient.ImportManifest(ctx, connect.NewRequest(&scribev1.ImportManifestRequest{
			Name: name, ManifestUrl: source.URL + "/manifest", IdempotencyKey: idempotencyKey,
		}))
		if err != nil {
			t.Fatalf("ImportManifest(%s): %v", name, err)
		}
		item := response.Msg.GetItem()
		if item == nil || len(item.GetImages()) != 1 {
			t.Fatalf("ImportManifest(%s) item = %#v", name, item)
		}
		return item
	}

	itemA := importItem("First shared Canvas import", "same-workspace-shared-canvas-a")
	itemB := importItem("Second shared Canvas import", "same-workspace-shared-canvas-b")
	t.Cleanup(func() {
		_ = items.DeleteForWorkspace(context.Background(), itemA.GetId(), store.AnonymousWorkspaceID)
		_ = items.DeleteForWorkspace(context.Background(), itemB.GetId(), store.AnonymousWorkspaceID)
	})
	imageA, imageB := itemA.GetImages()[0], itemB.GetImages()[0]
	if itemA.GetId() == itemB.GetId() || imageA.GetId() == imageB.GetId() || imageA.GetCanvasUri() == "" || imageA.GetCanvasUri() != imageB.GetCanvasUri() {
		t.Fatalf("duplicate import identities = item %q/%q image %d/%d Canvas %q/%q", itemA.GetId(), itemB.GetId(), imageA.GetId(), imageB.GetId(), imageA.GetCanvasUri(), imageB.GetCanvasUri())
	}

	loadPage := func(itemImageID uint64) *scribev1.GetAnnotationPageResponse {
		t.Helper()
		response, err := annotationClient.GetAnnotationPage(ctx, connect.NewRequest(&scribev1.GetAnnotationPageRequest{ItemImageId: itemImageID}))
		if err != nil {
			t.Fatalf("GetAnnotationPage(%d): %v", itemImageID, err)
		}
		return response.Msg
	}
	pageA, pageB := loadPage(imageA.GetId()), loadPage(imageB.GetId())
	if pageA.GetAnnotationPageJson() == pageB.GetAnnotationPageJson() {
		t.Fatal("duplicate imports reused one canonical AnnotationPage identity")
	}

	const privateEdit = "same-workspace first-import correction"
	savedA, err := annotationClient.SaveAnnotationPage(ctx, connect.NewRequest(&scribev1.SaveAnnotationPageRequest{
		ItemImageId: imageA.GetId(), AnnotationPageJson: replaceFirstAnnotationText(t, pageA.GetAnnotationPageJson(), privateEdit), ExpectedRevision: pageA.GetRevision(),
	}))
	if err != nil {
		t.Fatalf("SaveAnnotationPage(first import): %v", err)
	}

	search := func(itemImageID uint64) string {
		t.Helper()
		response, err := annotationClient.SearchAnnotations(ctx, connect.NewRequest(&scribev1.SearchAnnotationsRequest{
			ItemImageId: itemImageID, CanvasUri: imageA.GetCanvasUri(),
		}))
		if err != nil {
			t.Fatalf("SearchAnnotations(%d): %v", itemImageID, err)
		}
		return response.Msg.GetAnnotationPageJson()
	}
	if first, second := search(imageA.GetId()), search(imageB.GetId()); !strings.Contains(first, privateEdit) || strings.Contains(second, privateEdit) {
		t.Fatalf("same-Canvas search crossed item-image boundary: first=%t second=%t", strings.Contains(first, privateEdit), strings.Contains(second, privateEdit))
	}

	firstAnnotation := firstAnnotationID(t, savedA.Msg.GetAnnotationPageJson())
	_, err = annotationClient.GetAnnotation(ctx, connect.NewRequest(&scribev1.GetAnnotationRequest{
		ItemImageId: imageB.GetId(), Id: firstAnnotation,
	}))
	assertConnectCode(t, err, connect.CodeNotFound)

	for label, test := range map[string]struct {
		imageID  uint64
		revision uint64
		want     bool
	}{
		"first":  {imageID: imageA.GetId(), revision: savedA.Msg.GetRevision(), want: true},
		"second": {imageID: imageB.GetId(), revision: pageB.GetRevision(), want: false},
	} {
		status, body := tenantAnnotationExport(t, annotationClient, "", test.imageID, test.revision, "txt")
		if status != http.StatusOK || strings.Contains(body, privateEdit) != test.want {
			t.Fatalf("%s same-Canvas export status/body = %d/%q", label, status, body)
		}
	}

	if _, err := annotationClient.PublishItemImageEdits(ctx, connect.NewRequest(&scribev1.PublishItemImageEditsRequest{
		ItemImageId: imageA.GetId(), ExpectedRevision: savedA.Msg.GetRevision(),
	})); err != nil {
		t.Fatalf("PublishItemImageEdits(first import): %v", err)
	}
	publishedA, err := annotations.LoadPublishedPage(ctx, imageA.GetId())
	if err != nil || !strings.Contains(publishedA.Payload, privateEdit) {
		t.Fatalf("first import publication = %+v/%v", publishedA, err)
	}
	if _, err := annotations.LoadPublishedPage(ctx, imageB.GetId()); !errors.Is(err, store.ErrAnnotationPageNotFound) {
		t.Fatalf("second import unexpectedly shared publication: %v", err)
	}
}

func TestPublishedTripletGraphUsesExactItemImageIdentity(t *testing.T) {
	database := openTestDB(t)
	ctx := context.Background()
	workspaceID, userID := createServerTestWorkspace(t, database)
	items := store.NewItemStore(database)
	annotations := store.NewAnnotationStore(database)
	baseURL := (&Handler{}).publicAnnotationBaseURL()

	generatedImage := createServerTestUploadItemImage(t, database, workspaceID, userID, "https://source.example/canvas/generated-placeholder")
	generatedCanvas, err := iiif.ItemImageCanvasID(baseURL, generatedImage.ID)
	if err != nil {
		t.Fatalf("ItemImageCanvasID: %v", err)
	}
	if err := items.UpdateImageCanvasURI(ctx, generatedImage.ID, generatedCanvas); err != nil {
		t.Fatalf("persist generated Canvas: %v", err)
	}
	generatedImage.CanvasURI = generatedCanvas

	importedCanvas := "https://source.example/canvas/imported-exact-identity"
	importedImage := createServerTestItemImage(t, database, workspaceID, userID, importedCanvas)

	createPage := func(image store.ItemImage, text string) (store.AnnotationPage, string) {
		t.Helper()
		pageID, err := iiif.AnnotationPageID(baseURL, image.ID)
		if err != nil {
			t.Fatalf("AnnotationPageID(%d): %v", image.ID, err)
		}
		annotationID, err := iiif.AnnotationID(pageID, "line-publication")
		if err != nil {
			t.Fatalf("AnnotationID(%d): %v", image.ID, err)
		}
		payload, err := iiif.NewAnnotationPage(iiif.PageIdentity{
			PublicBaseURL: baseURL, ItemImageID: image.ID, CanvasURI: image.CanvasURI,
		}, []any{buildLineAnnotation(annotationID, image.CanvasURI, 1, 2, 31, 12, text)})
		if err != nil {
			t.Fatalf("NewAnnotationPage(%d): %v", image.ID, err)
		}
		page, err := annotations.SavePage(ctx, store.AnnotationPage{
			WorkspaceID: workspaceID, ItemImageID: image.ID, PageID: pageID, CanvasURI: image.CanvasURI, Payload: string(payload),
		}, 0)
		if err != nil {
			t.Fatalf("SavePage(%d): %v", image.ID, err)
		}
		return page, annotationID
	}
	generatedPage, generatedAnnotationID := createPage(generatedImage, "published generated Canvas text")
	importedPage, importedAnnotationID := createPage(importedImage, "published imported Canvas text")

	for _, imagePage := range []struct {
		image store.ItemImage
		page  store.AnnotationPage
	}{
		{image: generatedImage, page: generatedPage},
		{image: importedImage, page: importedPage},
	} {
		if _, err := annotations.PublishPage(ctx, workspaceID, imagePage.image.ID, store.AnnotationPublicationOptions{
			ExpectedRevision: imagePage.page.Revision,
		}); err != nil {
			t.Fatalf("PublishPage(%d): %v", imagePage.image.ID, err)
		}
	}

	generatedPage.Payload = replaceServerPageText(t, generatedPage.Payload, "private generated Canvas draft")
	privateGeneratedPage, err := annotations.SavePage(ctx, generatedPage, generatedPage.Revision)
	if err != nil {
		t.Fatalf("save private post-publication draft: %v", err)
	}
	if privateGeneratedPage.Revision <= generatedPage.Revision {
		t.Fatalf("private draft revision = %d, published source revision = %d", privateGeneratedPage.Revision, generatedPage.Revision)
	}

	handler := &Handler{items: items, annotations: annotations}
	buildGraph := func(image store.ItemImage) map[string][]byte {
		t.Helper()
		resources, err := handler.buildPublishedPresentationResources(ctx, image.ID)
		if err != nil {
			t.Fatalf("buildPublishedPresentationResources(%d): %v", image.ID, err)
		}
		graph := make(map[string][]byte, len(resources))
		for _, resource := range resources {
			if _, duplicate := graph[resource.ID]; duplicate {
				t.Fatalf("duplicate Triplet resource ID %q", resource.ID)
			}
			assertValidPresentationResource(t, resource.Payload)
			graph[resource.ID] = resource.Payload
		}
		return graph
	}

	generatedGraph := buildGraph(generatedImage)
	importedGraph := buildGraph(importedImage)
	// The item-wide aggregate Manifest is projected by its independently fenced
	// outbox, not duplicated in either image-scoped graph. An imported external
	// Canvas is also referenced rather than hosted by Scribe.
	if got, want := len(generatedGraph), 6; got != want {
		t.Fatalf("generated graph resource count = %d, want %d", got, want)
	}
	if got, want := len(importedGraph), 5; got != want {
		t.Fatalf("imported graph resource count = %d, want %d", got, want)
	}
	generatedChild, ok := generatedGraph[generatedAnnotationID]
	if !ok {
		t.Fatalf("generated graph omitted exact child Annotation %q", generatedAnnotationID)
	}
	if !strings.Contains(string(generatedChild), "published generated Canvas text") || strings.Contains(string(generatedChild), "private generated Canvas draft") {
		t.Fatalf("Triplet publication graph did not remain revision-scoped: %s", generatedChild)
	}
	if _, ok := importedGraph[importedAnnotationID]; !ok {
		t.Fatalf("imported graph omitted exact child Annotation %q", importedAnnotationID)
	}
	if _, ok := importedGraph[importedCanvas]; ok {
		t.Fatalf("external Canvas %q acquired a conflicting hosted resource", importedCanvas)
	}
	if _, ok := generatedGraph[generatedCanvas]; !ok {
		t.Fatalf("Scribe-owned Canvas %q was not materialized", generatedCanvas)
	}
	for resourceID := range generatedGraph {
		if _, collision := importedGraph[resourceID]; collision {
			t.Fatalf("separate item-image publication graphs collided at %q", resourceID)
		}
	}
}
