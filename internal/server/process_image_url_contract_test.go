package server

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/lehigh-university-libraries/scribe/internal/auth"
	ocrhandlers "github.com/lehigh-university-libraries/scribe/internal/handlers"
	"github.com/lehigh-university-libraries/scribe/internal/hocr"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
	"google.golang.org/protobuf/proto"
)

type processImageURLContractOCR struct{}

func (*processImageURLContractOCR) SetProviderCallAuditLogger(hocr.ProviderCallAuditLogger) {}

func (*processImageURLContractOCR) ProcessImageURLWithContext(context.Context, string, hocr.ProcessingContext) (*ocrhandlers.ProcessResult, error) {
	return &ocrhandlers.ProcessResult{
		ImageURL:  "https://images.example.test/processed.jpg",
		HOCR:      minimalHOCR,
		PlainText: "Course Catalog",
		Provider:  "contract-test",
		Model:     "layout-test",
	}, nil
}

func (*processImageURLContractOCR) ProcessImageURLTransientWithContext(context.Context, string, hocr.ProcessingContext) (*ocrhandlers.ProcessResult, error) {
	return nil, fmt.Errorf("unexpected transient processing call")
}

func (*processImageURLContractOCR) ProcessImageUploadWithContext(context.Context, string, []byte, hocr.ProcessingContext) (*ocrhandlers.ProcessResult, error) {
	return nil, fmt.Errorf("unexpected upload processing call")
}

func (*processImageURLContractOCR) StoreUploadedImage(context.Context, string, []byte) (string, error) {
	return "", fmt.Errorf("unexpected upload storage call")
}

func (*processImageURLContractOCR) TranscribeImageFileWithContext(context.Context, string, string, string) (string, error) {
	return "", fmt.Errorf("unexpected transcription call")
}

func TestProcessImageURLReturnsDurableJobAndProvenanceSession(t *testing.T) {
	database := openTestDB(t)
	ctx := context.Background()
	workspaceID, userID := createServerTestWorkspace(t, database)
	contexts := store.NewContextStore(database)
	processingContext, err := contexts.Create(ctx, store.Context{
		UserID: &userID, WorkspaceID: &workspaceID, Name: "process-image-contract",
		SegmentationModel: "layout-test", TranscriptionProvider: "custom", TranscriptionModel: "contract-test",
	})
	if err != nil {
		t.Fatalf("create processing context: %v", err)
	}
	items := store.NewItemStore(database)
	usageBefore, err := items.GetStorageQuotaUsage(ctx, workspaceID)
	if err != nil {
		t.Fatalf("load storage quota before ProcessImageURL: %v", err)
	}
	jobs := store.NewTranscriptionJobStore(database)
	handler := NewHandler(store.NewOCRRunStore(database), items, contexts, store.NewAnnotationStore(database), jobs, &auth.Manager{}, nil, nil)
	handler.ocr = &processImageURLContractOCR{}
	ctx = auth.WithPrincipal(ctx, auth.Principal{
		Authenticated: true, UserID: userID, WorkspaceID: workspaceID, WorkspaceRole: "write",
	})
	request := &scribev1.ProcessImageURLRequest{
		ImageUrl:            "https://images.example.test/source.jpg",
		ContextId:           processingContext.ID,
		Metadata:            `{"repository":"islandora"}`,
		IdempotencyKey:      "process-image-contract",
		ExternalReferenceId: "islandora:5678",
	}
	response, err := handler.ProcessImageURL(ctx, connect.NewRequest(request))
	if err != nil {
		t.Fatalf("ProcessImageURL: %v", err)
	}
	t.Cleanup(func() {
		_ = items.DeleteForWorkspace(context.Background(), response.Msg.GetItemId(), workspaceID)
	})
	if response.Msg.GetTranscriptionJobId() == 0 {
		t.Fatal("ProcessImageURL returned no durable transcription job ID")
	}
	if !strings.HasPrefix(response.Msg.GetSessionId(), "processing_") || response.Msg.GetSessionId() == response.Msg.GetItemId() {
		t.Fatalf("ProcessImageURL session/item = %q/%q", response.Msg.GetSessionId(), response.Msg.GetItemId())
	}
	job, err := jobs.Get(ctx, response.Msg.GetTranscriptionJobId())
	if err != nil || job.ItemImageID != response.Msg.GetItemImageId() || job.Status != store.TranscriptionJobStatusPending {
		t.Fatalf("returned transcription job = %+v/%v", job, err)
	}
	item, err := items.GetForWorkspace(ctx, response.Msg.GetItemId(), workspaceID)
	if err != nil {
		t.Fatalf("load ProcessImageURL item: %v", err)
	}
	if item.ExternalReferenceID != request.GetExternalReferenceId() || item.CallerIdempotencyKey != request.GetIdempotencyKey() || item.Metadata["repository"] != "islandora" {
		t.Fatalf("ProcessImageURL correlation data = %+v", item)
	}
	persistedImage, err := items.GetImageForWorkspace(ctx, response.Msg.GetItemImageId(), workspaceID)
	if err != nil {
		t.Fatalf("load ProcessImageURL image: %v", err)
	}
	if persistedImage.ImageURL != "https://images.example.test/processed.jpg" || persistedImage.StorageBytes != 0 {
		t.Fatalf("ProcessImageURL external image storage = %+v", persistedImage)
	}
	usageAfter, err := items.GetStorageQuotaUsage(ctx, workspaceID)
	if err != nil {
		t.Fatalf("load storage quota after ProcessImageURL: %v", err)
	}
	if usageAfter.UploadBlobBytes != usageBefore.UploadBlobBytes || usageAfter.Items != usageBefore.Items+1 || usageAfter.Images != usageBefore.Images+1 {
		t.Fatalf("ProcessImageURL external reference quota = before %+v / after %+v; want no owned blob bytes and one item/image", usageBefore, usageAfter)
	}

	replayed, err := handler.ProcessImageURL(ctx, connect.NewRequest(request))
	if err != nil {
		t.Fatalf("replay ProcessImageURL: %v", err)
	}
	if replayed.Msg.GetTranscriptionJobId() != response.Msg.GetTranscriptionJobId() || replayed.Msg.GetSessionId() != response.Msg.GetSessionId() {
		t.Fatalf("replayed job/session = %d/%q, want %d/%q", replayed.Msg.GetTranscriptionJobId(), replayed.Msg.GetSessionId(), response.Msg.GetTranscriptionJobId(), response.Msg.GetSessionId())
	}
	mismatch := proto.Clone(request).(*scribev1.ProcessImageURLRequest)
	mismatch.ExternalReferenceId = "islandora:different"
	if _, err := handler.ProcessImageURL(ctx, connect.NewRequest(mismatch)); connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("correlation-mismatched replay error = %v/%v, want already_exists", connect.CodeOf(err), err)
	}
}
