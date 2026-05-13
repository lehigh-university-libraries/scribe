package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	db "github.com/lehigh-university-libraries/scribe/internal/db"
)

type TranscriptionJobStatus string

const (
	TranscriptionJobStatusPending   TranscriptionJobStatus = "pending"
	TranscriptionJobStatusRunning   TranscriptionJobStatus = "running"
	TranscriptionJobStatusCompleted TranscriptionJobStatus = "completed"
	TranscriptionJobStatusFailed    TranscriptionJobStatus = "failed"
)

type TranscriptionJob struct {
	ID                       uint64                 `json:"id"`
	ItemImageID              uint64                 `json:"item_image_id"`
	ContextID                *uint64                `json:"context_id,omitempty"`
	Status                   TranscriptionJobStatus `json:"status"`
	TotalSegments            int                    `json:"total_segments"`
	CompletedSegments        int                    `json:"completed_segments"`
	FailedSegments           int                    `json:"failed_segments"`
	AttemptCount             int                    `json:"attempt_count"`
	MaxAttempts              int                    `json:"max_attempts"`
	LeaseOwner               string                 `json:"-"`
	CurrentAnnotationID      string                 `json:"current_annotation_id,omitempty"`
	CurrentAnnotationJSON    string                 `json:"current_annotation_json,omitempty"`
	LastResultAnnotationJSON string                 `json:"last_result_annotation_json,omitempty"`
	ErrorMessage             string                 `json:"error_message,omitempty"`
	CreatedAt                time.Time              `json:"created_at"`
	UpdatedAt                time.Time              `json:"updated_at"`
}

type TranscriptionJobStore struct {
	q    *db.Queries
	pool *sql.DB
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
	Status             ExternalRequestStatus
	ItemID             string
	ItemImageID        uint64
	TranscriptionJobID uint64
	AttemptCount       int
	MaxAttempts        int
	ErrorMessage       string
}

type WebhookDelivery struct {
	ID           uint64
	EventID      string
	EventType    string
	Subject      string
	BodyJSON     string
	TargetURL    string
	LeaseOwner   string
	AttemptCount int
	MaxAttempts  int
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
	return &TranscriptionJobStore{q: db.New(pool), pool: pool}
}

func (s *TranscriptionJobStore) Create(ctx context.Context, itemImageID uint64, contextID *uint64) (uint64, error) {
	id, err := s.q.CreateTranscriptionJob(ctx, db.CreateTranscriptionJobParams{
		ItemImageID: itemImageID,
		ContextID:   contextID,
	})
	if err != nil {
		return 0, fmt.Errorf("create transcription job: %w", err)
	}
	return id, nil
}

func (s *TranscriptionJobStore) Get(ctx context.Context, id uint64) (TranscriptionJob, error) {
	row, err := s.q.GetTranscriptionJob(ctx, id)
	if err != nil {
		return TranscriptionJob{}, fmt.Errorf("get transcription job: %w", err)
	}
	return rowToTranscriptionJob(row), nil
}

func (s *TranscriptionJobStore) ListByItemImage(ctx context.Context, itemImageID uint64) ([]TranscriptionJob, error) {
	rows, err := s.q.ListTranscriptionJobsByItemImage(ctx, itemImageID)
	if err != nil {
		return nil, fmt.Errorf("list transcription jobs: %w", err)
	}
	jobs := make([]TranscriptionJob, 0, len(rows))
	for _, row := range rows {
		jobs = append(jobs, rowToTranscriptionJob(row))
	}
	return jobs, nil
}

func (s *TranscriptionJobStore) ListByWorkspace(ctx context.Context, workspaceID uint64) ([]TranscriptionJob, error) {
	rows, err := s.q.ListTranscriptionJobsByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list transcription jobs by workspace: %w", err)
	}
	jobs := make([]TranscriptionJob, 0, len(rows))
	for _, row := range rows {
		jobs = append(jobs, rowToTranscriptionJob(row))
	}
	return jobs, nil
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
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	workerID := newLeaseOwner("worker")
	leaseUntil := time.Now().UTC().Add(10 * time.Minute)
	qtx := s.q.WithTx(tx)
	var job db.TranscriptionJob
	if cutoff.IsZero() {
		job, err = qtx.ClaimNextLeasedTranscriptionJob(ctx, leaseUntil, workerID)
	} else {
		job, err = qtx.ClaimNextLeasedTranscriptionJobOlderThan(ctx, cutoff, leaseUntil, workerID)
	}
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query pending job: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit claim: %w", err)
	}

	claimed, err := s.Get(ctx, job.ID)
	if err != nil {
		return nil, err
	}
	return &claimed, nil
}

func (s *TranscriptionJobStore) ClaimPendingByID(ctx context.Context, id uint64) (*TranscriptionJob, error) {
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	workerID := newLeaseOwner("worker")
	leaseUntil := time.Now().UTC().Add(10 * time.Minute)
	qtx := s.q.WithTx(tx)
	job, err := qtx.ClaimLeasedTranscriptionJobByID(ctx, id, leaseUntil, workerID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query job %d: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit claim job %d: %w", id, err)
	}

	claimed, err := s.Get(ctx, job.ID)
	if err != nil {
		return nil, err
	}
	return &claimed, nil
}

// SetTotalSegments sets the total segment count at the start of a job run.
func (s *TranscriptionJobStore) SetTotalSegments(ctx context.Context, id uint64, leaseOwner string, total int) error {
	converted, err := int32FromInt(total)
	if err != nil {
		return err
	}
	return s.q.SetTranscriptionJobTotalSegments(ctx, id, leaseOwner, converted)
}

// UpdateProgress records per-segment progress after each annotation is processed.
func (s *TranscriptionJobStore) UpdateProgress(ctx context.Context, id uint64, leaseOwner string,
	completed, failed int,
	currentAnnotationID, currentAnnotationJSON, lastResultAnnotationJSON string,
) error {
	completed32, err := int32FromInt(completed)
	if err != nil {
		return err
	}
	failed32, err := int32FromInt(failed)
	if err != nil {
		return err
	}
	return s.q.UpdateTranscriptionJobProgress(ctx, db.UpdateTranscriptionJobProgressParams{
		ID:                       id,
		LockedBy:                 nullableString(leaseOwner),
		CompletedSegments:        completed32,
		FailedSegments:           failed32,
		CurrentAnnotationID:      currentAnnotationID,
		CurrentAnnotationJSON:    currentAnnotationJSON,
		LastResultAnnotationJSON: lastResultAnnotationJSON,
	})
}

// Complete marks the job as completed and clears the current-segment fields.
func (s *TranscriptionJobStore) Complete(ctx context.Context, id uint64, leaseOwner string) error {
	return s.q.CompleteTranscriptionJobLeased(ctx, id, leaseOwner)
}

func (s *TranscriptionJobStore) ExtendLease(ctx context.Context, id uint64, leaseOwner string, leaseDuration time.Duration) error {
	if leaseDuration <= 0 {
		leaseDuration = 10 * time.Minute
	}
	return s.q.ExtendTranscriptionJobLease(ctx, id, leaseOwner, time.Now().UTC().Add(leaseDuration))
}

func (s *TranscriptionJobStore) CompleteWithWebhookEvent(ctx context.Context, id uint64, leaseOwner, eventID, eventType, subject, bodyJSON string, targets []string) error {
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.q.WithTx(tx)
	if err := qtx.CompleteTranscriptionJobLeased(ctx, id, leaseOwner); err != nil {
		return err
	}
	if strings.TrimSpace(eventID) != "" && strings.TrimSpace(bodyJSON) != "" {
		if err := qtx.InsertEventOutbox(ctx, eventID, eventType, s.eventWorkspaceID(ctx, qtx, subject), nullableString(subject), bodyJSON); err != nil {
			return err
		}
		for _, target := range targets {
			target = strings.TrimSpace(target)
			if target == "" {
				continue
			}
			if err := qtx.InsertWebhookDeliveryIfMissing(ctx, eventID, target, webhookTargetHash(target)); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (s *TranscriptionJobStore) FailWithWebhookEvent(ctx context.Context, id uint64, leaseOwner, errMsg, eventID, eventType, subject, bodyJSON string, targets []string) error {
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.q.WithTx(tx)
	if err := qtx.RetryOrFailTranscriptionJob(ctx, id, leaseOwner, nullableString(errMsg)); err != nil {
		return err
	}
	if strings.TrimSpace(eventID) != "" && strings.TrimSpace(bodyJSON) != "" {
		if err := qtx.InsertEventOutbox(ctx, eventID, eventType, s.eventWorkspaceID(ctx, qtx, subject), nullableString(subject), bodyJSON); err != nil {
			return err
		}
		for _, target := range targets {
			target = strings.TrimSpace(target)
			if target == "" {
				continue
			}
			if err := qtx.InsertWebhookDeliveryIfMissing(ctx, eventID, target, webhookTargetHash(target)); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// Fail records an error and either schedules a retry or marks the job failed.
func (s *TranscriptionJobStore) Fail(ctx context.Context, id uint64, leaseOwner, errMsg string) error {
	return s.q.RetryOrFailTranscriptionJob(ctx, id, leaseOwner, nullableString(errMsg))
}

func (s *TranscriptionJobStore) Defer(ctx context.Context, id uint64, leaseOwner, errMsg string, retryAfter time.Time) error {
	return s.q.DeferTranscriptionJobLease(ctx, id, leaseOwner, retryAfter, nullableString(errMsg))
}

func (s *TranscriptionJobStore) WorkspaceOwnsJob(ctx context.Context, workspaceID, jobID uint64) (bool, error) {
	return s.q.WorkspaceOwnsTranscriptionJob(ctx, workspaceID, jobID)
}

func (s *TranscriptionJobStore) ReserveExternalRequest(ctx context.Context, workspaceID uint64, source, key, eventHeader string) (ExternalRequest, bool, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		source = "external"
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return ExternalRequest{}, false, fmt.Errorf("idempotency key is required")
	}

	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return ExternalRequest{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	leaseUntil := time.Now().UTC().Add(10 * time.Minute)
	lockedBy := newLeaseOwner("request")
	qtx := s.q.WithTx(tx)

	reqRow, err := qtx.SelectExternalRequestForUpdate(ctx, db.SelectExternalRequestForUpdateManualParams{
		WorkspaceID:    workspaceID,
		Source:         source,
		IdempotencyKey: key,
	})
	if err == sql.ErrNoRows {
		id, insertErr := qtx.InsertExternalRequest(ctx, db.InsertExternalRequestManualParams{
			WorkspaceID:    workspaceID,
			Source:         source,
			IdempotencyKey: key,
			EventHeader:    nullableString(eventHeader),
			LeaseUntil:     sql.NullTime{Time: leaseUntil, Valid: true},
			LockedBy:       nullableString(lockedBy),
		})
		if insertErr != nil {
			return ExternalRequest{}, false, insertErr
		}
		if err := tx.Commit(); err != nil {
			return ExternalRequest{}, false, err
		}
		return ExternalRequest{ID: id, WorkspaceID: workspaceID, Source: source, IdempotencyKey: key, Status: ExternalRequestStatusInProgress}, true, nil
	}
	if err != nil {
		return ExternalRequest{}, false, err
	}
	req := dbExternalRequestToStore(reqRow)
	if exhaustedExternalRequest(reqRow) {
		if err := qtx.FailExternalRequest(ctx, db.FailExternalRequestManualParams{
			WorkspaceID:    workspaceID,
			Source:         source,
			IdempotencyKey: key,
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
			EventHeader: nullableString(eventHeader),
			LeaseUntil:  sql.NullTime{Time: leaseUntil, Valid: true},
			LockedBy:    nullableString(lockedBy),
		}); err != nil {
			return ExternalRequest{}, false, err
		}
		req.Status = ExternalRequestStatusInProgress
		req.ErrorMessage = ""
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

func (s *TranscriptionJobStore) CompleteExternalRequest(ctx context.Context, workspaceID uint64, source, key, itemID string, itemImageID, jobID uint64) error {
	return s.q.CompleteExternalRequest(ctx, db.CompleteExternalRequestManualParams{
		WorkspaceID:        workspaceID,
		Source:             source,
		IdempotencyKey:     key,
		ItemID:             nullableString(itemID),
		ItemImageID:        nullableUint64(itemImageID),
		TranscriptionJobID: nullableUint64(jobID),
	})
}

func (s *TranscriptionJobStore) FailExternalRequest(ctx context.Context, workspaceID uint64, source, key, errMsg string) error {
	return s.q.FailExternalRequest(ctx, db.FailExternalRequestManualParams{
		WorkspaceID:    workspaceID,
		Source:         source,
		IdempotencyKey: key,
		ErrorMessage:   nullableString(errMsg),
	})
}

func (s *TranscriptionJobStore) EnqueueWebhookEvent(ctx context.Context, eventID, eventType, subject, bodyJSON string, targets []string) error {
	if strings.TrimSpace(eventID) == "" || strings.TrimSpace(bodyJSON) == "" {
		return nil
	}
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	qtx := s.q.WithTx(tx)
	if err := qtx.InsertEventOutbox(ctx, eventID, eventType, s.eventWorkspaceID(ctx, qtx, subject), nullableString(subject), bodyJSON); err != nil {
		return err
	}
	for _, target := range targets {
		target = strings.TrimSpace(target)
		if target == "" {
			continue
		}
		if err := qtx.InsertWebhookDeliveryIfMissing(ctx, eventID, target, webhookTargetHash(target)); err != nil {
			return err
		}
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
	return s.q.MarkWebhookDeliveryFailed(ctx, id, leaseOwner, nullableString(errMsg))
}

func (s *TranscriptionJobStore) RetainWebhookEvents(ctx context.Context, olderThan time.Duration) error {
	if olderThan <= 0 {
		olderThan = 30 * 24 * time.Hour
	}
	cutoff := time.Now().UTC().Add(-olderThan)
	if err := s.q.DeleteDeliveredWebhookDeliveriesBeforeManual(ctx, cutoff); err != nil {
		return err
	}
	return s.q.DeleteEventOutboxBeforeManual(ctx, cutoff)
}

func (s *TranscriptionJobStore) EventOutboxHighWater(ctx context.Context) (uint64, error) {
	return s.q.GetEventOutboxHighWater(ctx)
}

func (s *TranscriptionJobStore) ListEventOutboxAfter(ctx context.Context, afterID uint64, limit int) ([]EventOutboxRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.q.ListEventOutboxAfterID(ctx, afterID, limit)
	if err != nil {
		return nil, err
	}
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
	return events, nil
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

func (s *TranscriptionJobStore) eventWorkspaceID(ctx context.Context, q *db.Queries, subject string) sql.NullInt64 {
	itemImageID, ok := itemImageIDFromSubject(subject)
	if !ok {
		return sql.NullInt64{}
	}
	workspaceID, err := q.GetWorkspaceIDForItemImageManual(ctx, itemImageID)
	if err != nil {
		return sql.NullInt64{}
	}
	return nullableUint64(workspaceID)
}

func itemImageIDFromSubject(subject string) (uint64, bool) {
	raw, ok := strings.CutPrefix(strings.TrimSpace(subject), "item-images/")
	if !ok || raw == "" || strings.Contains(raw, "/") {
		return 0, false
	}
	id, err := strconv.ParseUint(raw, 10, 64)
	return id, err == nil && id > 0
}

func rowToTranscriptionJob(row db.TranscriptionJob) TranscriptionJob {
	var j TranscriptionJob
	j.ID = row.ID
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
		j.LeaseOwner = row.LockedBy.String
	}
	return j
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

func dbExternalRequestToStore(req db.SelectExternalRequestForUpdateManualRow) ExternalRequest {
	out := ExternalRequest{
		ID:             req.ID,
		WorkspaceID:    req.WorkspaceID,
		Source:         req.Source,
		IdempotencyKey: req.IdempotencyKey,
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
	if req.ErrorMessage.Valid {
		out.ErrorMessage = req.ErrorMessage.String
	}
	return out
}

func dbWebhookDeliveryToStore(row db.ClaimWebhookDeliveriesManualRow) WebhookDelivery {
	out := WebhookDelivery{
		ID:           row.ID,
		EventID:      row.EventID,
		EventType:    row.EventType,
		BodyJSON:     row.BodyJson,
		TargetURL:    row.TargetUrl,
		AttemptCount: int(row.AttemptCount),
		MaxAttempts:  int(row.MaxAttempts),
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

func webhookTargetHash(target string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(target)))
	return hex.EncodeToString(sum[:])
}

func newLeaseOwner(prefix string) string {
	prefix = strings.Trim(strings.ToLower(strings.TrimSpace(prefix)), "-")
	if prefix == "" {
		prefix = "lease"
	}
	return prefix + "-" + uuid.NewString()
}
