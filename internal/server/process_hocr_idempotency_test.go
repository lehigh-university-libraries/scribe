package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/lehigh-university-libraries/scribe/internal/auth"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
	"google.golang.org/protobuf/proto"
)

func TestProcessHOCRExternalImageURLReservesZeroBlobBytesAndDoesNotInflateQuota(t *testing.T) {
	database := openTestDB(t)
	workspaceID, userID := createServerTestWorkspace(t, database)
	ocrRuns := store.NewOCRRunStore(database)
	items := store.NewItemStore(database)
	contexts := store.NewContextStore(database)
	annotations := store.NewAnnotationStore(database)
	jobs := store.NewTranscriptionJobStore(database)
	handler := NewHandler(ocrRuns, items, contexts, annotations, jobs, &auth.Manager{}, nil, nil)
	requestCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := auth.WithPrincipal(requestCtx, auth.Principal{
		Authenticated: true,
		UserID:        userID,
		WorkspaceID:   workspaceID,
		WorkspaceRole: "write",
	})
	usageBefore, err := items.GetStorageQuotaUsage(ctx, workspaceID)
	if err != nil {
		t.Fatalf("load storage quota before ProcessHOCR: %v", err)
	}

	// Hold the membership row after ProcessHOCR creates its quota reservation
	// but before the canonical ingest can commit. This makes the transient
	// reservation directly observable without adding a production-only seam.
	blocker, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin ProcessHOCR quota observer: %v", err)
	}
	blockerCommitted := false
	defer func() {
		if !blockerCommitted {
			_ = blocker.Rollback()
		}
	}()
	var role string
	if err := blocker.QueryRowContext(ctx, `
SELECT role
FROM workspace_members
WHERE workspace_id = ? AND user_id = ?
FOR UPDATE`, workspaceID, userID).Scan(&role); err != nil {
		t.Fatalf("lock ProcessHOCR workspace membership: %v", err)
	}

	request := &scribev1.ProcessHOCRRequest{
		Hocr:           minimalHOCR,
		ImageUrl:       "https://images.example.test/external-hocr.jpg",
		IdempotencyKey: "process-hocr-external-zero-blob-quota",
	}
	type processResult struct {
		response *connect.Response[scribev1.ProcessHOCRResponse]
		err      error
	}
	resultCh := make(chan processResult, 1)
	go func() {
		response, processErr := handler.ProcessHOCR(ctx, connect.NewRequest(request))
		resultCh <- processResult{response: response, err: processErr}
	}()

	var (
		reservedBlobBytes     uint64
		reservedDatabaseBytes uint64
		reservedItems         uint64
		reservedImages        uint64
		resourceKey           sql.NullString
	)
	reservationDeadline := time.Now().Add(10 * time.Second)
	for {
		err = database.QueryRowContext(ctx, `
SELECT reserved_bytes, reserved_database_bytes, reserved_items, reserved_images, resource_key
FROM workspace_storage_reservations
WHERE workspace_id = ?`, workspaceID).Scan(
			&reservedBlobBytes,
			&reservedDatabaseBytes,
			&reservedItems,
			&reservedImages,
			&resourceKey,
		)
		if err == nil {
			break
		}
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("inspect ProcessHOCR storage reservation: %v", err)
		}
		select {
		case result := <-resultCh:
			t.Fatalf("ProcessHOCR completed before quota inspection: response=%v error=%v", result.response, result.err)
		default:
		}
		if time.Now().After(reservationDeadline) {
			t.Fatal("timed out waiting for ProcessHOCR storage reservation")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if reservedBlobBytes != 0 || reservedDatabaseBytes != 0 || reservedItems != 1 || reservedImages != 1 || resourceKey.Valid {
		t.Fatalf(
			"external hOCR reservation = blob:%d database:%d items:%d images:%d resource:%q/%t, want 0/0/1/1/unbound",
			reservedBlobBytes,
			reservedDatabaseBytes,
			reservedItems,
			reservedImages,
			resourceKey.String,
			resourceKey.Valid,
		)
	}
	usageDuring, err := items.GetStorageQuotaUsage(ctx, workspaceID)
	if err != nil {
		t.Fatalf("load storage quota during ProcessHOCR: %v", err)
	}
	if usageDuring.ReservedUploadBlobBytes != usageBefore.ReservedUploadBlobBytes ||
		usageDuring.ReservedDatabaseBytes != usageBefore.ReservedDatabaseBytes ||
		usageDuring.ReservedItems != usageBefore.ReservedItems+1 ||
		usageDuring.ReservedImages != usageBefore.ReservedImages+1 {
		t.Fatalf("external hOCR in-flight quota = before %+v / during %+v", usageBefore, usageDuring)
	}

	if err := blocker.Commit(); err != nil {
		t.Fatalf("release ProcessHOCR quota observer: %v", err)
	}
	blockerCommitted = true
	var first *connect.Response[scribev1.ProcessHOCRResponse]
	select {
	case result := <-resultCh:
		if result.err != nil {
			t.Fatalf("ProcessHOCR external image URL: %v", result.err)
		}
		first = result.response
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for ProcessHOCR after quota inspection")
	}
	t.Cleanup(func() {
		_ = items.DeleteForWorkspace(context.Background(), first.Msg.GetItemId(), workspaceID)
	})

	persistedImage, err := items.GetImageForWorkspace(ctx, first.Msg.GetItemImageId(), workspaceID)
	if err != nil {
		t.Fatalf("load external hOCR image: %v", err)
	}
	if persistedImage.ImageURL != request.GetImageUrl() || persistedImage.StorageBytes != 0 {
		t.Fatalf("external hOCR image storage = %+v, want original URL with zero owned bytes", persistedImage)
	}
	usageAfter, err := items.GetStorageQuotaUsage(ctx, workspaceID)
	if err != nil {
		t.Fatalf("load storage quota after ProcessHOCR: %v", err)
	}
	if usageAfter.UploadBlobBytes != usageBefore.UploadBlobBytes ||
		usageAfter.ReservedUploadBlobBytes != usageBefore.ReservedUploadBlobBytes ||
		usageAfter.ReservedDatabaseBytes != usageBefore.ReservedDatabaseBytes ||
		usageAfter.ReservedItems != usageBefore.ReservedItems ||
		usageAfter.ReservedImages != usageBefore.ReservedImages ||
		usageAfter.Items != usageBefore.Items+1 ||
		usageAfter.Images != usageBefore.Images+1 ||
		usageAfter.DatabaseBytes <= usageBefore.DatabaseBytes {
		t.Fatalf("external hOCR committed quota = before %+v / after %+v", usageBefore, usageAfter)
	}

	replayed, err := handler.ProcessHOCR(ctx, connect.NewRequest(request))
	if err != nil {
		t.Fatalf("replay external hOCR import: %v", err)
	}
	if replayed.Msg.GetItemId() != first.Msg.GetItemId() || replayed.Msg.GetItemImageId() != first.Msg.GetItemImageId() {
		t.Fatalf(
			"replayed external hOCR IDs = %q/%d, want %q/%d",
			replayed.Msg.GetItemId(),
			replayed.Msg.GetItemImageId(),
			first.Msg.GetItemId(),
			first.Msg.GetItemImageId(),
		)
	}
	usageAfterReplay, err := items.GetStorageQuotaUsage(ctx, workspaceID)
	if err != nil {
		t.Fatalf("load storage quota after external hOCR replay: %v", err)
	}
	if usageAfterReplay != usageAfter {
		t.Fatalf("external hOCR replay inflated quota = before %+v / after %+v", usageAfter, usageAfterReplay)
	}
}

func TestProcessHOCRCommitsAndReplaysOneIdempotentResult(t *testing.T) {
	database := openTestDB(t)
	workspaceID, userID := createServerTestWorkspace(t, database)
	ocrRuns := store.NewOCRRunStore(database)
	items := store.NewItemStore(database)
	contexts := store.NewContextStore(database)
	annotations := store.NewAnnotationStore(database)
	jobs := store.NewTranscriptionJobStore(database)
	handler := NewHandler(ocrRuns, items, contexts, annotations, jobs, &auth.Manager{}, nil, nil)
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{
		Authenticated: true,
		UserID:        userID,
		WorkspaceID:   workspaceID,
		WorkspaceRole: "write",
	})
	const idempotencyKey = "process-hocr-replay"
	request := &scribev1.ProcessHOCRRequest{
		Hocr:                minimalHOCR,
		ImageUrl:            "https://images.example.test/page.jpg",
		IdempotencyKey:      idempotencyKey,
		Metadata:            `{"repository":"islandora"}`,
		ExternalReferenceId: "islandora:1234",
	}

	first, err := handler.ProcessHOCR(ctx, connect.NewRequest(request))
	if err != nil {
		t.Fatalf("first ProcessHOCR: %v", err)
	}
	t.Cleanup(func() {
		_ = items.DeleteForWorkspace(context.Background(), first.Msg.GetItemId(), workspaceID)
	})
	second, err := handler.ProcessHOCR(ctx, connect.NewRequest(request))
	if err != nil {
		t.Fatalf("replayed ProcessHOCR: %v", err)
	}
	if first.Msg.GetItemId() != second.Msg.GetItemId() || first.Msg.GetItemImageId() != second.Msg.GetItemImageId() {
		t.Fatalf("replay result = %q/%d, want %q/%d", second.Msg.GetItemId(), second.Msg.GetItemImageId(), first.Msg.GetItemId(), first.Msg.GetItemImageId())
	}
	if first.Msg.GetSessionId() == "" || first.Msg.GetSessionId() == first.Msg.GetItemId() || first.Msg.GetSessionId() != second.Msg.GetSessionId() {
		t.Fatalf("hOCR provenance session = first %q second %q item %q", first.Msg.GetSessionId(), second.Msg.GetSessionId(), first.Msg.GetItemId())
	}
	if first.Msg.GetTranscriptionJobId() != 0 || second.Msg.GetTranscriptionJobId() != 0 {
		t.Fatalf("synchronous hOCR job ids = %d/%d, want zero", first.Msg.GetTranscriptionJobId(), second.Msg.GetTranscriptionJobId())
	}
	storedItem, err := items.GetForWorkspace(ctx, first.Msg.GetItemId(), workspaceID)
	if err != nil {
		t.Fatalf("load hOCR item: %v", err)
	}
	if storedItem.ExternalReferenceID != request.GetExternalReferenceId() || storedItem.CallerIdempotencyKey != idempotencyKey || storedItem.Metadata["repository"] != "islandora" {
		t.Fatalf("hOCR item correlation data = %+v", storedItem)
	}

	mismatch := proto.Clone(request).(*scribev1.ProcessHOCRRequest)
	mismatch.Hocr = strings.Replace(minimalHOCR, "Course", "Changed", 1)
	if _, err := handler.ProcessHOCR(ctx, connect.NewRequest(mismatch)); connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("mismatched replay error = %v/%v, want already_exists", connect.CodeOf(err), err)
	}
	var itemCount, imageCount, pageCount, runCount int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM items WHERE workspace_id = ? AND source_type = 'hocr'`, workspaceID).Scan(&itemCount); err != nil {
		t.Fatalf("count hOCR items: %v", err)
	}
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM item_images WHERE item_id = ?`, first.Msg.GetItemId()).Scan(&imageCount); err != nil {
		t.Fatalf("count hOCR images: %v", err)
	}
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM annotation_pages WHERE workspace_id = ? AND item_image_id = ?`, workspaceID, first.Msg.GetItemImageId()).Scan(&pageCount); err != nil {
		t.Fatalf("count canonical pages: %v", err)
	}
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM ocr_runs WHERE item_image_id = ?`, first.Msg.GetItemImageId()).Scan(&runCount); err != nil {
		t.Fatalf("count OCR runs: %v", err)
	}
	if itemCount != 1 || imageCount != 1 || pageCount != 1 || runCount != 1 {
		t.Fatalf("durable hOCR result counts = items:%d images:%d pages:%d runs:%d; want one each", itemCount, imageCount, pageCount, runCount)
	}
	digest := sha256.Sum256([]byte(idempotencyKey))
	reservation, err := jobs.GetExternalRequest(ctx, workspaceID, "hocr-import", fmt.Sprintf("%x", digest[:]))
	if err != nil {
		t.Fatalf("load hOCR idempotency result: %v", err)
	}
	if reservation.Status != store.ExternalRequestStatusCompleted || reservation.ItemID != first.Msg.GetItemId() || reservation.ItemImageID != first.Msg.GetItemImageId() || reservation.SessionID != first.Msg.GetSessionId() || reservation.TranscriptionJobID != 0 {
		t.Fatalf("hOCR idempotency result = %#v", reservation)
	}
}
