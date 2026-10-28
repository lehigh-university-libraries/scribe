package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

func TestQueuedDocumentProfileRetainsItemOverride(t *testing.T) {
	pool := annotationTestDB(t)
	ctx := context.Background()
	workspace, image := createAnnotationTestResource(t, pool, uuid.NewString(), "https://example.org/canvas/profile")
	pages := store.NewAnnotationStore(pool)
	if _, err := pages.SavePage(ctx, canonicalTestPage(t, workspace, image, "https://example.org/canvas/profile", "checked"), 0); err != nil {
		t.Fatal(err)
	}
	items := store.NewItemStore(pool)
	img, err := items.GetImageForWorkspace(ctx, image, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err := items.SetDocumentType(ctx, img.ItemID, workspace, "ledger"); err != nil {
		t.Fatal(err)
	}
	jobs := store.NewTranscriptionJobStore(pool)
	jobID, err := jobs.Create(ctx, image, createAnnotationTestContext(t, pool, uuid.NewString()))
	if err != nil {
		t.Fatal(err)
	}
	if err := items.SetDocumentType(ctx, img.ItemID, workspace, "newspaper"); err != nil {
		t.Fatal(err)
	}
	job, err := jobs.Get(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot store.Context
	if err := json.Unmarshal(job.ContextSnapshot, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.DocumentType != "ledger" {
		t.Fatalf("queued document type changed: %q", snapshot.DocumentType)
	}
}

func TestGroundTruthSnapshotsAreImmutableScopedAndRevisionFenced(t *testing.T) {
	pool := annotationTestDB(t)
	ctx := context.Background()
	pages := store.NewAnnotationStore(pool)
	workspace, image := createAnnotationTestResource(t, pool, uuid.NewString(), "https://example.org/canvas/gt")
	otherWorkspace, _ := createAnnotationTestResource(t, pool, uuid.NewString(), "https://example.org/canvas/other")
	page, err := pages.SavePage(ctx, canonicalTestPage(t, workspace, image, "https://example.org/canvas/gt", "checked"), 0)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := store.GroundTruth{WorkspaceID: workspace, ItemImageID: image, Revision: page.Revision, Payload: page.Payload, OriginalPage: `{"htrPage":1}`, DocumentType: "generic", Transcript: "checked", ImageUrl: "https://example.org/image.jpg"}
	if err := pages.MarkGroundTruth(ctx, snapshot, true); err != nil {
		t.Fatal(err)
	}
	if err := pages.MarkGroundTruth(ctx, snapshot, true); err != nil {
		t.Fatalf("idempotent mark: %v", err)
	}
	snapshot.WorkspaceID = otherWorkspace
	if err := pages.MarkGroundTruth(ctx, snapshot, true); !errors.Is(err, store.ErrAnnotationPageNotFound) {
		t.Fatalf("cross-workspace mark: %v", err)
	}
	snapshot.WorkspaceID = workspace
	page.Payload = replacePageText(t, page.Payload, "new correction")
	next, err := pages.SavePage(ctx, page, page.Revision)
	if err != nil {
		t.Fatal(err)
	}
	marked, err := pages.HasGroundTruth(ctx, workspace, image, next.Revision)
	if err != nil || marked {
		t.Fatalf("new revision inherited marker: %v %v", marked, err)
	}
	if err := pages.MarkGroundTruth(ctx, snapshot, true); !errors.Is(err, store.ErrAnnotationRevisionConflict) {
		t.Fatalf("stale mark: %v", err)
	}
	got, err := pages.GroundTruthBundle(ctx, workspace, 0, "")
	if err != nil || len(got) != 1 || got[0].Payload != snapshot.Payload || got[0].Transcript != "checked" {
		t.Fatalf("immutable bundle: %+v %v", got, err)
	}
	got, err = pages.GroundTruthBundle(ctx, otherWorkspace, 0, "")
	if err != nil || len(got) != 0 {
		t.Fatalf("cross-workspace bundle: %+v %v", got, err)
	}
	if err := pages.MarkGroundTruth(ctx, snapshot, false); err != nil {
		t.Fatalf("unmark a superseded revision: %v", err)
	}
	got, err = pages.GroundTruthBundle(ctx, workspace, 0, "")
	if err != nil || len(got) != 0 {
		t.Fatalf("unmarked snapshot retained: %+v %v", got, err)
	}
}
