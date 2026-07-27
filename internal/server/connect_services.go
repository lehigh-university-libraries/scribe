package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/db"
	ocrhandlers "github.com/lehigh-university-libraries/scribe/internal/handlers"
	"github.com/lehigh-university-libraries/scribe/internal/hocr"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/providerregistry"
	"github.com/lehigh-university-libraries/scribe/internal/safehttp"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	"github.com/lehigh-university-libraries/scribe/internal/uploadref"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
)

var (
	errProcessHOCRLocalUpload  = errors.New("local upload URLs cannot be reused; send image_data")
	errProcessHOCRInvalidImage = errors.New("image URL must be an absolute public HTTP(S) URL")
)

func firstHeaderValue(h map[string][]string, key string) string {
	for k, values := range h {
		if strings.EqualFold(k, key) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

type externalProcessingRequest struct {
	source      string
	key         string
	requestHash string
	eventHeader string
	leaseOwner  string
}

func externalRequestFromHeaders(headers map[string][]string, idempotencyKey, defaultSource, requestHash string) externalProcessingRequest {
	key := strings.TrimSpace(idempotencyKey)
	source := strings.TrimSpace(firstHeaderValue(headers, "X-External-Source"))
	if source == "" {
		source = strings.TrimSpace(firstHeaderValue(headers, "X-Scribe-External-Source"))
	}
	eventHeader := strings.TrimSpace(firstHeaderValue(headers, "X-Islandora-Event"))
	if source == "" && eventHeader != "" {
		source = "islandora"
	}
	if source == "" {
		source = strings.TrimSpace(defaultSource)
	}
	if source == "" {
		source = "external"
	}
	if len(eventHeader) > 256*1024 {
		eventHeader = eventHeader[:256*1024]
	}
	if key == "" {
		return externalProcessingRequest{}
	}
	sum := sha256.Sum256([]byte(key))
	return externalProcessingRequest{
		source:      source,
		key:         hex.EncodeToString(sum[:]),
		requestHash: strings.TrimSpace(requestHash),
		eventHeader: eventHeader,
	}
}

// resolveContext returns the full store.Context for a request, resolving via
// explicit context ID or metadata-based selection rules.
func (h *Handler) resolveContext(ctx context.Context, contextID uint64, metadataJSON string) (store.Context, error) {
	return h.resolveContextForRequest(ctx, contextID, metadataJSON)
}

func resolveContextConnectError(err error) error {
	switch {
	case errors.Is(err, errInvalidContextMetadata), errors.Is(err, store.ErrInvalidContextMetadata):
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("metadata_json must be a bounded flat JSON object"))
	case errors.Is(err, store.ErrContextResolutionLimit):
		return connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("workspace selection rule evaluation limit exceeded"))
	case errors.Is(err, sql.ErrNoRows):
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("processing context not found"))
	default:
		return connect.NewError(connect.CodeInternal, fmt.Errorf("resolve processing context: %w", err))
	}
}

// processingContextFromStore converts a store.Context into an hocr.ProcessingContext.
func processingContextFromStore(c store.Context) hocr.ProcessingContext {
	return hocr.ProcessingContext{
		SegmentationModel:     c.SegmentationModel,
		TranscriptionProvider: effectiveProvider(c.TranscriptionProvider),
		TranscriptionModel:    effectiveModel(effectiveProvider(c.TranscriptionProvider), c.TranscriptionModel),
		Temperature:           c.Temperature,
		SystemPrompt:          c.SystemPrompt,
	}
}

func processingLimitProvider(c store.Context) string {
	// Provider concurrency is a property of the installed transcription
	// capability. Segmentor/model choices must not create independent buckets
	// that allow callers to exceed that provider's configured limit.
	return effectiveProvider(c.TranscriptionProvider)
}

func processingLimitSegmentor(c store.Context) string {
	descriptor, _, _, err := providerregistry.New(config.Get().Config).ResolveSegmentor(c.SegmentationModel)
	if err == nil && strings.TrimSpace(descriptor.ID) != "" {
		return "segmentor:" + strings.TrimSpace(descriptor.ID)
	}
	selection := strings.ToLower(strings.TrimSpace(c.SegmentationModel))
	if selection == "" {
		selection = "auto"
	}
	if kind, _, found := strings.Cut(selection, ":"); found {
		selection = kind
	}
	return "segmentor:" + selection
}

func (h *Handler) acquireProcessingSlot(ctx context.Context, workspaceID uint64, processingContext store.Context) (func(), error) {
	release, err := h.processingLimiter.Acquire(ctx, workspaceID, processingLimitProvider(processingContext))
	if err == nil {
		return release, nil
	}
	switch {
	case errors.Is(err, context.Canceled):
		return nil, connect.NewError(connect.CodeCanceled, fmt.Errorf("processing request canceled"))
	case errors.Is(err, context.DeadlineExceeded):
		return nil, connect.NewError(connect.CodeDeadlineExceeded, fmt.Errorf("processing request deadline exceeded"))
	default:
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("acquire processing capacity: %w", err))
	}
}

func imageProcessingConnectError(operation string, err error) error {
	if errors.Is(err, store.ErrStorageQuotaExceeded) {
		return storageQuotaConnectError(err)
	}
	if ocrhandlers.IsInvalidImageError(err) {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	// Preserve the diagnostic for the inner logging interceptor. The outer
	// sanitizer replaces Internal messages before they cross the API boundary.
	return connect.NewError(connect.CodeInternal, fmt.Errorf("%s: %w", operation, err))
}

func (h *Handler) ProcessImageURL(ctx context.Context, req *connect.Request[scribev1.ProcessImageURLRequest]) (*connect.Response[scribev1.ProcessImageURLResponse], error) {
	imageURL := strings.TrimSpace(req.Msg.GetImageUrl())
	if imageURL == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("image_url is required"))
	}
	idempotencyKey := strings.TrimSpace(req.Msg.GetIdempotencyKey())
	if idempotencyKey == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("idempotency_key is required"))
	}
	if h.transcriptionJobs == nil || h.ocrRuns == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("processing idempotency repository is not configured"))
	}
	metadata, err := normalizeItemMetadata(req.Msg.GetMetadata())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	externalReferenceID, err := normalizeExternalReferenceID(req.Msg.GetExternalReferenceId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	externalReq := externalRequestFromHeaders(
		req.Header(),
		idempotencyKey,
		"image-url",
		stableRequestHash(imageURL, strconv.FormatUint(req.Msg.GetContextId(), 10), metadata, externalReferenceID),
	)
	resolvedCtx, err := h.resolveContext(ctx, req.Msg.GetContextId(), metadata)
	if err != nil {
		return nil, resolveContextConnectError(err)
	}
	var contextID *uint64
	if resolvedCtx.ID > 0 {
		v := resolvedCtx.ID
		contextID = &v
	}
	reservation, created, err := h.transcriptionJobs.ReserveExternalRequest(ctx, h.currentWorkspaceID(ctx), externalReq.source, externalReq.key, externalReq.requestHash, externalReq.eventHeader)
	if err != nil {
		if errors.Is(err, store.ErrExternalRequestMismatch) {
			return nil, connect.NewError(connect.CodeAlreadyExists, err)
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("reserve external request: %w", err))
	}
	if !created {
		switch reservation.Status {
		case store.ExternalRequestStatusCompleted:
			if reservation.ItemImageID == 0 {
				return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("external request already completed without an item image"))
			}
			run, err := h.ocrRuns.GetByItemImageID(ctx, reservation.ItemImageID)
			if err != nil {
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("reload completed image URL import"))
			}
			return connect.NewResponse(&scribev1.ProcessImageURLResponse{
				ItemId:             reservation.ItemID,
				ItemImageId:        reservation.ItemImageID,
				SessionId:          run.SessionID,
				ImageUrl:           run.ImageURL,
				Hocr:               run.OriginalHOCR,
				PlainText:          run.OriginalText,
				TranscriptionJobId: reservation.TranscriptionJobID,
			}), nil
		case store.ExternalRequestStatusInProgress:
			return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("external request is already in progress"))
		default:
			return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("external request already exists"))
		}
	}
	externalReq.leaseOwner = reservation.LeaseOwner
	failExternal := func(err error) {
		if err != nil {
			failureCtx, cancel := context.WithTimeout(h.backgroundContext(), 10*time.Second)
			defer cancel()
			if failErr := h.transcriptionJobs.FailExternalRequest(failureCtx, h.currentWorkspaceID(ctx), externalReq.source, externalReq.key, externalReq.leaseOwner, err.Error()); failErr != nil {
				slog.Warn("failed to release external request reservation", "source", externalReq.source, "error_type", safeLogErrorType(failErr))
			}
		}
	}
	storageReservation, err := h.reserveStorageQuota(ctx, store.StorageQuotaRequest{
		Bytes:  uint64(maxDeclaredImageBytes),
		Items:  1,
		Images: 1,
	})
	if err != nil {
		failExternal(err)
		return nil, err
	}
	defer func() { h.releaseStorageQuota(storageReservation) }()

	// Ingest performs segmentation only. Transcription is always durable and
	// asynchronous so every client observes the same job semantics.
	pctx := processingContextFromStore(resolvedCtx)
	pctx.SegmentOnly = true
	callCtx := withStorageQuotaReservation(ctx, storageReservation)
	callCtx = hocr.WithProviderCallMetadata(callCtx, h.currentWorkspaceID(ctx), "", nil, contextID)
	releaseProcessing, err := h.acquireProcessingSlot(ctx, h.currentWorkspaceID(ctx), resolvedCtx)
	if err != nil {
		failExternal(err)
		return nil, err
	}
	result, err := func() (*ocrhandlers.ProcessResult, error) {
		defer releaseProcessing()
		return h.ocr.ProcessImageURLWithContext(callCtx, imageURL, pctx)
	}()

	if err != nil {
		h.queueUploadFromProcessingError(ctx, err)
		failExternal(err)
		return nil, imageProcessingConnectError("process image URL", err)
	}
	if strings.TrimSpace(result.SessionID) == "" {
		result.SessionID = "processing_" + uuid.NewString()
	}
	// Blob quota follows ownership, not the number of bytes fetched for OCR.
	// Results that retain an external image reference therefore carry zero;
	// immutable local uploads carry their exact positive StoredBytes from the
	// staging boundary. The conservative pre-processing reservation above bounds
	// either outcome, then this resize and the ingest-store URL/byte validation
	// make the committed accounting exact.
	storedBytes := result.StoredBytes
	storageReservation, err = h.resizeStorageQuota(ctx, storageReservation, store.StorageQuotaRequest{Bytes: storedBytes, Items: 1, Images: 1})
	if err != nil {
		h.queueUnreferencedUploads(ctx, h.currentWorkspaceID(ctx), []store.ItemImage{{ImageURL: result.ImageURL, StorageBytes: storedBytes}})
		failExternal(err)
		return nil, err
	}
	atomicExternal := &store.SingleFileIngestExternalRequest{
		Source:         externalReq.source,
		IdempotencyKey: externalReq.key,
		LeaseOwner:     externalReq.leaseOwner,
	}
	committedIngest, err := h.commitSingleFileOCRIngest(ctx, singleFileOCRIngestRequest{
		SourceType:           "url",
		SessionID:            result.SessionID,
		ImageURL:             result.ImageURL,
		SourceURL:            imageURL,
		SourceLabel:          imageURL,
		Metadata:             metadata,
		ExternalReferenceID:  externalReferenceID,
		CallerIdempotencyKey: idempotencyKey,
		StorageBytes:         storedBytes,
		ResolvedContext:      resolvedCtx,
		ContextID:            contextID,
		Provider:             result.Provider,
		Model:                result.Model,
		HOCR:                 result.HOCR,
		PlainText:            result.PlainText,
		ParsedHOCR:           result.ParsedHOCR,
		EnqueueTranscription: true,
		Reservation:          storageReservation,
		ExternalRequest:      atomicExternal,
	})
	if err != nil {
		h.queueUnreferencedUploads(ctx, h.currentWorkspaceID(ctx), []store.ItemImage{{ImageURL: result.ImageURL, StorageBytes: storedBytes}})
		failExternal(err)
		if errors.As(err, new(*store.TranscriptionJobQuotaExceededError)) {
			return nil, transcriptionJobConnectError("enqueue transcription job", err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	item, itemImage := committedIngest.Item, committedIngest.Image
	return connect.NewResponse(&scribev1.ProcessImageURLResponse{
		ItemId:             item.ID,
		ItemImageId:        itemImage.ID,
		SessionId:          result.SessionID,
		ImageUrl:           result.ImageURL,
		Hocr:               result.HOCR,
		PlainText:          result.PlainText,
		TranscriptionJobId: committedIngest.TranscriptionJobID,
	}), nil
}

func (h *Handler) ProcessHOCR(ctx context.Context, req *connect.Request[scribev1.ProcessHOCRRequest]) (*connect.Response[scribev1.ProcessHOCRResponse], error) {
	hocrXML := strings.TrimSpace(req.Msg.GetHocr())
	if hocrXML == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("hocr is required"))
	}
	if len(hocrXML) > maxInlineHOCRBytes {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("hocr exceeds 10 MiB limit"))
	}
	idempotencyKey := strings.TrimSpace(req.Msg.GetIdempotencyKey())
	if idempotencyKey == "" {
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
	parsedHOCR, err := hocr.ParseDocument(hocrXML)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid hocr"))
	}
	plainText := hocr.PlainText(parsedHOCR.Lines)

	imageURL := strings.TrimSpace(req.Msg.GetImageUrl())
	hasImageData := len(req.Msg.GetImageData()) > 0
	if hasImageData == (imageURL != "") {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("exactly one of image_url or image_data is required"))
	}
	filename := strings.TrimSpace(req.Msg.GetFilename())
	storageBytes := uint64(0)
	imageIdentity := "url:" + imageURL
	sourceLabel := imageURL
	if hasImageData {
		if err := ocrhandlers.ValidateUploadedImageData(req.Msg.GetImageData()); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		storageBytes = uint64(len(req.Msg.GetImageData()))
		imageDigest := sha256.Sum256(req.Msg.GetImageData())
		imageIdentity = "data:" + hex.EncodeToString(imageDigest[:])
		if filename == "" {
			filename = "upload.jpg"
		}
		sourceLabel = filename
	} else {
		if err := h.authorizeProcessHOCRImageURL(ctx, imageURL); err != nil {
			switch {
			case errors.Is(err, errProcessHOCRLocalUpload):
				return nil, connect.NewError(connect.CodeInvalidArgument, errProcessHOCRLocalUpload)
			case errors.Is(err, errProcessHOCRInvalidImage):
				return nil, connect.NewError(connect.CodeInvalidArgument, errProcessHOCRInvalidImage)
			default:
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("authorize image_url"))
			}
		}
	}
	if h.transcriptionJobs == nil || h.ocrRuns == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("hOCR idempotency repositories are not configured"))
	}
	hocrDigest := sha256.Sum256([]byte(hocrXML))
	externalReq := externalRequestFromHeaders(
		req.Header(),
		idempotencyKey,
		"hocr-import",
		stableRequestHash(hex.EncodeToString(hocrDigest[:]), imageIdentity, filename, metadata, externalReferenceID),
	)
	workspaceID := h.currentWorkspaceID(ctx)
	reservation, created, err := h.transcriptionJobs.ReserveExternalRequest(
		ctx,
		workspaceID,
		externalReq.source,
		externalReq.key,
		externalReq.requestHash,
		externalReq.eventHeader,
	)
	if err != nil {
		if errors.Is(err, store.ErrExternalRequestMismatch) {
			return nil, connect.NewError(connect.CodeAlreadyExists, err)
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("reserve hOCR import: %w", err))
	}
	if !created {
		if reservation.Status != store.ExternalRequestStatusCompleted || reservation.ItemImageID == 0 {
			return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("hOCR import already exists"))
		}
		run, loadErr := h.ocrRuns.GetByItemImageID(ctx, reservation.ItemImageID)
		if loadErr != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("reload completed hOCR import"))
		}
		return connect.NewResponse(&scribev1.ProcessHOCRResponse{
			ItemId:             reservation.ItemID,
			ItemImageId:        reservation.ItemImageID,
			ImageUrl:           run.ImageURL,
			Hocr:               run.OriginalHOCR,
			PlainText:          run.OriginalText,
			SessionId:          run.SessionID,
			TranscriptionJobId: reservation.TranscriptionJobID,
		}), nil
	}
	externalReq.leaseOwner = reservation.LeaseOwner
	failExternal := func(processingErr error) {
		if processingErr == nil {
			return
		}
		failureCtx, cancel := context.WithTimeout(h.backgroundContext(), 10*time.Second)
		defer cancel()
		if failErr := h.transcriptionJobs.FailExternalRequest(
			failureCtx,
			workspaceID,
			externalReq.source,
			externalReq.key,
			externalReq.leaseOwner,
			processingErr.Error(),
		); failErr != nil {
			slog.Warn("failed to release hOCR import reservation", "source", externalReq.source, "error_type", safeLogErrorType(failErr))
		}
	}
	storageReservation, err := h.reserveStorageQuota(ctx, store.StorageQuotaRequest{Bytes: storageBytes, Items: 1, Images: 1})
	if err != nil {
		failExternal(err)
		return nil, err
	}
	defer h.releaseStorageQuota(storageReservation)
	if hasImageData {
		storedURL, storeErr := h.ocr.StoreUploadedImage(withStorageQuotaReservation(ctx, storageReservation), filename, req.Msg.GetImageData())
		if storeErr != nil {
			h.queueUploadFromProcessingError(ctx, storeErr)
			failExternal(storeErr)
			if errors.Is(storeErr, store.ErrStorageQuotaExceeded) {
				return nil, storageQuotaConnectError(storeErr)
			}
			return nil, connect.NewError(connect.CodeInternal, storeErr)
		}
		imageURL = storedURL
	}

	importedContext := store.Context{
		Name:                  "Imported hOCR",
		SegmentationModel:     "imported",
		TranscriptionProvider: "custom",
		TranscriptionModel:    "custom",
	}
	committedIngest, err := h.commitSingleFileOCRIngest(ctx, singleFileOCRIngestRequest{
		SourceType:           "hocr",
		SessionID:            "processing_" + uuid.NewString(),
		ImageURL:             imageURL,
		SourceLabel:          sourceLabel,
		Metadata:             metadata,
		ExternalReferenceID:  externalReferenceID,
		CallerIdempotencyKey: idempotencyKey,
		StorageBytes:         storageBytes,
		ResolvedContext:      importedContext,
		Provider:             "custom",
		Model:                "custom",
		HOCR:                 hocrXML,
		PlainText:            plainText,
		ParsedHOCR:           &parsedHOCR,
		Reservation:          storageReservation,
		ExternalRequest: &store.SingleFileIngestExternalRequest{
			Source:         externalReq.source,
			IdempotencyKey: externalReq.key,
			LeaseOwner:     externalReq.leaseOwner,
		},
	})
	if err != nil {
		h.queueUnreferencedUploads(ctx, h.currentWorkspaceID(ctx), []store.ItemImage{{ImageURL: imageURL, StorageBytes: storageBytes}})
		failExternal(err)
		if errors.Is(err, store.ErrStorageQuotaExceeded) {
			return nil, storageQuotaConnectError(err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	item, itemImage := committedIngest.Item, committedIngest.Image
	run, err := h.ocrRuns.GetByItemImageID(ctx, itemImage.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("reload committed hOCR run"))
	}
	return connect.NewResponse(&scribev1.ProcessHOCRResponse{
		ItemId:             item.ID,
		ItemImageId:        itemImage.ID,
		SessionId:          run.SessionID,
		ImageUrl:           imageURL,
		Hocr:               hocrXML,
		PlainText:          plainText,
		TranscriptionJobId: committedIngest.TranscriptionJobID,
	}), nil
}

// authorizeProcessHOCRImageURL prevents a caller from converting knowledge of
// another tenant's private immutable upload name into a local ownership row.
// Absolute public external image URLs retain their existing import behavior;
// application-relative upload references are always rejected here.
func (h *Handler) authorizeProcessHOCRImageURL(_ context.Context, imageURL string) error {
	if _, localUpload := uploadref.NameFromURL(imageURL); localUpload {
		return errProcessHOCRLocalUpload
	}
	parsed, err := url.Parse(strings.TrimSpace(imageURL))
	if err != nil || safehttp.ValidatePublicURL(parsed) != nil {
		return errProcessHOCRInvalidImage
	}
	return nil
}

func (h *Handler) GetOCRRun(ctx context.Context, req *connect.Request[scribev1.GetOCRRunRequest]) (*connect.Response[scribev1.GetOCRRunResponse], error) {
	itemImageID := req.Msg.GetItemImageId()
	if itemImageID == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("item_image_id is required"))
	}
	if _, authErr := h.itemImageForRequest(ctx, itemImageID); authErr != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("ocr run not found"))
	}
	run, err := h.ocrRuns.GetByItemImageID(ctx, itemImageID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("ocr run not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &scribev1.GetOCRRunResponse{
		SessionId:           run.SessionID,
		ImageUrl:            run.ImageURL,
		Model:               run.Model,
		OriginalHocr:        run.OriginalHOCR,
		OriginalText:        run.OriginalText,
		LevenshteinDistance: int32FromIntBounded(run.LevenshteinDistance),
	}
	if run.ItemImageID != nil {
		resp.ItemImageId = *run.ItemImageID
	}
	if run.ContextID != nil {
		resp.ContextId = *run.ContextID
	}
	if run.CanonicalRevision != nil {
		resp.CanonicalRevision = *run.CanonicalRevision
	}
	return connect.NewResponse(resp), nil
}

func (h *Handler) ReprocessItemImage(ctx context.Context, req *connect.Request[scribev1.ReprocessItemImageRequest]) (*connect.Response[scribev1.ReprocessItemImageResponse], error) {
	itemImageID := req.Msg.GetItemImageId()
	if itemImageID == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("item_image_id is required"))
	}
	expectedRevision := req.Msg.GetExpectedRevision()
	if expectedRevision == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("expected_revision is required"))
	}
	requestedContextID := req.Msg.GetContextId()
	if h.transcriptionJobs == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("reprocess request repository is not configured"))
	}
	img, err := h.itemImageForRequest(ctx, itemImageID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("item image not found"))
	}
	basePage, err := h.currentAnnotationPage(ctx, itemImageID)
	if err != nil {
		return nil, annotationConnectError(err)
	}

	workspaceID := h.currentWorkspaceID(ctx)
	operationKey := fmt.Sprintf("%d:%d", itemImageID, expectedRevision)
	requestHash := stableRequestHash(
		strconv.FormatUint(itemImageID, 10),
		strconv.FormatUint(expectedRevision, 10),
		strconv.FormatUint(requestedContextID, 10),
	)
	if basePage.Revision != expectedRevision {
		prior, lookupErr := h.transcriptionJobs.GetExternalRequest(ctx, workspaceID, "image-reprocess", operationKey)
		if lookupErr == nil {
			if prior.Status == store.ExternalRequestStatusCompleted {
				if prior.RequestHash != requestHash {
					return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("this annotation revision was reprocessed with different parameters"))
				}
				return h.replayedReprocessResponse(ctx, itemImageID, prior)
			}
		} else if !errors.Is(lookupErr, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("look up image reprocess: %w", lookupErr))
		}
		return nil, connect.NewError(connect.CodeAborted, fmt.Errorf("canonical annotations changed; reload before reprocessing"))
	}
	canvasURI := strings.TrimSpace(img.CanvasURI)
	if canvasURI == "" || canvasURI != strings.TrimSpace(basePage.CanvasURI) {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("item image canonical canvas invariant is invalid"))
	}
	resolvedCtx, err := h.resolveContext(ctx, requestedContextID, "")
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("processing context not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("resolve processing context: %w", err))
	}
	contextID := &resolvedCtx.ID

	reservation, created, err := h.transcriptionJobs.ReserveExternalRequestForItemImage(
		ctx,
		workspaceID,
		itemImageID,
		"image-reprocess",
		operationKey,
		requestHash,
		"",
	)
	if err != nil {
		if errors.Is(err, store.ErrExternalRequestMismatch) {
			return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("this annotation revision is already being reprocessed with different parameters"))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("reserve image reprocess: %w", err))
	}
	if !created {
		switch reservation.Status {
		case store.ExternalRequestStatusCompleted:
			return h.replayedReprocessResponse(ctx, itemImageID, reservation)
		case store.ExternalRequestStatusInProgress:
			return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("this annotation revision is already being reprocessed"))
		default:
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("this annotation revision exhausted its reprocess attempts"))
		}
	}
	reservationCommitted := false
	defer func() {
		if reservationCommitted {
			return
		}
		failureCtx, cancel := context.WithTimeout(h.backgroundContext(), 10*time.Second)
		defer cancel()
		if failErr := h.transcriptionJobs.FailExternalRequest(
			failureCtx,
			workspaceID,
			"image-reprocess",
			operationKey,
			reservation.LeaseOwner,
			"image reprocess did not commit",
		); failErr != nil {
			slog.Warn("failed to release image reprocess reservation", "item_image_id", itemImageID, "error_type", safeLogErrorType(failErr))
		}
	}()

	pctx := processingContextFromStore(resolvedCtx)
	pctx.SegmentOnly = true
	callCtx := hocr.WithProviderCallMetadata(ctx, workspaceID, "", &img.ID, contextID)
	releaseProcessing, err := h.acquireProcessingSlot(ctx, workspaceID, resolvedCtx)
	if err != nil {
		return nil, err
	}
	result, err := func() (*ocrhandlers.ProcessResult, error) {
		defer releaseProcessing()
		return h.ocr.ProcessImageURLTransientWithContext(callCtx, img.ImageURL, pctx)
	}()
	if err != nil {
		return nil, imageProcessingConnectError("reprocess item image", err)
	}
	provider := result.Provider
	model := result.Model

	annotationScopeID := fmt.Sprintf("item-image-%d", itemImageID)
	parsedHOCR, err := parsedHOCRDocument(result.HOCR, result.ParsedHOCR)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("parse reprocessed hocr: %w", err))
	}
	annotationItems := buildLineAnnotations(annotationScopeID, canvasURI, parsedHOCR.Lines)
	annotationItems = append(annotationItems, buildWordAnnotations(annotationScopeID, canvasURI, parsedHOCR.Words)...)
	var pageDocument map[string]any
	if err := iiif.DecodeJSON([]byte(basePage.Payload), &pageDocument); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("decode canonical annotation page: %w", err))
	}
	pageDocument["items"] = annotationItems
	draftPage, err := json.Marshal(pageDocument)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("encode reprocessed annotation page: %w", err))
	}
	normalizedPage, err := iiif.NormalizeAnnotationPage(draftPage, iiif.PageIdentity{
		PublicBaseURL: h.publicAnnotationBaseURL(),
		ItemImageID:   itemImageID,
		CanvasURI:     canvasURI,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("validate reprocessed annotation page: %w", err))
	}
	if err := iiif.ValidateAnnotationPageGeometry(normalizedPage, img.Width, img.Height); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("validate reprocessed annotation geometry: %w", err))
	}
	basePage.Payload = string(normalizedPage)
	if userID := h.currentUserID(ctx); userID > 0 {
		basePage.UpdatedByUserID = &userID
	}
	event := h.newCloudEvent("dev.scribe.annotations.reprocessed", subjectForItemImage(itemImageID), map[string]any{
		"itemImageId":       itemImageID,
		"canvasUri":         canvasURI,
		"annotationCount":   len(annotationItems),
		"annotationPageId":  basePage.PageID,
		"canonicalRevision": basePage.Revision + 1,
	})
	eventBody, err := json.Marshal(event)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("encode reprocess event: %w", err))
	}
	commit, err := h.annotations.SavePageAndStartReprocessing(ctx, basePage, expectedRevision, store.AnnotationReprocessCommit{
		OCRRun: store.OCRRun{
			SessionID:    result.SessionID,
			ItemImageID:  &itemImageID,
			ContextID:    contextID,
			ImageURL:     img.ImageURL,
			Provider:     provider,
			Model:        model,
			OriginalHOCR: result.HOCR,
			OriginalText: result.PlainText,
		},
		Context:   resolvedCtx,
		EventID:   event.ID,
		EventType: event.Type,
		Subject:   event.Subject,
		BodyJSON:  string(eventBody),
		ExternalRequest: &store.AnnotationReprocessExternalRequest{
			Source:         "image-reprocess",
			IdempotencyKey: operationKey,
			LeaseOwner:     reservation.LeaseOwner,
		},
	})
	if err != nil {
		if errors.Is(err, store.ErrAnnotationRevisionConflict) {
			return nil, connect.NewError(connect.CodeAborted, fmt.Errorf("canonical annotations changed while reprocessing"))
		}
		return nil, transcriptionJobConnectError("commit reprocessed annotations", err)
	}
	reservationCommitted = true
	h.publishTranscriptionJob(ctx, commit.TranscriptionJobID)
	h.publishCloudEvent(event, false)

	return connect.NewResponse(&scribev1.ReprocessItemImageResponse{
		SessionId:          result.SessionID,
		ItemImageId:        itemImageID,
		ContextId:          resolvedCtx.ID,
		ImageUrl:           img.ImageURL,
		Hocr:               result.HOCR,
		PlainText:          result.PlainText,
		Provider:           provider,
		Model:              model,
		TranscriptionJobId: commit.TranscriptionJobID,
		CanonicalRevision:  commit.Page.Revision,
	}), nil
}

func (h *Handler) replayedReprocessResponse(
	ctx context.Context,
	itemImageID uint64,
	request store.ExternalRequest,
) (*connect.Response[scribev1.ReprocessItemImageResponse], error) {
	if request.ItemImageID != itemImageID || request.TranscriptionJobID == 0 || strings.TrimSpace(request.SessionID) == "" {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("completed image reprocess result is incomplete"))
	}
	run, err := h.ocrRuns.Get(ctx, request.SessionID)
	if err != nil || run.ItemImageID == nil || *run.ItemImageID != itemImageID {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("completed image reprocess OCR baseline is unavailable"))
	}
	job, err := h.transcriptionJobs.Get(ctx, request.TranscriptionJobID)
	if err != nil || job.ItemImageID != itemImageID || job.InputRevision == 0 {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("completed image reprocess job is unavailable"))
	}
	contextID := uint64(0)
	if run.ContextID != nil {
		contextID = *run.ContextID
	}
	return connect.NewResponse(&scribev1.ReprocessItemImageResponse{
		SessionId:          run.SessionID,
		ItemImageId:        itemImageID,
		ContextId:          contextID,
		ImageUrl:           run.ImageURL,
		Hocr:               run.OriginalHOCR,
		PlainText:          run.OriginalText,
		Provider:           run.Provider,
		Model:              run.Model,
		TranscriptionJobId: job.ID,
		CanonicalRevision:  job.InputRevision,
	}), nil
}

func contextProviderLabel(provider string) string {
	if descriptor, err := providerregistry.New(config.Get().Config).ResolveProvider(provider); err == nil {
		return descriptor.Label
	}
	if provider = strings.TrimSpace(provider); provider != "" {
		return provider
	}
	return "OCR"
}

func itemSourceLabel(sourceLabel, imageURL string) string {
	label := strings.TrimSpace(sourceLabel)
	if label == "" {
		label = strings.TrimSpace(imageURL)
	}
	if label == "" {
		return "OCR Item"
	}
	if parsed, err := url.Parse(label); err == nil {
		if base := path.Base(strings.TrimSpace(parsed.Path)); base != "" && base != "." && base != "/" {
			return base
		}
		if host := strings.TrimSpace(parsed.Host); host != "" {
			return host
		}
	}
	return label
}

func itemContextLabel(resolvedCtx store.Context) string {
	provider := contextProviderLabel(resolvedCtx.TranscriptionProvider)
	segmentation := strings.TrimSpace(resolvedCtx.SegmentationModel)
	if segmentation == "" {
		segmentation = "auto"
	}
	model := strings.TrimSpace(resolvedCtx.TranscriptionModel)
	if model == "" {
		model = "default"
	}
	return fmt.Sprintf("%s (%s, %s)", provider, segmentation, model)
}

type singleFileOCRIngestRequest struct {
	SourceType           string
	SessionID            string
	ImageURL             string
	SourceURL            string
	SourceLabel          string
	Metadata             string
	ExternalReferenceID  string
	CallerIdempotencyKey string
	StorageBytes         uint64
	ResolvedContext      store.Context
	ContextID            *uint64
	Provider             string
	Model                string
	HOCR                 string
	PlainText            string
	ParsedHOCR           *hocr.Document
	EnqueueTranscription bool
	Reservation          store.StorageQuotaReservation
	ExternalRequest      *store.SingleFileIngestExternalRequest
}

func (h *Handler) commitSingleFileOCRIngest(ctx context.Context, request singleFileOCRIngestRequest) (store.SingleFileIngestResult, error) {
	parsedHOCR, err := parsedHOCRDocument(request.HOCR, request.ParsedHOCR)
	if err != nil {
		return store.SingleFileIngestResult{}, fmt.Errorf("parse hOCR: %w", err)
	}
	lines, words := parsedHOCR.Lines, parsedHOCR.Words
	pageWidth, pageHeight := parsedHOCR.PageWidth, parsedHOCR.PageHeight
	if pageWidth <= 0 || pageHeight <= 0 || pageWidth > math.MaxInt32 || pageHeight > math.MaxInt32 {
		return store.SingleFileIngestResult{}, fmt.Errorf("hOCR page dimensions are required and must be within processing limits")
	}
	imageWidth := uint32(pageWidth)
	imageHeight := uint32(pageHeight)
	workspaceID := h.currentWorkspaceID(ctx)
	userID := h.currentUserID(ctx)
	itemID := "item_" + uuid.NewString()
	itemName := fmt.Sprintf("%s %s", itemSourceLabel(request.SourceLabel, request.ImageURL), itemContextLabel(request.ResolvedContext))
	var transcriptionContext *store.Context
	if request.EnqueueTranscription {
		processingContext := request.ResolvedContext
		transcriptionContext = &processingContext
	}
	result, err := h.annotations.CommitSingleFileIngest(ctx, store.SingleFileIngestCommit{
		Item: db.CreateItemParams{
			ID:                   itemID,
			UserID:               userID,
			WorkspaceID:          workspaceID,
			Name:                 itemName,
			SourceType:           request.SourceType,
			SourceURL:            request.SourceURL,
			Metadata:             request.Metadata,
			ExternalReferenceID:  request.ExternalReferenceID,
			CallerIdempotencyKey: request.CallerIdempotencyKey,
		},
		Image: db.CreateItemImageParams{
			ItemID:       itemID,
			Sequence:     0,
			ImageURL:     request.ImageURL,
			StorageBytes: request.StorageBytes,
			Width:        imageWidth,
			Height:       imageHeight,
		},
		OCRRun: store.OCRRun{
			SessionID:    request.SessionID,
			ContextID:    request.ContextID,
			ImageURL:     request.ImageURL,
			Provider:     request.Provider,
			Model:        request.Model,
			OriginalHOCR: request.HOCR,
			OriginalText: request.PlainText,
		},
		PublicBaseURL:        h.publicAnnotationBaseURL(),
		TranscriptionContext: transcriptionContext,
		Reservation:          request.Reservation,
		Limits:               configuredStorageQuotaLimits(),
		ExternalRequest:      request.ExternalRequest,
		BuildPage: func(itemImageID uint64, canvasURI string) (store.AnnotationPage, error) {
			annotationScopeID := fmt.Sprintf("item-image-%d", itemImageID)
			items := buildLineAnnotations(annotationScopeID, canvasURI, lines)
			items = append(items, buildWordAnnotations(annotationScopeID, canvasURI, words)...)
			payload, pageErr := iiif.NewAnnotationPage(iiif.PageIdentity{
				PublicBaseURL: h.publicAnnotationBaseURL(),
				ItemImageID:   itemImageID,
				CanvasURI:     canvasURI,
			}, items)
			if pageErr != nil {
				return store.AnnotationPage{}, pageErr
			}
			if pageErr := iiif.ValidateAnnotationPageGeometry(payload, imageWidth, imageHeight); pageErr != nil {
				return store.AnnotationPage{}, pageErr
			}
			pageID, pageErr := iiif.CanonicalPageID(h.publicAnnotationBaseURL(), itemImageID)
			if pageErr != nil {
				return store.AnnotationPage{}, pageErr
			}
			return store.AnnotationPage{
				WorkspaceID:     workspaceID,
				ItemImageID:     itemImageID,
				PageID:          pageID,
				CanvasURI:       canvasURI,
				Payload:         string(payload),
				UpdatedByUserID: &userID,
			}, nil
		},
	})
	if err != nil {
		return store.SingleFileIngestResult{}, err
	}
	h.publishEvent("dev.scribe.annotations.created", subjectForItemImage(result.Image.ID), map[string]any{
		"itemImageId":      result.Image.ID,
		"canvasUri":        result.Page.CanvasURI,
		"annotationCount":  len(lines) + len(words),
		"annotationPageId": result.Page.PageID,
	})
	if result.TranscriptionJobID != 0 {
		h.publishTranscriptionJob(ctx, result.TranscriptionJobID)
	}
	return result, nil
}

func parsedHOCRDocument(raw string, parsed *hocr.Document) (hocr.Document, error) {
	if parsed != nil {
		return *parsed, nil
	}
	return hocr.ParseDocument(raw)
}
