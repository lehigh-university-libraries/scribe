package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	db "github.com/lehigh-university-libraries/scribe/internal/db"
)

type TranscriptionJobStatus string

const (
	TranscriptionJobStatusPending    TranscriptionJobStatus = "pending"
	TranscriptionJobStatusRunning    TranscriptionJobStatus = "running"
	TranscriptionJobStatusCompleted  TranscriptionJobStatus = "completed"
	TranscriptionJobStatusFailed     TranscriptionJobStatus = "failed"
	TranscriptionJobStatusCanceled   TranscriptionJobStatus = "canceled"
	TranscriptionJobStatusSuperseded TranscriptionJobStatus = "superseded"
)

var (
	// ErrExternalRequestMismatch means an idempotency key was reused for a
	// semantically different operation.
	ErrExternalRequestMismatch = errors.New("idempotency key was reused for a different request")
	// ErrTranscriptionCanonicalPageRequired means a job was requested before
	// its canonical AnnotationPage had been initialized.
	ErrTranscriptionCanonicalPageRequired = errors.New("canonical annotation page is required before creating a transcription job")
	// ErrTranscriptionJobFence means a worker transition did not match the
	// current attempt, input revision, lease token, or unexpired lease.
	ErrTranscriptionJobFence = errors.New("transcription job attempt fence rejected")
	// ErrInvalidTranscriptionJobPage identifies an invalid bounded list request.
	ErrInvalidTranscriptionJobPage = errors.New("invalid transcription job page")
)

const (
	// DefaultTranscriptionJobPageSize is used when the API omits page_size.
	DefaultTranscriptionJobPageSize uint32 = 50
	// MaxTranscriptionJobPageSize is the hard API and repository page bound.
	MaxTranscriptionJobPageSize uint32 = 100
	// eventRetentionBatchSize must match the LIMIT in event_outbox.sql. Keeping
	// each delete transaction small prevents retention from monopolizing rows
	// while event producers and webhook workers are active.
	eventRetentionBatchSize int64 = 1000
)

// TranscriptionAttemptOutcome is the immutable terminal classification of one
// worker claim. Running is the only non-terminal value.
type TranscriptionAttemptOutcome string

const (
	TranscriptionAttemptRunning         TranscriptionAttemptOutcome = "running"
	TranscriptionAttemptCompleted       TranscriptionAttemptOutcome = "completed"
	TranscriptionAttemptRetryableFailed TranscriptionAttemptOutcome = "retryable_failed"
	TranscriptionAttemptFailed          TranscriptionAttemptOutcome = "failed"
	TranscriptionAttemptCanceled        TranscriptionAttemptOutcome = "canceled"
	TranscriptionAttemptSuperseded      TranscriptionAttemptOutcome = "superseded"
	TranscriptionAttemptLeaseExpired    TranscriptionAttemptOutcome = "lease_expired"
)

// TranscriptionAttemptFence addresses exactly one worker claim. It is required
// for every worker-owned mutation and includes the canonical input revision so
// a valid token cannot be replayed against different source state.
type TranscriptionAttemptFence struct {
	JobID         uint64
	AttemptNumber uint32
	InputRevision uint64
	LeaseToken    string
}

// TranscriptionJobAttempt is one auditable worker claim. ContextSnapshot and
// the fencing identity are write-once; outcome fields transition exactly once.
type TranscriptionJobAttempt struct {
	JobID            uint64
	AttemptNumber    uint32
	ContextSnapshot  json.RawMessage
	InputRevision    uint64
	LeaseOwner       string
	Outcome          TranscriptionAttemptOutcome
	SafeErrorMessage string
	ResultRevision   *uint64
	StartedAt        time.Time
	FinishedAt       *time.Time
}

type TranscriptionJob struct {
	ID                       uint64                    `json:"id"`
	WorkspaceID              uint64                    `json:"workspace_id"`
	ItemImageID              uint64                    `json:"item_image_id"`
	ContextID                *uint64                   `json:"context_id,omitempty"`
	ContextSnapshot          json.RawMessage           `json:"-"`
	InputRevision            uint64                    `json:"input_revision"`
	Status                   TranscriptionJobStatus    `json:"status"`
	TotalSegments            int                       `json:"total_segments"`
	CompletedSegments        int                       `json:"completed_segments"`
	FailedSegments           int                       `json:"failed_segments"`
	AttemptCount             int                       `json:"attempt_count"`
	MaxAttempts              int                       `json:"max_attempts"`
	LeaseToken               string                    `json:"-"`
	CurrentAnnotationID      string                    `json:"current_annotation_id,omitempty"`
	CurrentAnnotationJSON    string                    `json:"current_annotation_json,omitempty"`
	LastResultAnnotationJSON string                    `json:"last_result_annotation_json,omitempty"`
	ErrorMessage             string                    `json:"error_message,omitempty"`
	CreatedAt                time.Time                 `json:"created_at"`
	UpdatedAt                time.Time                 `json:"updated_at"`
	Attempts                 []TranscriptionJobAttempt `json:"attempts,omitempty"`
}

// TranscriptionJobPageCursor is the exclusive keyset boundary for a job page.
type TranscriptionJobPageCursor struct {
	CreatedAt time.Time
	ID        uint64
}

// TranscriptionJobSummary is the scalar-only list model. Full context,
// annotation progress payloads, lease state, and attempt history are available
// only through the point Get operation.
type TranscriptionJobSummary struct {
	ID                  uint64
	WorkspaceID         uint64
	ItemImageID         uint64
	ContextID           *uint64
	InputRevision       uint64
	Status              TranscriptionJobStatus
	TotalSegments       int
	CompletedSegments   int
	FailedSegments      int
	AttemptCount        int
	MaxAttempts         int
	CurrentAnnotationID string
	ErrorMessage        string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// TranscriptionJobPage contains bounded job summaries. Attempt history is
// deliberately loaded only by Get, avoiding an N+1 query on list operations.
type TranscriptionJobPage struct {
	Jobs       []TranscriptionJobSummary
	NextCursor *TranscriptionJobPageCursor
}

// TranscriptionQueueSnapshot is a deployment-wide, content-free view of work
// that a worker can claim now. It deliberately has no workspace, job, provider,
// or model dimensions so exported telemetry remains low-cardinality.
type TranscriptionQueueSnapshot struct {
	Depth         int64
	OldestAge     time.Duration
	ExpiredLeases int64
}

// Fence returns the complete identity required for a worker transition.
func (j TranscriptionJob) Fence() (TranscriptionAttemptFence, error) {
	attemptNumber, err := attemptNumberFromInt(j.AttemptCount)
	if err != nil {
		return TranscriptionAttemptFence{}, ErrTranscriptionJobFence
	}
	fence := TranscriptionAttemptFence{
		JobID:         j.ID,
		AttemptNumber: attemptNumber,
		InputRevision: j.InputRevision,
		LeaseToken:    strings.TrimSpace(j.LeaseToken),
	}
	if fence.JobID == 0 || fence.AttemptNumber == 0 || fence.InputRevision == 0 || fence.LeaseToken == "" {
		return TranscriptionAttemptFence{}, ErrTranscriptionJobFence
	}
	return fence, nil
}

type TranscriptionJobStore struct {
	q         *db.Queries
	pool      *sql.DB
	admission TranscriptionJobAdmissionPolicy
}

type ExternalRequestStatus string

const (
	ExternalRequestStatusInProgress ExternalRequestStatus = "in_progress"
	ExternalRequestStatusCompleted  ExternalRequestStatus = "completed"
	ExternalRequestStatusFailed     ExternalRequestStatus = "failed"
)

type ExternalRequest struct {
	ID                 uint64
	WorkspaceID        uint64
	Source             string
	IdempotencyKey     string
	RequestHash        string
	Status             ExternalRequestStatus
	ItemID             string
	ItemImageID        uint64
	TranscriptionJobID uint64
	SessionID          string
	AttemptCount       int
	MaxAttempts        int
	ErrorMessage       string
	LeaseOwner         string
}

func (s *TranscriptionJobStore) GetExternalRequest(ctx context.Context, workspaceID uint64, source, key string) (ExternalRequest, error) {
	if s == nil || s.pool == nil {
		return ExternalRequest{}, fmt.Errorf("external request store is not configured")
	}
	row, err := s.q.GetExternalRequestManual(ctx, db.GetExternalRequestManualParams{
		WorkspaceID:    workspaceID,
		Source:         strings.TrimSpace(source),
		IdempotencyKey: strings.TrimSpace(key),
	})
	if err != nil {
		return ExternalRequest{}, err
	}
	return dbExternalRequestModelToStore(row), nil
}

type WebhookDelivery struct {
	ID             uint64
	EventID        string
	EventType      string
	Subject        string
	BodyJSON       string
	SubscriptionID uint64
	TargetURL      string
	SigningSecret  []byte
	LeaseOwner     string
	AttemptCount   int
	MaxAttempts    int
}

type EventOutboxRecord struct {
	ID        uint64
	EventID   string
	EventType string
	Subject   string
	BodyJSON  string
	CreatedAt time.Time
}

func NewTranscriptionJobStore(pool *sql.DB) *TranscriptionJobStore {
	return NewTranscriptionJobStoreWithAdmission(pool, defaultTranscriptionJobAdmissionPolicy())
}

// NewTranscriptionJobStoreWithAdmission creates a job repository with an
// explicit durable per-workspace admission policy.
func NewTranscriptionJobStoreWithAdmission(pool *sql.DB, admission TranscriptionJobAdmissionPolicy) *TranscriptionJobStore {
	return &TranscriptionJobStore{q: db.New(pool), pool: pool, admission: admission}
}

// ClaimableQueueSnapshot reports pending jobs whose retry delay has elapsed and
// expired running leases. It uses the database clock for both eligibility and
// age so process clock skew cannot produce a misleading backlog measurement.
func (s *TranscriptionJobStore) ClaimableQueueSnapshot(ctx context.Context) (TranscriptionQueueSnapshot, error) {
	if s == nil || s.pool == nil {
		return TranscriptionQueueSnapshot{}, fmt.Errorf("transcription job store is not configured")
	}
	row, err := s.q.GetClaimableTranscriptionQueueSnapshot(ctx)
	if err != nil {
		return TranscriptionQueueSnapshot{}, fmt.Errorf("read claimable transcription queue: %w", err)
	}
	snapshot := TranscriptionQueueSnapshot{Depth: row.Depth, ExpiredLeases: row.ExpiredLeases}
	oldestAgeMicroseconds := row.OldestAgeMicroseconds
	if oldestAgeMicroseconds > 0 {
		snapshot.OldestAge = time.Duration(oldestAgeMicroseconds) * time.Microsecond
	}
	return snapshot, nil
}

func (s *TranscriptionJobStore) Create(ctx context.Context, itemImageID uint64, processingContext Context) (uint64, error) {
	if s == nil || s.pool == nil || itemImageID == 0 {
		return 0, fmt.Errorf("create transcription job: store and item image are required")
	}
	if processingContext.ID == 0 {
		return 0, fmt.Errorf("create transcription job: context is required")
	}
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin create transcription job: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.q.WithTx(tx)
	workspaceID, err := lockTranscriptionAdmissionWorkspace(ctx, queries, itemImageID)
	if err != nil {
		return 0, fmt.Errorf("lock transcription job workspace: %w", err)
	}
	lockedContext, contextSnapshot, err := lockContextSnapshotForWorkspace(ctx, queries, processingContext.ID, workspaceID)
	if err != nil {
		return 0, fmt.Errorf("lock transcription job context: %w", err)
	}
	contextID := nullableUint64(lockedContext.ID)
	id, err := s.createLocked(ctx, queries, workspaceID, itemImageID, contextID, contextSnapshot, ErrTranscriptionCanonicalPageRequired, func() (sql.Result, error) {
		return queries.CreateTranscriptionJobManual(ctx, db.CreateTranscriptionJobManualParams{
			ItemImageID:     itemImageID,
			ContextID:       contextID,
			ContextSnapshot: contextSnapshot,
		})
	})
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit create transcription job: %w", err)
	}
	return id, nil
}

// CreateForUploadBatchFile admits a job from the immutable context snapshot
// owned by a currently leased upload-batch file. No caller-supplied context
// fields or mutable live context row can alter the selected processing model.
func (s *TranscriptionJobStore) CreateForUploadBatchFile(
	ctx context.Context,
	workspaceID uint64,
	batchID string,
	sequence uint32,
	leaseOwner string,
	itemImageID uint64,
) (uint64, error) {
	if s == nil || s.pool == nil || workspaceID == 0 || strings.TrimSpace(batchID) == "" || sequence == 0 || strings.TrimSpace(leaseOwner) == "" || itemImageID == 0 {
		return 0, fmt.Errorf("create upload batch transcription job: workspace, batch file lease, and item image are required")
	}
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin create upload batch transcription job: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.q.WithTx(tx)
	lockedWorkspaceID, err := lockTranscriptionAdmissionWorkspace(ctx, queries, itemImageID)
	if err != nil {
		return 0, fmt.Errorf("lock upload batch transcription workspace: %w", err)
	}
	if lockedWorkspaceID != workspaceID {
		return 0, ErrUploadBatchNotFound
	}
	batchRow, _, err := lockActiveUploadBatchFileAttempt(ctx, queries, workspaceID, batchID, sequence, leaseOwner)
	if err != nil {
		return 0, err
	}
	contextSnapshot := append(json.RawMessage(nil), batchRow.ContextSnapshot...)
	if err := validateUploadBatchJobContext(batchRow, workspaceID); err != nil {
		return 0, fmt.Errorf("validate upload batch transcription context: %w", err)
	}
	id, err := s.createLocked(ctx, queries, workspaceID, itemImageID, batchRow.ContextID, contextSnapshot, ErrUploadBatchFileFence, func() (sql.Result, error) {
		return queries.CreateUploadBatchTranscriptionJobManual(ctx, db.CreateUploadBatchTranscriptionJobManualParams{
			WorkspaceID: workspaceID,
			BatchID:     strings.TrimSpace(batchID),
			Sequence:    sequence,
			LockedBy:    nullableString(strings.TrimSpace(leaseOwner)),
			ItemImageID: itemImageID,
		})
	})
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit create upload batch transcription job: %w", err)
	}
	return id, nil
}

func validateUploadBatchJobContext(batch db.UploadBatch, workspaceID uint64) error {
	var snapshot Context
	if err := json.Unmarshal(batch.ContextSnapshot, &snapshot); err != nil {
		return fmt.Errorf("decode immutable context snapshot: %w", err)
	}
	if snapshot.ID == 0 {
		return errors.New("immutable context snapshot has no id")
	}
	if snapshot.WorkspaceID != nil && *snapshot.WorkspaceID != workspaceID {
		return errors.New("immutable context snapshot belongs to another workspace")
	}
	if batch.ContextID.Valid {
		if batch.ContextID.Int64 <= 0 || uint64(batch.ContextID.Int64) != snapshot.ID {
			return errors.New("live context identity differs from immutable snapshot")
		}
		if !batch.ContextScopeID.Valid || batch.ContextScopeID.Int64 < 0 {
			return errors.New("live context scope is invalid")
		}
		scopeID := uint64(batch.ContextScopeID.Int64)
		switch {
		case scopeID == 0 && snapshot.WorkspaceID != nil:
			return errors.New("system context snapshot has a workspace owner")
		case scopeID == workspaceID && (snapshot.WorkspaceID == nil || *snapshot.WorkspaceID != workspaceID):
			return errors.New("workspace context snapshot has an invalid owner")
		case scopeID != 0 && scopeID != workspaceID:
			return errors.New("live context scope belongs to another workspace")
		}
	} else if batch.ContextScopeID.Valid {
		return errors.New("detached context retained a live scope")
	}
	return nil
}

func (s *TranscriptionJobStore) createLocked(
	ctx context.Context,
	queries *db.Queries,
	workspaceID, itemImageID uint64,
	contextID sql.NullInt64,
	contextSnapshot json.RawMessage,
	emptyInsertErr error,
	insert func() (sql.Result, error),
) (uint64, error) {
	var id uint64
	for attempt := 0; attempt < 3; attempt++ {
		active, activeErr := queries.LockActiveTranscriptionJobForUpdateManual(ctx, nullableUint64(itemImageID))
		inputRevision, revisionErr := queries.LockCanonicalRevisionForTranscriptionJobManual(ctx, itemImageID)
		if errors.Is(revisionErr, sql.ErrNoRows) {
			return 0, ErrTranscriptionCanonicalPageRequired
		}
		if revisionErr != nil {
			return 0, fmt.Errorf("lock transcription input revision: %w", revisionErr)
		}
		if inputRevision == 0 {
			return 0, ErrTranscriptionCanonicalPageRequired
		}
		hasActiveJob := activeErr == nil
		if hasActiveJob {
			if activeTranscriptionJobMatches(active, contextID, contextSnapshot, inputRevision) {
				id = active.ID
				break
			}
			if active.Status == db.TranscriptionJobsStatusRunning {
				fence, fenceErr := attemptFenceFromRow(active)
				if fenceErr != nil {
					return 0, fmt.Errorf("supersede active transcription attempt: %w", fenceErr)
				}
				if finishErr := finishTranscriptionAttempt(ctx, queries, fence, TranscriptionAttemptSuperseded, "superseded by a newer transcription request", nil); finishErr != nil {
					return 0, fmt.Errorf("supersede active transcription attempt: %w", finishErr)
				}
			}
			result, updateErr := queries.SupersedeTranscriptionJobByIDManual(ctx, active.ID)
			if updateErr := requireOneAffected(result, updateErr); updateErr != nil {
				return 0, fmt.Errorf("supersede active transcription job: %w", updateErr)
			}
		} else if !errors.Is(activeErr, sql.ErrNoRows) {
			return 0, fmt.Errorf("lock active transcription job: %w", activeErr)
		}
		if !hasActiveJob {
			if admissionErr := enforceTranscriptionJobAdmission(ctx, queries, workspaceID, s.admission); admissionErr != nil {
				return 0, admissionErr
			}
		}

		result, createErr := insert()
		if createErr != nil {
			return 0, fmt.Errorf("create transcription job: %w", createErr)
		}
		insertedID, idErr := result.LastInsertId()
		if idErr != nil {
			return 0, fmt.Errorf("read transcription job id: %w", idErr)
		}
		if insertedID <= 0 {
			return 0, emptyInsertErr
		}
		id = uint64(insertedID)
		created, loadErr := queries.GetTranscriptionJobManual(ctx, id)
		if loadErr != nil {
			return 0, fmt.Errorf("reload created transcription job: %w", loadErr)
		}
		if activeTranscriptionJobMatches(created, contextID, contextSnapshot, inputRevision) {
			break
		}
		id = 0
	}
	if id == 0 {
		return 0, fmt.Errorf("create transcription job: concurrent active job did not converge")
	}
	return id, nil
}

func (s *TranscriptionJobStore) Get(ctx context.Context, id uint64) (TranscriptionJob, error) {
	row, err := s.q.GetTranscriptionJob(ctx, id)
	if err != nil {
		return TranscriptionJob{}, fmt.Errorf("get transcription job: %w", err)
	}
	return s.jobWithAttempts(ctx, row)
}

// GetActiveByItemImage returns the one pending or running job for an image.
func (s *TranscriptionJobStore) GetActiveByItemImage(ctx context.Context, itemImageID uint64) (TranscriptionJob, error) {
	row, err := s.q.GetActiveTranscriptionJobByItemImage(ctx, itemImageID)
	if err != nil {
		return TranscriptionJob{}, fmt.Errorf("get active transcription job: %w", err)
	}
	return s.jobWithAttempts(ctx, row)
}

// ListPage returns one workspace-scoped keyset page. itemImageID zero means all
// workspace jobs. The returned summaries never include attempt history; callers
// use Get when that independently bounded audit is required.
func (s *TranscriptionJobStore) ListPage(
	ctx context.Context,
	workspaceID, itemImageID uint64,
	pageSize uint32,
	cursor *TranscriptionJobPageCursor,
) (TranscriptionJobPage, error) {
	if s == nil || s.pool == nil {
		return TranscriptionJobPage{}, fmt.Errorf("transcription job store is not configured")
	}
	if workspaceID == 0 || pageSize == 0 || pageSize > MaxTranscriptionJobPageSize {
		return TranscriptionJobPage{}, fmt.Errorf("%w: workspace and page size between 1 and %d are required", ErrInvalidTranscriptionJobPage, MaxTranscriptionJobPageSize)
	}
	cursorCreatedAt := sql.NullTime{}
	var cursorID uint64
	if cursor != nil {
		if cursor.CreatedAt.IsZero() || cursor.ID == 0 {
			return TranscriptionJobPage{}, fmt.Errorf("%w: cursor requires created_at and id", ErrInvalidTranscriptionJobPage)
		}
		cursorCreatedAt = sql.NullTime{Time: cursor.CreatedAt.UTC(), Valid: true}
		cursorID = cursor.ID
	}
	rows, err := s.q.ListTranscriptionJobsPage(ctx, db.TranscriptionJobPageParams{
		WorkspaceID:     workspaceID,
		ItemImageID:     itemImageID,
		CursorCreatedAt: cursorCreatedAt,
		CursorID:        cursorID,
		PageLimit:       int32(pageSize + 1), // #nosec G115 -- bounded by MaxTranscriptionJobPageSize.
	})
	if err != nil {
		return TranscriptionJobPage{}, fmt.Errorf("list transcription jobs: %w", err)
	}
	hasMore := len(rows) > int(pageSize)
	if hasMore {
		rows = rows[:pageSize]
	}
	jobs := make([]TranscriptionJobSummary, 0, len(rows))
	for _, row := range rows {
		jobs = append(jobs, rowToTranscriptionJobSummary(row))
	}
	page := TranscriptionJobPage{Jobs: jobs}
	if hasMore {
		last := rows[len(rows)-1]
		page.NextCursor = &TranscriptionJobPageCursor{CreatedAt: last.CreatedAt.UTC(), ID: last.ID}
	}
	return page, nil
}

// ClaimNextPending atomically claims the oldest pending job, marking it as
// running. Returns nil when no pending jobs exist.
func (s *TranscriptionJobStore) ClaimNextPending(ctx context.Context) (*TranscriptionJob, error) {
	return s.claimNextPending(ctx, time.Time{})
}

func (s *TranscriptionJobStore) ClaimNextPendingOlderThan(ctx context.Context, cutoff time.Time) (*TranscriptionJob, error) {
	return s.claimNextPending(ctx, cutoff)
}

func (s *TranscriptionJobStore) claimNextPending(ctx context.Context, cutoff time.Time) (*TranscriptionJob, error) {
	return s.claim(ctx, 0, cutoff)
}

func (s *TranscriptionJobStore) ClaimPendingByID(ctx context.Context, id uint64) (*TranscriptionJob, error) {
	if id == 0 {
		return nil, nil
	}
	return s.claim(ctx, id, time.Time{})
}

func (s *TranscriptionJobStore) claim(ctx context.Context, jobID uint64, cutoff time.Time) (*TranscriptionJob, error) {
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin transcription claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.q.WithTx(tx)
	for {
		var row db.TranscriptionJob
		switch {
		case jobID > 0:
			row, err = qtx.ClaimLeasedTranscriptionJobByIDManual(ctx, jobID)
		case !cutoff.IsZero():
			row, err = qtx.ClaimNextLeasedTranscriptionJobOlderThanManual(ctx, cutoff)
		default:
			row, err = qtx.ClaimNextLeasedTranscriptionJobManual(ctx)
		}
		if errors.Is(err, sql.ErrNoRows) {
			if commitErr := tx.Commit(); commitErr != nil {
				return nil, fmt.Errorf("commit transcription recovery: %w", commitErr)
			}
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("select claimable transcription job: %w", err)
		}

		previousLeaseToken := row.LockedBy
		if row.Status == db.TranscriptionJobsStatusRunning {
			previousFence, fenceErr := attemptFenceFromRow(row)
			if fenceErr != nil {
				return nil, fmt.Errorf("recover expired transcription attempt: %w", fenceErr)
			}
			if finishErr := finishTranscriptionAttempt(ctx, qtx, previousFence, TranscriptionAttemptLeaseExpired, "worker lease expired", nil); finishErr != nil {
				return nil, fmt.Errorf("finish expired transcription attempt: %w", finishErr)
			}
			if row.AttemptCount >= row.MaxAttempts {
				result, failErr := qtx.FailExpiredTranscriptionJobManual(ctx, db.FailExpiredTranscriptionJobManualParams{
					ID:            row.ID,
					AttemptNumber: row.AttemptCount,
					InputRevision: row.InputRevision,
					LeaseToken:    row.LockedBy,
				})
				if failErr := requireOneAffected(result, failErr); failErr != nil {
					return nil, fmt.Errorf("fail exhausted transcription job: %w", failErr)
				}
				if jobID > 0 {
					if commitErr := tx.Commit(); commitErr != nil {
						return nil, fmt.Errorf("commit exhausted transcription job: %w", commitErr)
					}
					return nil, nil
				}
				continue
			}
		}

		leaseToken := newLeaseOwner("worker")
		leaseUntil := time.Now().UTC().Add(10 * time.Minute)
		result, markErr := qtx.MarkTranscriptionJobLeasedManual(ctx, db.MarkTranscriptionJobLeasedManualParams{
			LeaseUntil:           sql.NullTime{Time: leaseUntil, Valid: true},
			LeaseToken:           nullableString(leaseToken),
			ID:                   row.ID,
			PreviousAttemptCount: row.AttemptCount,
			InputRevision:        row.InputRevision,
			PreviousLeaseToken:   previousLeaseToken,
		})
		if markErr := requireOneAffected(result, markErr); markErr != nil {
			return nil, fmt.Errorf("lease transcription job: %w", markErr)
		}
		attemptNumber := row.AttemptCount + 1
		result, insertErr := qtx.InsertTranscriptionJobAttemptManual(ctx, db.InsertTranscriptionJobAttemptManualParams{
			LeaseOwner:    transcriptionLeaseOwner(),
			LeaseToken:    leaseToken,
			JobID:         row.ID,
			AttemptNumber: attemptNumber,
			InputRevision: row.InputRevision,
			JobLeaseToken: nullableString(leaseToken),
		})
		if insertErr := requireOneAffected(result, insertErr); insertErr != nil {
			return nil, fmt.Errorf("record transcription attempt: %w", insertErr)
		}
		claimedRow, loadErr := qtx.GetTranscriptionJobManual(ctx, row.ID)
		if loadErr != nil {
			return nil, fmt.Errorf("reload claimed transcription job: %w", loadErr)
		}
		attemptRows, listErr := qtx.ListTranscriptionJobAttemptsManual(ctx, row.ID)
		if listErr != nil {
			return nil, fmt.Errorf("load transcription attempt history: %w", listErr)
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return nil, fmt.Errorf("commit transcription claim: %w", commitErr)
		}
		claimed := rowToTranscriptionJob(claimedRow)
		claimed.Attempts = attemptRowsToStore(attemptRows)
		return &claimed, nil
	}
}

// SetTotalSegments sets the total segment count for the fenced attempt.
func (s *TranscriptionJobStore) SetTotalSegments(ctx context.Context, fence TranscriptionAttemptFence, total int) error {
	attemptNumber, err := transcriptionAttemptNumberToDB(fence)
	if err != nil {
		return err
	}
	converted, err := int32FromInt(total)
	if err != nil {
		return err
	}
	result, err := s.q.SetTranscriptionJobTotalSegmentsManual(ctx, db.SetTranscriptionJobTotalSegmentsManualParams{
		TotalSegments: converted,
		ID:            fence.JobID,
		AttemptNumber: attemptNumber,
		InputRevision: fence.InputRevision,
		LeaseToken:    nullableString(fence.LeaseToken),
	})
	return requireFenceAffected(result, err)
}

// UpdateProgress records per-segment progress after each annotation is processed.
func (s *TranscriptionJobStore) UpdateProgress(ctx context.Context, fence TranscriptionAttemptFence,
	completed, failed int,
	currentAnnotationID, currentAnnotationJSON, lastResultAnnotationJSON string,
) error {
	attemptNumber, err := transcriptionAttemptNumberToDB(fence)
	if err != nil {
		return err
	}
	completed32, err := int32FromInt(completed)
	if err != nil {
		return err
	}
	failed32, err := int32FromInt(failed)
	if err != nil {
		return err
	}
	result, err := s.q.UpdateTranscriptionJobProgressManual(ctx, db.UpdateTranscriptionJobProgressManualParams{
		CompletedSegments:        completed32,
		FailedSegments:           failed32,
		CurrentAnnotationID:      nullableString(currentAnnotationID),
		CurrentAnnotationJson:    nullableString(currentAnnotationJSON),
		LastResultAnnotationJson: nullableString(lastResultAnnotationJSON),
		ID:                       fence.JobID,
		AttemptNumber:            attemptNumber,
		InputRevision:            fence.InputRevision,
		LeaseToken:               nullableString(fence.LeaseToken),
	})
	return requireFenceAffected(result, err)
}

func (s *TranscriptionJobStore) Cancel(ctx context.Context, id uint64) error {
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transcription cancellation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.q.WithTx(tx)
	job, err := qtx.LockTranscriptionJobForUpdateManual(ctx, id)
	if err != nil {
		return err
	}
	switch job.Status {
	case db.TranscriptionJobsStatusCanceled:
		return tx.Commit()
	case db.TranscriptionJobsStatusPending:
		// A pending job has no running attempt to terminalize.
	case db.TranscriptionJobsStatusRunning:
		fence, fenceErr := attemptFenceFromRow(job)
		if fenceErr != nil {
			return fenceErr
		}
		if finishErr := finishTranscriptionAttempt(ctx, qtx, fence, TranscriptionAttemptCanceled, "canceled by user", nil); finishErr != nil {
			return finishErr
		}
	default:
		return sql.ErrNoRows
	}
	result, err := qtx.CancelTranscriptionJobManual(ctx, id)
	if err := requireOneAffected(result, err); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transcription cancellation: %w", err)
	}
	return nil
}

func (s *TranscriptionJobStore) ExtendLease(ctx context.Context, fence TranscriptionAttemptFence, leaseDuration time.Duration) error {
	attemptNumber, err := transcriptionAttemptNumberToDB(fence)
	if err != nil {
		return err
	}
	if leaseDuration <= 0 {
		leaseDuration = 10 * time.Minute
	}
	result, err := s.q.ExtendTranscriptionJobLeaseManual(ctx, db.ExtendTranscriptionJobLeaseManualParams{
		LeaseUntil:    sql.NullTime{Time: time.Now().UTC().Add(leaseDuration), Valid: true},
		ID:            fence.JobID,
		AttemptNumber: attemptNumber,
		InputRevision: fence.InputRevision,
		LeaseToken:    nullableString(fence.LeaseToken),
	})
	return requireFenceAffected(result, err)
}

func (s *TranscriptionJobStore) FailWithWebhookEvent(ctx context.Context, fence TranscriptionAttemptFence, errMsg, eventID, eventType, subject, bodyJSON string) (bool, error) {
	return s.failWithWebhookEvent(ctx, fence, errMsg, eventID, eventType, subject, bodyJSON, false, time.Time{})
}

func (s *TranscriptionJobStore) PermanentlyFailWithWebhookEvent(ctx context.Context, fence TranscriptionAttemptFence, errMsg, eventID, eventType, subject, bodyJSON string) error {
	_, err := s.failWithWebhookEvent(ctx, fence, errMsg, eventID, eventType, subject, bodyJSON, true, time.Time{})
	return err
}

func (s *TranscriptionJobStore) failWithWebhookEvent(ctx context.Context, fence TranscriptionAttemptFence, errMsg, eventID, eventType, subject, bodyJSON string, permanent bool, retryAfter time.Time) (bool, error) {
	attemptNumber, err := transcriptionAttemptNumberToDB(fence)
	if err != nil {
		return false, err
	}
	safeMessage := safeTranscriptionErrorMessage(errMsg)
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.q.WithTx(tx)
	if _, err := qtx.LockActiveTranscriptionJobLeaseManual(ctx, db.LockActiveTranscriptionJobLeaseManualParams{
		ID:            fence.JobID,
		AttemptNumber: attemptNumber,
		InputRevision: fence.InputRevision,
		LeaseToken:    nullableString(fence.LeaseToken),
	}); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrTranscriptionJobFence
		}
		return false, err
	}
	job, err := qtx.GetTranscriptionJobManual(ctx, fence.JobID)
	if err != nil {
		return false, err
	}
	terminal := permanent || job.AttemptCount >= job.MaxAttempts
	outcome := TranscriptionAttemptRetryableFailed
	if terminal {
		outcome = TranscriptionAttemptFailed
	}
	if err := finishTranscriptionAttempt(ctx, qtx, fence, outcome, safeMessage, nil); err != nil {
		return false, err
	}
	var result sql.Result
	switch {
	case permanent || (terminal && !retryAfter.IsZero()):
		result, err = qtx.PermanentlyFailTranscriptionJobManual(ctx, db.PermanentlyFailTranscriptionJobManualParams{
			ErrorMessage:  nullableString(safeMessage),
			ID:            fence.JobID,
			AttemptNumber: attemptNumber,
			InputRevision: fence.InputRevision,
			LeaseToken:    nullableString(fence.LeaseToken),
		})
	case !retryAfter.IsZero():
		result, err = qtx.DeferTranscriptionJobLeaseManual(ctx, db.DeferTranscriptionJobLeaseManualParams{
			RetryAfter:    sql.NullTime{Time: retryAfter, Valid: true},
			ErrorMessage:  nullableString(safeMessage),
			ID:            fence.JobID,
			AttemptNumber: attemptNumber,
			InputRevision: fence.InputRevision,
			LeaseToken:    nullableString(fence.LeaseToken),
		})
	default:
		result, err = qtx.RetryOrFailTranscriptionJobManual(ctx, db.RetryOrFailTranscriptionJobManualParams{
			ErrorMessage:  nullableString(safeMessage),
			ID:            fence.JobID,
			AttemptNumber: attemptNumber,
			InputRevision: fence.InputRevision,
			LeaseToken:    nullableString(fence.LeaseToken),
		})
	}
	if err := requireFenceAffected(result, err); err != nil {
		return false, err
	}
	if terminal && strings.TrimSpace(eventID) != "" && strings.TrimSpace(bodyJSON) != "" {
		workspaceID, workspaceErr := s.eventWorkspaceID(ctx, qtx, subject)
		if workspaceErr != nil {
			return false, workspaceErr
		}
		if err := qtx.InsertEventOutbox(ctx, eventID, eventType, workspaceID, nullableString(subject), bodyJSON); err != nil {
			return false, err
		}
		if err := qtx.InsertWorkspaceWebhookDeliveries(ctx, eventID); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return terminal, nil
}

// Fail records an error and either schedules a retry or marks the job failed.
func (s *TranscriptionJobStore) Fail(ctx context.Context, fence TranscriptionAttemptFence, errMsg string) error {
	_, err := s.failWithWebhookEvent(ctx, fence, errMsg, "", "", "", "", false, time.Time{})
	return err
}

func (s *TranscriptionJobStore) Defer(ctx context.Context, fence TranscriptionAttemptFence, errMsg string, retryAfter time.Time) error {
	_, err := s.failWithWebhookEvent(ctx, fence, errMsg, "", "", "", "", false, retryAfter)
	return err
}

func (s *TranscriptionJobStore) WorkspaceOwnsJob(ctx context.Context, workspaceID, jobID uint64) (bool, error) {
	return s.q.WorkspaceOwnsTranscriptionJob(ctx, workspaceID, jobID)
}

func (s *TranscriptionJobStore) ReserveExternalRequest(ctx context.Context, workspaceID uint64, source, key, requestHash, eventHeader string) (ExternalRequest, bool, error) {
	return s.reserveExternalRequest(ctx, workspaceID, source, key, requestHash, eventHeader, 0)
}

// ReserveExternalRequestForItemImage creates or reclaims an idempotency record
// tied to a workspace-owned image. Binding the record at reservation time lets
// resource deletion remove both successful and abandoned operations.
func (s *TranscriptionJobStore) ReserveExternalRequestForItemImage(ctx context.Context, workspaceID, itemImageID uint64, source, key, requestHash, eventHeader string) (ExternalRequest, bool, error) {
	if itemImageID == 0 {
		return ExternalRequest{}, false, fmt.Errorf("item image id is required")
	}
	return s.reserveExternalRequest(ctx, workspaceID, source, key, requestHash, eventHeader, itemImageID)
}

func (s *TranscriptionJobStore) reserveExternalRequest(ctx context.Context, workspaceID uint64, source, key, requestHash, eventHeader string, itemImageID uint64) (ExternalRequest, bool, error) {
	if s == nil || s.pool == nil {
		return ExternalRequest{}, false, fmt.Errorf("external request store is not configured")
	}
	if workspaceID == 0 {
		return ExternalRequest{}, false, fmt.Errorf("workspace id is required")
	}
	source = strings.TrimSpace(source)
	if source == "" {
		source = "external"
	}
	if len(source) > 64 {
		return ExternalRequest{}, false, fmt.Errorf("external request source exceeds 64 bytes")
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return ExternalRequest{}, false, fmt.Errorf("idempotency key is required")
	}
	if len(key) > 128 {
		return ExternalRequest{}, false, fmt.Errorf("idempotency key exceeds 128 bytes")
	}
	requestHash = strings.TrimSpace(requestHash)
	if len(requestHash) != sha256.Size*2 || strings.Trim(requestHash, "0123456789abcdef") != "" {
		return ExternalRequest{}, false, fmt.Errorf("request hash must be a lowercase SHA-256 digest")
	}

	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return ExternalRequest{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	leaseUntil := time.Now().UTC().Add(10 * time.Minute)
	lockedBy := newLeaseOwner("request")
	qtx := s.q.WithTx(tx)
	if _, err := qtx.LockWorkspaceForUseManual(ctx, workspaceID); err != nil {
		return ExternalRequest{}, false, fmt.Errorf("lock external request workspace: %w", err)
	}
	if itemImageID != 0 {
		if _, err := qtx.LockItemImageForUseManual(ctx, db.LockItemImageForUseManualParams{
			ID:          itemImageID,
			WorkspaceID: workspaceID,
		}); errors.Is(err, sql.ErrNoRows) {
			return ExternalRequest{}, false, sql.ErrNoRows
		} else if err != nil {
			return ExternalRequest{}, false, fmt.Errorf("lock external request item image: %w", err)
		}
	}

	if _, err := qtx.InsertExternalRequest(ctx, db.InsertExternalRequestManualParams{
		WorkspaceID:    workspaceID,
		Source:         source,
		IdempotencyKey: key,
		RequestHash:    requestHash,
		ItemImageID:    nullableUint64(itemImageID),
		EventHeader:    nullableString(eventHeader),
		LeaseUntil:     sql.NullTime{Time: leaseUntil, Valid: true},
		LockedBy:       nullableString(lockedBy),
	}); err != nil {
		return ExternalRequest{}, false, err
	}
	reqRow, err := qtx.SelectExternalRequestForUpdate(ctx, db.SelectExternalRequestForUpdateManualParams{
		WorkspaceID:    workspaceID,
		Source:         source,
		IdempotencyKey: key,
	})
	if err != nil {
		return ExternalRequest{}, false, err
	}
	req := dbExternalRequestToStore(reqRow)
	if reqRow.RequestHash != requestHash {
		return ExternalRequest{}, false, ErrExternalRequestMismatch
	}
	if reqRow.LockedBy.Valid && reqRow.LockedBy.String == lockedBy {
		if err := tx.Commit(); err != nil {
			return ExternalRequest{}, false, err
		}
		return req, true, nil
	}
	if failedExternalRequestExhausted(reqRow) {
		if err := tx.Commit(); err != nil {
			return ExternalRequest{}, false, err
		}
		return req, false, nil
	}
	if exhaustedExternalRequest(reqRow) {
		if err := qtx.FailExternalRequest(ctx, db.FailExternalRequestManualParams{
			WorkspaceID:    workspaceID,
			Source:         source,
			IdempotencyKey: key,
			LockedBy:       reqRow.LockedBy,
			ErrorMessage:   nullableString("external request attempts exhausted"),
		}); err != nil {
			return ExternalRequest{}, false, err
		}
		req.Status = ExternalRequestStatusFailed
		req.ErrorMessage = "external request attempts exhausted"
		if err := tx.Commit(); err != nil {
			return ExternalRequest{}, false, err
		}
		return req, false, nil
	}
	if req.Status == ExternalRequestStatusFailed || staleExternalRequest(reqRow) {
		if err := qtx.ReclaimExternalRequest(ctx, db.ReclaimExternalRequestManualParams{
			ID:          req.ID,
			ItemImageID: nullableUint64(itemImageID),
			EventHeader: nullableString(eventHeader),
			LeaseUntil:  sql.NullTime{Time: leaseUntil, Valid: true},
			LockedBy:    nullableString(lockedBy),
		}); err != nil {
			return ExternalRequest{}, false, err
		}
		req.Status = ExternalRequestStatusInProgress
		req.ErrorMessage = ""
		req.LeaseOwner = lockedBy
		req.AttemptCount++
		if err := tx.Commit(); err != nil {
			return ExternalRequest{}, false, err
		}
		return req, true, nil
	}
	if err := tx.Commit(); err != nil {
		return ExternalRequest{}, false, err
	}
	return req, false, nil
}

func (s *TranscriptionJobStore) CompleteExternalRequest(ctx context.Context, workspaceID uint64, source, key, leaseOwner, itemID string, itemImageID, jobID uint64) error {
	if s == nil || s.pool == nil || workspaceID == 0 {
		return fmt.Errorf("complete external request: store and workspace are required")
	}
	source = strings.TrimSpace(source)
	key = strings.TrimSpace(key)
	itemID = strings.TrimSpace(itemID)
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("complete external request: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.q.WithTx(tx)
	// Keep the same parent-to-child order as reservation and resource deletion:
	// workspace, item, image, job, then the external-request leaf. Taking the
	// idempotency row first here inverted reserveExternalRequest's order and
	// allowed completion/reclaim to deadlock under load.
	if _, err := queries.LockWorkspaceForUseManual(ctx, workspaceID); err != nil {
		return fmt.Errorf("complete external request: lock workspace: %w", err)
	}
	if itemID != "" {
		if _, err := queries.LockItemForUseManual(ctx, db.LockItemForUseManualParams{ID: itemID, WorkspaceID: workspaceID}); err != nil {
			return fmt.Errorf("complete external request: lock item: %w", err)
		}
	}
	if itemImageID != 0 {
		if _, err := queries.LockItemImageForUseManual(ctx, db.LockItemImageForUseManualParams{ID: itemImageID, WorkspaceID: workspaceID}); err != nil {
			return fmt.Errorf("complete external request: lock item image: %w", err)
		}
	}
	if jobID != 0 {
		if _, err := queries.LockTranscriptionJobForExternalRequestUseManual(ctx, db.LockTranscriptionJobForExternalRequestUseManualParams{
			ID:          jobID,
			WorkspaceID: workspaceID,
		}); err != nil {
			return fmt.Errorf("complete external request: lock transcription job: %w", err)
		}
	}
	if _, err := queries.SelectExternalRequestForUpdate(ctx, db.SelectExternalRequestForUpdateManualParams{
		WorkspaceID:    workspaceID,
		Source:         source,
		IdempotencyKey: key,
	}); err != nil {
		return err
	}
	if err := queries.CompleteExternalRequest(ctx, db.CompleteExternalRequestManualParams{
		WorkspaceID:        workspaceID,
		Source:             source,
		IdempotencyKey:     key,
		LockedBy:           nullableString(leaseOwner),
		ItemID:             nullableString(itemID),
		ItemImageID:        nullableUint64(itemImageID),
		TranscriptionJobID: nullableUint64(jobID),
	}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("complete external request: commit: %w", err)
	}
	return nil
}

func (s *TranscriptionJobStore) FailExternalRequest(ctx context.Context, workspaceID uint64, source, key, leaseOwner, errMsg string) error {
	return s.q.FailExternalRequest(ctx, db.FailExternalRequestManualParams{
		WorkspaceID:    workspaceID,
		Source:         source,
		IdempotencyKey: key,
		LockedBy:       nullableString(leaseOwner),
		ErrorMessage:   nullableString(safeExternalRequestErrorMessage(errMsg)),
	})
}

// safeExternalRequestErrorMessage is the persistence boundary for failures
// originating outside Scribe's trust boundary. Provider responses, request
// bodies, credential-bearing URLs, and infrastructure diagnostics must never
// become durable request state. Keep these values categorical so they remain
// useful to clients and operators without reflecting attacker-controlled text.
func safeExternalRequestErrorMessage(message string) string {
	return safeDurableFailureMessage(durableFailureExternalRequest, message)
}

func (s *TranscriptionJobStore) EnqueueWebhookEvent(ctx context.Context, eventID, eventType, subject, bodyJSON string) error {
	if strings.TrimSpace(eventID) == "" || strings.TrimSpace(bodyJSON) == "" {
		return nil
	}
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	qtx := s.q.WithTx(tx)
	workspaceID, err := s.eventWorkspaceID(ctx, qtx, subject)
	if err != nil {
		return err
	}
	if err := qtx.InsertEventOutbox(ctx, eventID, eventType, workspaceID, nullableString(subject), bodyJSON); err != nil {
		return err
	}
	if err := qtx.InsertWorkspaceWebhookDeliveries(ctx, eventID); err != nil {
		return err
	}
	return tx.Commit()
}

// SystemWebhookEventType is deliberately closed: global events are visible to
// privileged system consumers and must never be created by falling back from a
// malformed tenant resource subject.
type SystemWebhookEventType string

const SystemWebhookEventMaintenance SystemWebhookEventType = "dev.scribe.system.maintenance"

func (s *TranscriptionJobStore) EnqueueSystemWebhookEvent(ctx context.Context, eventID string, eventType SystemWebhookEventType, bodyJSON string) error {
	if eventType != SystemWebhookEventMaintenance {
		return fmt.Errorf("system webhook event type is not allowed")
	}
	if strings.TrimSpace(eventID) == "" || strings.TrimSpace(bodyJSON) == "" {
		return nil
	}
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.q.WithTx(tx)
	if err := qtx.InsertEventOutbox(ctx, eventID, string(eventType), sql.NullInt64{}, sql.NullString{}, bodyJSON); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *TranscriptionJobStore) ClaimWebhookDeliveries(ctx context.Context, limit int) ([]WebhookDelivery, error) {
	if limit <= 0 {
		limit = 10
	}
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	qtx := s.q.WithTx(tx)
	rows, err := qtx.ClaimWebhookDeliveries(ctx, limit, time.Now().UTC().Add(2*time.Minute), newLeaseOwner("webhook"))
	if err != nil {
		return nil, err
	}
	deliveries := make([]WebhookDelivery, 0, limit)
	for _, row := range rows {
		deliveries = append(deliveries, dbWebhookDeliveryToStore(row))
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return deliveries, nil
}

func (s *TranscriptionJobStore) MarkWebhookDeliveryDelivered(ctx context.Context, id uint64, leaseOwner string) error {
	return s.q.MarkWebhookDeliveryDelivered(ctx, id, leaseOwner)
}

func (s *TranscriptionJobStore) MarkWebhookDeliveryFailed(ctx context.Context, id uint64, leaseOwner, errMsg string) error {
	message := safeDurableFailureMessage(durableFailureWebhook, errMsg)
	return s.q.MarkWebhookDeliveryFailed(ctx, id, leaseOwner, nullableString(message))
}

func (s *TranscriptionJobStore) RetainWebhookEvents(ctx context.Context, olderThan time.Duration) error {
	if olderThan <= 0 {
		olderThan = 30 * 24 * time.Hour
	}
	cutoff := time.Now().UTC().Add(-olderThan)
	for {
		removed, err := s.retainWebhookEventBatch(ctx, cutoff)
		if err != nil {
			return err
		}
		if removed < eventRetentionBatchSize {
			return nil
		}
	}
}

func (s *TranscriptionJobStore) retainWebhookEventBatch(ctx context.Context, cutoff time.Time) (int64, error) {
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("retain webhook events: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.q.WithTx(tx)
	eventIDs, err := queries.LockEventOutboxRetentionBatchManual(ctx, cutoff)
	if err != nil {
		return 0, fmt.Errorf("retain webhook events: lock event batch: %w", err)
	}
	if len(eventIDs) == 0 {
		if err := tx.Commit(); err != nil {
			return 0, fmt.Errorf("retain webhook events: commit empty batch: %w", err)
		}
		return 0, nil
	}
	if err := queries.DeleteWebhookDeliveriesForEventIDsManual(ctx, eventIDs); err != nil {
		return 0, fmt.Errorf("retain webhook events: delete delivery batch: %w", err)
	}
	result, err := queries.DeleteEventOutboxForIDsManual(ctx, eventIDs)
	if err != nil {
		return 0, fmt.Errorf("retain webhook events: delete event batch: %w", err)
	}
	removed, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("retain webhook events: inspect event batch: %w", err)
	}
	if removed != int64(len(eventIDs)) {
		return 0, fmt.Errorf("retain webhook events: deleted %d events, want %d", removed, len(eventIDs))
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("retain webhook events: commit: %w", err)
	}
	return removed, nil
}

// RetainExternalRequests removes terminal and abandoned idempotency records in
// bounded batches. Active leases are never eligible, even when their creation
// timestamp is old.
func (s *TranscriptionJobStore) RetainExternalRequests(ctx context.Context, olderThan time.Duration) error {
	if olderThan <= 0 {
		olderThan = 30 * 24 * time.Hour
	}
	cutoff := time.Now().UTC().Add(-olderThan)
	for {
		result, err := s.q.DeleteRetainableExternalRequestsManual(ctx, cutoff)
		if err != nil {
			return err
		}
		removed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if removed < 1000 {
			return nil
		}
	}
}

func (s *TranscriptionJobStore) EventOutboxHighWaterForWorkspace(ctx context.Context, workspaceID uint64) (uint64, error) {
	return s.q.GetEventOutboxHighWaterForWorkspace(ctx, workspaceID)
}

func (s *TranscriptionJobStore) ListEventOutboxAfterForWorkspace(ctx context.Context, afterID, workspaceID uint64, limit int) ([]EventOutboxRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.q.ListEventOutboxAfterIDForWorkspace(ctx, afterID, workspaceID, limit)
	if err != nil {
		return nil, err
	}
	return eventOutboxRowsToRecords(rows), nil
}

func eventOutboxRowsToRecords(rows []db.EventOutbox) []EventOutboxRecord {
	events := make([]EventOutboxRecord, 0, len(rows))
	for _, row := range rows {
		event := EventOutboxRecord{
			ID:        row.ID,
			EventID:   row.EventID,
			EventType: row.EventType,
			BodyJSON:  row.BodyJson,
			CreatedAt: row.CreatedAt,
		}
		if row.Subject.Valid {
			event.Subject = row.Subject.String
		}
		events = append(events, event)
	}
	return events
}

func (s *TranscriptionJobStore) eventWorkspaceID(ctx context.Context, q *db.Queries, subject string) (sql.NullInt64, error) {
	itemImageID, ok := itemImageIDFromSubject(subject)
	if !ok {
		return sql.NullInt64{}, fmt.Errorf("event subject must identify an item image")
	}
	workspaceID, err := q.GetWorkspaceIDForItemImageManual(ctx, itemImageID)
	if err != nil {
		return sql.NullInt64{}, fmt.Errorf("resolve event subject workspace: %w", err)
	}
	return nullableUint64(workspaceID), nil
}

func itemImageIDFromSubject(subject string) (uint64, bool) {
	raw, ok := strings.CutPrefix(strings.TrimSpace(subject), "item-images/")
	if !ok || raw == "" {
		return 0, false
	}
	idPart, _, _ := strings.Cut(raw, "/")
	id, err := strconv.ParseUint(idPart, 10, 64)
	return id, err == nil && id > 0
}

func (s *TranscriptionJobStore) jobWithAttempts(ctx context.Context, row db.TranscriptionJob) (TranscriptionJob, error) {
	job := rowToTranscriptionJob(row)
	rows, err := s.q.ListTranscriptionJobAttemptsManual(ctx, row.ID)
	if err != nil {
		return TranscriptionJob{}, fmt.Errorf("list transcription attempts: %w", err)
	}
	job.Attempts = attemptRowsToStore(rows)
	return job, nil
}

func attemptRowsToStore(rows []db.TranscriptionJobAttempt) []TranscriptionJobAttempt {
	attempts := make([]TranscriptionJobAttempt, 0, len(rows))
	for _, row := range rows {
		attempt := TranscriptionJobAttempt{
			JobID:           row.JobID,
			AttemptNumber:   row.AttemptNumber,
			ContextSnapshot: append(json.RawMessage(nil), row.ContextSnapshot...),
			InputRevision:   row.InputRevision,
			LeaseOwner:      row.LeaseOwner,
			Outcome:         TranscriptionAttemptOutcome(row.Outcome),
			StartedAt:       row.StartedAt,
		}
		if row.SafeErrorMessage.Valid {
			attempt.SafeErrorMessage = row.SafeErrorMessage.String
		}
		if row.ResultRevision.Valid && row.ResultRevision.Int64 > 0 {
			resultRevision := uint64(row.ResultRevision.Int64)
			attempt.ResultRevision = &resultRevision
		}
		if row.FinishedAt.Valid {
			finishedAt := row.FinishedAt.Time
			attempt.FinishedAt = &finishedAt
		}
		attempts = append(attempts, attempt)
	}
	return attempts
}

func attemptFenceFromRow(row db.TranscriptionJob) (TranscriptionAttemptFence, error) {
	attemptNumber, err := attemptNumberFromInt(int(row.AttemptCount))
	if err != nil {
		return TranscriptionAttemptFence{}, ErrTranscriptionJobFence
	}
	fence := TranscriptionAttemptFence{
		JobID:         row.ID,
		AttemptNumber: attemptNumber,
		InputRevision: row.InputRevision,
	}
	if row.LockedBy.Valid {
		fence.LeaseToken = row.LockedBy.String
	}
	if err := validateTranscriptionFence(fence); err != nil {
		return TranscriptionAttemptFence{}, err
	}
	return fence, nil
}

func validateTranscriptionFence(fence TranscriptionAttemptFence) error {
	if fence.JobID == 0 || fence.AttemptNumber == 0 || fence.AttemptNumber > math.MaxInt32 || fence.InputRevision == 0 || strings.TrimSpace(fence.LeaseToken) == "" {
		return ErrTranscriptionJobFence
	}
	return nil
}

func transcriptionAttemptNumberToDB(fence TranscriptionAttemptFence) (int32, error) {
	if err := validateTranscriptionFence(fence); err != nil {
		return 0, err
	}
	attemptNumber, err := int32FromUint32(fence.AttemptNumber)
	if err != nil {
		return 0, ErrTranscriptionJobFence
	}
	return attemptNumber, nil
}

func finishTranscriptionAttempt(
	ctx context.Context,
	queries *db.Queries,
	fence TranscriptionAttemptFence,
	outcome TranscriptionAttemptOutcome,
	safeErrorMessage string,
	resultRevision *uint64,
) error {
	if err := validateTranscriptionFence(fence); err != nil {
		return err
	}
	if outcome == TranscriptionAttemptRunning {
		return fmt.Errorf("finish transcription attempt: running is not terminal")
	}
	safeErrorMessage = strings.TrimSpace(safeErrorMessage)
	if outcome == TranscriptionAttemptCompleted && safeErrorMessage != "" {
		return fmt.Errorf("finish transcription attempt: completed outcome cannot contain an error")
	}
	if outcome != TranscriptionAttemptCompleted && safeErrorMessage == "" {
		return fmt.Errorf("finish transcription attempt: terminal failure requires a safe error")
	}
	result := sql.NullInt64{}
	if resultRevision != nil {
		if outcome != TranscriptionAttemptCompleted || *resultRevision == 0 || *resultRevision > math.MaxInt64 {
			return fmt.Errorf("finish transcription attempt: invalid result revision")
		}
		result = sql.NullInt64{Int64: int64(*resultRevision), Valid: true}
	} else if outcome == TranscriptionAttemptCompleted {
		return fmt.Errorf("finish transcription attempt: completed outcome requires result revision")
	}
	sqlResult, err := queries.FinishTranscriptionJobAttemptManual(ctx, db.FinishTranscriptionJobAttemptManualParams{
		Outcome:          db.TranscriptionJobAttemptsOutcome(outcome),
		SafeErrorMessage: nullableString(safeErrorMessage),
		ResultRevision:   result,
		JobID:            fence.JobID,
		AttemptNumber:    fence.AttemptNumber,
		InputRevision:    fence.InputRevision,
		LeaseToken:       fence.LeaseToken,
	})
	return requireFenceAffected(sqlResult, err)
}

func requireOneAffected(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	if result == nil {
		return sql.ErrNoRows
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func requireFenceAffected(result sql.Result, err error) error {
	if err := requireOneAffected(result, err); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrTranscriptionJobFence
		}
		return err
	}
	return nil
}

func activeTranscriptionJobMatches(row db.TranscriptionJob, contextID sql.NullInt64, contextSnapshot json.RawMessage, inputRevision uint64) bool {
	contextMatches := row.ContextID.Valid == contextID.Valid
	if contextMatches && contextID.Valid {
		contextMatches = row.ContextID.Int64 > 0 && row.ContextID.Int64 == contextID.Int64
	}
	return contextMatches && row.InputRevision == inputRevision && equivalentJSON(row.ContextSnapshot, contextSnapshot)
}

func equivalentJSON(left, right []byte) bool {
	normalize := func(value []byte) ([]byte, error) {
		decoder := json.NewDecoder(bytes.NewReader(value))
		decoder.UseNumber()
		var decoded any
		if err := decoder.Decode(&decoded); err != nil {
			return nil, err
		}
		return json.Marshal(decoded)
	}
	leftNormalized, leftErr := normalize(left)
	rightNormalized, rightErr := normalize(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftNormalized, rightNormalized)
}

// SafeTranscriptionFailureMessage converts an internal or provider error into
// the bounded categorical value allowed in jobs, attempts, and CloudEvents.
func SafeTranscriptionFailureMessage(err error) string {
	if err == nil {
		return "transcription attempt failed"
	}
	return safeTranscriptionErrorMessage(err.Error())
}

func safeTranscriptionErrorMessage(message string) string {
	normalized := strings.ToLower(strings.TrimSpace(message))
	switch {
	case strings.Contains(normalized, "canonical annotation page") && strings.Contains(normalized, "changed"):
		return "canonical annotation page changed during transcription"
	case strings.Contains(normalized, "canonical annotation page") && (strings.Contains(normalized, "missing") || strings.Contains(normalized, "not found") || strings.Contains(normalized, "required")):
		return "canonical annotation page is unavailable"
	case strings.Contains(normalized, "context snapshot"):
		return "transcription context snapshot is invalid"
	case strings.Contains(normalized, "transcription job") && strings.Contains(normalized, "segments") && strings.Contains(normalized, "maximum"):
		return "transcription job exceeds configured segment limit"
	case strings.Contains(normalized, "workspace provider credential") && strings.Contains(normalized, "not configured"):
		return "workspace provider credential is not configured"
	case strings.Contains(normalized, "workspace provider credential") && strings.Contains(normalized, "invalid"):
		return "workspace provider credential is invalid"
	case strings.Contains(normalized, "worker shutting down"), strings.Contains(normalized, "context canceled"), strings.Contains(normalized, "deadline exceeded"):
		return "transcription attempt was interrupted"
	case strings.Contains(normalized, "provider"), strings.Contains(normalized, "transcription failed for"):
		return "transcription provider request failed"
	default:
		return "transcription attempt failed"
	}
}

func rowToTranscriptionJob(row db.TranscriptionJob) TranscriptionJob {
	var j TranscriptionJob
	j.ID = row.ID
	j.WorkspaceID = row.WorkspaceID
	j.ItemImageID = row.ItemImageID
	j.Status = TranscriptionJobStatus(row.Status)
	j.TotalSegments = int(row.TotalSegments)
	j.CompletedSegments = int(row.CompletedSegments)
	j.FailedSegments = int(row.FailedSegments)
	j.AttemptCount = int(row.AttemptCount)
	j.MaxAttempts = int(row.MaxAttempts)
	if j.MaxAttempts == 0 {
		j.MaxAttempts = 3
	}
	j.CreatedAt = row.CreatedAt
	j.UpdatedAt = row.UpdatedAt
	if v, ok := uint64PtrFromNullInt64(row.ContextID); ok {
		j.ContextID = v
	}
	j.ContextSnapshot = append(json.RawMessage(nil), row.ContextSnapshot...)
	j.InputRevision = row.InputRevision
	if row.CurrentAnnotationID.Valid {
		j.CurrentAnnotationID = row.CurrentAnnotationID.String
	}
	if row.CurrentAnnotationJson.Valid {
		j.CurrentAnnotationJSON = row.CurrentAnnotationJson.String
	}
	if row.LastResultAnnotationJson.Valid {
		j.LastResultAnnotationJSON = row.LastResultAnnotationJson.String
	}
	if row.ErrorMessage.Valid {
		j.ErrorMessage = row.ErrorMessage.String
	}
	if row.LockedBy.Valid {
		j.LeaseToken = row.LockedBy.String
	}
	return j
}

func rowToTranscriptionJobSummary(row db.TranscriptionJobSummary) TranscriptionJobSummary {
	job := TranscriptionJobSummary{
		ID:                row.ID,
		WorkspaceID:       row.WorkspaceID,
		ItemImageID:       row.ItemImageID,
		InputRevision:     row.InputRevision,
		Status:            TranscriptionJobStatus(row.Status),
		TotalSegments:     int(row.TotalSegments),
		CompletedSegments: int(row.CompletedSegments),
		FailedSegments:    int(row.FailedSegments),
		AttemptCount:      int(row.AttemptCount),
		MaxAttempts:       int(row.MaxAttempts),
		CreatedAt:         row.CreatedAt,
		UpdatedAt:         row.UpdatedAt,
	}
	if job.MaxAttempts == 0 {
		job.MaxAttempts = 3
	}
	if contextID, ok := uint64PtrFromNullInt64(row.ContextID); ok {
		job.ContextID = contextID
	}
	if row.CurrentAnnotationID.Valid {
		job.CurrentAnnotationID = row.CurrentAnnotationID.String
	}
	if row.ErrorMessage.Valid {
		job.ErrorMessage = row.ErrorMessage.String
	}
	return job
}

func staleExternalRequest(req db.SelectExternalRequestForUpdateManualRow) bool {
	return req.Status == db.ExternalRequestsStatusInProgress &&
		req.LeaseUntil.Valid &&
		time.Now().UTC().After(req.LeaseUntil.Time) &&
		int(req.AttemptCount) < int(req.MaxAttempts)
}

func exhaustedExternalRequest(req db.SelectExternalRequestForUpdateManualRow) bool {
	return req.Status == db.ExternalRequestsStatusInProgress &&
		req.LeaseUntil.Valid &&
		time.Now().UTC().After(req.LeaseUntil.Time) &&
		int(req.AttemptCount) >= int(req.MaxAttempts)
}

func failedExternalRequestExhausted(req db.SelectExternalRequestForUpdateManualRow) bool {
	return req.Status == db.ExternalRequestsStatusFailed &&
		int(req.AttemptCount) >= int(req.MaxAttempts)
}

func dbExternalRequestToStore(req db.SelectExternalRequestForUpdateManualRow) ExternalRequest {
	out := ExternalRequest{
		ID:             req.ID,
		WorkspaceID:    req.WorkspaceID,
		Source:         req.Source,
		IdempotencyKey: req.IdempotencyKey,
		RequestHash:    req.RequestHash,
		Status:         ExternalRequestStatus(req.Status),
		AttemptCount:   int(req.AttemptCount),
		MaxAttempts:    int(req.MaxAttempts),
	}
	if req.ItemID.Valid {
		out.ItemID = req.ItemID.String
	}
	if req.ItemImageID.Valid && req.ItemImageID.Int64 > 0 {
		out.ItemImageID = uint64(req.ItemImageID.Int64)
	}
	if req.TranscriptionJobID.Valid && req.TranscriptionJobID.Int64 > 0 {
		out.TranscriptionJobID = uint64(req.TranscriptionJobID.Int64)
	}
	if req.SessionID.Valid {
		out.SessionID = req.SessionID.String
	}
	if req.ErrorMessage.Valid {
		out.ErrorMessage = req.ErrorMessage.String
	}
	if req.LockedBy.Valid {
		out.LeaseOwner = req.LockedBy.String
	}
	return out
}

func dbExternalRequestModelToStore(req db.ExternalRequest) ExternalRequest {
	out := ExternalRequest{
		ID:             req.ID,
		WorkspaceID:    req.WorkspaceID,
		Source:         req.Source,
		IdempotencyKey: req.IdempotencyKey,
		RequestHash:    req.RequestHash,
		Status:         ExternalRequestStatus(req.Status),
		AttemptCount:   int(req.AttemptCount),
		MaxAttempts:    int(req.MaxAttempts),
	}
	if req.ItemID.Valid {
		out.ItemID = req.ItemID.String
	}
	if req.ItemImageID.Valid && req.ItemImageID.Int64 > 0 {
		out.ItemImageID = uint64(req.ItemImageID.Int64)
	}
	if req.TranscriptionJobID.Valid && req.TranscriptionJobID.Int64 > 0 {
		out.TranscriptionJobID = uint64(req.TranscriptionJobID.Int64)
	}
	if req.SessionID.Valid {
		out.SessionID = req.SessionID.String
	}
	if req.ErrorMessage.Valid {
		out.ErrorMessage = req.ErrorMessage.String
	}
	if req.LockedBy.Valid {
		out.LeaseOwner = req.LockedBy.String
	}
	return out
}

func dbWebhookDeliveryToStore(row db.ClaimWebhookDeliveriesManualRow) WebhookDelivery {
	out := WebhookDelivery{
		ID:             row.ID,
		EventID:        row.EventID,
		EventType:      row.EventType,
		BodyJSON:       row.BodyJson,
		SubscriptionID: row.SubscriptionID,
		TargetURL:      row.TargetUrl,
		SigningSecret:  append([]byte(nil), row.SigningSecret...),
		AttemptCount:   int(row.AttemptCount),
		MaxAttempts:    int(row.MaxAttempts),
	}
	if row.Subject.Valid {
		out.Subject = row.Subject.String
	}
	if row.LockedBy.Valid {
		out.LeaseOwner = row.LockedBy.String
	}
	return out
}

func nullableString(v string) sql.NullString {
	v = strings.TrimSpace(v)
	if v == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: v, Valid: true}
}

func nullableUint64(v uint64) sql.NullInt64 {
	if v == 0 || v > math.MaxInt64 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(v), Valid: true}
}

func newLeaseOwner(prefix string) string {
	prefix = strings.Trim(strings.ToLower(strings.TrimSpace(prefix)), "-")
	if prefix == "" {
		prefix = "lease"
	}
	return prefix + "-" + uuid.NewString()
}

func transcriptionLeaseOwner() string {
	hostname, err := os.Hostname()
	if err != nil || strings.TrimSpace(hostname) == "" {
		return "scribe-worker"
	}
	owner := "scribe-worker@" + strings.TrimSpace(hostname)
	if len(owner) > 128 {
		owner = owner[:128]
	}
	return owner
}
