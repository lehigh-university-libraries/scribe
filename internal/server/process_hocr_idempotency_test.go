package server

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/lehigh-university-libraries/scribe/internal/auth"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
	"google.golang.org/protobuf/proto"
)

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
		Hocr:           minimalHOCR,
		ImageUrl:       "https://images.example.test/page.jpg",
		IdempotencyKey: idempotencyKey,
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
	if reservation.Status != store.ExternalRequestStatusCompleted || reservation.ItemID != first.Msg.GetItemId() || reservation.ItemImageID != first.Msg.GetItemImageId() {
		t.Fatalf("hOCR idempotency result = %#v", reservation)
	}
}
