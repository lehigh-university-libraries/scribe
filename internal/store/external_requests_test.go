package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	dbgen "github.com/lehigh-university-libraries/scribe/internal/db"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

func TestExternalRequestBindingAndRetention(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	workspaceA, imageA := createAnnotationTestResource(t, database, suffix+"-a", "https://source.example/canvas/"+suffix+"-a")
	workspaceB, imageB := createAnnotationTestResource(t, database, suffix+"-b", "https://source.example/canvas/"+suffix+"-b")
	workspaceC, imageC := createAnnotationTestResource(t, database, suffix+"-c", "https://source.example/canvas/"+suffix+"-c")
	jobStore := store.NewTranscriptionJobStore(database)

	boundKey := "bound-" + suffix
	bound, created, err := jobStore.ReserveExternalRequestForItemImage(
		ctx, workspaceA, imageA, "image-reprocess", boundKey, fmt.Sprintf("%064x", 1), "",
	)
	if err != nil || !created || bound.ItemImageID != imageA {
		t.Fatalf("bound reservation = %+v, created %t, error %v", bound, created, err)
	}
	if _, _, err := jobStore.ReserveExternalRequestForItemImage(
		ctx, workspaceA, imageB, "image-reprocess", "foreign-"+suffix, fmt.Sprintf("%064x", 2), "",
	); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-workspace reservation error = %v, want sql.ErrNoRows", err)
	}

	if err := store.NewItemStore(database).DeleteItemImageForWorkspace(ctx, imageA, workspaceA); err != nil {
		t.Fatalf("delete bound item image: %v", err)
	}
	if _, err := jobStore.GetExternalRequest(ctx, workspaceA, "image-reprocess", boundKey); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("bound request after image deletion error = %v, want sql.ErrNoRows", err)
	}

	completedKey := "completed-" + suffix
	completed, created, err := jobStore.ReserveExternalRequest(ctx, workspaceC, "ingest", completedKey, fmt.Sprintf("%064x", 6), "")
	if err != nil || !created {
		t.Fatalf("completed reservation = %+v, created %t, error %v", completed, created, err)
	}
	var itemC string
	if err := database.QueryRowContext(ctx, `SELECT item_id FROM item_images WHERE id = ?`, imageC).Scan(&itemC); err != nil {
		t.Fatalf("load completed reservation item: %v", err)
	}
	if err := jobStore.CompleteExternalRequest(ctx, workspaceC, "ingest", completedKey, completed.LeaseOwner, itemC, imageC, 0); err != nil {
		t.Fatalf("complete item reservation: %v", err)
	}
	if err := store.NewItemStore(database).DeleteForWorkspace(ctx, itemC, workspaceC); err != nil {
		t.Fatalf("delete item with completed reservation: %v", err)
	}
	if _, err := jobStore.GetExternalRequest(ctx, workspaceC, "ingest", completedKey); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("completed request after item deletion error = %v, want sql.ErrNoRows", err)
	}

	failedKey := "failed-" + suffix
	failed, created, err := jobStore.ReserveExternalRequest(ctx, workspaceB, "retention-test", failedKey, fmt.Sprintf("%064x", 3), "")
	if err != nil || !created {
		t.Fatalf("failed reservation = %+v, created %t, error %v", failed, created, err)
	}
	if err := jobStore.FailExternalRequest(ctx, workspaceB, "retention-test", failedKey, failed.LeaseOwner, "safe failure"); err != nil {
		t.Fatalf("fail terminal reservation: %v", err)
	}

	expiredKey := "expired-" + suffix
	if _, created, err := jobStore.ReserveExternalRequest(ctx, workspaceB, "retention-test", expiredKey, fmt.Sprintf("%064x", 4), ""); err != nil || !created {
		t.Fatalf("expired reservation created %t, error %v", created, err)
	}
	liveKey := "live-" + suffix
	if _, created, err := jobStore.ReserveExternalRequest(ctx, workspaceB, "retention-test", liveKey, fmt.Sprintf("%064x", 5), ""); err != nil || !created {
		t.Fatalf("live reservation created %t, error %v", created, err)
	}

	old := time.Now().UTC().Add(-48 * time.Hour)
	if _, err := database.ExecContext(ctx, `
UPDATE external_requests
SET updated_at = ?, lease_until = CASE
  WHEN idempotency_key = ? THEN DATE_SUB(NOW(), INTERVAL 1 HOUR)
  ELSE lease_until
END
WHERE workspace_id = ?
  AND source = 'retention-test'
  AND idempotency_key IN (?, ?, ?)`, old, expiredKey, workspaceB, failedKey, expiredKey, liveKey); err != nil {
		t.Fatalf("age external requests: %v", err)
	}

	if err := jobStore.RetainExternalRequests(ctx, 24*time.Hour); err != nil {
		t.Fatalf("RetainExternalRequests: %v", err)
	}
	for _, removedKey := range []string{failedKey, expiredKey} {
		if _, err := jobStore.GetExternalRequest(ctx, workspaceB, "retention-test", removedKey); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("retained request %q error = %v, want sql.ErrNoRows", removedKey, err)
		}
	}
	if live, err := jobStore.GetExternalRequest(ctx, workspaceB, "retention-test", liveKey); err != nil || live.Status != store.ExternalRequestStatusInProgress {
		t.Fatalf("live request = %+v, error %v; want preserved in-progress request", live, err)
	}
}

func TestExternalRequestFailurePersistsOnlyBoundedCategory(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	workspaceID, _ := createAnnotationTestResource(t, database, suffix, "https://source.example/canvas/"+suffix)
	jobStore := store.NewTranscriptionJobStore(database)
	key := "redaction-" + suffix

	request, created, err := jobStore.ReserveExternalRequest(
		ctx,
		workspaceID,
		"provider-redaction-test",
		key,
		fmt.Sprintf("%064x", 7),
		"",
	)
	if err != nil || !created {
		t.Fatalf("ReserveExternalRequest = %+v, created %t, error %v", request, created, err)
	}

	untrusted := strings.Repeat(
		"upstream provider returned 401 unauthorized; "+
			"url=https://user:TOKEN-SENTINEL@provider.example/path?api_key=QUERY-SENTINEL; "+
			"response_body=BODY-SENTINEL; SQL=SELECT * FROM secrets; Vault=VAULT-SENTINEL; ",
		32,
	)
	if err := jobStore.FailExternalRequest(
		ctx,
		workspaceID,
		"provider-redaction-test",
		key,
		request.LeaseOwner,
		untrusted,
	); err != nil {
		t.Fatalf("FailExternalRequest: %v", err)
	}

	failed, err := jobStore.GetExternalRequest(ctx, workspaceID, "provider-redaction-test", key)
	if err != nil {
		t.Fatalf("GetExternalRequest: %v", err)
	}
	if failed.ErrorMessage != "external provider authentication failed" {
		t.Fatalf("persisted failure category = %q", failed.ErrorMessage)
	}
	if len(failed.ErrorMessage) > 128 {
		t.Fatalf("persisted failure length = %d, want at most 128", len(failed.ErrorMessage))
	}
	lower := strings.ToLower(failed.ErrorMessage)
	for _, sentinel := range []string{
		"token-sentinel",
		"api_key",
		"query-sentinel",
		"body-sentinel",
		"select * from secrets",
		"vault-sentinel",
		"provider.example",
	} {
		if strings.Contains(lower, sentinel) {
			t.Errorf("persisted failure contains untrusted sentinel %q: %q", sentinel, failed.ErrorMessage)
		}
	}
}

func TestExternalRequestCompletionRequiresOneOwnedResourceTuple(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	workspaceID, imageA := createAnnotationTestResource(t, database, suffix+"-tuple-a", "https://source.example/canvas/"+suffix+"-tuple-a")
	foreignWorkspace, foreignImage := createAnnotationTestResource(t, database, suffix+"-tuple-foreign", "https://source.example/canvas/"+suffix+"-tuple-foreign")
	var userID uint64
	if err := database.QueryRow(`SELECT user_id FROM workspace_members WHERE workspace_id = ? AND role = 'admin' LIMIT 1`, workspaceID).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	itemStore := store.NewItemStore(database)
	itemB := "external-tuple-b-" + suffix
	if _, err := itemStore.Create(ctx, dbgen.CreateItemParams{
		ID: itemB, UserID: userID, WorkspaceID: workspaceID, Name: "tuple B", SourceType: "manifest",
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = itemStore.DeleteForWorkspace(context.Background(), itemB, workspaceID) })
	canvasB := "https://source.example/canvas/" + suffix + "-tuple-b"
	imageB, err := itemStore.AddImage(ctx, dbgen.CreateItemImageParams{
		ItemID: itemB, ImageURL: "https://images.example/tuple-b.jpg", CanvasURI: canvasB, Width: 100, Height: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.NewAnnotationStore(database).SavePage(ctx, canonicalTestPage(t, workspaceID, imageB.ID, canvasB, "tuple B"), 0); err != nil {
		t.Fatal(err)
	}
	processingContext, err := store.NewContextStore(database).Create(ctx, store.Context{
		UserID: &userID, WorkspaceID: &workspaceID, Name: "tuple-context-" + suffix,
		SegmentationModel: "tesseract", TranscriptionProvider: "tesseract", TranscriptionModel: "eng",
	})
	if err != nil {
		t.Fatal(err)
	}
	jobStore := store.NewTranscriptionJobStore(database)
	jobB, err := jobStore.Create(ctx, imageB.ID, processingContext)
	if err != nil {
		t.Fatal(err)
	}
	var itemA string
	if err := database.QueryRow(`SELECT item_id FROM item_images WHERE id = ?`, imageA).Scan(&itemA); err != nil {
		t.Fatal(err)
	}
	var foreignItem string
	if err := database.QueryRow(`SELECT item_id FROM item_images WHERE id = ?`, foreignImage).Scan(&foreignItem); err != nil {
		t.Fatal(err)
	}

	for index, completion := range []struct {
		itemID  string
		imageID uint64
		jobID   uint64
	}{
		{itemID: itemA, imageID: imageA, jobID: jobB},
		{itemID: foreignItem, imageID: foreignImage},
	} {
		key := fmt.Sprintf("tuple-%d-%s", index, suffix)
		request, created, reserveErr := jobStore.ReserveExternalRequest(ctx, workspaceID, "tuple-test", key, fmt.Sprintf("%064x", index+1), "")
		if reserveErr != nil || !created {
			t.Fatalf("reserve tuple request %d = %+v/%t/%v", index, request, created, reserveErr)
		}
		if err := jobStore.CompleteExternalRequest(ctx, workspaceID, "tuple-test", key, request.LeaseOwner, completion.itemID, completion.imageID, completion.jobID); err == nil {
			t.Fatalf("mismatched completion %d unexpectedly succeeded", index)
		}
		stored, loadErr := jobStore.GetExternalRequest(ctx, workspaceID, "tuple-test", key)
		if loadErr != nil || stored.Status != store.ExternalRequestStatusInProgress || stored.ItemID != "" || stored.ItemImageID != 0 || stored.TranscriptionJobID != 0 {
			t.Fatalf("rejected completion %d mutated request = %+v/%v", index, stored, loadErr)
		}
	}
	_ = foreignWorkspace
}

func TestExternalRequestCompletionUsesParentBeforeLeafLockOrder(t *testing.T) {
	database := annotationTestDB(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	workspaceID, _ := createAnnotationTestResource(t, database, suffix+"-lock-order", "https://source.example/canvas/"+suffix+"-lock-order")
	jobs := store.NewTranscriptionJobStore(database)
	key := "lock-order-" + suffix
	request, created, err := jobs.ReserveExternalRequest(ctx, workspaceID, "lock-order", key, fmt.Sprintf("%064x", 42), "")
	if err != nil || !created {
		t.Fatalf("reserve lock-order request = %+v/%t/%v", request, created, err)
	}

	workspaceBlocker, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin workspace blocker: %v", err)
	}
	defer func() { _ = workspaceBlocker.Rollback() }()
	var lockedWorkspace uint64
	if err := workspaceBlocker.QueryRowContext(ctx, `SELECT id FROM workspaces WHERE id = ? FOR UPDATE`, workspaceID).Scan(&lockedWorkspace); err != nil {
		t.Fatalf("lock external-request workspace: %v", err)
	}

	completionDone := make(chan error, 1)
	go func() {
		completionDone <- jobs.CompleteExternalRequest(ctx, workspaceID, "lock-order", key, request.LeaseOwner, "", 0, 0)
	}()
	select {
	case err := <-completionDone:
		t.Fatalf("completion bypassed workspace parent lock: %v", err)
	case <-time.After(150 * time.Millisecond):
	}

	leafProbe, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin external-request leaf probe: %v", err)
	}
	var requestID uint64
	if err := leafProbe.QueryRowContext(ctx, `
SELECT id FROM external_requests
WHERE workspace_id = ? AND source = 'lock-order' AND idempotency_key = ?
FOR UPDATE NOWAIT`, workspaceID, key).Scan(&requestID); err != nil {
		_ = leafProbe.Rollback()
		t.Fatalf("completion locked external-request leaf before workspace parent: %v", err)
	}
	if err := leafProbe.Rollback(); err != nil {
		t.Fatalf("release external-request leaf probe: %v", err)
	}
	if err := workspaceBlocker.Commit(); err != nil {
		t.Fatalf("release external-request workspace blocker: %v", err)
	}
	select {
	case err := <-completionDone:
		if err != nil {
			t.Fatalf("complete request after workspace release: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("external request completion did not resume")
	}
}
