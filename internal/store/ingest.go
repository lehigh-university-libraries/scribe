package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	db "github.com/lehigh-university-libraries/scribe/internal/db"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/uploadref"
)

// SingleFileIngestExternalRequest identifies an operation reservation that
// must become complete in the same transaction as the ingest result.
type SingleFileIngestExternalRequest struct {
	Source         string
	IdempotencyKey string
	LeaseOwner     string
}

// SingleFileIngestPageFactory builds the initial canonical page after the
// database has assigned the image ID. Implementations must be deterministic
// and side-effect free: the function runs inside the ingest transaction.
type SingleFileIngestPageFactory func(itemImageID uint64, canvasURI string) (AnnotationPage, error)

// SingleFileIngestCommit contains every durable result of URL, upload, or
// inline-hOCR ingestion. TranscriptionContext is nil only for an imported hOCR
// page that should not enqueue model-driven transcription.
type SingleFileIngestCommit struct {
	Item                 db.CreateItemParams
	Image                db.CreateItemImageParams
	OCRRun               OCRRun
	PublicBaseURL        string
	TranscriptionContext *Context
	Reservation          StorageQuotaReservation
	Limits               StorageQuotaLimits
	ExternalRequest      *SingleFileIngestExternalRequest
	BuildPage            SingleFileIngestPageFactory
}

// SingleFileIngestResult is returned only after the complete ingest commit is
// durable. A non-zero TranscriptionJobID is ready to publish to the queue.
type SingleFileIngestResult struct {
	Item               Item
	Image              ItemImage
	Page               AnnotationPage
	TranscriptionJobID uint64
}

// ManifestIngestCanvas is one bounded, prefetched Canvas in an atomic manifest
// ingest. OCRRun is nil when the source provides no hOCR; BuildPage must still
// return a complete (possibly empty) canonical AnnotationPage.
type ManifestIngestCanvas struct {
	Image      db.CreateItemImageParams
	OCRRun     *OCRRun
	BuildPage  SingleFileIngestPageFactory
	EnqueueJob bool
}

// ManifestIngestCommit contains every relational side effect of one manifest
// import. Remote fetches are completed before this value is built; all tenant
// rows and the idempotency result commit together.
type ManifestIngestCommit struct {
	Item                 db.CreateItemParams
	Canvases             []ManifestIngestCanvas
	PublicBaseURL        string
	TranscriptionContext *Context
	Reservation          StorageQuotaReservation
	Limits               StorageQuotaLimits
	ExternalRequest      *SingleFileIngestExternalRequest
}

type ManifestIngestResult struct {
	Item                Item
	TranscriptionJobIDs []uint64
}

// CommitManifestIngest prevents a process crash from exposing a partial item
// or losing the idempotency result. Complete pages exist for every Canvas,
// including image-only manifests with no imported OCR baseline.
func (s *AnnotationStore) CommitManifestIngest(ctx context.Context, commit ManifestIngestCommit) (result ManifestIngestResult, err error) {
	if s == nil || s.pool == nil {
		return ManifestIngestResult{}, fmt.Errorf("commit manifest ingest: store is not configured")
	}
	if err := validateManifestIngestCommit(commit); err != nil {
		return ManifestIngestResult{}, err
	}
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return ManifestIngestResult{}, fmt.Errorf("commit manifest ingest: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err = lockStorageQuotaGuards(ctx, tx, commit.Item.WorkspaceID); err != nil {
		return ManifestIngestResult{}, fmt.Errorf("commit manifest ingest: lock quota guards: %w", err)
	}
	queries := s.q.WithTx(tx)
	if _, err := queries.LockWorkspaceMemberRole(ctx, commit.Item.WorkspaceID, commit.Item.UserID); err != nil {
		return ManifestIngestResult{}, fmt.Errorf("commit manifest ingest: lock workspace membership: %w", err)
	}
	lockedReservation, err := queries.LockLiveStorageQuotaReservation(ctx, db.LockLiveStorageQuotaReservationParams{
		ID: commit.Reservation.ID, WorkspaceID: commit.Item.WorkspaceID,
	})
	if err != nil {
		return ManifestIngestResult{}, fmt.Errorf("commit manifest ingest: lock storage reservation: %w", err)
	}
	if lockedReservation.ReservedItems < 1 || uint64(lockedReservation.ReservedImages) < uint64(len(commit.Canvases)) || lockedReservation.ResourceKey.Valid {
		return ManifestIngestResult{}, fmt.Errorf("commit manifest ingest: storage reservation does not cover the external manifest")
	}
	if err = queries.CreateItem(ctx, commit.Item); err != nil {
		return ManifestIngestResult{}, fmt.Errorf("commit manifest ingest: create item: %w", err)
	}

	var contextSnapshot json.RawMessage
	var contextID *uint64
	if commit.TranscriptionContext != nil {
		lockedContext, snapshot, contextErr := lockContextSnapshotForWorkspace(ctx, queries, commit.TranscriptionContext.ID, commit.Item.WorkspaceID)
		if contextErr != nil {
			return ManifestIngestResult{}, fmt.Errorf("commit manifest ingest: lock context: %w", contextErr)
		}
		contextSnapshot = snapshot
		value := lockedContext.ID
		contextID = &value
	}

	jobIDs := make([]uint64, 0, len(commit.Canvases))
	for position, canvas := range commit.Canvases {
		imageParams := canvas.Image
		imageParams.ItemID = commit.Item.ID
		imageID, createErr := queries.CreateItemImage(ctx, imageParams)
		if createErr != nil {
			return ManifestIngestResult{}, fmt.Errorf("commit manifest ingest: create Canvas %d: %w", position+1, createErr)
		}
		canvasURI := strings.TrimSpace(imageParams.CanvasURI)
		if canvasURI == "" {
			canvasURI, createErr = iiif.ItemImageCanvasID(commit.PublicBaseURL, imageID)
			if createErr != nil {
				return ManifestIngestResult{}, fmt.Errorf("commit manifest ingest: construct Canvas %d identity: %w", position+1, createErr)
			}
			if updateErr := queries.UpdateItemImageCanvasURI(ctx, imageID, canvasURI); updateErr != nil {
				return ManifestIngestResult{}, fmt.Errorf("commit manifest ingest: persist Canvas %d identity: %w", position+1, updateErr)
			}
		}
		page, pageErr := canvas.BuildPage(imageID, canvasURI)
		if pageErr != nil {
			return ManifestIngestResult{}, fmt.Errorf("commit manifest ingest: build Canvas %d page: %w", position+1, pageErr)
		}
		page.WorkspaceID = commit.Item.WorkspaceID
		page.ItemImageID = imageID
		page.CanvasURI = canvasURI
		entries, row, prepareErr := prepareInitialAnnotationPage(page, imageParams.Width, imageParams.Height)
		if prepareErr != nil {
			return ManifestIngestResult{}, fmt.Errorf("commit manifest ingest: prepare Canvas %d page: %w", position+1, prepareErr)
		}
		if createErr := queries.CreateAnnotationPage(ctx, row); createErr != nil {
			return ManifestIngestResult{}, fmt.Errorf("commit manifest ingest: create Canvas %d page: %w", position+1, createErr)
		}
		if indexErr := queries.ReplaceAnnotationIndex(ctx, commit.Item.WorkspaceID, imageID, indexEntriesToRows(entries)); indexErr != nil {
			return ManifestIngestResult{}, fmt.Errorf("commit manifest ingest: index Canvas %d page: %w", position+1, indexErr)
		}
		if canvas.OCRRun != nil {
			run := *canvas.OCRRun
			run.ItemImageID = &imageID
			run.ImageURL = imageParams.ImageURL
			if runErr := insertCurrentOCRRun(ctx, queries, run); runErr != nil {
				return ManifestIngestResult{}, fmt.Errorf("commit manifest ingest: save Canvas %d OCR baseline: %w", position+1, runErr)
			}
		}
		if canvas.EnqueueJob {
			if commit.TranscriptionContext == nil || contextID == nil {
				return ManifestIngestResult{}, fmt.Errorf("commit manifest ingest: Canvas %d needs a transcription context", position+1)
			}
			workspaceID, lockErr := lockTranscriptionAdmissionWorkspace(ctx, queries, imageID)
			if lockErr != nil {
				return ManifestIngestResult{}, fmt.Errorf("commit manifest ingest: lock Canvas %d job admission: %w", position+1, lockErr)
			}
			if workspaceID != commit.Item.WorkspaceID {
				return ManifestIngestResult{}, ErrAnnotationPageResource
			}
			if admissionErr := enforceTranscriptionJobAdmission(ctx, queries, workspaceID, s.admission); admissionErr != nil {
				return ManifestIngestResult{}, admissionErr
			}
			jobID, jobErr := queries.CreateTranscriptionJob(ctx, db.CreateTranscriptionJobParams{
				ItemImageID: imageID, ContextID: contextID, ContextSnapshot: contextSnapshot,
			})
			if jobErr != nil {
				return ManifestIngestResult{}, fmt.Errorf("commit manifest ingest: create Canvas %d job: %w", position+1, jobErr)
			}
			jobIDs = append(jobIDs, jobID)
		}
	}

	if external := commit.ExternalRequest; external != nil {
		if err := queries.CompleteExternalRequest(ctx, db.CompleteExternalRequestManualParams{
			WorkspaceID: commit.Item.WorkspaceID, Source: strings.TrimSpace(external.Source),
			IdempotencyKey: strings.TrimSpace(external.IdempotencyKey), LockedBy: nullableString(external.LeaseOwner),
			ItemID: nullableString(commit.Item.ID), ItemImageID: sql.NullInt64{}, TranscriptionJobID: sql.NullInt64{}, SessionID: sql.NullString{},
		}); err != nil {
			return ManifestIngestResult{}, fmt.Errorf("commit manifest ingest: complete external request: %w", err)
		}
	}
	itemRow, err := queries.GetItem(ctx, commit.Item.ID)
	if err != nil {
		return ManifestIngestResult{}, fmt.Errorf("commit manifest ingest: reload item: %w", err)
	}
	imageRows, err := queries.ListItemImages(ctx, commit.Item.ID)
	if err != nil {
		return ManifestIngestResult{}, fmt.Errorf("commit manifest ingest: reload Canvases: %w", err)
	}
	durableBytes, measureErr := itemDurableDatabaseBytes(ctx, queries, commit.Item.WorkspaceID, commit.Item.ID)
	if measureErr != nil {
		return ManifestIngestResult{}, fmt.Errorf("commit manifest ingest: measure item storage: %w", measureErr)
	}
	if err := retireSingleFileIngestReservation(ctx, queries, commit.Reservation, lockedReservation, StorageQuotaRequest{
		DurableBytes: durableBytes, Items: 1, Images: uint64(len(commit.Canvases)),
	}, commit.Limits); err != nil {
		return ManifestIngestResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return ManifestIngestResult{}, fmt.Errorf("commit manifest ingest: commit: %w", err)
	}
	item := rowToItem(itemRow)
	item.Images = make([]ItemImage, 0, len(imageRows))
	for _, imageRow := range imageRows {
		item.Images = append(item.Images, rowToItemImage(imageRow))
	}
	return ManifestIngestResult{Item: item, TranscriptionJobIDs: jobIDs}, nil
}

func validateManifestIngestCommit(commit ManifestIngestCommit) error {
	if strings.TrimSpace(commit.Item.ID) == "" || commit.Item.WorkspaceID == 0 || commit.Item.UserID == 0 {
		return fmt.Errorf("commit manifest ingest: item identity and ownership are required")
	}
	if len(commit.Canvases) == 0 || strings.TrimSpace(commit.PublicBaseURL) == "" {
		return fmt.Errorf("commit manifest ingest: at least one Canvas and public base URL are required")
	}
	if commit.Reservation.ID == "" || commit.Reservation.WorkspaceID != commit.Item.WorkspaceID {
		return fmt.Errorf("commit manifest ingest: matching storage reservation is required")
	}
	if err := validateStorageQuotaLimits(commit.Limits); err != nil {
		return fmt.Errorf("commit manifest ingest: %w", err)
	}
	for position, canvas := range commit.Canvases {
		if strings.TrimSpace(canvas.Image.ImageURL) == "" || canvas.BuildPage == nil {
			return fmt.Errorf("commit manifest ingest: Canvas %d image and page factory are required", position+1)
		}
		if err := validateImageStorageReference(canvas.Image.ImageURL, canvas.Image.StorageBytes); err != nil {
			return fmt.Errorf("commit manifest ingest: Canvas %d: %w", position+1, err)
		}
		if _, localUpload := uploadref.NameFromURL(canvas.Image.ImageURL); localUpload {
			return fmt.Errorf("commit manifest ingest: Canvas %d: imported manifest images must be remote resources", position+1)
		}
		if canvas.EnqueueJob && (commit.TranscriptionContext == nil || commit.TranscriptionContext.ID == 0) {
			return fmt.Errorf("commit manifest ingest: Canvas %d transcription context is required", position+1)
		}
		if run := canvas.OCRRun; run != nil && strings.TrimSpace(run.OriginalHOCR) == "" {
			return fmt.Errorf("commit manifest ingest: Canvas %d OCR baseline is empty", position+1)
		}
	}
	if external := commit.ExternalRequest; external != nil {
		if strings.TrimSpace(external.Source) == "" || strings.TrimSpace(external.IdempotencyKey) == "" || strings.TrimSpace(external.LeaseOwner) == "" {
			return fmt.Errorf("commit manifest ingest: external request fence is incomplete")
		}
	}
	return nil
}

// CommitSingleFileIngest atomically creates an item, image, current immutable
// OCR baseline, canonical AnnotationPage and derived index, optional
// transcription job, optional idempotency completion, and staging retirement.
// No partially readable image is possible if any boundary fails.
func (s *AnnotationStore) CommitSingleFileIngest(ctx context.Context, commit SingleFileIngestCommit) (result SingleFileIngestResult, err error) {
	if s == nil || s.pool == nil {
		return SingleFileIngestResult{}, fmt.Errorf("commit single-file ingest: store is not configured")
	}
	if err := validateSingleFileIngestCommit(commit); err != nil {
		return SingleFileIngestResult{}, err
	}

	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return SingleFileIngestResult{}, fmt.Errorf("commit single-file ingest: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err = lockStorageQuotaGuards(ctx, tx, commit.Item.WorkspaceID); err != nil {
		return SingleFileIngestResult{}, fmt.Errorf("commit single-file ingest: lock quota guards: %w", err)
	}
	queries := s.q.WithTx(tx)
	if _, err := queries.LockWorkspaceMemberRole(ctx, commit.Item.WorkspaceID, commit.Item.UserID); err != nil {
		return SingleFileIngestResult{}, fmt.Errorf("commit single-file ingest: lock workspace membership: %w", err)
	}

	lockedReservation, err := lockSingleFileIngestReservation(ctx, queries, commit)
	if err != nil {
		return SingleFileIngestResult{}, err
	}
	if err = queries.CreateItem(ctx, commit.Item); err != nil {
		return SingleFileIngestResult{}, fmt.Errorf("commit single-file ingest: create item: %w", err)
	}
	commit.Image.ItemID = commit.Item.ID
	imageID, err := queries.CreateItemImage(ctx, commit.Image)
	if err != nil {
		return SingleFileIngestResult{}, fmt.Errorf("commit single-file ingest: create image: %w", err)
	}
	canvasURI := strings.TrimSpace(commit.Image.CanvasURI)
	if canvasURI == "" {
		canvasURI, err = iiif.ItemImageCanvasID(commit.PublicBaseURL, imageID)
		if err != nil {
			return SingleFileIngestResult{}, fmt.Errorf("commit single-file ingest: construct canvas URI: %w", err)
		}
	}

	page, err := commit.BuildPage(imageID, canvasURI)
	if err != nil {
		return SingleFileIngestResult{}, fmt.Errorf("commit single-file ingest: build canonical page: %w", err)
	}
	if strings.TrimSpace(commit.Image.CanvasURI) == "" {
		if err := queries.UpdateItemImageCanvasURI(ctx, imageID, canvasURI); err != nil {
			return SingleFileIngestResult{}, fmt.Errorf("commit single-file ingest: persist canvas URI: %w", err)
		}
	}
	page.WorkspaceID = commit.Item.WorkspaceID
	page.ItemImageID = imageID
	page.CanvasURI = canvasURI
	pageEntries, pageRow, err := prepareInitialAnnotationPage(page, commit.Image.Width, commit.Image.Height)
	if err != nil {
		return SingleFileIngestResult{}, fmt.Errorf("commit single-file ingest: %w", err)
	}
	if err := queries.CreateAnnotationPage(ctx, pageRow); err != nil {
		return SingleFileIngestResult{}, fmt.Errorf("commit single-file ingest: create canonical page: %w", err)
	}
	if err := queries.ReplaceAnnotationIndex(ctx, page.WorkspaceID, imageID, indexEntriesToRows(pageEntries)); err != nil {
		return SingleFileIngestResult{}, fmt.Errorf("commit single-file ingest: create annotation index: %w", err)
	}

	commit.OCRRun.ItemImageID = &imageID
	commit.OCRRun.ImageURL = commit.Image.ImageURL
	if err := insertCurrentOCRRun(ctx, queries, commit.OCRRun); err != nil {
		return SingleFileIngestResult{}, fmt.Errorf("commit single-file ingest: %w", err)
	}

	jobID := uint64(0)
	if commit.TranscriptionContext != nil {
		workspaceID, lockErr := lockTranscriptionAdmissionWorkspace(ctx, queries, imageID)
		if lockErr != nil {
			return SingleFileIngestResult{}, fmt.Errorf("commit single-file ingest: lock transcription admission: %w", lockErr)
		}
		if workspaceID != commit.Item.WorkspaceID {
			return SingleFileIngestResult{}, ErrAnnotationPageResource
		}
		if admissionErr := enforceTranscriptionJobAdmission(ctx, queries, workspaceID, s.admission); admissionErr != nil {
			return SingleFileIngestResult{}, admissionErr
		}
		lockedContext, contextSnapshot, contextErr := lockContextSnapshotForWorkspace(ctx, queries, commit.TranscriptionContext.ID, workspaceID)
		if contextErr != nil {
			return SingleFileIngestResult{}, fmt.Errorf("commit single-file ingest: lock context: %w", contextErr)
		}
		contextID := lockedContext.ID
		jobID, err = queries.CreateTranscriptionJob(ctx, db.CreateTranscriptionJobParams{
			ItemImageID:     imageID,
			ContextID:       &contextID,
			ContextSnapshot: contextSnapshot,
		})
		if err != nil {
			return SingleFileIngestResult{}, fmt.Errorf("commit single-file ingest: create transcription job: %w", err)
		}
	}

	if external := commit.ExternalRequest; external != nil {
		if err := queries.CompleteExternalRequest(ctx, db.CompleteExternalRequestManualParams{
			WorkspaceID:        commit.Item.WorkspaceID,
			Source:             strings.TrimSpace(external.Source),
			IdempotencyKey:     strings.TrimSpace(external.IdempotencyKey),
			LockedBy:           nullableString(external.LeaseOwner),
			ItemID:             nullableString(commit.Item.ID),
			ItemImageID:        nullableUint64(imageID),
			TranscriptionJobID: nullableUint64(jobID),
			SessionID:          nullableString(commit.OCRRun.SessionID),
		}); err != nil {
			return SingleFileIngestResult{}, fmt.Errorf("commit single-file ingest: complete external request: %w", err)
		}
	}
	itemRow, err := queries.GetItem(ctx, commit.Item.ID)
	if err != nil {
		return SingleFileIngestResult{}, fmt.Errorf("commit single-file ingest: reload item: %w", err)
	}
	imageRow, err := queries.GetItemImage(ctx, imageID)
	if err != nil {
		return SingleFileIngestResult{}, fmt.Errorf("commit single-file ingest: reload image: %w", err)
	}
	pageStored, err := queries.GetAnnotationPage(ctx, commit.Item.WorkspaceID, imageID)
	if err != nil {
		return SingleFileIngestResult{}, fmt.Errorf("commit single-file ingest: reload page: %w", err)
	}
	durableBytes, err := itemImageDurableDatabaseBytes(ctx, queries, commit.Item.WorkspaceID, imageID)
	if err != nil {
		return SingleFileIngestResult{}, fmt.Errorf("commit single-file ingest: measure durable database payload: %w", err)
	}
	if err := retireSingleFileIngestReservation(ctx, queries, commit.Reservation, lockedReservation, StorageQuotaRequest{
		DurableBytes: durableBytes,
		Items:        1,
		Images:       1,
	}, commit.Limits); err != nil {
		return SingleFileIngestResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return SingleFileIngestResult{}, fmt.Errorf("commit single-file ingest: commit: %w", err)
	}
	storedPage, err := annotationPageFromRow(pageStored, nil)
	if err != nil {
		return SingleFileIngestResult{}, err
	}
	item := rowToItem(itemRow)
	image := rowToItemImage(imageRow)
	item.Images = []ItemImage{image}
	return SingleFileIngestResult{Item: item, Image: image, Page: storedPage, TranscriptionJobID: jobID}, nil
}

func validateSingleFileIngestCommit(commit SingleFileIngestCommit) error {
	if strings.TrimSpace(commit.Item.ID) == "" || commit.Item.WorkspaceID == 0 || commit.Item.UserID == 0 {
		return fmt.Errorf("commit single-file ingest: item identity and ownership are required")
	}
	if strings.TrimSpace(commit.Image.ImageURL) == "" || strings.TrimSpace(commit.PublicBaseURL) == "" || commit.BuildPage == nil {
		return fmt.Errorf("commit single-file ingest: image URL, public base URL, and page factory are required")
	}
	if err := validateImageStorageReference(commit.Image.ImageURL, commit.Image.StorageBytes); err != nil {
		return fmt.Errorf("commit single-file ingest: %w", err)
	}
	if strings.TrimSpace(commit.OCRRun.SessionID) == "" || strings.TrimSpace(commit.OCRRun.OriginalHOCR) == "" {
		return fmt.Errorf("commit single-file ingest: OCR run identity and baseline are required")
	}
	if commit.TranscriptionContext != nil && commit.TranscriptionContext.ID == 0 {
		return fmt.Errorf("commit single-file ingest: persisted transcription context is required")
	}
	if commit.Reservation.ID == "" || commit.Reservation.WorkspaceID != commit.Item.WorkspaceID {
		return fmt.Errorf("commit single-file ingest: matching storage reservation is required")
	}
	if err := validateStorageQuotaLimits(commit.Limits); err != nil {
		return fmt.Errorf("commit single-file ingest: %w", err)
	}
	if external := commit.ExternalRequest; external != nil {
		if strings.TrimSpace(external.Source) == "" || strings.TrimSpace(external.IdempotencyKey) == "" || strings.TrimSpace(external.LeaseOwner) == "" {
			return fmt.Errorf("commit single-file ingest: external request fence is incomplete")
		}
	}
	return nil
}

func prepareInitialAnnotationPage(page AnnotationPage, imageWidth, imageHeight uint32) ([]AnnotationIndexEntry, db.AnnotationPage, error) {
	page.PageID = strings.TrimSpace(page.PageID)
	page.CanvasURI = strings.TrimSpace(page.CanvasURI)
	page.Payload = strings.TrimSpace(page.Payload)
	if page.PageID == "" || page.CanvasURI == "" || page.Payload == "" {
		return nil, db.AnnotationPage{}, fmt.Errorf("canonical page identity and payload are required")
	}
	identity, err := iiif.PageIdentityFromPageID(page.PageID, page.CanvasURI)
	if err != nil {
		return nil, db.AnnotationPage{}, err
	}
	if identity.ItemImageID != page.ItemImageID {
		return nil, db.AnnotationPage{}, fmt.Errorf("canonical page ID belongs to item image %d, want %d", identity.ItemImageID, page.ItemImageID)
	}
	if err := iiif.ValidateCanonicalAnnotationPage([]byte(page.Payload), identity); err != nil {
		return nil, db.AnnotationPage{}, err
	}
	if err := iiif.ValidateAnnotationPageGeometry([]byte(page.Payload), imageWidth, imageHeight); err != nil {
		return nil, db.AnnotationPage{}, err
	}
	entries, err := annotationIndexEntries(page)
	if err != nil {
		return nil, db.AnnotationPage{}, err
	}
	for _, entry := range entries {
		if entry.CanvasURI != page.CanvasURI {
			return nil, db.AnnotationPage{}, fmt.Errorf("annotation %q targets a different canvas", entry.ID)
		}
	}
	row, err := annotationPageToRow(page)
	return entries, row, err
}

func lockSingleFileIngestReservation(ctx context.Context, queries *db.Queries, commit SingleFileIngestCommit) (db.WorkspaceStorageReservation, error) {
	reservation, err := queries.LockLiveStorageQuotaReservation(ctx, db.LockLiveStorageQuotaReservationParams{
		ID: commit.Reservation.ID, WorkspaceID: commit.Item.WorkspaceID,
	})
	if err != nil {
		return db.WorkspaceStorageReservation{}, fmt.Errorf("commit single-file ingest: lock storage reservation: %w", err)
	}
	if reservation.ReservedItems < 1 || reservation.ReservedImages < 1 {
		return db.WorkspaceStorageReservation{}, fmt.Errorf("commit single-file ingest: storage reservation does not cover one item and image")
	}
	uploadName, isUpload := uploadref.ImmutableNameFromURL(commit.Image.ImageURL)
	if _, localButNonCanonical := uploadref.NameFromURL(commit.Image.ImageURL); localButNonCanonical && !isUpload {
		return db.WorkspaceStorageReservation{}, fmt.Errorf("commit single-file ingest: local upload identity is not immutable")
	}
	if isUpload {
		if !reservation.ResourceKey.Valid || reservation.ResourceKey.String != uploadName {
			return db.WorkspaceStorageReservation{}, fmt.Errorf("commit single-file ingest: upload staging identity does not match image")
		}
		cleanup, cleanupErr := queries.LockResourceCleanupByKindKey(ctx, db.LockResourceCleanupByKindKeyParams{
			Kind: db.ResourceCleanupOutboxKindUploadBlob, ResourceKey: uploadName,
		})
		if cleanupErr != nil {
			return db.WorkspaceStorageReservation{}, fmt.Errorf("commit single-file ingest: lock staged upload: %w", cleanupErr)
		}
		if cleanup.WorkspaceID != commit.Item.WorkspaceID || cleanup.StorageBytes != commit.Image.StorageBytes {
			return db.WorkspaceStorageReservation{}, fmt.Errorf("commit single-file ingest: staged upload accounting does not match image")
		}
		if cleanup.DeleteFencedAt.Valid {
			return db.WorkspaceStorageReservation{}, fmt.Errorf("commit single-file ingest: %w", ErrUploadBlobRetired)
		}
	} else if reservation.ResourceKey.Valid {
		return db.WorkspaceStorageReservation{}, fmt.Errorf("commit single-file ingest: external image reservation is bound to an upload")
	}
	return reservation, nil
}

func retireSingleFileIngestReservation(ctx context.Context, queries *db.Queries, reservation StorageQuotaReservation, locked db.WorkspaceStorageReservation, usedAddition StorageQuotaRequest, limits StorageQuotaLimits) error {
	globalRow, err := queries.LockStorageQuotaUsage(ctx, 0)
	if err != nil {
		return fmt.Errorf("commit single-file ingest: reload global quota usage: %w", err)
	}
	workspaceRow, err := queries.LockStorageQuotaUsage(ctx, reservation.WorkspaceID)
	if err != nil {
		return fmt.Errorf("commit single-file ingest: reload workspace quota usage: %w", err)
	}
	current := storageQuotaRequestFromReservation(locked)
	if err := checkStorageQuotaReplacement(
		storageQuotaUsageFromRow(globalRow), storageQuotaUsageFromRow(workspaceRow),
		current, StorageQuotaRequest{}, usedAddition, limits,
	); err != nil {
		return fmt.Errorf("commit single-file ingest: %w", err)
	}
	if err := replaceStorageQuotaReservationAccounting(ctx, queries, reservation.WorkspaceID, current, StorageQuotaRequest{}, usedAddition); err != nil {
		return fmt.Errorf("commit single-file ingest: account committed resources: %w", err)
	}
	if locked.ResourceKey.Valid {
		result, deleteErr := queries.DeleteResourceCleanupByKindKey(ctx, db.DeleteResourceCleanupByKindKeyParams{
			Kind: db.ResourceCleanupOutboxKindUploadBlob, ResourceKey: locked.ResourceKey.String,
		})
		if deleteErr := requireOneAffected(result, deleteErr); deleteErr != nil {
			return fmt.Errorf("commit single-file ingest: retire staged upload: %w", deleteErr)
		}
	}
	result, err := queries.DeleteStorageQuotaReservation(ctx, db.DeleteStorageQuotaReservationParams{
		ID: reservation.ID, WorkspaceID: reservation.WorkspaceID,
	})
	if err := requireOneAffected(result, err); err != nil {
		return fmt.Errorf("commit single-file ingest: retire storage reservation: %w", err)
	}
	return nil
}
