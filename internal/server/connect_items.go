package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/lehigh-university-libraries/scribe/internal/db"
	ocrhandlers "github.com/lehigh-university-libraries/scribe/internal/handlers"
	"github.com/lehigh-university-libraries/scribe/internal/hocr"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/safehttp"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	"github.com/lehigh-university-libraries/scribe/internal/uploadlimits"
	"github.com/lehigh-university-libraries/scribe/internal/uploadref"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
	"golang.org/x/sync/errgroup"
)

const (
	maxRemoteHOCRBytes               int64  = 10 << 20
	maxInlineHOCRBytes                      = 10 << 20
	maxUploadBatchFiles                     = 1000
	maxDeclaredImageBytes            uint64 = uint64(uploadlimits.MaxImageBytes)
	maxUploadBatchTotalBytes         uint64 = 2 << 30
	maxItemNameRunes                        = 255
	maxItemMetadataBytes                    = 1 << 20
	uploadBatchLeaseHeartbeatEvery          = 5 * time.Minute
	uploadBatchLeaseRenewTimeout            = 10 * time.Second
	defaultManifestImportTimeout            = 90 * time.Second
	maxConcurrentManifestHOCRFetches        = 4
)

type uploadBatchFailureStage uint8

const (
	uploadBatchFailureAdmission uploadBatchFailureStage = iota
	uploadBatchFailureSegmentationOutput
	uploadBatchFailureQuotaResize
	uploadBatchFailureLeaseRenewal
	uploadBatchFailureImageCommit
	uploadBatchFailureOCRRunCommit
	uploadBatchFailureAnnotationCommit
	uploadBatchFailureTranscriptionEnqueue
	uploadBatchFailureItemReload
	uploadBatchFailureBatchCommit
)

var errManifestImportBudgetExceeded = errors.New("manifest import byte budget exceeded")

// --- ItemService Connect handlers ---

func (h *Handler) ListItems(ctx context.Context, req *connect.Request[scribev1.ListItemsRequest]) (*connect.Response[scribev1.ListItemsResponse], error) {
	if h.itemPageTokens == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("item pagination is not configured"))
	}
	workspaceID := h.currentWorkspaceID(ctx)
	pageSize, query, cursor, err := normalizeItemPageRequest(req.Msg.GetPageSize(), req.Msg.GetPageToken(), workspaceID, req.Msg.GetQuery(), h.itemPageTokens)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	page, err := h.items.ListPage(ctx, workspaceID, pageSize, query, cursor)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	nextPageToken, err := h.itemPageTokens.encode(page.NextCursor, workspaceID, query)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("encode item page token"))
	}
	resp := &scribev1.ListItemsResponse{
		Items:         make([]*scribev1.ItemSummary, 0, len(page.Items)),
		NextPageToken: nextPageToken,
	}
	for _, it := range page.Items {
		resp.Items = append(resp.Items, storeItemSummaryToProto(it))
	}
	return connect.NewResponse(resp), nil
}

func (h *Handler) GetItem(ctx context.Context, req *connect.Request[scribev1.GetItemRequest]) (*connect.Response[scribev1.GetItemResponse], error) {
	it, err := h.itemForRequest(ctx, req.Msg.GetItemId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("item not found"))
	}
	revisions, err := h.annotations.ListItemRevisions(ctx, h.currentWorkspaceID(ctx), it.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("load item annotation revisions"))
	}
	response := &scribev1.GetItemResponse{
		Item:                storeItemToProto(it),
		AnnotationRevisions: make([]*scribev1.ItemImageRevision, 0, len(revisions)),
	}
	for _, revision := range revisions {
		response.AnnotationRevisions = append(response.AnnotationRevisions, &scribev1.ItemImageRevision{
			ItemImageId: revision.ItemImageID,
			Revision:    revision.Revision,
		})
	}
	return connect.NewResponse(response), nil
}

func (h *Handler) PrepareItemExport(ctx context.Context, req *connect.Request[scribev1.PrepareItemExportRequest]) (*connect.Response[scribev1.PrepareItemExportResponse], error) {
	if h.itemExportTokens == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("item export signer is not configured"))
	}
	format, err := annotationExportFormatName(req.Msg.GetFormat())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	expected := make(map[uint64]uint64, len(req.Msg.GetExpectedRevisions()))
	for _, entry := range req.Msg.GetExpectedRevisions() {
		if entry == nil || entry.GetItemImageId() == 0 || entry.GetRevision() == 0 {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("every expected revision requires an item image and revision"))
		}
		if _, duplicate := expected[entry.GetItemImageId()]; duplicate {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("expected revisions must contain unique item image IDs"))
		}
		expected[entry.GetItemImageId()] = entry.GetRevision()
	}
	workspaceID := h.currentWorkspaceID(ctx)
	release, allowed := h.exportLimiter.TryAcquire(fmt.Sprintf("workspace:%d", workspaceID))
	if !allowed {
		return nil, connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("export concurrency limit exceeded"))
	}
	defer release()
	plan, err := h.loadCanonicalItemExportSnapshot(ctx, req.Msg.GetItemId(), format, expected)
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled):
			return nil, connect.NewError(connect.CodeCanceled, fmt.Errorf("item export preparation canceled"))
		case errors.Is(err, context.DeadlineExceeded):
			return nil, connect.NewError(connect.CodeDeadlineExceeded, fmt.Errorf("item export preparation timed out"))
		case errors.Is(err, errItemExportRevisionConflict):
			return nil, connect.NewError(connect.CodeAborted, fmt.Errorf("canonical annotations changed; reload before exporting"))
		case errors.Is(err, errItemExportSourceLimit):
			return nil, connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("item export exceeds the source-byte limit"))
		case errors.Is(err, errItemExportInvalid):
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid item export request"))
		default:
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("prepare item export failed"))
		}
	}
	token, expiresAt, err := h.itemExportTokens.encode(workspaceID, plan)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("prepare item export failed"))
	}
	revisions := make([]*scribev1.ItemImageRevision, 0, len(plan.Pages))
	for _, page := range plan.Pages {
		revisions = append(revisions, &scribev1.ItemImageRevision{ItemImageId: page.Image.ID, Revision: page.Page.Revision})
	}
	return connect.NewResponse(&scribev1.PrepareItemExportResponse{
		DownloadUrl: "/v1/item-exports/" + token,
		Filename:    plan.Filename,
		MediaType:   plan.MediaType,
		Revisions:   revisions,
		ExpiresAt:   expiresAt.Format(time.RFC3339Nano),
	}), nil
}

func (h *Handler) ImportManifest(ctx context.Context, req *connect.Request[scribev1.ImportManifestRequest]) (*connect.Response[scribev1.ImportManifestResponse], error) {
	workspaceID := h.currentWorkspaceID(ctx)
	name := strings.TrimSpace(req.Msg.GetName())
	requestedName := name
	if err := validateItemName(name); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	manifestURL := strings.TrimSpace(req.Msg.GetManifestUrl())
	if manifestURL == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("manifest_url is required"))
	}
	rawIdempotencyKey := strings.TrimSpace(req.Msg.GetIdempotencyKey())
	if rawIdempotencyKey == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("idempotency_key is required"))
	}
	metadata, err := normalizeItemMetadata(req.Msg.GetMetadata())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	externalReferenceID, err := normalizeExternalReferenceID(req.Msg.GetExternalReferenceId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if h.transcriptionJobs == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("manifest idempotency repository is not configured"))
	}

	var requestedContextID *uint64
	if req.Msg.GetContextId() > 0 {
		// A caller-selected context is part of the authorization boundary even
		// when every Canvas happens to contain hOCR and no processing job is
		// needed. Reject foreign IDs before reserving capacity or contacting an
		// external server.
		if _, err := h.contextForRead(ctx, req.Msg.GetContextId()); err != nil {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("context not found"))
		}
		value := req.Msg.GetContextId()
		requestedContextID = &value
	}

	var itemReservation store.ExternalRequest
	digest := sha256.Sum256([]byte(rawIdempotencyKey))
	reservation, created, err := h.transcriptionJobs.ReserveExternalRequest(
		ctx,
		workspaceID,
		"item-create",
		fmt.Sprintf("%x", digest[:]),
		stableRequestHash(requestedName, "manifest", manifestURL, metadata, externalReferenceID, strconv.FormatUint(req.Msg.GetContextId(), 10)),
		"",
	)
	if err != nil {
		if errors.Is(err, store.ErrExternalRequestMismatch) {
			return nil, connect.NewError(connect.CodeAlreadyExists, err)
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("reserve item creation: %w", err))
	}
	if !created {
		if reservation.Status == store.ExternalRequestStatusCompleted && reservation.ItemID != "" {
			existing, loadErr := h.itemForRequest(ctx, reservation.ItemID)
			if loadErr != nil {
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("reload idempotent item creation"))
			}
			return connect.NewResponse(&scribev1.ImportManifestResponse{Item: storeItemToProto(existing)}), nil
		}
		return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("item creation is already in progress"))
	}
	itemReservation = reservation
	failItemReservation := func(processingErr error) {
		if itemReservation.LeaseOwner != "" && processingErr != nil {
			failureCtx, cancel := context.WithTimeout(h.backgroundContext(), 10*time.Second)
			defer cancel()
			if failErr := h.transcriptionJobs.FailExternalRequest(
				failureCtx,
				workspaceID,
				"item-create",
				itemReservation.IdempotencyKey,
				itemReservation.LeaseOwner,
				processingErr.Error(),
			); failErr != nil {
				slog.Warn("failed to release item creation reservation", "error_type", safeLogErrorType(failErr))
			}
		}
	}

	// One deadline owns capacity admission, the manifest request, and every
	// optional hOCR request. Besides bounding the fan-out, it keeps the
	// idempotency lease from expiring while a request waits for import capacity.
	// The safe HTTP client also applies per-request limits, but this aggregate
	// bound prevents a large manifest from multiplying those deadlines.
	importTimeout := h.manifestImportTimeout
	if importTimeout <= 0 {
		importTimeout = defaultManifestImportTimeout
	}
	importCtx, cancelImport := context.WithTimeout(ctx, importTimeout)
	defer cancelImport()
	releaseImport, acquireErr := h.acquireManifestImportSlot(importCtx, workspaceID)
	if acquireErr != nil {
		failItemReservation(acquireErr)
		return nil, acquireErr
	}
	defer releaseImport()

	m, sourceManifest, fetchErr := fetchIIIFManifest(importCtx, manifestURL, h.maxManifestCanvases)
	if fetchErr != nil {
		failItemReservation(fetchErr)
		return nil, manifestImportConnectError(fetchErr)
	}
	prefetchedCanvases, extractErr := extractCanvasesFromManifest(m)
	if extractErr != nil {
		failItemReservation(extractErr)
		return nil, connect.NewError(connect.CodeInvalidArgument, extractErr)
	}
	if name == "" || name == manifestURL {
		if label := extractManifestLabel(m); label != "" {
			name = label
		}
	}
	if name == "" {
		name = "Untitled Item"
	}
	if nameErr := validateItemName(name); nameErr != nil {
		failItemReservation(nameErr)
		return nil, connect.NewError(connect.CodeInvalidArgument, nameErr)
	}
	sourceBytes := uint64(len(sourceManifest))
	if sourceBytes > h.maxManifestImportBytes {
		budgetErr := fmt.Errorf("%w: source manifest and imported hOCR exceed %d bytes", errManifestImportBudgetExceeded, h.maxManifestImportBytes)
		failItemReservation(budgetErr)
		return nil, connect.NewError(connect.CodeResourceExhausted, budgetErr)
	}
	manifestDurableBytes := sourceBytes
	prefetchedCanvases, hocrDurableBytes, prefetchErr := prefetchManifestHOCR(importCtx, prefetchedCanvases, h.maxManifestImportBytes-sourceBytes)
	if prefetchErr != nil {
		failItemReservation(prefetchErr)
		return nil, manifestImportConnectError(prefetchErr)
	}
	if hocrDurableBytes > h.maxManifestImportBytes-sourceBytes {
		budgetErr := fmt.Errorf("%w: source manifest and imported hOCR exceed %d bytes", errManifestImportBudgetExceeded, h.maxManifestImportBytes)
		failItemReservation(budgetErr)
		return nil, connect.NewError(connect.CodeResourceExhausted, budgetErr)
	}
	manifestDurableBytes += hocrDurableBytes
	cancelImport()

	quotaRequest := store.StorageQuotaRequest{
		Items:        1,
		Images:       uint64(len(prefetchedCanvases)),
		DurableBytes: manifestDurableBytes,
	}
	storageReservation, err := h.reserveStorageQuota(ctx, quotaRequest)
	if err != nil {
		failItemReservation(err)
		return nil, err
	}
	defer h.releaseStorageQuota(storageReservation)
	committed, commitErr := h.commitManifestItem(ctx, manifestItemCommitRequest{
		ItemID:               "item_" + uuid.NewString(),
		Name:                 name,
		ManifestURL:          manifestURL,
		Metadata:             metadata,
		ExternalReferenceID:  externalReferenceID,
		CallerIdempotencyKey: rawIdempotencyKey,
		SourceManifest:       string(sourceManifest),
		Canvases:             prefetchedCanvases,
		RequestedContextID:   requestedContextID,
		Reservation:          storageReservation,
		ExternalRequest:      itemReservation,
	})
	if commitErr != nil {
		failItemReservation(commitErr)
		if errors.As(commitErr, new(*store.TranscriptionJobQuotaExceededError)) {
			return nil, transcriptionJobConnectError("enqueue manifest transcription", commitErr)
		}
		if errors.Is(commitErr, store.ErrStorageQuotaExceeded) {
			return nil, storageQuotaConnectError(commitErr)
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("commit manifest ingest: %w", commitErr))
	}
	for _, jobID := range committed.TranscriptionJobIDs {
		h.publishTranscriptionJob(ctx, jobID)
	}
	return connect.NewResponse(&scribev1.ImportManifestResponse{Item: storeItemToProto(committed.Item)}), nil
}

func (h *Handler) StartUploadBatch(ctx context.Context, req *connect.Request[scribev1.StartUploadBatchRequest]) (*connect.Response[scribev1.StartUploadBatchResponse], error) {
	batchID := strings.TrimSpace(req.Msg.GetBatchId())
	metadata, err := normalizeItemMetadata(req.Msg.GetMetadata())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	externalReferenceID, err := normalizeExternalReferenceID(req.Msg.GetExternalReferenceId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	files, requestHash, err := normalizeUploadBatchRequest(req.Msg)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	workspaceID := h.currentWorkspaceID(ctx)
	if existing, loadErr := h.items.GetUploadBatch(ctx, workspaceID, batchID); loadErr == nil {
		if existing.RequestHash != requestHash {
			return nil, connect.NewError(connect.CodeAlreadyExists, store.ErrUploadBatchRequestMismatch)
		}
		item, itemErr := h.itemForRequest(ctx, existing.ItemID)
		if itemErr != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("load upload batch item: %w", itemErr))
		}
		return connect.NewResponse(&scribev1.StartUploadBatchResponse{Item: storeItemToProto(item), Batch: storeUploadBatchToProto(existing)}), nil
	} else if !errors.Is(loadErr, store.ErrUploadBatchNotFound) {
		return nil, connect.NewError(connect.CodeInternal, loadErr)
	}

	resolvedContext, err := h.resolveContext(ctx, req.Msg.GetContextId(), metadata)
	if err != nil {
		return nil, resolveContextConnectError(err)
	}
	name := strings.TrimSpace(req.Msg.GetName())
	if name == "" {
		name = files[0].Filename
	}
	if err := validateItemName(name); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	batchQuota := store.StorageQuotaRequest{Items: 1, Images: uint64(len(files))}
	for _, file := range files {
		if batchQuota.Bytes > ^uint64(0)-file.Size {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("declared batch size overflows quota accounting"))
		}
		batchQuota.Bytes += file.Size
	}
	storageReservation, err := h.reserveStorageQuota(ctx, batchQuota)
	if err != nil {
		return nil, err
	}
	defer h.releaseStorageQuota(storageReservation)
	batch, err := h.items.StartUploadBatch(ctx, store.StartUploadBatchParams{
		WorkspaceID:          workspaceID,
		UserID:               h.currentUserID(ctx),
		BatchID:              batchID,
		ItemID:               "item_" + uuid.NewString(),
		Name:                 name,
		Metadata:             metadata,
		ExternalReferenceID:  externalReferenceID,
		CallerIdempotencyKey: batchID,
		Context:              resolvedContext,
		RequestHash:          requestHash,
		Files:                files,
	})
	if err != nil {
		if errors.Is(err, store.ErrUploadBatchRequestMismatch) {
			return nil, connect.NewError(connect.CodeAlreadyExists, err)
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("start upload batch: %w", err))
	}
	item, err := h.itemForRequest(ctx, batch.ItemID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("load upload batch item: %w", err))
	}
	return connect.NewResponse(&scribev1.StartUploadBatchResponse{Item: storeItemToProto(item), Batch: storeUploadBatchToProto(batch)}), nil
}

func validateItemName(name string) error {
	if utf8.RuneCountInString(strings.TrimSpace(name)) > maxItemNameRunes {
		return fmt.Errorf("item name must be at most %d characters", maxItemNameRunes)
	}
	return nil
}

func normalizeItemMetadata(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "{}", nil
	}
	if len(raw) > maxItemMetadataBytes {
		return "", fmt.Errorf("metadata must be at most %d bytes", maxItemMetadataBytes)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil || decoded == nil {
		return "", fmt.Errorf("metadata must be a non-null JSON object")
	}
	normalized, err := json.Marshal(decoded)
	if err != nil {
		return "", fmt.Errorf("encode metadata: %w", err)
	}
	if len(normalized) > maxItemMetadataBytes {
		return "", fmt.Errorf("metadata must be at most %d bytes", maxItemMetadataBytes)
	}
	return string(normalized), nil
}

func normalizeExternalReferenceID(raw string) (string, error) {
	normalized := strings.TrimSpace(raw)
	if !utf8.ValidString(normalized) || utf8.RuneCountInString(normalized) > 512 || strings.IndexFunc(normalized, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("external_reference_id must contain at most 512 characters without control characters")
	}
	return normalized, nil
}

func (h *Handler) GetUploadBatch(ctx context.Context, req *connect.Request[scribev1.GetUploadBatchRequest]) (*connect.Response[scribev1.GetUploadBatchResponse], error) {
	batch, err := h.items.GetUploadBatch(ctx, h.currentWorkspaceID(ctx), strings.TrimSpace(req.Msg.GetBatchId()))
	if err != nil {
		if errors.Is(err, store.ErrUploadBatchNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	item, err := h.itemForRequest(ctx, batch.ItemID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("load upload batch item: %w", err))
	}
	return connect.NewResponse(&scribev1.GetUploadBatchResponse{Item: storeItemToProto(item), Batch: storeUploadBatchToProto(batch)}), nil
}

func (h *Handler) CancelUploadBatch(ctx context.Context, req *connect.Request[scribev1.CancelUploadBatchRequest]) (*connect.Response[scribev1.CancelUploadBatchResponse], error) {
	batch, err := h.items.CancelUploadBatch(ctx, h.currentWorkspaceID(ctx), strings.TrimSpace(req.Msg.GetBatchId()))
	if err != nil {
		return nil, uploadBatchConnectError(err)
	}
	return connect.NewResponse(&scribev1.CancelUploadBatchResponse{Batch: storeUploadBatchToProto(batch)}), nil
}

func (h *Handler) UploadItemImage(ctx context.Context, req *connect.Request[scribev1.UploadItemImageRequest]) (*connect.Response[scribev1.UploadItemImageResponse], error) {
	batchID := strings.TrimSpace(req.Msg.GetBatchId())
	imageData := req.Msg.GetImageData()
	imageWidth, imageHeight, err := ocrhandlers.UploadedImageDimensions(imageData)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	contentDigest := sha256.Sum256(imageData)
	workspaceID := h.currentWorkspaceID(ctx)
	batch, batchFile, claimed, err := h.items.ClaimUploadBatchFile(
		ctx,
		workspaceID,
		batchID,
		req.Msg.GetSequence(),
		uint64(len(imageData)),
		fmt.Sprintf("%x", contentDigest[:]),
	)
	if err != nil {
		return nil, uploadBatchConnectError(err)
	}
	if !claimed {
		item, itemErr := h.itemForRequest(ctx, batch.ItemID)
		image, imageErr := h.items.GetImageForWorkspace(ctx, batchFile.ItemImageID, workspaceID)
		if itemErr != nil || imageErr != nil || batchFile.TranscriptionJobID == 0 {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("reload completed batch upload"))
		}
		return connect.NewResponse(&scribev1.UploadItemImageResponse{
			Item:               storeItemToProto(item),
			Image:              storeItemImageToProto(image),
			TranscriptionJobId: batchFile.TranscriptionJobID,
			Batch:              storeUploadBatchToProto(batch),
		}), nil
	}
	attemptCommitted := false
	attemptPublicError := uploadBatchFailureMessage(uploadBatchFailureAdmission, nil)
	markLeaseFailure := func(err error) error {
		attemptPublicError = uploadBatchFailureAfterLeaseRenewal(attemptPublicError, err)
		return err
	}
	defer func() {
		if attemptCommitted {
			return
		}
		failureCtx, cancel := context.WithTimeout(h.backgroundContext(), 15*time.Second)
		defer cancel()
		if abortErr := h.items.AbortUploadBatchFileAttempt(failureCtx, workspaceID, batchID, batchFile.Sequence, batchFile.LeaseOwner, attemptPublicError); abortErr != nil &&
			!errors.Is(abortErr, store.ErrUploadBatchFileFence) && !errors.Is(abortErr, store.ErrUploadBatchNotFound) {
			slog.Warn("failed to abort upload batch file attempt", "batch_id", batchID, "sequence", batchFile.Sequence, "error_type", safeLogErrorType(abortErr))
		}
	}()
	storageReservation, err := h.reserveStorageQuota(ctx, store.StorageQuotaRequest{Bytes: uint64(len(imageData)), Images: 1})
	if err != nil {
		return nil, err
	}
	defer func() { h.releaseStorageQuota(storageReservation) }()
	renewLease := func(renewCtx context.Context) error {
		return h.items.RenewUploadBatchFileLease(renewCtx, workspaceID, batchID, batchFile.Sequence, batchFile.LeaseOwner)
	}
	attemptPublicError = uploadBatchFailureMessage(uploadBatchFailureLeaseRenewal, nil)
	if err := markLeaseFailure(renewLease(ctx)); err != nil {
		return nil, uploadBatchConnectError(err)
	}
	attemptPublicError = uploadBatchFailureMessage(uploadBatchFailureAdmission, nil)
	ticker := time.NewTicker(uploadBatchLeaseHeartbeatEvery)
	leaseCtx, stopHeartbeat, leaseFailures := newUploadBatchLeaseHeartbeat(ctx, ticker.C, renewLease)
	leaseFailure := func() error {
		select {
		case leaseErr := <-leaseFailures:
			return leaseErr
		default:
			return nil
		}
	}
	// This defer is registered after the abort defer so LIFO cleanup records a
	// late heartbeat failure before the abort persists the fixed public stage.
	defer func() {
		ticker.Stop()
		stopHeartbeat()
		_ = markLeaseFailure(leaseFailure())
	}()
	renewBeforeSideEffect := func() error {
		if err := markLeaseFailure(leaseFailure()); err != nil {
			return err
		}
		return markLeaseFailure(renewLease(leaseCtx))
	}
	processingContext, err := batch.Context()
	if err != nil {
		if leaseErr := markLeaseFailure(leaseFailure()); leaseErr != nil {
			return nil, uploadBatchConnectError(leaseErr)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	contextID := processingContext.ID
	pctx := processingContextFromStore(processingContext)
	pctx.SegmentOnly = true
	callCtx := withStorageQuotaReservation(leaseCtx, storageReservation)
	callCtx = hocr.WithProviderCallMetadata(callCtx, workspaceID, "", nil, &contextID)
	releaseProcessing, err := h.acquireSegmentationProcessingSlot(leaseCtx, workspaceID, processingContext)
	if err != nil {
		if leaseErr := markLeaseFailure(leaseFailure()); leaseErr != nil {
			return nil, uploadBatchConnectError(leaseErr)
		}
		return nil, err
	}
	attemptPublicError = uploadBatchFailureMessage(uploadBatchFailureSegmentationOutput, nil)
	result, err := func() (*ocrhandlers.ProcessResult, error) {
		defer releaseProcessing()
		return h.ocr.ProcessImageUploadWithContext(callCtx, batchFile.Filename, imageData, pctx)
	}()
	if err != nil {
		attemptPublicError = uploadBatchFailureMessage(uploadBatchFailureSegmentationOutput, err)
		h.queueUploadFromProcessingError(ctx, err)
		if leaseErr := markLeaseFailure(leaseFailure()); leaseErr != nil {
			return nil, uploadBatchConnectError(leaseErr)
		}
		return nil, imageProcessingConnectError("process upload batch file", err)
	}
	storedBytes := result.StoredBytes
	if storedBytes == 0 {
		storedBytes = uint64(len(imageData))
	}
	attemptPublicError = uploadBatchFailureMessage(uploadBatchFailureQuotaResize, nil)
	storageReservation, err = h.resizeStorageQuota(leaseCtx, storageReservation, store.StorageQuotaRequest{Bytes: storedBytes, Images: 1})
	if err != nil {
		h.queueUnreferencedUploads(h.backgroundContext(), workspaceID, []store.ItemImage{{ImageURL: result.ImageURL, StorageBytes: storedBytes}})
		if leaseErr := markLeaseFailure(leaseFailure()); leaseErr != nil {
			return nil, uploadBatchConnectError(leaseErr)
		}
		return nil, err
	}
	if err := renewBeforeSideEffect(); err != nil {
		h.queueUnreferencedUploads(h.backgroundContext(), workspaceID, []store.ItemImage{{ImageURL: result.ImageURL, StorageBytes: storedBytes}})
		return nil, uploadBatchConnectError(err)
	}
	attemptPublicError = uploadBatchFailureMessage(uploadBatchFailureImageCommit, nil)
	img, err := h.items.EnsureUploadBatchImage(
		leaseCtx, workspaceID, storageReservation, batchID, batchFile.Sequence, batchFile.LeaseOwner,
		result.ImageURL, storedBytes, imageWidth, imageHeight, h.publicAnnotationBaseURL(),
	)
	if err != nil {
		h.queueUnreferencedUploads(h.backgroundContext(), workspaceID, []store.ItemImage{{ImageURL: result.ImageURL, StorageBytes: storedBytes}})
		if leaseErr := markLeaseFailure(leaseFailure()); leaseErr != nil {
			return nil, uploadBatchConnectError(leaseErr)
		}
		return nil, uploadBatchConnectError(err)
	}
	sessionID := fmt.Sprintf("%s-seq%d", batch.ItemID, batchFile.Sequence)
	if err := renewBeforeSideEffect(); err != nil {
		return nil, uploadBatchConnectError(err)
	}
	attemptPublicError = uploadBatchFailureMessage(uploadBatchFailureOCRRunCommit, nil)
	if err := h.ocrRuns.Create(leaseCtx, store.OCRRun{
		SessionID:    sessionID,
		ItemImageID:  &img.ID,
		ContextID:    &contextID,
		ImageURL:     result.ImageURL,
		Provider:     result.Provider,
		Model:        result.Model,
		OriginalHOCR: result.HOCR,
		OriginalText: result.PlainText,
	}); err != nil {
		if leaseErr := markLeaseFailure(leaseFailure()); leaseErr != nil {
			return nil, uploadBatchConnectError(leaseErr)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := renewBeforeSideEffect(); err != nil {
		return nil, uploadBatchConnectError(err)
	}
	attemptPublicError = uploadBatchFailureMessage(uploadBatchFailureAnnotationCommit, nil)
	if err := h.ensureItemImageCanvasAndAnnotations(leaseCtx, store.OCRRun{SessionID: sessionID, ItemImageID: &img.ID, OriginalHOCR: result.HOCR}, img.ID, result.ParsedHOCR); err != nil {
		if leaseErr := markLeaseFailure(leaseFailure()); leaseErr != nil {
			return nil, uploadBatchConnectError(leaseErr)
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("initialize annotations: %w", err))
	}
	if err := renewBeforeSideEffect(); err != nil {
		return nil, uploadBatchConnectError(err)
	}
	attemptPublicError = uploadBatchFailureMessage(uploadBatchFailureTranscriptionEnqueue, nil)
	jobID, err := h.transcriptionJobs.CreateForUploadBatchFile(
		leaseCtx, workspaceID, batchID, batchFile.Sequence, batchFile.LeaseOwner, img.ID,
	)
	if err != nil {
		if leaseErr := markLeaseFailure(leaseFailure()); leaseErr != nil {
			return nil, uploadBatchConnectError(leaseErr)
		}
		return nil, transcriptionJobConnectError("enqueue transcription", err)
	}
	attemptPublicError = uploadBatchFailureMessage(uploadBatchFailureItemReload, nil)
	item, err := h.itemForRequest(leaseCtx, batch.ItemID)
	if err != nil {
		if leaseErr := markLeaseFailure(leaseFailure()); leaseErr != nil {
			return nil, uploadBatchConnectError(leaseErr)
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("reload uploaded item: %w", err))
	}
	if err := renewBeforeSideEffect(); err != nil {
		return nil, uploadBatchConnectError(err)
	}
	attemptPublicError = uploadBatchFailureMessage(uploadBatchFailureBatchCommit, nil)
	completionCtx, cancel := context.WithTimeout(h.backgroundContext(), 15*time.Second)
	batch, err = h.items.CompleteUploadBatchFile(completionCtx, workspaceID, batchID, batchFile.Sequence, batchFile.LeaseOwner, img.ID, jobID)
	cancel()
	if err != nil {
		if leaseErr := markLeaseFailure(leaseFailure()); leaseErr != nil {
			return nil, uploadBatchConnectError(leaseErr)
		}
		return nil, uploadBatchConnectError(err)
	}
	attemptCommitted = true
	h.publishTranscriptionJob(ctx, jobID)
	return connect.NewResponse(&scribev1.UploadItemImageResponse{
		Item:               storeItemToProto(item),
		Image:              storeItemImageToProto(img),
		TranscriptionJobId: jobID,
		Batch:              storeUploadBatchToProto(batch),
	}), nil
}

func uploadBatchFailureMessage(stage uploadBatchFailureStage, err error) string {
	if providerFailure, ok := hocr.SafeProviderFailureMessage(err); ok {
		return providerFailure
	}
	if processingFailure, ok := ocrhandlers.SafeUploadProcessingFailureMessage(err); ok {
		return processingFailure
	}
	switch stage {
	case uploadBatchFailureSegmentationOutput:
		return "segmentation output failed"
	case uploadBatchFailureQuotaResize:
		return "quota resize failed"
	case uploadBatchFailureLeaseRenewal:
		return "lease renewal failed"
	case uploadBatchFailureImageCommit:
		return "image commit failed"
	case uploadBatchFailureOCRRunCommit:
		return "ocr run commit failed"
	case uploadBatchFailureAnnotationCommit:
		return "annotation commit failed"
	case uploadBatchFailureTranscriptionEnqueue:
		return "transcription enqueue failed"
	case uploadBatchFailureItemReload:
		return "item reload failed"
	case uploadBatchFailureBatchCommit:
		return "batch commit failed"
	case uploadBatchFailureAdmission:
		fallthrough
	default:
		return "admission failed"
	}
}

func uploadBatchFailureAfterLeaseRenewal(current string, err error) string {
	if err == nil {
		return current
	}
	return uploadBatchFailureMessage(uploadBatchFailureLeaseRenewal, nil)
}

func normalizeUploadBatchRequest(req *scribev1.StartUploadBatchRequest) ([]store.UploadBatchFileInput, string, error) {
	if len(req.GetFiles()) == 0 || len(req.GetFiles()) > maxUploadBatchFiles {
		return nil, "", fmt.Errorf("files must contain between 1 and %d entries", maxUploadBatchFiles)
	}
	metadata, err := normalizeItemMetadata(req.GetMetadata())
	if err != nil {
		return nil, "", err
	}
	externalReferenceID, err := normalizeExternalReferenceID(req.GetExternalReferenceId())
	if err != nil {
		return nil, "", err
	}
	fields := []string{strings.TrimSpace(req.GetName()), strconv.FormatUint(req.GetContextId(), 10), metadata, externalReferenceID}
	files := make([]store.UploadBatchFileInput, 0, len(req.GetFiles()))
	var totalBytes uint64
	for index, input := range req.GetFiles() {
		filename := strings.TrimSpace(input.GetFilename())
		contentSHA256 := strings.TrimSpace(input.GetContentSha256())
		if filename == "" || len(filename) > 255 || strings.ContainsAny(filename, `/\`) || strings.IndexFunc(filename, unicode.IsControl) >= 0 {
			return nil, "", fmt.Errorf("file %d has an invalid filename", index+1)
		}
		if input.GetSize() == 0 || input.GetSize() > maxDeclaredImageBytes {
			return nil, "", fmt.Errorf("file %d size must be between 1 and %d bytes", index+1, maxDeclaredImageBytes)
		}
		if len(contentSHA256) != sha256.Size*2 || strings.Trim(contentSHA256, "0123456789abcdef") != "" {
			return nil, "", fmt.Errorf("file %d has an invalid content_sha256", index+1)
		}
		if totalBytes > maxUploadBatchTotalBytes-input.GetSize() {
			return nil, "", fmt.Errorf("batch size exceeds %d bytes", maxUploadBatchTotalBytes)
		}
		totalBytes += input.GetSize()
		files = append(files, store.UploadBatchFileInput{Filename: filename, Size: input.GetSize(), ContentSHA256: contentSHA256})
		fields = append(fields, filename, strconv.FormatUint(input.GetSize(), 10), contentSHA256)
	}
	return files, stableRequestHash(fields...), nil
}

func stableRequestHash(fields ...string) string {
	digest := sha256.New()
	for _, field := range fields {
		writeHashField(digest, field)
	}
	return fmt.Sprintf("%x", digest.Sum(nil))
}

func writeHashField(target hash.Hash, value string) {
	_, _ = target.Write([]byte(strconv.Itoa(len(value))))
	_, _ = target.Write([]byte{':'})
	_, _ = target.Write([]byte(value))
}

func uploadBatchConnectError(err error) error {
	switch {
	case errors.Is(err, store.ErrUploadBatchNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, store.ErrUploadBatchFileMismatch):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, store.ErrUploadBatchFileInProgress):
		return connect.NewError(connect.CodeAlreadyExists, err)
	case errors.Is(err, store.ErrUploadBatchCanceled), errors.Is(err, store.ErrUploadBatchCompleted), errors.Is(err, store.ErrUploadBatchFileAttemptsExhausted), errors.Is(err, store.ErrUploadBatchFileFence):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}

func newUploadBatchLeaseHeartbeat(parent context.Context, ticks <-chan time.Time, renew func(context.Context) error) (context.Context, func(), <-chan error) {
	leaseCtx, cancel := context.WithCancel(parent)
	failures := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-leaseCtx.Done():
				return
			case _, ok := <-ticks:
				if !ok {
					return
				}
				renewCtx, stopRenew := context.WithTimeout(leaseCtx, uploadBatchLeaseRenewTimeout)
				err := renew(renewCtx)
				stopRenew()
				if err == nil {
					continue
				}
				if leaseCtx.Err() != nil {
					return
				}
				failures <- err
				cancel()
				return
			}
		}
	}()
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			cancel()
			<-done
		})
	}
	return leaseCtx, stop, failures
}

func (h *Handler) DeleteItem(ctx context.Context, req *connect.Request[scribev1.DeleteItemRequest]) (*connect.Response[scribev1.DeleteItemResponse], error) {
	if err := h.items.DeleteForWorkspace(ctx, req.Msg.GetItemId(), h.currentWorkspaceID(ctx)); err != nil {
		if isNotFoundError(err) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("item not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&scribev1.DeleteItemResponse{}), nil
}

func (h *Handler) ListItemProviderCallAudits(ctx context.Context, req *connect.Request[scribev1.ListItemProviderCallAuditsRequest]) (*connect.Response[scribev1.ListItemProviderCallAuditsResponse], error) {
	itemID := strings.TrimSpace(req.Msg.GetItemId())
	if _, err := h.itemForRequest(ctx, itemID); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("item not found"))
	}
	limit := int(req.Msg.GetLimit())
	if limit == 0 {
		limit = 100
	}
	if h.providerCallAudits == nil {
		return connect.NewResponse(&scribev1.ListItemProviderCallAuditsResponse{}), nil
	}
	audits, err := h.providerCallAudits.ListByItem(ctx, h.currentWorkspaceID(ctx), itemID, limit)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list provider call audits: %w", err))
	}
	response := &scribev1.ListItemProviderCallAuditsResponse{Audits: make([]*scribev1.ProviderCallAudit, 0, len(audits))}
	for _, audit := range audits {
		entry := &scribev1.ProviderCallAudit{
			Id:                audit.ID,
			ItemId:            audit.ItemID,
			ItemImageSequence: audit.ItemImageSequence,
			ItemImageLabel:    audit.ItemImageLabel,
			SessionId:         audit.SessionID,
			Provider:          audit.Provider,
			Model:             audit.Model,
			Operation:         audit.Operation,
			ErrorMessage:      audit.ErrorMessage,
			DurationMs:        audit.DurationMS,
			CreatedAt:         audit.CreatedAt.UTC().Format(time.RFC3339Nano),
		}
		if audit.ItemImageID != nil {
			entry.ItemImageId = audit.ItemImageID
		}
		if audit.ContextID != nil {
			entry.ContextId = audit.ContextID
		}
		if audit.HTTPStatus != nil {
			status := int32FromIntBounded(*audit.HTTPStatus)
			entry.HttpStatus = &status
		}
		response.Audits = append(response.Audits, entry)
	}
	return connect.NewResponse(response), nil
}

func (h *Handler) queueUnreferencedUploads(ctx context.Context, workspaceID uint64, images []store.ItemImage) {
	if h == nil || h.items == nil {
		return
	}
	if ctx == nil {
		ctx = h.backgroundContext()
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	uploads := make(map[string]uint64, len(images))
	for _, image := range images {
		imageURL := strings.TrimSpace(image.ImageURL)
		_, ok := uploadNameFromURL(imageURL)
		if !ok {
			continue
		}
		currentBytes, exists := uploads[imageURL]
		if !exists || image.StorageBytes > currentBytes {
			uploads[imageURL] = image.StorageBytes
		}
	}
	for imageURL, storageBytes := range uploads {
		if err := h.items.EnqueueUploadCleanup(cleanupCtx, workspaceID, imageURL, storageBytes); err != nil {
			slog.Warn("failed to enqueue orphaned upload cleanup", "upload_id", safeLogValueID(imageURL), "error_type", safeLogErrorType(err))
		}
	}
}

func (h *Handler) queueUploadFromProcessingError(ctx context.Context, processingErr error) {
	imageURL, storageBytes, ok := ocrhandlers.StoredUploadDetails(processingErr)
	if !ok {
		return
	}
	h.queueUnreferencedUploads(ctx, h.currentWorkspaceID(ctx), []store.ItemImage{{ImageURL: imageURL, StorageBytes: storageBytes}})
}

func uploadNameFromURL(raw string) (string, bool) {
	return uploadref.ImmutableNameFromURL(raw)
}

func (h *Handler) acquireManifestImportSlot(ctx context.Context, workspaceID uint64) (func(), error) {
	if h == nil || h.processingLimiter == nil {
		return func() {}, nil
	}
	release, err := h.processingLimiter.Acquire(ctx, workspaceID, "iiif-manifest-import")
	if err == nil {
		return release, nil
	}
	switch {
	case errors.Is(err, context.Canceled):
		return nil, connect.NewError(connect.CodeCanceled, fmt.Errorf("manifest import canceled"))
	case errors.Is(err, context.DeadlineExceeded):
		return nil, connect.NewError(connect.CodeDeadlineExceeded, fmt.Errorf("manifest import deadline exceeded"))
	default:
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("acquire manifest import capacity: %w", err))
	}
}

type manifestItemCommitRequest struct {
	ItemID               string
	Name                 string
	ManifestURL          string
	Metadata             string
	ExternalReferenceID  string
	CallerIdempotencyKey string
	SourceManifest       string
	Canvases             []canvasInfo
	RequestedContextID   *uint64
	Reservation          store.StorageQuotaReservation
	ExternalRequest      store.ExternalRequest
}

func (h *Handler) commitManifestItem(ctx context.Context, request manifestItemCommitRequest) (store.ManifestIngestResult, error) {
	workspaceID := h.currentWorkspaceID(ctx)
	userID := h.currentUserID(ctx)
	needsTranscription := false
	canvases := make([]store.ManifestIngestCanvas, 0, len(request.Canvases))
	for sequence, canvas := range request.Canvases {
		var parsedHOCR hocr.Document
		if strings.TrimSpace(canvas.hocrXML) != "" {
			var err error
			parsedHOCR, err = parsedHOCRDocument(canvas.hocrXML, canvas.parsedHOCR)
			if err != nil {
				return store.ManifestIngestResult{}, fmt.Errorf("parse Canvas %d hOCR: %w", sequence+1, err)
			}
		} else {
			needsTranscription = true
		}
		canvasCopy := canvas
		sequenceCopy := sequence
		parsed := parsedHOCR
		factory := func(itemImageID uint64, canvasURI string) (store.AnnotationPage, error) {
			annotationItems := make([]any, 0)
			if strings.TrimSpace(canvasCopy.hocrXML) != "" {
				// Rebuild IDs after the database assigns the tenant-owned image
				// identity; imported Canvas IDs remain targets/provenance only.
				scope := fmt.Sprintf("item-image-%d", itemImageID)
				annotationItems = append(annotationItems, buildLineAnnotations(scope, canvasURI, parsed.Lines)...)
				annotationItems = append(annotationItems, buildWordAnnotations(scope, canvasURI, parsed.Words)...)
			}
			payload, err := iiif.NewAnnotationPage(iiif.PageIdentity{
				PublicBaseURL: h.publicAnnotationBaseURL(), ItemImageID: itemImageID, CanvasURI: canvasURI,
			}, annotationItems)
			if err != nil {
				return store.AnnotationPage{}, err
			}
			pageID, err := h.annotationPageIDForItemImage(itemImageID)
			if err != nil {
				return store.AnnotationPage{}, err
			}
			return store.AnnotationPage{PageID: pageID, CanvasURI: canvasURI, Payload: string(payload)}, nil
		}
		var run *store.OCRRun
		if strings.TrimSpace(canvas.hocrXML) != "" {
			run = &store.OCRRun{
				SessionID: fmt.Sprintf("%s-seq%d", request.ItemID, sequenceCopy), ImageURL: canvas.imageURL,
				Provider: "manifest", Model: "imported", OriginalHOCR: canvas.hocrXML, OriginalText: canvas.plainText,
			}
		}
		canvases = append(canvases, store.ManifestIngestCanvas{
			Image: db.CreateItemImageParams{
				Sequence: uint32(sequence), ImageURL: canvasCopy.imageURL, CanvasURI: canvasCopy.canvasURI,
				Width: canvasCopy.width, Height: canvasCopy.height, Label: canvasCopy.label, HocrURL: canvasCopy.hocrURL,
			},
			OCRRun: run, BuildPage: factory, EnqueueJob: strings.TrimSpace(canvas.hocrXML) == "",
		})
	}
	var processingContext *store.Context
	if needsTranscription {
		var resolved store.Context
		var err error
		if request.RequestedContextID != nil && *request.RequestedContextID > 0 {
			resolved, err = h.contexts.GetForWorkspace(ctx, *request.RequestedContextID, workspaceID)
		} else {
			resolved, _, err = h.contexts.ResolveForWorkspace(ctx, workspaceID, nil)
		}
		if err != nil {
			return store.ManifestIngestResult{}, fmt.Errorf("resolve manifest transcription context: %w", err)
		}
		processingContext = &resolved
	}
	var external *store.SingleFileIngestExternalRequest
	if request.ExternalRequest.LeaseOwner != "" {
		external = &store.SingleFileIngestExternalRequest{
			Source: "item-create", IdempotencyKey: request.ExternalRequest.IdempotencyKey, LeaseOwner: request.ExternalRequest.LeaseOwner,
		}
	}
	return h.annotations.CommitManifestIngest(ctx, store.ManifestIngestCommit{
		Item: db.CreateItemParams{
			ID: request.ItemID, UserID: userID, WorkspaceID: workspaceID, Name: request.Name,
			SourceType: "manifest", SourceURL: request.ManifestURL, Metadata: request.Metadata, SourceManifest: request.SourceManifest,
			ExternalReferenceID: request.ExternalReferenceID, CallerIdempotencyKey: request.CallerIdempotencyKey,
		},
		Canvases: canvases, PublicBaseURL: h.publicAnnotationBaseURL(), TranscriptionContext: processingContext,
		Reservation: request.Reservation, Limits: configuredStorageQuotaLimits(), ExternalRequest: external,
	})
}

func manifestImportConnectError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return connect.NewError(connect.CodeCanceled, fmt.Errorf("manifest import canceled"))
	case errors.Is(err, context.DeadlineExceeded):
		return connect.NewError(connect.CodeDeadlineExceeded, fmt.Errorf("manifest import timed out"))
	case errors.Is(err, errManifestImportBudgetExceeded):
		return connect.NewError(connect.CodeResourceExhausted, err)
	default:
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
}

type manifestByteBudget struct {
	mu   sync.Mutex
	max  uint64
	used uint64
}

func (b *manifestByteBudget) claim(size uint64) error {
	if size == 0 {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if size > b.max-b.used {
		return fmt.Errorf("%w: maximum is %d bytes", errManifestImportBudgetExceeded, b.max)
	}
	b.used += size
	return nil
}

func (b *manifestByteBudget) consumed() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used
}

type manifestBudgetReader struct {
	reader io.Reader
	budget *manifestByteBudget
}

func (r *manifestBudgetReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 {
		if budgetErr := r.budget.claim(uint64(n)); budgetErr != nil {
			return 0, budgetErr
		}
	}
	return n, err
}

func prefetchManifestHOCR(ctx context.Context, canvases []canvasInfo, maxTotalBytes uint64) ([]canvasInfo, uint64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	prefetched := append([]canvasInfo(nil), canvases...)
	indexes := make([]int, 0, len(prefetched))
	for index := range prefetched {
		if strings.TrimSpace(prefetched[index].hocrURL) != "" {
			indexes = append(indexes, index)
		}
	}
	if len(indexes) == 0 {
		return prefetched, 0, nil
	}
	if maxTotalBytes == 0 {
		return nil, 0, fmt.Errorf("%w: no aggregate response capacity remains", errManifestImportBudgetExceeded)
	}

	remoteBudget := &manifestByteBudget{max: maxTotalBytes}
	durableBudget := &manifestByteBudget{max: maxTotalBytes}
	group, groupCtx := errgroup.WithContext(ctx)
	jobs := make(chan int)
	group.Go(func() error {
		defer close(jobs)
		for _, index := range indexes {
			select {
			case jobs <- index:
			case <-groupCtx.Done():
				return nil
			}
		}
		return nil
	})
	workerCount := min(maxConcurrentManifestHOCRFetches, len(indexes))
	for range workerCount {
		group.Go(func() error {
			for index := range jobs {
				hocrXML, _, err := fetchHOCRContent(groupCtx, prefetched[index].hocrURL, maxRemoteHOCRBytes, remoteBudget)
				if err != nil {
					return fmt.Errorf("fetch hOCR for canvas %d: %w", index+1, err)
				}
				parsedHOCR, parseErr := hocr.ParseDocument(hocrXML)
				if parseErr != nil {
					return fmt.Errorf("parse hOCR for canvas %d: invalid document", index+1)
				}
				plainText := hocr.PlainText(parsedHOCR.Lines)
				hocrBytes := uint64(len(hocrXML))
				plainTextBytes := uint64(len(plainText))
				if plainTextBytes > ^uint64(0)-hocrBytes {
					return fmt.Errorf("%w: durable payload size overflow", errManifestImportBudgetExceeded)
				}
				if err := durableBudget.claim(hocrBytes + plainTextBytes); err != nil {
					return err
				}
				prefetched[index].hocrXML = hocrXML
				prefetched[index].plainText = plainText
				prefetched[index].parsedHOCR = &parsedHOCR
				if prefetched[index].width == 0 && parsedHOCR.PageWidth > 0 && uint64(parsedHOCR.PageWidth) <= math.MaxUint32 {
					prefetched[index].width = uint32(parsedHOCR.PageWidth)
				}
				if prefetched[index].height == 0 && parsedHOCR.PageHeight > 0 && uint64(parsedHOCR.PageHeight) <= math.MaxUint32 {
					prefetched[index].height = uint32(parsedHOCR.PageHeight)
				}
			}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, 0, err
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	return prefetched, durableBudget.consumed(), nil
}

func fetchHOCRContent(ctx context.Context, hocrURL string, maxBytes int64, aggregateBudget *manifestByteBudget) (string, uint64, error) {
	if maxBytes <= 0 {
		return "", 0, fmt.Errorf("%w: no response capacity remains", errManifestImportBudgetExceeded)
	}
	if aggregateBudget == nil {
		return "", 0, fmt.Errorf("%w: aggregate response budget is required", errManifestImportBudgetExceeded)
	}
	resp, err := safehttp.Get(ctx, hocrURL)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", 0, fmt.Errorf("fetch hocr: status %d", resp.StatusCode)
	}
	if resp.ContentLength > maxBytes {
		return "", 0, fmt.Errorf("%w: response exceeds %d bytes", errManifestImportBudgetExceeded, maxBytes)
	}
	b, err := io.ReadAll(&manifestBudgetReader{
		reader: io.LimitReader(resp.Body, maxBytes+1),
		budget: aggregateBudget,
	})
	if err != nil {
		return "", 0, err
	}
	if int64(len(b)) > maxBytes {
		return "", 0, fmt.Errorf("%w: response exceeds %d bytes", errManifestImportBudgetExceeded, maxBytes)
	}
	return strings.TrimSpace(string(b)), uint64(len(b)), nil
}

// --- proto conversion helpers ---

func storeItemSummaryToProto(item store.ItemSummary) *scribev1.ItemSummary {
	summary := &scribev1.ItemSummary{
		Id:                  item.ID,
		Name:                item.Name,
		SourceType:          item.SourceType,
		ImageCount:          item.ImageCount,
		CreatedAt:           item.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:           item.UpdatedAt.UTC().Format(time.RFC3339),
		ExternalReferenceId: item.ExternalReferenceID,
	}
	if item.PreviewImage != nil {
		summary.PreviewImage = storeItemImageToProto(*item.PreviewImage)
	}
	return summary
}

func storeItemToProto(it store.Item) *scribev1.Item {
	metaJSON := ""
	if it.Metadata != nil {
		if b, err := json.Marshal(it.Metadata); err == nil {
			metaJSON = string(b)
		}
	}
	proto := &scribev1.Item{
		Id:                  it.ID,
		UserId:              it.UserID,
		Name:                it.Name,
		SourceType:          it.SourceType,
		SourceUrl:           it.SourceURL,
		Metadata:            metaJSON,
		CreatedAt:           it.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:           it.UpdatedAt.UTC().Format(time.RFC3339),
		ExternalReferenceId: it.ExternalReferenceID,
		Images:              make([]*scribev1.ItemImage, 0, len(it.Images)),
	}
	for _, img := range it.Images {
		proto.Images = append(proto.Images, storeItemImageToProto(img))
	}
	return proto
}

func storeItemImageToProto(img store.ItemImage) *scribev1.ItemImage {
	return &scribev1.ItemImage{
		Id:        img.ID,
		ItemId:    img.ItemID,
		Sequence:  img.Sequence,
		ImageUrl:  img.ImageURL,
		CanvasUri: img.CanvasURI,
		Label:     img.Label,
		Width:     img.Width,
		Height:    img.Height,
	}
}

func storeUploadBatchToProto(batch store.UploadBatch) *scribev1.UploadBatch {
	result := &scribev1.UploadBatch{
		Id:             batch.ID,
		ItemId:         batch.ItemID,
		ContextId:      batch.ContextID,
		Status:         uploadBatchStatusToProto(batch.Status),
		Files:          make([]*scribev1.UploadBatchFile, 0, len(batch.Files)),
		CompletedFiles: batch.CompletedFiles(),
		FailedFiles:    batch.FailedFiles(),
		CreatedAt:      batch.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:      batch.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	for _, file := range batch.Files {
		result.Files = append(result.Files, &scribev1.UploadBatchFile{
			Sequence:           file.Sequence,
			Filename:           file.Filename,
			Size:               file.Size,
			ContentSha256:      file.ContentSHA256,
			Status:             uploadBatchFileStatusToProto(file.Status),
			AttemptCount:       file.AttemptCount,
			MaxAttempts:        file.MaxAttempts,
			ItemImageId:        file.ItemImageID,
			TranscriptionJobId: file.TranscriptionJobID,
			ErrorMessage:       file.ErrorMessage,
		})
	}
	return result
}

func uploadBatchStatusToProto(status store.UploadBatchStatus) scribev1.UploadBatchStatus {
	switch status {
	case store.UploadBatchStatusInProgress:
		return scribev1.UploadBatchStatus_UPLOAD_BATCH_STATUS_IN_PROGRESS
	case store.UploadBatchStatusCompleted:
		return scribev1.UploadBatchStatus_UPLOAD_BATCH_STATUS_COMPLETED
	case store.UploadBatchStatusCanceled:
		return scribev1.UploadBatchStatus_UPLOAD_BATCH_STATUS_CANCELED
	default:
		return scribev1.UploadBatchStatus_UPLOAD_BATCH_STATUS_UNSPECIFIED
	}
}

func uploadBatchFileStatusToProto(status store.UploadBatchFileStatus) scribev1.UploadBatchFileStatus {
	switch status {
	case store.UploadBatchFileStatusPending:
		return scribev1.UploadBatchFileStatus_UPLOAD_BATCH_FILE_STATUS_PENDING
	case store.UploadBatchFileStatusProcessing:
		return scribev1.UploadBatchFileStatus_UPLOAD_BATCH_FILE_STATUS_PROCESSING
	case store.UploadBatchFileStatusCompleted:
		return scribev1.UploadBatchFileStatus_UPLOAD_BATCH_FILE_STATUS_COMPLETED
	case store.UploadBatchFileStatusFailed:
		return scribev1.UploadBatchFileStatus_UPLOAD_BATCH_FILE_STATUS_FAILED
	case store.UploadBatchFileStatusCanceled:
		return scribev1.UploadBatchFileStatus_UPLOAD_BATCH_FILE_STATUS_CANCELED
	default:
		return scribev1.UploadBatchFileStatus_UPLOAD_BATCH_FILE_STATUS_UNSPECIFIED
	}
}
