package server

import (
	"context"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	"github.com/lehigh-university-libraries/scribe/internal/auth"
	dbstore "github.com/lehigh-university-libraries/scribe/internal/db"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
	"github.com/lehigh-university-libraries/scribe/proto/scribe/v1/scribev1connect"
)

func TestSaveAnnotationPageCommitsCorrectionMetricWithCanonicalRevision(t *testing.T) {
	database := openTestDB(t)
	ctx := context.Background()
	workspaceID, userID := createServerTestWorkspace(t, database)
	contextStore := store.NewContextStore(database)
	processingContext, err := contextStore.Create(ctx, store.Context{
		UserID:                &userID,
		WorkspaceID:           &workspaceID,
		Name:                  uniqueName("canonical-metric-context"),
		SegmentationModel:     "scribe",
		TranscriptionProvider: "ollama",
		TranscriptionModel:    "test-model",
	})
	if err != nil {
		t.Fatalf("create metric context: %v", err)
	}
	t.Cleanup(func() {
		_ = contextStore.Delete(context.Background(), processingContext.ID)
	})

	canvasURI := "https://source.example/canvas/" + uniqueName("canonical-metric")
	image := createServerTestItemImage(t, database, workspaceID, userID, canvasURI)
	itemStore := store.NewItemStore(database)
	annotationStore := store.NewAnnotationStore(database)
	runStore := store.NewOCRRunStore(database)
	handler := NewHandler(runStore, itemStore, contextStore, annotationStore, nil, nil, nil, nil)
	pageID, err := iiif.CanonicalPageID(handler.publicAnnotationBaseURL(), image.ID)
	if err != nil {
		t.Fatalf("build canonical metric page ID: %v", err)
	}
	annotationID, err := iiif.AnnotationID(pageID, "metric-line")
	if err != nil {
		t.Fatalf("build canonical metric annotation ID: %v", err)
	}
	payload, err := iiif.NewAnnotationPage(iiif.PageIdentity{
		PublicBaseURL: handler.publicAnnotationBaseURL(),
		ItemImageID:   image.ID,
		CanvasURI:     image.CanvasURI,
	}, []any{mustDecodeAnnotation(canonicalMutationTestAnnotation(annotationID, image.CanvasURI, "cat"))})
	if err != nil {
		t.Fatalf("build canonical metric page: %v", err)
	}
	seeded, err := annotationStore.SavePage(ctx, store.AnnotationPage{
		WorkspaceID: workspaceID,
		ItemImageID: image.ID,
		PageID:      pageID,
		CanvasURI:   image.CanvasURI,
		Payload:     string(payload),
	}, 0)
	if err != nil {
		t.Fatalf("seed canonical metric page: %v", err)
	}

	contextID := processingContext.ID
	if err := runStore.Create(ctx, store.OCRRun{
		SessionID:    uniqueName("canonical-metric-run"),
		ItemImageID:  &image.ID,
		ContextID:    &contextID,
		ImageURL:     image.ImageURL,
		Provider:     processingContext.TranscriptionProvider,
		Model:        processingContext.TranscriptionModel,
		OriginalHOCR: "<html></html>",
		OriginalText: "cat",
	}); err != nil {
		t.Fatalf("create canonical metric OCR baseline: %v", err)
	}
	baseline, err := runStore.GetByItemImageID(ctx, image.ID)
	if err != nil {
		t.Fatalf("load canonical metric OCR baseline: %v", err)
	}
	if baseline.CanonicalRevision != nil || baseline.LevenshteinDistance != 0 {
		t.Fatalf("uncorrected baseline metric = revision %v distance %d", baseline.CanonicalRevision, baseline.LevenshteinDistance)
	}

	appServer := newTenantScopedServer(t, handler, map[string]testTenantIdentity{
		"workspace": {workspaceID: workspaceID, userID: userID},
	})
	client := scribev1connect.NewAnnotationServiceClient(http.DefaultClient, appServer.URL)
	edited := replaceServerPageText(t, seeded.Payload, "cut")
	saved, err := client.SaveAnnotationPage(ctx, tenantConnectRequest("workspace", &scribev1.SaveAnnotationPageRequest{
		ItemImageId:        image.ID,
		AnnotationPageJson: edited,
		ExpectedRevision:   seeded.Revision,
	}))
	if err != nil {
		t.Fatalf("SaveAnnotationPage canonical metric edit: %v", err)
	}
	if saved.Msg.GetRevision() != seeded.Revision+1 {
		t.Fatalf("saved canonical metric revision = %d, want %d", saved.Msg.GetRevision(), seeded.Revision+1)
	}
	corrected, err := runStore.GetByItemImageID(ctx, image.ID)
	if err != nil {
		t.Fatalf("load committed canonical metric: %v", err)
	}
	if corrected.CanonicalRevision == nil || *corrected.CanonicalRevision != saved.Msg.GetRevision() || corrected.LevenshteinDistance != 1 {
		t.Fatalf("committed canonical metric = revision %v distance %d, want revision %d distance 1", corrected.CanonicalRevision, corrected.LevenshteinDistance, saved.Msg.GetRevision())
	}
	aggregate, err := runStore.GetContextMetrics(ctx, workspaceID, processingContext.ID)
	if err != nil {
		t.Fatalf("load committed context metric: %v", err)
	}
	if aggregate.TotalRuns != 1 || aggregate.CorrectedRuns != 1 || aggregate.AvgLevenshteinDistance != 1 {
		t.Fatalf("committed context metric = %+v, want one corrected run at distance 1", aggregate)
	}

	_, err = client.SaveAnnotationPage(ctx, tenantConnectRequest("workspace", &scribev1.SaveAnnotationPageRequest{
		ItemImageId:        image.ID,
		AnnotationPageJson: replaceServerPageText(t, seeded.Payload, "dog"),
		ExpectedRevision:   seeded.Revision,
	}))
	if connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("stale SaveAnnotationPage error = %v, want aborted", err)
	}
	reloaded, err := client.GetAnnotationPage(ctx, tenantConnectRequest("workspace", &scribev1.GetAnnotationPageRequest{ItemImageId: image.ID}))
	if err != nil {
		t.Fatalf("reload canonical page after stale save: %v", err)
	}
	if reloaded.Msg.GetRevision() != saved.Msg.GetRevision() || reloaded.Msg.GetAnnotationPageJson() != saved.Msg.GetAnnotationPageJson() {
		t.Fatalf("stale save changed canonical page: got revision %d, want %d", reloaded.Msg.GetRevision(), saved.Msg.GetRevision())
	}
	afterConflict, err := runStore.GetByItemImageID(ctx, image.ID)
	if err != nil {
		t.Fatalf("load canonical metric after stale save: %v", err)
	}
	if afterConflict.CanonicalRevision == nil || *afterConflict.CanonicalRevision != saved.Msg.GetRevision() || afterConflict.LevenshteinDistance != 1 {
		t.Fatalf("stale save changed canonical metric = revision %v distance %d", afterConflict.CanonicalRevision, afterConflict.LevenshteinDistance)
	}
}

func TestGetContextMetricsScopesSharedSystemContextToWorkspace(t *testing.T) {
	database := openTestDB(t)
	ctx := context.Background()
	contextStore := store.NewContextStore(database)
	itemStore := store.NewItemStore(database)
	runStore := store.NewOCRRunStore(database)
	annotationStore := store.NewAnnotationStore(database)

	firstUserID := createTestUser(t, database, uniqueName("metrics-first-user"))
	secondUserID := createTestUser(t, database, uniqueName("metrics-second-user"))
	firstWorkspaceID := createTestWorkspace(t, database, firstUserID, uniqueName("metrics-first-workspace"))
	secondWorkspaceID := createTestWorkspace(t, database, secondUserID, uniqueName("metrics-second-workspace"))

	systemContext, err := contextStore.Create(ctx, store.Context{
		Name:                  uniqueName("metrics-system-context"),
		SegmentationModel:     "scribe",
		TranscriptionProvider: "ollama",
		TranscriptionModel:    "test-model",
	})
	if err != nil {
		t.Fatalf("create system context: %v", err)
	}

	createRun := func(itemID string, userID, workspaceID uint64, distance int) {
		t.Helper()
		item, err := itemStore.Create(ctx, dbstore.CreateItemParams{
			ID:          itemID,
			UserID:      userID,
			WorkspaceID: workspaceID,
			Name:        itemID,
			SourceType:  "upload",
		})
		if err != nil {
			t.Fatalf("create item %q: %v", itemID, err)
		}
		image, err := itemStore.AddImage(ctx, dbstore.CreateItemImageParams{
			ItemID: item.ID, Sequence: 0, ImageURL: "https://example.test/" + itemID + ".jpg", CanvasURI: "https://example.test/canvas/" + itemID,
		})
		if err != nil {
			t.Fatalf("create image for %q: %v", itemID, err)
		}
		contextID := systemContext.ID
		if err := runStore.Create(ctx, store.OCRRun{
			SessionID:    itemID + "-run",
			ItemImageID:  &image.ID,
			ContextID:    &contextID,
			ImageURL:     image.ImageURL,
			Provider:     "ollama",
			Model:        "test-model",
			OriginalHOCR: "<html></html>",
			OriginalText: "baseline",
		}); err != nil {
			t.Fatalf("create OCR run for %q: %v", itemID, err)
		}
		pageID, err := iiif.CanonicalPageID("https://scribe.example", image.ID)
		if err != nil {
			t.Fatalf("build canonical page ID for %q: %v", itemID, err)
		}
		payload, err := iiif.NewAnnotationPage(iiif.PageIdentity{
			PublicBaseURL: "https://scribe.example",
			ItemImageID:   image.ID,
			CanvasURI:     image.CanvasURI,
		}, nil)
		if err != nil {
			t.Fatalf("build canonical metric page for %q: %v", itemID, err)
		}
		if _, err := annotationStore.SavePageWithCorrectionMetric(ctx, store.AnnotationPage{
			WorkspaceID: workspaceID,
			ItemImageID: image.ID,
			PageID:      pageID,
			CanvasURI:   image.CanvasURI,
			Payload:     string(payload),
		}, 0, &store.AnnotationCorrectionMetric{LevenshteinDistance: distance}); err != nil {
			t.Fatalf("save canonical correction metric for %q: %v", itemID, err)
		}
	}

	firstItemID := uniqueName("metrics-first-item")
	secondItemID := uniqueName("metrics-second-item")
	createRun(firstItemID, firstUserID, firstWorkspaceID, 7)
	createRun(secondItemID, secondUserID, secondWorkspaceID, 31)
	t.Cleanup(func() {
		_ = itemStore.DeleteForWorkspace(context.Background(), firstItemID, firstWorkspaceID)
		_ = itemStore.DeleteForWorkspace(context.Background(), secondItemID, secondWorkspaceID)
		_ = contextStore.Delete(context.Background(), systemContext.ID)
	})

	firstMetrics, err := runStore.GetContextMetrics(ctx, firstWorkspaceID, systemContext.ID)
	if err != nil {
		t.Fatalf("GetContextMetrics(first workspace): %v", err)
	}
	if firstMetrics.TotalRuns != 1 || firstMetrics.CorrectedRuns != 1 || firstMetrics.AvgLevenshteinDistance != 7 {
		t.Fatalf("first workspace metrics = %#v; want one corrected run at distance 7", firstMetrics)
	}
	secondMetrics, err := runStore.GetContextMetrics(ctx, secondWorkspaceID, systemContext.ID)
	if err != nil {
		t.Fatalf("GetContextMetrics(second workspace): %v", err)
	}
	if secondMetrics.TotalRuns != 1 || secondMetrics.CorrectedRuns != 1 || secondMetrics.AvgLevenshteinDistance != 31 {
		t.Fatalf("second workspace metrics = %#v; want one corrected run at distance 31", secondMetrics)
	}

	handler := &Handler{
		ocrRuns:  runStore,
		contexts: contextStore,
		auth:     &auth.Manager{},
	}
	requestContext := auth.WithPrincipal(ctx, auth.Principal{
		Authenticated: true,
		UserID:        firstUserID,
		WorkspaceID:   firstWorkspaceID,
	})
	response, err := handler.GetContextMetrics(requestContext, connect.NewRequest(&scribev1.GetContextMetricsRequest{
		ContextId: systemContext.ID,
	}))
	if err != nil {
		t.Fatalf("GetContextMetrics handler: %v", err)
	}
	got := response.Msg.GetMetrics()
	if got.GetTotalRuns() != 1 || got.GetCorrectedRuns() != 1 || got.GetAvgLevenshteinDistance() != 7 {
		t.Fatalf("handler metrics = %#v; want only first workspace", got)
	}
}
