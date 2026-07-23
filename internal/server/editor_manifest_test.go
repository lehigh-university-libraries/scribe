package server

import (
	"context"
	"encoding/json"
	"testing"

	"connectrpc.com/connect"
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
