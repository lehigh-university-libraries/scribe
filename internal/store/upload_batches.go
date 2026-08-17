package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lehigh-university-libraries/scribe/internal/db"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/uploadref"
)

var (
	// ErrUploadBatchNotFound means the workspace-scoped batch does not exist.
	ErrUploadBatchNotFound = errors.New("upload batch not found")
	// ErrUploadBatchRequestMismatch means a batch ID was reused for another file set.
	ErrUploadBatchRequestMismatch = errors.New("upload batch id was reused for a different request")
	// ErrUploadBatchCanceled means no more files may be committed to the batch.
	ErrUploadBatchCanceled = errors.New("upload batch is canceled")
	// ErrUploadBatchCompleted means the completed batch is immutable.
	ErrUploadBatchCompleted = errors.New("upload batch is completed")
	// ErrUploadBatchFileMismatch means uploaded bytes do not match the declared file.
	ErrUploadBatchFileMismatch = errors.New("uploaded file does not match its batch declaration")
	// ErrUploadBatchFileInProgress means another request owns the active file lease.
	ErrUploadBatchFileInProgress = errors.New("upload batch file is already being processed")
	// ErrUploadBatchFileAttemptsExhausted means automatic file retry is no longer allowed.
	ErrUploadBatchFileAttemptsExhausted = errors.New("upload batch file attempts are exhausted")
	// ErrUploadBatchFileFence means an upload request no longer owns its file lease.
	ErrUploadBatchFileFence = errors.New("upload batch file lease was lost")
)

// UploadBatchStatus is the durable lifecycle of a multi-file ingest.
type UploadBatchStatus string

const (
	UploadBatchStatusInProgress UploadBatchStatus = "in_progress"
	UploadBatchStatusCompleted  UploadBatchStatus = "completed"
	UploadBatchStatusCanceled   UploadBatchStatus = "canceled"
)

// UploadBatchFileStatus is the durable lifecycle of one declared file.
type UploadBatchFileStatus string

const (
	UploadBatchFileStatusPending    UploadBatchFileStatus = "pending"
	UploadBatchFileStatusProcessing UploadBatchFileStatus = "processing"
	UploadBatchFileStatusCompleted  UploadBatchFileStatus = "completed"
	UploadBatchFileStatusFailed     UploadBatchFileStatus = "failed"
	UploadBatchFileStatusCanceled   UploadBatchFileStatus = "canceled"
	uploadBatchFileLeaseDuration                          = 30 * time.Minute
)

// UploadBatchFile is one immutable input declaration plus its processing state.
type UploadBatchFile struct {
	Sequence           uint32
	Filename           string
	Size               uint64
	ContentSHA256      string
	Status             UploadBatchFileStatus
	AttemptCount       uint32
	MaxAttempts        uint32
	ItemImageID        uint64
	TranscriptionJobID uint64
	ErrorMessage       string
	LeaseOwner         string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// UploadBatch is a workspace-scoped, resumable multi-file ingest.
type UploadBatch struct {
	WorkspaceID     uint64
	ID              string
	ItemID          string
	ContextID       uint64
	ContextSnapshot json.RawMessage
	RequestHash     string
	Status          UploadBatchStatus
	Files           []UploadBatchFile
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Context returns the immutable processing context selected when the batch started.
func (b UploadBatch) Context() (Context, error) {
	var processingContext Context
	if err := json.Unmarshal(b.ContextSnapshot, &processingContext); err != nil {
		return Context{}, fmt.Errorf("decode upload batch context: %w", err)
	}
	if processingContext.ID == 0 {
		return Context{}, fmt.Errorf("decode upload batch context: context id is required")
	}
	return processingContext, nil
}

// CompletedFiles returns the number of durably completed inputs.
func (b UploadBatch) CompletedFiles() uint32 {
	var completed uint32
	for _, file := range b.Files {
		if file.Status == UploadBatchFileStatusCompleted {
			completed++
		}
	}
	return completed
}

// FailedFiles returns the number of inputs currently awaiting an explicit retry.
func (b UploadBatch) FailedFiles() uint32 {
	var failed uint32
	for _, file := range b.Files {
		if file.Status == UploadBatchFileStatusFailed {
			failed++
		}
	}
	return failed
}

// File returns one sequence from the fixed batch declaration.
func (b UploadBatch) File(sequence uint32) (UploadBatchFile, bool) {
	for _, file := range b.Files {
		if file.Sequence == sequence {
			return file, true
		}
	}
	return UploadBatchFile{}, false
}

// UploadBatchFileInput declares a file before any expensive processing begins.
type UploadBatchFileInput struct {
	Filename      string
	Size          uint64
	ContentSHA256 string
}

// StartUploadBatchParams contains the immutable identity of a batch ingest.
type StartUploadBatchParams struct {
	WorkspaceID          uint64
	UserID               uint64
	BatchID              string
	ItemID               string
	Name                 string
	Metadata             string
	ExternalReferenceID  string
	CallerIdempotencyKey string
	Context              Context
	RequestHash          string
	Files                []UploadBatchFileInput
}

// StartUploadBatch creates an item and its complete file declaration atomically,
// or returns the existing batch when the ID and request hash match.
func (s *ItemStore) StartUploadBatch(ctx context.Context, params StartUploadBatchParams) (UploadBatch, error) {
	if s == nil || s.pool == nil {
		return UploadBatch{}, fmt.Errorf("start upload batch: store is not configured")
	}
	if params.WorkspaceID == 0 || params.UserID == 0 || strings.TrimSpace(params.BatchID) == "" || strings.TrimSpace(params.ItemID) == "" {
		return UploadBatch{}, fmt.Errorf("start upload batch: workspace, user, batch, and item are required")
	}
	if params.Context.ID == 0 {
		return UploadBatch{}, fmt.Errorf("start upload batch: context is required")
	}
	if len(params.Files) == 0 {
		return UploadBatch{}, fmt.Errorf("start upload batch: at least one file is required")
	}
	creationToken := "batch-" + uuid.NewString()
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return UploadBatch{}, fmt.Errorf("start upload batch: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.q.WithTx(tx)
	if err := lockStorageQuotaGuards(ctx, tx, params.WorkspaceID); err != nil {
		return UploadBatch{}, fmt.Errorf("start upload batch: lock storage usage: %w", err)
	}
	if _, err := queries.LockWorkspaceMemberRole(ctx, params.WorkspaceID, params.UserID); err != nil {
		return UploadBatch{}, fmt.Errorf("start upload batch: lock workspace membership: %w", err)
	}
	lockedContext, contextSnapshot, err := lockContextSnapshotForWorkspace(ctx, queries, params.Context.ID, params.WorkspaceID)
	if err != nil {
		return UploadBatch{}, fmt.Errorf("start upload batch: lock context: %w", err)
	}
	if err := queries.CreateItem(ctx, db.CreateItemParams{
		ID:                   params.ItemID,
		UserID:               params.UserID,
		WorkspaceID:          params.WorkspaceID,
		Name:                 strings.TrimSpace(params.Name),
		SourceType:           "upload",
		Metadata:             params.Metadata,
		ExternalReferenceID:  params.ExternalReferenceID,
		CallerIdempotencyKey: params.CallerIdempotencyKey,
	}); err != nil {
		return UploadBatch{}, fmt.Errorf("start upload batch: create item: %w", err)
	}
	contextID, err := nullableUint64Checked(lockedContext.ID)
	if err != nil {
		return UploadBatch{}, fmt.Errorf("start upload batch: %w", err)
	}
	if err := queries.InsertUploadBatchManual(ctx, db.InsertUploadBatchManualParams{
		WorkspaceID:     params.WorkspaceID,
		ID:              strings.TrimSpace(params.BatchID),
		ItemID:          params.ItemID,
		ContextID:       contextID,
		ContextSnapshot: contextSnapshot,
		RequestHash:     strings.TrimSpace(params.RequestHash),
		CreationToken:   creationToken,
	}); err != nil {
		return UploadBatch{}, fmt.Errorf("start upload batch: reserve batch: %w", err)
	}
	row, err := queries.LockUploadBatchManual(ctx, db.LockUploadBatchManualParams{
		WorkspaceID: params.WorkspaceID,
		ID:          strings.TrimSpace(params.BatchID),
	})
	if err != nil {
		return UploadBatch{}, fmt.Errorf("start upload batch: lock batch: %w", err)
	}
	if row.CreationToken != creationToken {
		if row.RequestHash != strings.TrimSpace(params.RequestHash) {
			return UploadBatch{}, ErrUploadBatchRequestMismatch
		}
		// The item inserted by this replay belongs only to the uncommitted
		// transaction and is discarded. Load the authoritative batch afterward.
		if err := tx.Rollback(); err != nil {
			return UploadBatch{}, fmt.Errorf("start upload batch: roll back replay item: %w", err)
		}
		return s.GetUploadBatch(ctx, params.WorkspaceID, params.BatchID)
	}
	for index, file := range params.Files {
		inserted, err := queries.InsertUploadBatchFileManual(ctx, db.InsertUploadBatchFileManualParams{
			WorkspaceID:   params.WorkspaceID,
			BatchID:       params.BatchID,
			Sequence:      uint32(index + 1), // #nosec G115 -- caller caps batches far below uint32.
			Filename:      strings.TrimSpace(file.Filename),
			Size:          file.Size,
			ContentSha256: strings.TrimSpace(file.ContentSHA256),
		})
		if err != nil {
			return UploadBatch{}, fmt.Errorf("start upload batch: declare file %d: %w", index+1, err)
		}
		if inserted != 1 {
			return UploadBatch{}, fmt.Errorf("start upload batch: declaration parent changed")
		}
	}
	if err := addStorageQuotaUsed(ctx, queries, params.WorkspaceID, StorageQuotaRequest{Items: 1}); err != nil {
		return UploadBatch{}, fmt.Errorf("start upload batch: account item: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return UploadBatch{}, fmt.Errorf("start upload batch: commit: %w", err)
	}
	return s.GetUploadBatch(ctx, params.WorkspaceID, params.BatchID)
}

// GetUploadBatch loads the batch and every declared file for one workspace.
func (s *ItemStore) GetUploadBatch(ctx context.Context, workspaceID uint64, batchID string) (UploadBatch, error) {
	if s == nil || s.q == nil {
		return UploadBatch{}, ErrUploadBatchNotFound
	}
	row, err := s.q.GetUploadBatchManual(ctx, db.GetUploadBatchManualParams{WorkspaceID: workspaceID, ID: strings.TrimSpace(batchID)})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return UploadBatch{}, ErrUploadBatchNotFound
		}
		return UploadBatch{}, fmt.Errorf("get upload batch: %w", err)
	}
	files, err := s.q.ListUploadBatchFilesManual(ctx, db.ListUploadBatchFilesManualParams{WorkspaceID: workspaceID, BatchID: row.ID})
	if err != nil {
		return UploadBatch{}, fmt.Errorf("get upload batch files: %w", err)
	}
	return uploadBatchFromDB(row, files), nil
}

// WorkspaceOwnsUploadBatch performs the side-effect-free ownership check used
// by route authorization without loading the batch's potentially large file
// list.
func (s *ItemStore) WorkspaceOwnsUploadBatch(ctx context.Context, workspaceID uint64, batchID string) (bool, error) {
	if s == nil || s.q == nil || workspaceID == 0 || strings.TrimSpace(batchID) == "" {
		return false, nil
	}
	_, err := s.q.GetUploadBatchManual(ctx, db.GetUploadBatchManualParams{
		WorkspaceID: workspaceID,
		ID:          strings.TrimSpace(batchID),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("authorize upload batch: %w", err)
	}
	return true, nil
}

// ClaimUploadBatchFile acquires a retryable processing lease, or returns the
// completed file without claiming it so callers can replay the prior response.
func (s *ItemStore) ClaimUploadBatchFile(ctx context.Context, workspaceID uint64, batchID string, sequence uint32, size uint64, contentSHA256 string) (UploadBatch, UploadBatchFile, bool, error) {
	if s == nil || s.pool == nil {
		return UploadBatch{}, UploadBatchFile{}, false, fmt.Errorf("claim upload batch file: store is not configured")
	}
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return UploadBatch{}, UploadBatchFile{}, false, fmt.Errorf("claim upload batch file: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockStorageQuotaGuards(ctx, tx, workspaceID); err != nil {
		return UploadBatch{}, UploadBatchFile{}, false, fmt.Errorf("claim upload batch file: lock quota guards: %w", err)
	}
	queries := s.q.WithTx(tx)
	batchRow, err := queries.LockUploadBatchManual(ctx, db.LockUploadBatchManualParams{WorkspaceID: workspaceID, ID: strings.TrimSpace(batchID)})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return UploadBatch{}, UploadBatchFile{}, false, ErrUploadBatchNotFound
		}
		return UploadBatch{}, UploadBatchFile{}, false, fmt.Errorf("claim upload batch file: lock batch: %w", err)
	}
	switch UploadBatchStatus(batchRow.Status) {
	case UploadBatchStatusCanceled:
		return UploadBatch{}, UploadBatchFile{}, false, ErrUploadBatchCanceled
	case UploadBatchStatusCompleted:
		// The requested file may be replayed below; an undeclared sequence is
		// still reported as not found rather than as a mutable completed batch.
	}
	fileRow, err := queries.LockUploadBatchFileManual(ctx, db.LockUploadBatchFileManualParams{WorkspaceID: workspaceID, BatchID: batchRow.ID, Sequence: sequence})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return UploadBatch{}, UploadBatchFile{}, false, ErrUploadBatchNotFound
		}
		return UploadBatch{}, UploadBatchFile{}, false, fmt.Errorf("claim upload batch file: lock file: %w", err)
	}
	if fileRow.Size != size || fileRow.ContentSha256 != strings.TrimSpace(contentSHA256) {
		return UploadBatch{}, UploadBatchFile{}, false, ErrUploadBatchFileMismatch
	}
	file := uploadBatchFileFromDB(fileRow)
	if file.Status == UploadBatchFileStatusCompleted {
		if err := tx.Commit(); err != nil {
			return UploadBatch{}, UploadBatchFile{}, false, fmt.Errorf("claim upload batch file: commit replay: %w", err)
		}
		batch, loadErr := s.GetUploadBatch(ctx, workspaceID, batchID)
		return batch, file, false, loadErr
	}
	if UploadBatchStatus(batchRow.Status) == UploadBatchStatusCompleted {
		return UploadBatch{}, UploadBatchFile{}, false, ErrUploadBatchCompleted
	}
	if file.AttemptCount >= file.MaxAttempts {
		return UploadBatch{}, UploadBatchFile{}, false, ErrUploadBatchFileAttemptsExhausted
	}
	if file.Status == UploadBatchFileStatusProcessing && fileRow.LeaseUntil.Valid && time.Now().UTC().Before(fileRow.LeaseUntil.Time) {
		return UploadBatch{}, UploadBatchFile{}, false, ErrUploadBatchFileInProgress
	}
	// Any image attached to a non-completed declaration is provisional. Remove
	// it before changing owners so a reclaimed attempt cannot inherit partial
	// OCR, annotations, or a job written by the expired attempt.
	cleanupRequest, err := cleanupUploadBatchFileImage(ctx, queries, workspaceID, batchRow.ID, sequence)
	if err != nil {
		return UploadBatch{}, UploadBatchFile{}, false, fmt.Errorf("claim upload batch file: clean prior attempt: %w", err)
	}
	leaseOwner := "upload-" + uuid.NewString()
	updated, err := queries.ClaimUploadBatchFileManual(ctx, db.ClaimUploadBatchFileManualParams{
		LeaseUntil:  sql.NullTime{Time: time.Now().UTC().Add(uploadBatchFileLeaseDuration), Valid: true},
		LockedBy:    nullableString(leaseOwner),
		WorkspaceID: workspaceID,
		BatchID:     batchRow.ID,
		Sequence:    sequence,
	})
	if err != nil {
		return UploadBatch{}, UploadBatchFile{}, false, fmt.Errorf("claim upload batch file: update lease: %w", err)
	}
	if updated != 1 {
		return UploadBatch{}, UploadBatchFile{}, false, ErrUploadBatchFileFence
	}
	if err := subtractStorageQuotaUsed(ctx, queries, workspaceID, cleanupRequest); err != nil {
		return UploadBatch{}, UploadBatchFile{}, false, fmt.Errorf("claim upload batch file: account prior attempt cleanup: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return UploadBatch{}, UploadBatchFile{}, false, fmt.Errorf("claim upload batch file: commit: %w", err)
	}
	batch, err := s.GetUploadBatch(ctx, workspaceID, batchID)
	if err != nil {
		return UploadBatch{}, UploadBatchFile{}, false, err
	}
	claimed, ok := batch.File(sequence)
	if !ok {
		return UploadBatch{}, UploadBatchFile{}, false, ErrUploadBatchNotFound
	}
	claimed.LeaseOwner = leaseOwner
	return batch, claimed, true, nil
}

// RenewUploadBatchFileLease extends an unexpired lease while the same attempt
// still owns an in-progress batch file. An expired lease cannot be resurrected.
func (s *ItemStore) RenewUploadBatchFileLease(ctx context.Context, workspaceID uint64, batchID string, sequence uint32, leaseOwner string) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("renew upload batch file lease: store is not configured")
	}
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("renew upload batch file lease: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.q.WithTx(tx)
	if _, _, err := lockActiveUploadBatchFileAttempt(ctx, queries, workspaceID, batchID, sequence, leaseOwner); err != nil {
		return err
	}
	updated, err := queries.RenewUploadBatchFileManual(ctx, db.RenewUploadBatchFileManualParams{
		LeaseUntil:  sql.NullTime{Time: time.Now().UTC().Add(uploadBatchFileLeaseDuration), Valid: true},
		WorkspaceID: workspaceID,
		BatchID:     strings.TrimSpace(batchID),
		Sequence:    sequence,
		LockedBy:    nullableString(strings.TrimSpace(leaseOwner)),
	})
	if err != nil {
		return fmt.Errorf("renew upload batch file lease: %w", err)
	}
	if updated != 1 {
		return ErrUploadBatchFileFence
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("renew upload batch file lease: commit: %w", err)
	}
	return nil
}

// AbortUploadBatchFileAttempt removes provisional relational and external
// resources and records a retryable failure in one transaction. Cleanup is a
// no-op with ErrUploadBatchFileFence once another attempt owns or completes the
// file, so a stale request can never delete its successor's image.
func (s *ItemStore) AbortUploadBatchFileAttempt(ctx context.Context, workspaceID uint64, batchID string, sequence uint32, leaseOwner, publicError string) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("abort upload batch file attempt: store is not configured")
	}
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("abort upload batch file attempt: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockStorageQuotaGuards(ctx, tx, workspaceID); err != nil {
		return fmt.Errorf("abort upload batch file attempt: lock quota guards: %w", err)
	}
	queries := s.q.WithTx(tx)
	batchRow, err := queries.LockUploadBatchManual(ctx, db.LockUploadBatchManualParams{
		WorkspaceID: workspaceID,
		ID:          strings.TrimSpace(batchID),
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUploadBatchNotFound
		}
		return fmt.Errorf("abort upload batch file attempt: lock batch: %w", err)
	}
	fileRow, err := queries.LockUploadBatchFileManual(ctx, db.LockUploadBatchFileManualParams{
		WorkspaceID: workspaceID,
		BatchID:     batchRow.ID,
		Sequence:    sequence,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUploadBatchNotFound
		}
		return fmt.Errorf("abort upload batch file attempt: lock file: %w", err)
	}
	if UploadBatchStatus(batchRow.Status) != UploadBatchStatusInProgress ||
		UploadBatchFileStatus(fileRow.Status) != UploadBatchFileStatusProcessing ||
		!fileRow.LockedBy.Valid || fileRow.LockedBy.String != strings.TrimSpace(leaseOwner) {
		return ErrUploadBatchFileFence
	}
	cleanupRequest, err := cleanupUploadBatchFileImage(ctx, queries, workspaceID, batchRow.ID, sequence)
	if err != nil {
		return fmt.Errorf("abort upload batch file attempt: clean provisional image: %w", err)
	}
	updated, err := queries.FailUploadBatchFileManual(ctx, db.FailUploadBatchFileManualParams{
		ErrorMessage: nullableString(strings.TrimSpace(publicError)),
		WorkspaceID:  workspaceID,
		BatchID:      batchRow.ID,
		Sequence:     sequence,
		LockedBy:     nullableString(strings.TrimSpace(leaseOwner)),
	})
	if err != nil {
		return fmt.Errorf("abort upload batch file attempt: mark failed: %w", err)
	}
	if updated != 1 {
		return ErrUploadBatchFileFence
	}
	if err := subtractStorageQuotaUsed(ctx, queries, workspaceID, cleanupRequest); err != nil {
		return fmt.Errorf("abort upload batch file attempt: account provisional image cleanup: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("abort upload batch file attempt: commit: %w", err)
	}
	return nil
}

// EnsureUploadBatchImage creates the provisional item image while atomically
// checking the active file lease and transferring its admitted image capacity
// from reserved to used. Reclaim removes any prior attempt's image before
// assigning the new owner.
func (s *ItemStore) EnsureUploadBatchImage(ctx context.Context, workspaceID uint64, reservation StorageQuotaReservation, batchID string, sequence uint32, leaseOwner, imageURL string, storageBytes uint64, width, height uint32, publicBaseURL string) (ItemImage, error) {
	if s == nil || s.pool == nil {
		return ItemImage{}, fmt.Errorf("ensure upload batch image: store is not configured")
	}
	if reservation.ID == "" || reservation.WorkspaceID != workspaceID {
		return ItemImage{}, fmt.Errorf("ensure upload batch image: matching storage reservation is required")
	}
	if width == 0 || height == 0 {
		return ItemImage{}, fmt.Errorf("ensure upload batch image: image dimensions are required")
	}
	if err := validateImageStorageReference(imageURL, storageBytes); err != nil {
		return ItemImage{}, fmt.Errorf("ensure upload batch image: %w", err)
	}
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return ItemImage{}, fmt.Errorf("ensure upload batch image: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.q.WithTx(tx)
	if err := lockStorageQuotaGuards(ctx, tx, workspaceID); err != nil {
		return ItemImage{}, fmt.Errorf("ensure upload batch image: lock storage usage: %w", err)
	}
	batchRow, _, err := lockActiveUploadBatchFileAttempt(ctx, queries, workspaceID, batchID, sequence, leaseOwner)
	if err != nil {
		return ItemImage{}, err
	}
	if _, err := queries.LockItemForUseManual(ctx, db.LockItemForUseManualParams{
		ID:          batchRow.ItemID,
		WorkspaceID: workspaceID,
	}); err != nil {
		return ItemImage{}, fmt.Errorf("ensure upload batch image: lock item: %w", err)
	}
	uploadBytes := uint64(0)
	stagedUploadName := ""
	if name, isUpload := uploadref.ImmutableNameFromURL(imageURL); isUpload {
		cleanup, staged, cleanupErr := lockUploadCleanupForReference(ctx, queries, workspaceID, name)
		if cleanupErr != nil {
			return ItemImage{}, fmt.Errorf("ensure upload batch image: inspect staged upload: %w", cleanupErr)
		}
		if staged {
			if cleanup.StorageBytes != storageBytes {
				return ItemImage{}, fmt.Errorf("ensure upload batch image: staged upload accounting does not match image")
			}
			stagedUploadName = name
		} else {
			uploadBytes = storageBytes
		}
	}
	lockedReservation, err := queries.LockLiveStorageQuotaReservation(ctx, db.LockLiveStorageQuotaReservationParams{
		ID: reservation.ID, WorkspaceID: workspaceID,
	})
	if err != nil {
		return ItemImage{}, fmt.Errorf("ensure upload batch image: lock storage reservation: %w", err)
	}
	if stagedUploadName != "" {
		if !lockedReservation.ResourceKey.Valid || lockedReservation.ResourceKey.String != stagedUploadName {
			return ItemImage{}, fmt.Errorf("ensure upload batch image: staged upload reservation does not match image")
		}
	} else if lockedReservation.ResourceKey.Valid {
		return ItemImage{}, fmt.Errorf("ensure upload batch image: storage reservation is bound to another upload")
	}
	result, err := queries.EnsureUploadBatchImageManual(ctx, db.EnsureUploadBatchImageManualParams{
		ImageUrl:     strings.TrimSpace(imageURL),
		StorageBytes: storageBytes,
		Width:        sql.NullInt32{Int32: int32(width), Valid: true},  // #nosec G115 -- upload dimensions are bounded well below MaxInt32.
		Height:       sql.NullInt32{Int32: int32(height), Valid: true}, // #nosec G115 -- upload dimensions are bounded well below MaxInt32.
		WorkspaceID:  workspaceID,
		BatchID:      strings.TrimSpace(batchID),
		Sequence:     sequence,
		LockedBy:     nullableString(strings.TrimSpace(leaseOwner)),
	})
	if err != nil {
		return ItemImage{}, fmt.Errorf("ensure upload batch image: %w", err)
	}
	imageID, err := result.LastInsertId()
	if err != nil {
		return ItemImage{}, fmt.Errorf("ensure upload batch image: load id: %w", err)
	}
	if imageID <= 0 {
		return ItemImage{}, ErrUploadBatchFileFence
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return ItemImage{}, fmt.Errorf("ensure upload batch image: inspect insert result: %w", err)
	}
	if rowsAffected == 1 {
		if err := transferStorageQuotaReservationToUsed(ctx, queries, lockedReservation, StorageQuotaRequest{Bytes: uploadBytes, Images: 1}); err != nil {
			return ItemImage{}, fmt.Errorf("ensure upload batch image: account image: %w", err)
		}
	}
	canvasURI, err := iiif.ItemImageCanvasID(publicBaseURL, uint64(imageID)) // #nosec G115 -- positive database identifier.
	if err != nil {
		return ItemImage{}, fmt.Errorf("ensure upload batch image: construct Canvas identity: %w", err)
	}
	if err := queries.UpdateItemImageCanvasURI(ctx, uint64(imageID), canvasURI); err != nil { // #nosec G115 -- positive database identifier.
		return ItemImage{}, fmt.Errorf("ensure upload batch image: persist Canvas identity: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ItemImage{}, fmt.Errorf("ensure upload batch image: commit: %w", err)
	}
	return s.GetImageForWorkspace(ctx, uint64(imageID), workspaceID) // #nosec G115 -- positive database identifier.
}

// CompleteUploadBatchFile fences the result against cancellation and marks the
// batch completed when every declared input has committed successfully.
func (s *ItemStore) CompleteUploadBatchFile(ctx context.Context, workspaceID uint64, batchID string, sequence uint32, leaseOwner string, itemImageID, transcriptionJobID uint64) (UploadBatch, error) {
	if itemImageID == 0 || transcriptionJobID == 0 {
		return UploadBatch{}, ErrUploadBatchFileFence
	}
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return UploadBatch{}, fmt.Errorf("complete upload batch file: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.q.WithTx(tx)
	if _, _, err := lockActiveUploadBatchFileAttempt(ctx, queries, workspaceID, batchID, sequence, leaseOwner); err != nil {
		return UploadBatch{}, err
	}
	if _, err := queries.LockUploadBatchCompletionImageManual(ctx, db.LockUploadBatchCompletionImageManualParams{
		WorkspaceID: workspaceID,
		BatchID:     strings.TrimSpace(batchID),
		ItemImageID: itemImageID,
	}); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return UploadBatch{}, ErrUploadBatchFileFence
		}
		return UploadBatch{}, fmt.Errorf("complete upload batch file: lock image: %w", err)
	}
	if _, err := queries.LockUploadBatchCompletionJobManual(ctx, db.LockUploadBatchCompletionJobManualParams{
		WorkspaceID:        workspaceID,
		BatchID:            strings.TrimSpace(batchID),
		ItemImageID:        itemImageID,
		TranscriptionJobID: transcriptionJobID,
	}); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return UploadBatch{}, ErrUploadBatchFileFence
		}
		return UploadBatch{}, fmt.Errorf("complete upload batch file: lock transcription job: %w", err)
	}
	itemImage, err := nullableUint64Checked(itemImageID)
	if err != nil {
		return UploadBatch{}, err
	}
	job, err := nullableUint64Checked(transcriptionJobID)
	if err != nil {
		return UploadBatch{}, err
	}
	updated, err := queries.CompleteUploadBatchFileManual(ctx, db.CompleteUploadBatchFileManualParams{
		ItemImageID:        itemImage,
		TranscriptionJobID: job,
		WorkspaceID:        workspaceID,
		BatchID:            strings.TrimSpace(batchID),
		Sequence:           sequence,
		LockedBy:           nullableString(strings.TrimSpace(leaseOwner)),
	})
	if err != nil {
		return UploadBatch{}, fmt.Errorf("complete upload batch file: update file: %w", err)
	}
	if updated != 1 {
		return UploadBatch{}, ErrUploadBatchFileFence
	}
	if _, err := queries.CompleteUploadBatchIfReadyManual(ctx, db.CompleteUploadBatchIfReadyManualParams{WorkspaceID: workspaceID, BatchID: strings.TrimSpace(batchID)}); err != nil {
		return UploadBatch{}, fmt.Errorf("complete upload batch file: update batch: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return UploadBatch{}, fmt.Errorf("complete upload batch file: commit: %w", err)
	}
	return s.GetUploadBatch(ctx, workspaceID, batchID)
}

// CancelUploadBatch atomically fences active file requests and cancels all
// pending or running transcription jobs already committed by the batch.
func (s *ItemStore) CancelUploadBatch(ctx context.Context, workspaceID uint64, batchID string) (UploadBatch, error) {
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return UploadBatch{}, fmt.Errorf("cancel upload batch: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockStorageQuotaGuards(ctx, tx, workspaceID); err != nil {
		return UploadBatch{}, fmt.Errorf("cancel upload batch: lock quota guards: %w", err)
	}
	queries := s.q.WithTx(tx)
	row, err := queries.LockUploadBatchManual(ctx, db.LockUploadBatchManualParams{WorkspaceID: workspaceID, ID: strings.TrimSpace(batchID)})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return UploadBatch{}, ErrUploadBatchNotFound
		}
		return UploadBatch{}, fmt.Errorf("cancel upload batch: lock: %w", err)
	}
	switch UploadBatchStatus(row.Status) {
	case UploadBatchStatusCompleted:
		return UploadBatch{}, ErrUploadBatchCompleted
	case UploadBatchStatusCanceled:
		// Idempotent replay: the first cancel already fenced every side effect.
	default:
		if updated, err := queries.CancelUploadBatchManual(ctx, db.CancelUploadBatchManualParams{WorkspaceID: workspaceID, ID: row.ID}); err != nil {
			return UploadBatch{}, fmt.Errorf("cancel upload batch: update batch: %w", err)
		} else if updated != 1 {
			return UploadBatch{}, ErrUploadBatchFileFence
		}
		if err := queries.CancelUploadBatchJobsManual(ctx, db.CancelUploadBatchJobsManualParams{WorkspaceID: workspaceID, BatchID: row.ID}); err != nil {
			return UploadBatch{}, fmt.Errorf("cancel upload batch: cancel jobs: %w", err)
		}
		incompleteImages, err := queries.ListIncompleteUploadBatchImagesForCleanupManual(ctx, db.ListIncompleteUploadBatchImagesForCleanupManualParams{WorkspaceID: workspaceID, BatchID: row.ID})
		if err != nil {
			return UploadBatch{}, fmt.Errorf("cancel upload batch: lock incomplete images: %w", err)
		}
		var durableBytes uint64
		for _, image := range incompleteImages {
			imageDurableBytes, measureErr := itemImageDurableDatabaseBytes(ctx, queries, workspaceID, image.ID)
			if measureErr != nil {
				return UploadBatch{}, fmt.Errorf("cancel upload batch: measure incomplete image storage: %w", measureErr)
			}
			var ok bool
			durableBytes, ok = addUint64(durableBytes, imageDurableBytes)
			if !ok {
				return UploadBatch{}, fmt.Errorf("cancel upload batch: incomplete image storage overflows quota accounting")
			}
			if err := enqueueImageCleanup(ctx, queries, image.ID, image.ImageUrl, workspaceID, image.StorageBytes, time.Now().UTC()); err != nil {
				return UploadBatch{}, fmt.Errorf("cancel upload batch: enqueue image cleanup: %w", err)
			}
			cleared, err := queries.ClearUploadBatchFileResourcesManual(ctx, db.ClearUploadBatchFileResourcesManualParams{
				WorkspaceID: workspaceID,
				BatchID:     row.ID,
				Sequence:    image.Sequence,
			})
			if err != nil {
				return UploadBatch{}, fmt.Errorf("cancel upload batch: detach incomplete file resources: %w", err)
			}
			if cleared != 1 {
				return UploadBatch{}, ErrUploadBatchFileFence
			}
			if err := deleteItemResourceGraph(ctx, queries, workspaceID, image.ItemID, image.ID); err != nil {
				return UploadBatch{}, fmt.Errorf("cancel upload batch: delete incomplete image graph: %w", err)
			}
		}
		if err := queries.CancelUploadBatchFilesManual(ctx, db.CancelUploadBatchFilesManualParams{WorkspaceID: workspaceID, BatchID: row.ID}); err != nil {
			return UploadBatch{}, fmt.Errorf("cancel upload batch: cancel files: %w", err)
		}
		if len(incompleteImages) > 0 {
			if err := subtractStorageQuotaUsed(ctx, queries, workspaceID, StorageQuotaRequest{
				DurableBytes: durableBytes,
				Images:       uint64(len(incompleteImages)),
			}); err != nil {
				return UploadBatch{}, fmt.Errorf("cancel upload batch: account incomplete image deletion: %w", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return UploadBatch{}, fmt.Errorf("cancel upload batch: commit: %w", err)
	}
	return s.GetUploadBatch(ctx, workspaceID, batchID)
}

func uploadBatchFromDB(row db.UploadBatch, fileRows []db.UploadBatchFile) UploadBatch {
	batch := UploadBatch{
		WorkspaceID:     row.WorkspaceID,
		ID:              row.ID,
		ItemID:          row.ItemID,
		ContextSnapshot: append(json.RawMessage(nil), row.ContextSnapshot...),
		RequestHash:     row.RequestHash,
		Status:          UploadBatchStatus(row.Status),
		Files:           make([]UploadBatchFile, 0, len(fileRows)),
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	}
	if row.ContextID.Valid && row.ContextID.Int64 > 0 {
		batch.ContextID = uint64(row.ContextID.Int64)
	}
	for _, file := range fileRows {
		batch.Files = append(batch.Files, uploadBatchFileFromDB(file))
	}
	return batch
}

func uploadBatchFileFromDB(row db.UploadBatchFile) UploadBatchFile {
	file := UploadBatchFile{
		Sequence:      row.Sequence,
		Filename:      row.Filename,
		Size:          row.Size,
		ContentSHA256: row.ContentSha256,
		Status:        UploadBatchFileStatus(row.Status),
		AttemptCount:  row.AttemptCount,
		MaxAttempts:   row.MaxAttempts,
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
	}
	if row.ItemImageID.Valid && row.ItemImageID.Int64 > 0 {
		file.ItemImageID = uint64(row.ItemImageID.Int64)
	}
	if row.TranscriptionJobID.Valid && row.TranscriptionJobID.Int64 > 0 {
		file.TranscriptionJobID = uint64(row.TranscriptionJobID.Int64)
	}
	if row.ErrorMessage.Valid {
		file.ErrorMessage = row.ErrorMessage.String
	}
	if row.LockedBy.Valid {
		file.LeaseOwner = row.LockedBy.String
	}
	return file
}

func lockActiveUploadBatchFileAttempt(ctx context.Context, queries *db.Queries, workspaceID uint64, batchID string, sequence uint32, leaseOwner string) (db.UploadBatch, db.UploadBatchFile, error) {
	batchRow, err := queries.LockUploadBatchManual(ctx, db.LockUploadBatchManualParams{
		WorkspaceID: workspaceID,
		ID:          strings.TrimSpace(batchID),
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return db.UploadBatch{}, db.UploadBatchFile{}, ErrUploadBatchNotFound
		}
		return db.UploadBatch{}, db.UploadBatchFile{}, fmt.Errorf("lock upload batch attempt: batch: %w", err)
	}
	fileRow, err := queries.LockUploadBatchFileManual(ctx, db.LockUploadBatchFileManualParams{
		WorkspaceID: workspaceID,
		BatchID:     batchRow.ID,
		Sequence:    sequence,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return db.UploadBatch{}, db.UploadBatchFile{}, ErrUploadBatchNotFound
		}
		return db.UploadBatch{}, db.UploadBatchFile{}, fmt.Errorf("lock upload batch attempt: file: %w", err)
	}
	owner := strings.TrimSpace(leaseOwner)
	if UploadBatchStatus(batchRow.Status) != UploadBatchStatusInProgress ||
		UploadBatchFileStatus(fileRow.Status) != UploadBatchFileStatusProcessing ||
		owner == "" || !fileRow.LockedBy.Valid || fileRow.LockedBy.String != owner ||
		!fileRow.LeaseUntil.Valid || !time.Now().UTC().Before(fileRow.LeaseUntil.Time) {
		return db.UploadBatch{}, db.UploadBatchFile{}, ErrUploadBatchFileFence
	}
	return batchRow, fileRow, nil
}

func cleanupUploadBatchFileImage(ctx context.Context, queries *db.Queries, workspaceID uint64, batchID string, sequence uint32) (StorageQuotaRequest, error) {
	image, err := queries.LockUploadBatchFileImageForCleanupManual(ctx, db.LockUploadBatchFileImageForCleanupManualParams{
		WorkspaceID: workspaceID,
		BatchID:     strings.TrimSpace(batchID),
		Sequence:    sequence,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return StorageQuotaRequest{}, nil
		}
		return StorageQuotaRequest{}, err
	}
	if err := enqueueImageCleanup(ctx, queries, image.ID, image.ImageUrl, workspaceID, image.StorageBytes, time.Now().UTC()); err != nil {
		return StorageQuotaRequest{}, err
	}
	cleared, err := queries.ClearUploadBatchFileResourcesManual(ctx, db.ClearUploadBatchFileResourcesManualParams{
		WorkspaceID: workspaceID,
		BatchID:     strings.TrimSpace(batchID),
		Sequence:    sequence,
	})
	if err != nil {
		return StorageQuotaRequest{}, err
	}
	if cleared != 1 {
		return StorageQuotaRequest{}, ErrUploadBatchFileFence
	}
	durableBytes, err := itemImageDurableDatabaseBytes(ctx, queries, workspaceID, image.ID)
	if err != nil {
		return StorageQuotaRequest{}, err
	}
	if err := deleteItemResourceGraph(ctx, queries, workspaceID, image.ItemID, image.ID); err != nil {
		return StorageQuotaRequest{}, err
	}
	return StorageQuotaRequest{DurableBytes: durableBytes, Images: 1}, nil
}

func nullableUint64Checked(value uint64) (sql.NullInt64, error) {
	if value == 0 {
		return sql.NullInt64{}, nil
	}
	if value > uint64(^uint64(0)>>1) {
		return sql.NullInt64{}, fmt.Errorf("value exceeds signed database range")
	}
	return sql.NullInt64{Int64: int64(value), Valid: true}, nil // #nosec G115 -- range checked above.
}
