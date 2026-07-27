package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/lehigh-university-libraries/scribe/internal/auth"
	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
)

func TestGetEditorManifestReturnsPrivateTripletIdentityThroughConnect(t *testing.T) {
	previous := config.Get()
	configured := previous
	configured.Config.PublicBaseURL = "https://scribe.example"
	configured.Config.IIIF.Base = "https://scribe.example/iiif/3"
	configured.Config.Annotation.TripletPresentationBase = "https://scribe.example/presentation/v3"
	config.Init(configured)
	t.Cleanup(func() { config.Init(previous) })

	database := openTestDB(t)
	canvasID := "https://scribe.example/presentation/v3/item-image-editor/canvas/source"
	image := createServerTestUploadItemImage(t, database, store.AnonymousWorkspaceID, store.AnonymousUserID, canvasID)
	page, err := iiif.NewAnnotationPage(iiif.PageIdentity{
		PublicBaseURL: configured.Config.Annotation.TripletPresentationBase,
		ItemImageID:   image.ID,
		CanvasURI:     canvasID,
	}, nil)
	if err != nil {
		t.Fatalf("build canonical page: %v", err)
	}
	pageID, err := iiif.AnnotationPageID(configured.Config.Annotation.TripletPresentationBase, image.ID)
	if err != nil {
		t.Fatal(err)
	}
	annotations := store.NewAnnotationStore(database)
	if _, err := annotations.SavePage(context.Background(), store.AnnotationPage{
		WorkspaceID: store.AnonymousWorkspaceID,
		ItemImageID: image.ID,
		PageID:      pageID,
		CanvasURI:   canvasID,
		Payload:     string(page),
	}, 0); err != nil {
		t.Fatalf("save canonical page: %v", err)
	}
	secondCanvasID := canvasID + "-sibling"
	result, err := database.ExecContext(context.Background(), `
INSERT INTO item_images (workspace_id, item_id, sequence, image_url, canvas_uri, width, height)
VALUES (?, ?, 2, 'https://images.example/sibling.jpg', ?, 640, 480)`, store.AnonymousWorkspaceID, image.ItemID, secondCanvasID)
	if err != nil {
		t.Fatalf("create sibling image: %v", err)
	}
	secondID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	secondPage, err := iiif.NewAnnotationPage(iiif.PageIdentity{
		PublicBaseURL: configured.Config.Annotation.TripletPresentationBase,
		ItemImageID:   uint64(secondID), // #nosec G115 -- positive test fixture identifier.
		CanvasURI:     secondCanvasID,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	secondPageID, err := iiif.AnnotationPageID(configured.Config.Annotation.TripletPresentationBase, uint64(secondID)) // #nosec G115 -- positive test fixture identifier.
	if err != nil {
		t.Fatal(err)
	}
	if _, err := annotations.SavePage(context.Background(), store.AnnotationPage{
		WorkspaceID: store.AnonymousWorkspaceID,
		ItemImageID: uint64(secondID), // #nosec G115 -- positive test fixture identifier.
		PageID:      secondPageID,
		CanvasURI:   secondCanvasID,
		Payload:     string(secondPage),
	}, 0); err != nil {
		t.Fatalf("save sibling canonical page: %v", err)
	}
	sourceManifest, err := json.Marshal(map[string]any{
		"@context": []any{
			"https://example.org/iiif-extension/context.json",
			"http://iiif.io/api/presentation/3/context.json",
		},
		"id":                 "https://repository.example/manifest/review-scope",
		"type":               "Manifest",
		"label":              map[string]any{"en": []any{"Imported multi-page item"}},
		"ex:manifestSibling": secondCanvasID,
		"items": []any{
			map[string]any{
				"id": canvasID, "type": "Canvas", "width": 640, "height": 480,
				"ex:canvasSibling": secondCanvasID,
			},
			map[string]any{"id": secondCanvasID, "type": "Canvas", "width": 640, "height": 480},
		},
		"start": map[string]any{"id": secondCanvasID, "type": "Canvas"},
		"structures": []any{map[string]any{
			"id": "https://repository.example/range/review-scope", "type": "Range",
			"items": []any{
				map[string]any{"id": canvasID, "type": "Canvas"},
				map[string]any{"id": secondCanvasID, "type": "Canvas"},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(context.Background(), `
UPDATE items
SET source_manifest = ?
WHERE workspace_id = ? AND id = ?`, string(sourceManifest), store.AnonymousWorkspaceID, image.ItemID); err != nil {
		t.Fatalf("attach imported multi-page Manifest: %v", err)
	}

	handler := &Handler{items: store.NewItemStore(database), annotations: annotations}
	response, err := handler.GetEditorManifest(context.Background(), connect.NewRequest(&scribev1.GetEditorManifestRequest{
		ItemImageId: image.ID,
	}))
	if err != nil {
		t.Fatalf("GetEditorManifest: %v", err)
	}
	if response.Msg.GetItem() == nil || response.Msg.GetItem().GetId() != image.ItemID || response.Msg.GetSelectedCanvasId() != canvasID {
		t.Fatalf("editor manifest identity = %#v", response.Msg)
	}
	manifestBytes := []byte(response.Msg.GetManifestJson())
	if err := iiif.ValidateManifest(manifestBytes); err != nil {
		t.Fatalf("editor Manifest is invalid: %v", err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	wantID, err := iiif.ItemManifestID(configured.Config.Annotation.TripletPresentationBase, image.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	if manifest["id"] != wantID {
		t.Fatalf("Manifest id = %q, want Triplet identity %q", manifest["id"], wantID)
	}
	if !strings.Contains(string(manifestBytes), secondCanvasID) {
		t.Fatal("ordinary full-item Manifest did not retain sibling source references")
	}

	reviewContext := auth.WithPrincipal(context.Background(), auth.Principal{
		Authenticated:     true,
		AuthType:          "review_session",
		WorkspaceID:       store.AnonymousWorkspaceID,
		ScopedItemID:      image.ItemID,
		ScopedItemImageID: image.ID,
	})
	reviewResponse, err := handler.GetEditorManifest(reviewContext, connect.NewRequest(&scribev1.GetEditorManifestRequest{ItemImageId: image.ID}))
	if err != nil {
		t.Fatalf("GetEditorManifest for review session: %v", err)
	}
	if got := reviewResponse.Msg.GetItem().GetImages(); len(got) != 1 || got[0].GetId() != image.ID {
		t.Fatalf("review item exposed sibling images: %+v", got)
	}
	var reviewManifest struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(reviewResponse.Msg.GetManifestJson()), &reviewManifest); err != nil {
		t.Fatal(err)
	}
	if len(reviewManifest.Items) != 1 || reviewManifest.Items[0].ID != canvasID {
		t.Fatalf("review Manifest exposed sibling canvases: %+v", reviewManifest.Items)
	}
	if strings.Contains(reviewResponse.Msg.GetManifestJson(), secondCanvasID) ||
		strings.Contains(reviewResponse.Msg.GetManifestJson(), "ex:manifestSibling") ||
		strings.Contains(reviewResponse.Msg.GetManifestJson(), "ex:canvasSibling") {
		t.Fatalf("review Manifest retained sibling source provenance: %s", reviewResponse.Msg.GetManifestJson())
	}
}

func TestPublicIIIFImageBaseURL(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		publicBase string
		configured string
		want       string
	}{
		{name: "absolute configured", publicBase: "https://app.example", configured: "https://images.example/iiif/3/", want: "https://images.example/iiif/3"},
		{name: "relative configured", publicBase: "https://app.example/base", configured: "/iiif/3", want: "https://app.example/base/iiif/3"},
		{name: "default path", publicBase: "https://app.example", want: "https://app.example/iiif/3"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := publicIIIFImageBaseURL(test.publicBase, test.configured)
			if err != nil || got != test.want {
				t.Fatalf("publicIIIFImageBaseURL() = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}
