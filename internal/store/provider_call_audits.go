package store

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	db "github.com/lehigh-university-libraries/scribe/internal/db"
)

const (
	maxProviderAuditSessionBytes   = 128
	maxProviderAuditProviderBytes  = 64
	maxProviderAuditModelBytes     = 255
	maxProviderAuditOperationBytes = 64
	maxProviderAuditErrorBytes     = 2 << 10
	providerAuditMinimumQuotaBytes = 512
	providerAuditRetentionBatch    = 500
)

type ProviderCallAudit struct {
	ID            uint64    `json:"id"`
	WorkspaceID   uint64    `json:"workspace_id"`
	SessionID     string    `json:"session_id,omitempty"`
	ItemImageID   *uint64   `json:"item_image_id,omitempty"`
	ContextID     *uint64   `json:"context_id,omitempty"`
	Provider      string    `json:"provider"`
	Model         string    `json:"model"`
	Operation     string    `json:"operation"`
	ErrorMessage  string    `json:"error_message,omitempty"`
	HTTPStatus    *int      `json:"http_status,omitempty"`
	DurationMS    int64     `json:"duration_ms"`
	DatabaseBytes uint64    `json:"-"`
	CreatedAt     time.Time `json:"created_at"`
}

type ItemProviderCallAudit struct {
	ProviderCallAudit
	ItemID            string `json:"item_id"`
	ItemImageSequence uint32 `json:"item_image_sequence,omitempty"`
	ItemImageLabel    string `json:"item_image_label,omitempty"`
}

type ProviderCallAuditStore struct {
	q     *db.Queries
	pool  *sql.DB
	quota StorageQuotaLimits
}

func NewProviderCallAuditStore(pool *sql.DB) *ProviderCallAuditStore {
	return &ProviderCallAuditStore{q: db.New(pool), pool: pool, quota: unboundedStorageQuotaLimits()}
}

// SetStorageQuotaLimits installs the same tenant and deployment byte ceiling
// used by canonical pages and OCR baselines. Provider call metadata is durable
// application state and may not bypass that boundary.
func (s *ProviderCallAuditStore) SetStorageQuotaLimits(limits StorageQuotaLimits) error {
	if s == nil {
		return fmt.Errorf("provider call audit store is not configured")
	}
	if err := validateStorageQuotaLimits(limits); err != nil {
		return err
	}
	s.quota = limits
	return nil
}

func (s *ProviderCallAuditStore) Create(ctx context.Context, audit ProviderCallAudit) error {
	if s == nil || s.pool == nil || s.q == nil {
		return fmt.Errorf("insert provider call audit: store is not configured")
	}
	normalized, err := normalizeProviderCallAudit(audit)
	if err != nil {
		return fmt.Errorf("insert provider call audit: %w", err)
	}
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("insert provider call audit: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockStorageQuotaGuards(ctx, tx, normalized.WorkspaceID); err != nil {
		return fmt.Errorf("insert provider call audit: lock storage usage: %w", err)
	}
	queries := s.q.WithTx(tx)
	if normalized.ItemImageID != nil {
		if _, lockErr := queries.LockItemImageForUseManual(ctx, db.LockItemImageForUseManualParams{
			ID:          *normalized.ItemImageID,
			WorkspaceID: normalized.WorkspaceID,
		}); lockErr != nil {
			return fmt.Errorf("insert provider call audit: lock item image: %w", lockErr)
		}
	}
	if normalized.ContextID != nil {
		if _, contextErr := queries.LockContextForUseManual(ctx, db.LockContextForUseManualParams{
			ContextID: *normalized.ContextID, WorkspaceID: nullableUint64(normalized.WorkspaceID),
		}); contextErr != nil {
			return fmt.Errorf("insert provider call audit: lock context: %w", contextErr)
		}
	}
	if normalized.WorkspaceID > math.MaxInt64 {
		return fmt.Errorf("insert provider call audit: workspace ID is out of range")
	}
	result, err := queries.InsertProviderCallAudit(ctx, db.InsertProviderCallAuditParams{
		AuditWorkspaceID:   int64(normalized.WorkspaceID), // #nosec G115 -- checked immediately below.
		AuditSessionID:     nullString(normalized.SessionID),
		AuditItemImageID:   uint64ToNullInt64(normalized.ItemImageID),
		AuditContextID:     uint64ToNullInt64(normalized.ContextID),
		AuditProvider:      normalized.Provider,
		AuditModel:         normalized.Model,
		AuditOperation:     normalized.Operation,
		AuditErrorMessage:  nullString(normalized.ErrorMessage),
		AuditHttpStatus:    providerAuditHTTPStatus(normalized.HTTPStatus),
		AuditDurationMs:    uint64(normalized.DurationMS), // #nosec G115 -- normalizeProviderCallAudit rejects negative durations.
		AuditDatabaseBytes: normalized.DatabaseBytes,
	})
	if err != nil {
		return fmt.Errorf("insert provider call audit: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("insert provider call audit: inspect ownership guard: %w", err)
	}
	if inserted != 1 {
		return fmt.Errorf("insert provider call audit: item image does not belong to workspace")
	}
	if err := applyStorageQuotaUsedDeltaWithLimits(ctx, queries, normalized.WorkspaceID, 0, normalized.DatabaseBytes, s.quota); err != nil {
		return fmt.Errorf("insert provider call audit: account durable storage: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("insert provider call audit: commit: %w", err)
	}
	return nil
}

func (s *ProviderCallAuditStore) ListByItem(ctx context.Context, workspaceID uint64, itemID string, limit int) ([]ItemProviderCallAudit, error) {
	if s == nil || s.q == nil || workspaceID == 0 || strings.TrimSpace(itemID) == "" {
		return nil, fmt.Errorf("list provider call audits by item: workspace and item are required")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := s.q.ListProviderCallAuditsByItem(ctx, db.ListProviderCallAuditsByItemParams{
		WorkspaceID: workspaceID, ItemID: strings.TrimSpace(itemID), Limit: int32(limit), // #nosec G115 -- bounded above.
	})
	if err != nil {
		return nil, fmt.Errorf("list provider call audits by item: %w", err)
	}
	audits := make([]ItemProviderCallAudit, 0, len(rows))
	for _, row := range rows {
		audit := ItemProviderCallAudit{
			ProviderCallAudit: ProviderCallAudit{
				ID: row.ID, WorkspaceID: row.WorkspaceID, SessionID: row.SessionID,
				Provider: row.Provider, Model: row.Model, Operation: row.Operation,
				ErrorMessage: row.ErrorMessage, DurationMS: providerAuditDuration(row.DurationMs), CreatedAt: row.CreatedAt,
			},
			ItemID: row.ItemID, ItemImageSequence: row.ItemImageSequence, ItemImageLabel: row.ItemImageLabel,
		}
		if value, ok := uint64PtrFromNullInt64(row.ItemImageID); ok {
			audit.ItemImageID = value
		}
		if value, ok := uint64PtrFromNullInt64(row.ContextID); ok {
			audit.ContextID = value
		}
		if row.HttpStatus.Valid {
			value := int(row.HttpStatus.Int32)
			audit.HTTPStatus = &value
		}
		audits = append(audits, audit)
	}
	return audits, nil
}

func (s *ProviderCallAuditStore) Retain(ctx context.Context, olderThan time.Duration) error {
	if s == nil || s.pool == nil || s.q == nil {
		return fmt.Errorf("retain provider call audits: store is not configured")
	}
	if olderThan <= 0 {
		olderThan = 30 * 24 * time.Hour
	}
	cutoff := time.Now().UTC().Add(-olderThan)
	afterWorkspaceID := uint64(0)
	for {
		workspaceIDs, err := s.q.ListExpiredProviderAuditWorkspaces(ctx, db.ListExpiredProviderAuditWorkspacesParams{
			Cutoff: cutoff, AfterWorkspaceID: afterWorkspaceID,
		})
		if err != nil {
			return fmt.Errorf("list retained provider call audit workspaces: %w", err)
		}
		for _, workspaceID := range workspaceIDs {
			for {
				deleted, retainErr := s.retainWorkspaceBatch(ctx, workspaceID, cutoff)
				if retainErr != nil {
					return retainErr
				}
				if deleted < providerAuditRetentionBatch {
					break
				}
			}
			afterWorkspaceID = workspaceID
		}
		if len(workspaceIDs) < 100 {
			return nil
		}
	}
}

func (s *ProviderCallAuditStore) retainWorkspaceBatch(ctx context.Context, workspaceID uint64, cutoff time.Time) (int, error) {
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("retain provider call audits: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockStorageQuotaGuards(ctx, tx, workspaceID); err != nil {
		return 0, fmt.Errorf("retain provider call audits: lock storage usage: %w", err)
	}
	queries := s.q.WithTx(tx)
	rows, err := queries.LockExpiredProviderAuditBatch(ctx, db.LockExpiredProviderAuditBatchParams{
		WorkspaceID: workspaceID, Cutoff: cutoff, Limit: providerAuditRetentionBatch,
	})
	if err != nil {
		return 0, fmt.Errorf("retain provider call audits: lock batch: %w", err)
	}
	if len(rows) == 0 {
		if err := tx.Commit(); err != nil {
			return 0, fmt.Errorf("retain provider call audits: commit empty batch: %w", err)
		}
		return 0, nil
	}
	var databaseBytes uint64
	for _, row := range rows {
		if math.MaxUint64-databaseBytes < row.DatabaseBytes {
			return 0, fmt.Errorf("retain provider call audits: database byte accounting overflow")
		}
		databaseBytes += row.DatabaseBytes
	}
	result, err := queries.DeleteExpiredProviderAuditBatch(ctx, db.DeleteExpiredProviderAuditBatchParams{
		WorkspaceID: workspaceID, Cutoff: cutoff, Limit: providerAuditRetentionBatch,
	})
	if err != nil {
		return 0, fmt.Errorf("retain provider call audits: delete batch: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("retain provider call audits: inspect deleted batch: %w", err)
	}
	if affected != int64(len(rows)) {
		return 0, fmt.Errorf("retain provider call audits: deleted %d rows, want %d", affected, len(rows))
	}
	if err := subtractStorageQuotaUsed(ctx, queries, workspaceID, StorageQuotaRequest{DurableBytes: databaseBytes}); err != nil {
		return 0, fmt.Errorf("retain provider call audits: release durable storage: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("retain provider call audits: commit: %w", err)
	}
	return len(rows), nil
}

func normalizeProviderCallAudit(audit ProviderCallAudit) (ProviderCallAudit, error) {
	if audit.WorkspaceID == 0 {
		return ProviderCallAudit{}, fmt.Errorf("workspace is required")
	}
	if audit.DurationMS < 0 {
		return ProviderCallAudit{}, fmt.Errorf("duration must not be negative")
	}
	if audit.ItemImageID != nil && (*audit.ItemImageID == 0 || *audit.ItemImageID > math.MaxInt64) {
		return ProviderCallAudit{}, fmt.Errorf("item image identifier is invalid")
	}
	if audit.ContextID != nil && (*audit.ContextID == 0 || *audit.ContextID > math.MaxInt64) {
		return ProviderCallAudit{}, fmt.Errorf("context identifier is invalid")
	}
	if audit.HTTPStatus != nil && (*audit.HTTPStatus < 100 || *audit.HTTPStatus > 599) {
		return ProviderCallAudit{}, fmt.Errorf("http status is invalid")
	}
	audit.SessionID = strings.TrimSpace(audit.SessionID)
	audit.Provider = strings.TrimSpace(audit.Provider)
	audit.Model = strings.TrimSpace(audit.Model)
	audit.Operation = strings.TrimSpace(audit.Operation)
	audit.ErrorMessage = strings.TrimSpace(audit.ErrorMessage)
	fields := []struct {
		name     string
		value    string
		maxBytes int
		required bool
	}{
		{name: "session", value: audit.SessionID, maxBytes: maxProviderAuditSessionBytes},
		{name: "provider", value: audit.Provider, maxBytes: maxProviderAuditProviderBytes, required: true},
		{name: "model", value: audit.Model, maxBytes: maxProviderAuditModelBytes},
		{name: "operation", value: audit.Operation, maxBytes: maxProviderAuditOperationBytes, required: true},
	}
	for _, field := range fields {
		if !utf8.ValidString(field.value) {
			return ProviderCallAudit{}, fmt.Errorf("%s is not valid UTF-8", field.name)
		}
		if field.required && field.value == "" {
			return ProviderCallAudit{}, fmt.Errorf("%s is required", field.name)
		}
		if len(field.value) > field.maxBytes {
			return ProviderCallAudit{}, fmt.Errorf("%s exceeds %d bytes", field.name, field.maxBytes)
		}
	}
	if !utf8.ValidString(audit.ErrorMessage) {
		return ProviderCallAudit{}, fmt.Errorf("error message is not valid UTF-8")
	}
	audit.ErrorMessage = truncateUTF8Bytes(audit.ErrorMessage, maxProviderAuditErrorBytes)
	audit.DatabaseBytes = uint64(len(audit.SessionID) + len(audit.Provider) + len(audit.Model) + len(audit.Operation) + len(audit.ErrorMessage))
	if audit.DatabaseBytes < providerAuditMinimumQuotaBytes {
		audit.DatabaseBytes = providerAuditMinimumQuotaBytes
	}
	return audit, nil
}

func truncateUTF8Bytes(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	const suffix = " [TRUNCATED]"
	end := maxBytes - len(suffix)
	if end < 0 {
		end = 0
	}
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end] + suffix
}

func providerAuditHTTPStatus(value *int) sql.NullInt32 {
	if value == nil {
		return sql.NullInt32{}
	}
	return sql.NullInt32{Int32: int32(*value), Valid: true} // #nosec G115 -- normalized to 100..599.
}

func providerAuditDuration(value uint64) int64 {
	if value > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(value) // #nosec G115 -- bounded above.
}

func nullString(v string) sql.NullString {
	if v == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: v, Valid: true}
}
