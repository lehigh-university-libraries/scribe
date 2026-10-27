package server

import (
	"context"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/google/uuid"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
)

func (h *Handler) PublishItemImageEdits(ctx context.Context, req *scribev1.PublishItemImageEditsRequest) (*scribev1.PublishItemImageEditsResponse, error) {
	itemImageID := req.GetItemImageId()
	if itemImageID == 0 {
		return nil, connect.Errorf(connect.CodeInvalidArgument, "item_image_id is required")
	}
	if req.GetExpectedRevision() == 0 {
		return nil, connect.Errorf(connect.CodeInvalidArgument, "expected_revision is required")
	}
	userID := h.currentUserID(ctx)
	var publishedBy *uint64
	if userID > 0 {
		publishedBy = &userID
	}
	published, err := h.annotations.PublishPage(ctx, h.currentWorkspaceID(ctx), itemImageID, store.AnnotationPublicationOptions{
		ExpectedRevision:  req.GetExpectedRevision(),
		PublishedByUserID: publishedBy,
		EventID:           uuid.NewString(),
		EventType:         "dev.scribe.annotations.published",
		Subject:           subjectForItemImage(itemImageID),
	})
	if err != nil {
		return nil, annotationConnectError(err)
	}
	return &scribev1.PublishItemImageEditsResponse{
		ItemImageId:        itemImageID,
		CanvasUri:          published.CanvasURI,
		AnnotationPageJson: published.Payload,
		PublishedAt:        published.PublishedAt.UTC().Format(time.RFC3339Nano),
		PublishedRevision:  published.PublishedRevision,
		PublicUrl:          published.PageID,
	}, nil
}
