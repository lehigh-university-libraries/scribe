package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/lehigh-university-libraries/htr/pkg/providers"
	ocrhandlers "github.com/lehigh-university-libraries/scribe/internal/handlers"
	"github.com/lehigh-university-libraries/scribe/internal/hocr"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
	"github.com/lehigh-university-libraries/scribe/proto/scribe/v1/scribev1connect"
)

type uploadBatchAcceptanceCall struct {
	filename          string
	contentSHA256     string
	processingContext hocr.ProcessingContext
}

type uploadBatchAcceptanceBlock struct {
	filename    string
	started     chan struct{}
	release     chan struct{}
	startedOnce sync.Once
	releaseOnce sync.Once
}

type uploadBatchAcceptanceOCR struct {
	mu                sync.Mutex
	calls             []uploadBatchAcceptanceCall
	successfulUploads []string
	failOnceFilename  string
	failOnceError     error
	failedOnce        bool
	block             *uploadBatchAcceptanceBlock
}

const uploadBatchAcceptanceHOCR = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html>
<html>
  <body>
    <div class="ocr_page" id="page_1" title="bbox 0 0 2 2">
      <span class="ocr_line" id="line_1" title="bbox 0 0 2 2">
        <span class="ocrx_word" id="word_1" title="bbox 0 0 1 1; x_wconf 98">new</span>
      </span>
    </div>
  </body>
</html>`

func (*uploadBatchAcceptanceOCR) SetProviderCallAuditLogger(hocr.ProviderCallAuditLogger) {}

func (*uploadBatchAcceptanceOCR) ProcessImageURLWithContext(context.Context, string, hocr.ProcessingContext) (*ocrhandlers.ProcessResult, error) {
	return nil, fmt.Errorf("unexpected durable URL processing call")
}

func (*uploadBatchAcceptanceOCR) ProcessImageURLTransientWithContext(context.Context, string, hocr.ProcessingContext) (*ocrhandlers.ProcessResult, error) {
	return nil, fmt.Errorf("unexpected transient URL processing call")
}

func (f *uploadBatchAcceptanceOCR) ProcessImageUploadWithContext(ctx context.Context, filename string, imageData []byte, processingContext hocr.ProcessingContext) (*ocrhandlers.ProcessResult, error) {
	digest := sha256.Sum256(imageData)
	call := uploadBatchAcceptanceCall{
		filename:          filename,
		contentSHA256:     fmt.Sprintf("%x", digest[:]),
		processingContext: cloneUploadBatchProcessingContext(processingContext),
	}

	f.mu.Lock()
	f.calls = append(f.calls, call)
	callNumber := len(f.calls)
	shouldFail := filename == f.failOnceFilename && !f.failedOnce
	if shouldFail {
		f.failedOnce = true
	}
	var block *uploadBatchAcceptanceBlock
	if f.block != nil && filename == f.block.filename {
		block = f.block
		f.block = nil
	}
	f.mu.Unlock()

	if shouldFail {
		if f.failOnceError != nil {
			return nil, f.failOnceError
		}
		return nil, fmt.Errorf("temporary segmentation failure")
	}
	if block != nil {
		block.startedOnce.Do(func() { close(block.started) })
		select {
		case <-block.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	uploadName := fmt.Sprintf("%x-00000000-0000-4000-8000-%012x.png", digest[:], callNumber)
	f.mu.Lock()
	f.successfulUploads = append(f.successfulUploads, uploadName)
	f.mu.Unlock()
	return &ocrhandlers.ProcessResult{
		SessionID:   fmt.Sprintf("upload-batch-acceptance-%d", callNumber),
		HOCR:        uploadBatchAcceptanceHOCR,
		PlainText:   "new segmented text",
		ImageURL:    "/static/uploads/" + uploadName,
		StoredBytes: uint64(len(imageData)),
		Provider:    "test-segmentor",
		Model:       "test-layout",
	}, nil
}

func (*uploadBatchAcceptanceOCR) StoreUploadedImage(context.Context, string, []byte) (string, error) {
	return "", fmt.Errorf("unexpected direct upload persistence call")
}

func (*uploadBatchAcceptanceOCR) TranscribeImageFileWithContext(context.Context, string, string, string) (string, error) {
	return "", fmt.Errorf("unexpected synchronous transcription call")
}

func (f *uploadBatchAcceptanceOCR) blockNext(filename string) (<-chan struct{}, func()) {
	block := &uploadBatchAcceptanceBlock{
		filename: filename,
		started:  make(chan struct{}),
		release:  make(chan struct{}),
	}
	f.mu.Lock()
	f.block = block
	f.mu.Unlock()
	return block.started, func() {
		block.releaseOnce.Do(func() { close(block.release) })
	}
}

func (f *uploadBatchAcceptanceOCR) recordedCalls() []uploadBatchAcceptanceCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	calls := make([]uploadBatchAcceptanceCall, len(f.calls))
	copy(calls, f.calls)
	return calls
}

func (f *uploadBatchAcceptanceOCR) uploadNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.successfulUploads...)
}

func cloneUploadBatchProcessingContext(value hocr.ProcessingContext) hocr.ProcessingContext {
	cloned := value
	if value.Temperature != nil {
		temperature := *value.Temperature
		cloned.Temperature = &temperature
	}
	return cloned
}

func TestUploadBatchConnectAcceptanceResumeIdempotencyAndCancellation(t *testing.T) {
	database := openTestDB(t)
	ctx := context.Background()
	workspaceID, userID := createServerTestWorkspace(t, database)
	contextStore := store.NewContextStore(database)
	selectedTemperature := 0.37
	selectedContext, err := contextStore.Create(ctx, store.Context{
		UserID:                &userID,
		WorkspaceID:           &workspaceID,
		Name:                  "upload-batch-acceptance-" + uuid.NewString(),
		SegmentationModel:     "tesseract",
		TranscriptionProvider: "tesseract",
		TranscriptionModel:    "tesseract",
		Temperature:           &selectedTemperature,
		SystemPrompt:          "immutable selected prompt",
	})
	if err != nil {
		t.Fatalf("create selected context: %v", err)
	}

	ocrRuns := store.NewOCRRunStore(database)
	items := store.NewItemStore(database)
	annotations := store.NewAnnotationStore(database)
	jobs := store.NewTranscriptionJobStore(database)
	fakeOCR := &uploadBatchAcceptanceOCR{
		failOnceFilename: "resume-page-2.png",
		failOnceError: providers.NewError(
			providers.ErrorAuthentication,
			http.StatusForbidden,
			false,
			nil,
		),
	}
	handler := NewHandler(ocrRuns, items, contextStore, annotations, jobs, nil, nil, nil)
	handler.ocr = fakeOCR
	appServer := newTenantScopedServer(t, handler, map[string]testTenantIdentity{
		"workspace": {workspaceID: workspaceID, userID: userID},
	})
	itemClient := scribev1connect.NewItemServiceClient(http.DefaultClient, appServer.URL)
	t.Cleanup(func() {
		for _, uploadName := range fakeOCR.uploadNames() {
			_, _ = database.ExecContext(context.Background(), `DELETE FROM resource_cleanup_outbox WHERE kind = 'upload_blob' AND resource_key = ?`, uploadName)
		}
	})

	resumePage1 := uploadBatchAcceptancePNG(t, color.RGBA{R: 0x20, G: 0x40, B: 0x60, A: 0xff})
	resumePage2 := uploadBatchAcceptancePNG(t, color.RGBA{R: 0x70, G: 0x80, B: 0x90, A: 0xff})
	resumeBatchID := "resume-" + uuid.NewString()
	resumeRequest := &scribev1.StartUploadBatchRequest{
		BatchId:   resumeBatchID,
		Name:      "Resumable upload acceptance",
		ContextId: selectedContext.ID,
		Files: []*scribev1.UploadBatchFileInput{
			uploadBatchAcceptanceInput("resume-page-1.png", resumePage1),
			uploadBatchAcceptanceInput("resume-page-2.png", resumePage2),
		},
	}
	started, err := itemClient.StartUploadBatch(ctx, tenantConnectRequest("workspace", resumeRequest))
	if err != nil {
		t.Fatalf("StartUploadBatch: %v", err)
	}
	if started.Msg.GetItem() == nil || started.Msg.GetBatch() == nil || started.Msg.GetBatch().GetStatus() != scribev1.UploadBatchStatus_UPLOAD_BATCH_STATUS_IN_PROGRESS {
		t.Fatalf("started upload batch = %#v", started.Msg)
	}
	startedItemID := started.Msg.GetItem().GetId()
	if startedItemID == "" || started.Msg.GetBatch().GetContextId() != selectedContext.ID || len(started.Msg.GetBatch().GetFiles()) != 2 {
		t.Fatalf("started upload batch identity = %#v", started.Msg.GetBatch())
	}
	storedBatch, err := items.GetUploadBatch(ctx, workspaceID, resumeBatchID)
	if err != nil {
		t.Fatalf("load started batch: %v", err)
	}
	storedSelectedContext, err := storedBatch.Context()
	if err != nil {
		t.Fatalf("decode started batch context: %v", err)
	}
	assertUploadBatchStoredContext(t, storedSelectedContext, selectedContext)

	mutatedContext := selectedContext
	mutatedTemperature := 0.91
	mutatedContext.SegmentationModel = "scribe"
	mutatedContext.Temperature = &mutatedTemperature
	mutatedContext.SystemPrompt = "context changed after the batch started"
	if _, err := contextStore.UpdateForWorkspace(ctx, mutatedContext, workspaceID, userID); err != nil {
		t.Fatalf("mutate selected context after batch start: %v", err)
	}
	restarted, err := itemClient.StartUploadBatch(ctx, tenantConnectRequest("workspace", resumeRequest))
	if err != nil {
		t.Fatalf("replay StartUploadBatch: %v", err)
	}
	if restarted.Msg.GetItem().GetId() != startedItemID || restarted.Msg.GetBatch().GetId() != resumeBatchID {
		t.Fatalf("replayed batch created different state: item=%q batch=%q", restarted.Msg.GetItem().GetId(), restarted.Msg.GetBatch().GetId())
	}

	firstUpload, err := itemClient.UploadItemImage(ctx, tenantConnectRequest("workspace", &scribev1.UploadItemImageRequest{
		BatchId: resumeBatchID, Sequence: 1, ImageData: resumePage1,
	}))
	if err != nil {
		t.Fatalf("UploadItemImage sequence 1: %v", err)
	}
	if firstUpload.Msg.GetImage() == nil || firstUpload.Msg.GetTranscriptionJobId() == 0 ||
		firstUpload.Msg.GetBatch().GetCompletedFiles() != 1 || firstUpload.Msg.GetBatch().GetStatus() != scribev1.UploadBatchStatus_UPLOAD_BATCH_STATUS_IN_PROGRESS {
		t.Fatalf("first upload response = %#v", firstUpload.Msg)
	}

	_, err = itemClient.UploadItemImage(ctx, tenantConnectRequest("workspace", &scribev1.UploadItemImageRequest{
		BatchId: resumeBatchID, Sequence: 2, ImageData: resumePage2,
	}))
	assertConnectCode(t, err, connect.CodeInternal)
	partial, err := itemClient.GetUploadBatch(ctx, tenantConnectRequest("workspace", &scribev1.GetUploadBatchRequest{BatchId: resumeBatchID}))
	if err != nil {
		t.Fatalf("GetUploadBatch after partial failure: %v", err)
	}
	partialBatch := partial.Msg.GetBatch()
	failedFile := uploadBatchAcceptanceFile(t, partialBatch, 2)
	if partialBatch.GetStatus() != scribev1.UploadBatchStatus_UPLOAD_BATCH_STATUS_IN_PROGRESS ||
		partialBatch.GetCompletedFiles() != 1 || partialBatch.GetFailedFiles() != 1 ||
		failedFile.GetStatus() != scribev1.UploadBatchFileStatus_UPLOAD_BATCH_FILE_STATUS_FAILED || failedFile.GetAttemptCount() != 1 ||
		failedFile.GetErrorMessage() != "provider request failed with HTTP status 403" {
		t.Fatalf("partial upload batch = %#v", partialBatch)
	}

	retriedUpload, err := itemClient.UploadItemImage(ctx, tenantConnectRequest("workspace", &scribev1.UploadItemImageRequest{
		BatchId: resumeBatchID, Sequence: 2, ImageData: resumePage2,
	}))
	if err != nil {
		t.Fatalf("retry UploadItemImage sequence 2: %v", err)
	}
	retriedFile := uploadBatchAcceptanceFile(t, retriedUpload.Msg.GetBatch(), 2)
	if retriedUpload.Msg.GetImage() == nil || retriedUpload.Msg.GetTranscriptionJobId() == 0 ||
		retriedUpload.Msg.GetBatch().GetStatus() != scribev1.UploadBatchStatus_UPLOAD_BATCH_STATUS_COMPLETED ||
		retriedUpload.Msg.GetBatch().GetCompletedFiles() != 2 || retriedUpload.Msg.GetBatch().GetFailedFiles() != 0 ||
		retriedFile.GetStatus() != scribev1.UploadBatchFileStatus_UPLOAD_BATCH_FILE_STATUS_COMPLETED || retriedFile.GetAttemptCount() != 2 {
		t.Fatalf("retried upload response = %#v", retriedUpload.Msg)
	}

	callCountBeforeReplay := len(fakeOCR.recordedCalls())
	replayedUpload, err := itemClient.UploadItemImage(ctx, tenantConnectRequest("workspace", &scribev1.UploadItemImageRequest{
		BatchId: resumeBatchID, Sequence: 1, ImageData: resumePage1,
	}))
	if err != nil {
		t.Fatalf("replay completed UploadItemImage: %v", err)
	}
	if replayedUpload.Msg.GetImage().GetId() != firstUpload.Msg.GetImage().GetId() ||
		replayedUpload.Msg.GetTranscriptionJobId() != firstUpload.Msg.GetTranscriptionJobId() ||
		len(fakeOCR.recordedCalls()) != callCountBeforeReplay {
		t.Fatalf("completed upload replay duplicated work: first=%#v replay=%#v calls=%d/%d", firstUpload.Msg, replayedUpload.Msg, callCountBeforeReplay, len(fakeOCR.recordedCalls()))
	}
	completed, err := itemClient.GetUploadBatch(ctx, tenantConnectRequest("workspace", &scribev1.GetUploadBatchRequest{BatchId: resumeBatchID}))
	if err != nil {
		t.Fatalf("GetUploadBatch after completion: %v", err)
	}
	if completed.Msg.GetItem().GetId() != startedItemID || len(completed.Msg.GetItem().GetImages()) != 2 ||
		completed.Msg.GetBatch().GetStatus() != scribev1.UploadBatchStatus_UPLOAD_BATCH_STATUS_COMPLETED {
		t.Fatalf("completed durable batch = %#v", completed.Msg)
	}

	resumeCalls := fakeOCR.recordedCalls()
	wantResumeFilenames := []string{"resume-page-1.png", "resume-page-2.png", "resume-page-2.png"}
	wantResumeDigests := []string{
		resumeRequest.GetFiles()[0].GetContentSha256(),
		resumeRequest.GetFiles()[1].GetContentSha256(),
		resumeRequest.GetFiles()[1].GetContentSha256(),
	}
	if len(resumeCalls) != len(wantResumeFilenames) {
		t.Fatalf("segmentation calls = %d, want %d: %#v", len(resumeCalls), len(wantResumeFilenames), resumeCalls)
	}
	for index, call := range resumeCalls {
		if call.filename != wantResumeFilenames[index] || call.contentSHA256 != wantResumeDigests[index] {
			t.Fatalf("segmentation call %d input = %q/%q, want %q/%q", index+1, call.filename, call.contentSHA256, wantResumeFilenames[index], wantResumeDigests[index])
		}
		assertUploadBatchProcessingContext(t, call.processingContext, selectedContext)
	}

	for _, upload := range []*connect.Response[scribev1.UploadItemImageResponse]{firstUpload, retriedUpload} {
		job, err := jobs.Get(ctx, upload.Msg.GetTranscriptionJobId())
		if err != nil {
			t.Fatalf("load durable transcription job %d: %v", upload.Msg.GetTranscriptionJobId(), err)
		}
		if job.Status != store.TranscriptionJobStatusPending || job.ContextID == nil || *job.ContextID != selectedContext.ID || job.InputRevision == 0 {
			t.Fatalf("durable transcription job = %+v", job)
		}
		var snapshottedContext store.Context
		if err := json.Unmarshal(job.ContextSnapshot, &snapshottedContext); err != nil {
			t.Fatalf("decode durable job context snapshot: %v", err)
		}
		assertUploadBatchStoredContext(t, snapshottedContext, selectedContext)
		imageJobs, err := jobs.ListPage(ctx, workspaceID, upload.Msg.GetImage().GetId(), store.MaxTranscriptionJobPageSize, nil)
		if err != nil || len(imageJobs.Jobs) != 1 || imageJobs.Jobs[0].ID != job.ID {
			t.Fatalf("jobs for image %d = %+v/%v, want only %d", upload.Msg.GetImage().GetId(), imageJobs, err, job.ID)
		}
	}

	cancelPage1 := uploadBatchAcceptancePNG(t, color.RGBA{R: 0xa0, G: 0x30, B: 0x20, A: 0xff})
	cancelPage2 := uploadBatchAcceptancePNG(t, color.RGBA{R: 0x10, G: 0xc0, B: 0x50, A: 0xff})
	cancelBatchID := "cancel-" + uuid.NewString()
	cancelRequest := &scribev1.StartUploadBatchRequest{
		BatchId:   cancelBatchID,
		Name:      "Cancellation fence acceptance",
		ContextId: selectedContext.ID,
		Files: []*scribev1.UploadBatchFileInput{
			uploadBatchAcceptanceInput("cancel-page-1.png", cancelPage1),
			uploadBatchAcceptanceInput("cancel-page-2.png", cancelPage2),
		},
	}
	if _, err := itemClient.StartUploadBatch(ctx, tenantConnectRequest("workspace", cancelRequest)); err != nil {
		t.Fatalf("StartUploadBatch for cancellation: %v", err)
	}
	cancelFirstUpload, err := itemClient.UploadItemImage(ctx, tenantConnectRequest("workspace", &scribev1.UploadItemImageRequest{
		BatchId: cancelBatchID, Sequence: 1, ImageData: cancelPage1,
	}))
	if err != nil {
		t.Fatalf("UploadItemImage before cancellation: %v", err)
	}
	if cancelFirstUpload.Msg.GetBatch().GetCompletedFiles() != 1 || cancelFirstUpload.Msg.GetTranscriptionJobId() == 0 {
		t.Fatalf("first cancellation-batch upload = %#v", cancelFirstUpload.Msg)
	}

	blocked, releaseBlockedUpload := fakeOCR.blockNext("cancel-page-2.png")
	t.Cleanup(releaseBlockedUpload)
	blockedUploadErr := make(chan error, 1)
	go func() {
		_, uploadErr := itemClient.UploadItemImage(context.Background(), tenantConnectRequest("workspace", &scribev1.UploadItemImageRequest{
			BatchId: cancelBatchID, Sequence: 2, ImageData: cancelPage2,
		}))
		blockedUploadErr <- uploadErr
	}()
	select {
	case <-blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for in-flight segmentation call")
	}

	canceled, err := itemClient.CancelUploadBatch(ctx, tenantConnectRequest("workspace", &scribev1.CancelUploadBatchRequest{BatchId: cancelBatchID}))
	if err != nil {
		t.Fatalf("CancelUploadBatch: %v", err)
	}
	canceledBatch := canceled.Msg.GetBatch()
	if canceledBatch.GetStatus() != scribev1.UploadBatchStatus_UPLOAD_BATCH_STATUS_CANCELED || canceledBatch.GetCompletedFiles() != 1 ||
		uploadBatchAcceptanceFile(t, canceledBatch, 1).GetStatus() != scribev1.UploadBatchFileStatus_UPLOAD_BATCH_FILE_STATUS_COMPLETED ||
		uploadBatchAcceptanceFile(t, canceledBatch, 2).GetStatus() != scribev1.UploadBatchFileStatus_UPLOAD_BATCH_FILE_STATUS_CANCELED {
		t.Fatalf("canceled upload batch = %#v", canceledBatch)
	}
	idempotentCancel, err := itemClient.CancelUploadBatch(ctx, tenantConnectRequest("workspace", &scribev1.CancelUploadBatchRequest{BatchId: cancelBatchID}))
	if err != nil || idempotentCancel.Msg.GetBatch().GetStatus() != scribev1.UploadBatchStatus_UPLOAD_BATCH_STATUS_CANCELED {
		t.Fatalf("idempotent CancelUploadBatch = %#v/%v", idempotentCancel, err)
	}

	releaseBlockedUpload()
	select {
	case uploadErr := <-blockedUploadErr:
		assertConnectCode(t, uploadErr, connect.CodeFailedPrecondition)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for canceled upload to observe its fence")
	}
	callsAfterCanceledUpload := len(fakeOCR.recordedCalls())
	if callsAfterCanceledUpload != 5 {
		t.Fatalf("segmentation calls after cancellation = %d, want 5", callsAfterCanceledUpload)
	}

	canceledJob, err := jobs.Get(ctx, cancelFirstUpload.Msg.GetTranscriptionJobId())
	if err != nil || canceledJob.Status != store.TranscriptionJobStatusCanceled {
		t.Fatalf("committed batch job after cancellation = %+v/%v", canceledJob, err)
	}
	canceledState, err := itemClient.GetUploadBatch(ctx, tenantConnectRequest("workspace", &scribev1.GetUploadBatchRequest{BatchId: cancelBatchID}))
	if err != nil {
		t.Fatalf("GetUploadBatch after cancellation: %v", err)
	}
	canceledFile := uploadBatchAcceptanceFile(t, canceledState.Msg.GetBatch(), 2)
	if canceledState.Msg.GetBatch().GetStatus() != scribev1.UploadBatchStatus_UPLOAD_BATCH_STATUS_CANCELED ||
		len(canceledState.Msg.GetItem().GetImages()) != 1 || canceledFile.GetItemImageId() != 0 || canceledFile.GetTranscriptionJobId() != 0 {
		t.Fatalf("durable canceled batch state = %#v", canceledState.Msg)
	}

	workspaceJobs, err := jobs.ListPage(ctx, workspaceID, 0, store.MaxTranscriptionJobPageSize, nil)
	if err != nil || len(workspaceJobs.Jobs) != 3 {
		t.Fatalf("workspace jobs after upload batches = %+v/%v, want exactly three", workspaceJobs, err)
	}
	_, err = itemClient.UploadItemImage(ctx, tenantConnectRequest("workspace", &scribev1.UploadItemImageRequest{
		BatchId: cancelBatchID, Sequence: 2, ImageData: cancelPage2,
	}))
	assertConnectCode(t, err, connect.CodeFailedPrecondition)
	if len(fakeOCR.recordedCalls()) != callsAfterCanceledUpload {
		t.Fatalf("post-cancel upload reached segmentation: calls=%d, want %d", len(fakeOCR.recordedCalls()), callsAfterCanceledUpload)
	}

	uploadNames := fakeOCR.uploadNames()
	if len(uploadNames) != 4 {
		t.Fatalf("successful fake uploads = %#v, want four provider results", uploadNames)
	}
	var orphanCleanupCount int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM resource_cleanup_outbox WHERE kind = 'upload_blob' AND resource_key = ?`, uploadNames[len(uploadNames)-1]).Scan(&orphanCleanupCount); err != nil {
		t.Fatalf("count fenced upload cleanup: %v", err)
	}
	if orphanCleanupCount != 1 {
		t.Fatalf("fenced upload cleanup count = %d, want 1", orphanCleanupCount)
	}
}

func uploadBatchAcceptancePNG(t *testing.T, fill color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			img.SetRGBA(x, y, fill)
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatalf("encode PNG fixture: %v", err)
	}
	return encoded.Bytes()
}

func uploadBatchAcceptanceInput(filename string, imageData []byte) *scribev1.UploadBatchFileInput {
	digest := sha256.Sum256(imageData)
	return &scribev1.UploadBatchFileInput{
		Filename:      filename,
		Size:          uint64(len(imageData)),
		ContentSha256: fmt.Sprintf("%x", digest[:]),
	}
}

func uploadBatchAcceptanceFile(t *testing.T, batch *scribev1.UploadBatch, sequence uint32) *scribev1.UploadBatchFile {
	t.Helper()
	if batch != nil {
		for _, file := range batch.GetFiles() {
			if file.GetSequence() == sequence {
				return file
			}
		}
	}
	t.Fatalf("upload batch has no sequence %d: %#v", sequence, batch)
	return nil
}

func assertUploadBatchProcessingContext(t *testing.T, got hocr.ProcessingContext, want store.Context) {
	t.Helper()
	if got.SegmentationModel != want.SegmentationModel || got.TranscriptionProvider != want.TranscriptionProvider ||
		got.TranscriptionModel != want.TranscriptionModel || got.SystemPrompt != want.SystemPrompt || !got.SegmentOnly {
		t.Fatalf("processing context = %+v, want immutable selection %+v with segment_only", got, want)
	}
	if got.Temperature == nil || want.Temperature == nil || *got.Temperature != *want.Temperature {
		t.Fatalf("processing temperature = %v, want %v", got.Temperature, want.Temperature)
	}
}

func assertUploadBatchStoredContext(t *testing.T, got, want store.Context) {
	t.Helper()
	if got.ID != want.ID || got.UserID == nil || want.UserID == nil || *got.UserID != *want.UserID ||
		got.WorkspaceID == nil || want.WorkspaceID == nil || *got.WorkspaceID != *want.WorkspaceID ||
		got.Name != want.Name || got.SegmentationModel != want.SegmentationModel ||
		got.TranscriptionProvider != want.TranscriptionProvider || got.TranscriptionModel != want.TranscriptionModel ||
		got.SystemPrompt != want.SystemPrompt {
		t.Fatalf("stored context snapshot = %+v, want %+v", got, want)
	}
	if got.Temperature == nil || want.Temperature == nil || *got.Temperature != *want.Temperature {
		t.Fatalf("stored context temperature = %v, want %v", got.Temperature, want.Temperature)
	}
}
