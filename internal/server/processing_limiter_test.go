package server

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	"github.com/lehigh-university-libraries/scribe/internal/worklimit"
)

func TestSegmentationSlotsShareCapacityBySegmentor(t *testing.T) {
	limiter, err := worklimit.NewHierarchical(2, 2, 1)
	if err != nil {
		t.Fatalf("create processing limiter: %v", err)
	}
	handler := &Handler{processingLimiter: limiter}
	workspaceID := uint64(42)

	release, err := handler.acquireSegmentationProcessingSlot(context.Background(), workspaceID, store.Context{
		SegmentationModel:     "scribe",
		TranscriptionProvider: "tesseract",
	})
	if err != nil {
		t.Fatalf("acquire first segmentation slot: %v", err)
	}
	defer release()

	waitCtx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, err = handler.acquireSegmentationProcessingSlot(waitCtx, workspaceID, store.Context{
		SegmentationModel:     "scribe",
		TranscriptionProvider: "gemini",
	})
	if got := connect.CodeOf(err); got != connect.CodeDeadlineExceeded {
		t.Fatalf("second segmentation slot code = %v, want %v (error %v)", got, connect.CodeDeadlineExceeded, err)
	}
}

func TestSegmentationSlotsDoNotShareCapacityByTranscriber(t *testing.T) {
	limiter, err := worklimit.NewHierarchical(2, 2, 1)
	if err != nil {
		t.Fatalf("create processing limiter: %v", err)
	}
	handler := &Handler{processingLimiter: limiter}
	workspaceID := uint64(42)

	releaseScribe, err := handler.acquireSegmentationProcessingSlot(context.Background(), workspaceID, store.Context{
		SegmentationModel:     "scribe",
		TranscriptionProvider: "tesseract",
	})
	if err != nil {
		t.Fatalf("acquire scribe segmentation slot: %v", err)
	}
	defer releaseScribe()

	acquireCtx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	releaseTesseract, err := handler.acquireSegmentationProcessingSlot(acquireCtx, workspaceID, store.Context{
		SegmentationModel:     "tesseract",
		TranscriptionProvider: "tesseract",
	})
	if err != nil {
		t.Fatalf("unrelated segmentor was blocked by shared transcriber: %v", err)
	}
	releaseTesseract()
}

func TestSegmentationSlotDoesNotShareCapacityWithTranscriptionProvider(t *testing.T) {
	limiter, err := worklimit.NewHierarchical(2, 2, 1)
	if err != nil {
		t.Fatalf("create processing limiter: %v", err)
	}
	handler := &Handler{processingLimiter: limiter}
	workspaceID := uint64(42)
	processingContext := store.Context{
		SegmentationModel:     "tesseract",
		TranscriptionProvider: "tesseract",
	}

	releaseTranscription, err := handler.acquireTranscriptionProcessingSlot(context.Background(), workspaceID, processingContext)
	if err != nil {
		t.Fatalf("acquire transcription slot: %v", err)
	}
	defer releaseTranscription()

	acquireCtx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	releaseSegmentation, err := handler.acquireSegmentationProcessingSlot(acquireCtx, workspaceID, processingContext)
	if err != nil {
		t.Fatalf("segmentor was blocked by its same-named transcription provider: %v", err)
	}
	releaseSegmentation()
}

func TestTranscriptionSlotsShareCapacityByProvider(t *testing.T) {
	limiter, err := worklimit.NewHierarchical(2, 2, 1)
	if err != nil {
		t.Fatalf("create processing limiter: %v", err)
	}
	handler := &Handler{processingLimiter: limiter}
	workspaceID := uint64(42)

	release, err := handler.acquireTranscriptionProcessingSlot(context.Background(), workspaceID, store.Context{
		SegmentationModel:     "scribe",
		TranscriptionProvider: "tesseract",
	})
	if err != nil {
		t.Fatalf("acquire first transcription slot: %v", err)
	}
	defer release()

	waitCtx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, err = handler.acquireTranscriptionProcessingSlot(waitCtx, workspaceID, store.Context{
		SegmentationModel:     "tesseract",
		TranscriptionProvider: "tesseract",
	})
	if got := connect.CodeOf(err); got != connect.CodeDeadlineExceeded {
		t.Fatalf("second transcription slot code = %v, want %v (error %v)", got, connect.CodeDeadlineExceeded, err)
	}
}
