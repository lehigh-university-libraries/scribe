package server

import (
	"context"
	"net/http"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/lehigh-university-libraries/htr/pkg/ocrpage"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
	"github.com/lehigh-university-libraries/scribe/proto/scribe/v1/scribev1connect"
)

func TestGroundTruthAPIIsScopedAndRequiresExactCheckedRevision(t *testing.T) {
	pool := openTestDB(t)
	ctx := context.Background()
	workspace, user := createServerTestWorkspace(t, pool)
	otherWorkspace, otherUser := createServerTestWorkspace(t, pool)
	canvas := "https://example.org/canvas/ground-truth"
	image := createServerTestItemImage(t, pool, workspace, user, canvas)
	items, runs, annotations := store.NewItemStore(pool), store.NewOCRRunStore(pool), store.NewAnnotationStore(pool)
	h := NewHandler(runs, items, nil, annotations, nil, nil, nil, nil)
	page := ocrpage.FromLines(int(image.Width), int(image.Height), []ocrpage.Line{{ID: "l", Text: "checked", Box: ocrpage.Rect{X1: 10, Y1: 10, X2: 90, Y2: 30}}})
	baseline, err := ocrpage.WriteJSON(page)
	if err != nil {
		t.Fatal(err)
	}
	pageItems, err := pageAnnotations("scope", canvas, page)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := iiif.NewAnnotationPage(iiif.PageIdentity{PublicBaseURL: h.publicAnnotationBaseURL(), ItemImageID: image.ID, CanvasURI: canvas}, pageItems)
	if err != nil {
		t.Fatal(err)
	}
	pageID, _ := h.annotationPageIDForItemImage(image.ID)
	if _, err := annotations.SavePage(ctx, store.AnnotationPage{WorkspaceID: workspace, ItemImageID: image.ID, CanvasURI: canvas, PageID: pageID, Payload: string(payload)}, 0); err != nil {
		t.Fatal(err)
	}
	if err := runs.Create(ctx, store.OCRRun{SessionID: "gt-acceptance-" + image.ItemID, ItemImageID: &image.ID, ImageURL: image.ImageURL, OriginalPage: string(baseline), OriginalText: "checked"}); err != nil {
		t.Fatal(err)
	}
	server := newTenantScopedServer(t, h, map[string]testTenantIdentity{"owner": {workspaceID: workspace, userID: user}, "other": {workspaceID: otherWorkspace, userID: otherUser}})
	client := scribev1connect.NewAnnotationServiceClient(connect.NewClient(connecthttp.NewTransport(http.DefaultClient, server.URL, connecthttp.WithReadMaxBytes(0))))
	owner := tenantConnectContext(ctx, "owner")
	request := &scribev1.MarkGroundTruthRequest{ItemImageId: image.ID, ExpectedRevision: 1, Marked: true}
	if _, err := client.MarkGroundTruth(tenantConnectContext(ctx, "other"), request); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("foreign mark: %v", err)
	}
	if _, err := client.MarkGroundTruth(owner, request); err != nil {
		t.Fatal(err)
	}
	loaded, err := client.GetAnnotationPage(owner, &scribev1.GetAnnotationPageRequest{ItemImageId: image.ID})
	if err != nil || !loaded.GetGroundTruth() {
		t.Fatalf("marker: %+v %v", loaded, err)
	}
	exported, err := client.ExportGroundTruth(owner, &scribev1.ExportGroundTruthRequest{ItemId: image.ItemID})
	if err != nil || len(exported.GetContent()) == 0 {
		t.Fatalf("bundle: %v", err)
	}
	if _, err := client.ExportGroundTruth(tenantConnectContext(ctx, "other"), &scribev1.ExportGroundTruthRequest{ItemId: image.ItemID}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("foreign bundle: %v", err)
	}
	request.ExpectedRevision = 2
	if _, err := client.MarkGroundTruth(owner, request); connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("stale mark: %v", err)
	}
	request.ExpectedRevision = 1
	request.Marked = false
	if _, err := client.MarkGroundTruth(owner, request); err != nil {
		t.Fatal(err)
	}
	loaded, err = client.GetAnnotationPage(owner, &scribev1.GetAnnotationPageRequest{ItemImageId: image.ID})
	if err != nil || loaded.GetGroundTruth() {
		t.Fatalf("unmark: %+v %v", loaded, err)
	}
}
