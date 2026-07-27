package server

import (
	"context"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
)

func (h *Handler) PublishItemImageEdits(ctx context.Context, req *connect.Request[scribev1.PublishItemImageEditsRequest]) (*connect.Response[scribev1.PublishItemImageEditsResponse], error) {
	itemImageID := req.Msg.GetItemImageId()
	if itemImageID == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("item_image_id is required"))
	}
	if req.Msg.GetExpectedRevision() == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("expected_revision is required"))
	}
	userID := h.currentUserID(ctx)
	var publishedBy *uint64
	if userID > 0 {
		publishedBy = &userID
	}
	published, err := h.annotations.PublishPage(ctx, h.currentWorkspaceID(ctx), itemImageID, store.AnnotationPublicationOptions{
		ExpectedRevision:  req.Msg.GetExpectedRevision(),
		PublishedByUserID: publishedBy,
		EventID:           uuid.NewString(),
		EventType:         "dev.scribe.annotations.published",
		Subject:           subjectForItemImage(itemImageID),
	})
	if err != nil {
		return nil, annotationConnectError(err)
	}
	return connect.NewResponse(&scribev1.PublishItemImageEditsResponse{
		ItemImageId:        itemImageID,
		CanvasUri:          published.CanvasURI,
		AnnotationPageJson: published.Payload,
		PublishedAt:        published.PublishedAt.UTC().Format(time.RFC3339Nano),
		PublishedRevision:  published.PublishedRevision,
		PublicUrl:          published.PageID,
	}), nil
}
