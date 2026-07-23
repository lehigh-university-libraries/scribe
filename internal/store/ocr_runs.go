package store

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"
	"time"

	db "github.com/lehigh-university-libraries/scribe/internal/db"
)

type OCRRun struct {
	SessionID           string    `json:"session_id"`
	ItemImageID         *uint64   `json:"item_image_id,omitempty"`
	ContextID           *uint64   `json:"context_id,omitempty"`
	ImageURL            string    `json:"image_url"`
	Provider            string    `json:"provider"`
	Model               string    `json:"model"`
	OriginalHOCR        string    `json:"original_hocr"`
	OriginalText        string    `json:"original_text"`
	CanonicalRevision   *uint64   `json:"canonical_revision,omitempty"`
	LevenshteinDistance int       `json:"levenshtein_distance"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

type OCRRunStore struct {
	q     *db.Queries
	pool  *sql.DB
	quota StorageQuotaLimits
}

func NewOCRRunStore(pool *sql.DB) *OCRRunStore {
	return &OCRRunStore{q: db.New(pool), pool: pool, quota: unboundedStorageQuotaLimits()}
}

// SetStorageQuotaLimits installs the same durable payload boundary used by
// canonical page transactions. Direct provenance imports may never bypass the
// configured workspace or deployment ceiling.
func (s *OCRRunStore) SetStorageQuotaLimits(limits StorageQuotaLimits) error {
	if s == nil {
		return fmt.Errorf("ocr run store is not configured")
	}
	if err := validateStorageQuotaLimits(limits); err != nil {
		return err
	}
	s.quota = limits
	return nil
}

// ContextMetrics holds aggregate statistics for all OCR runs belonging to a context.
type ContextMetrics struct {
	ContextID              uint64  `json:"context_id"`
	TotalRuns              int64   `json:"total_runs"`
	CorrectedRuns          int64   `json:"corrected_runs"`
	AvgLevenshteinDistance float64 `json:"avg_levenshtein_distance"`
}

// GetContextMetrics returns aggregate metrics for the workspace's current OCR
// runs in the given context. System contexts are shared, but their usage and
// correction metrics are never shared across workspaces.
func (s *OCRRunStore) GetContextMetrics(ctx context.Context, workspaceID, contextID uint64) (ContextMetrics, error) {
	metricsRow, err := s.q.GetContextOCRRunMetrics(ctx, workspaceID, contextID)
	if err != nil {
		return ContextMetrics{}, fmt.Errorf("get context metrics: %w", err)
	}
	return ContextMetrics{
		ContextID:              contextID,
		TotalRuns:              metricsRow.TotalRuns,
		CorrectedRuns:          metricsRow.CorrectedRuns,
		AvgLevenshteinDistance: metricsRow.AvgLevenshteinDistance,
	}, nil
}

func (s *OCRRunStore) Create(ctx context.Context, run OCRRun) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("insert OCR run: store is not configured")
	}
	if run.ItemImageID == nil || *run.ItemImageID == 0 {
		return fmt.Errorf("insert OCR run: item image is required")
	}
	image, err := s.q.GetItemImage(ctx, *run.ItemImageID)
	if err != nil {
		return fmt.Errorf("insert OCR run: load item image: %w", err)
	}
	item, err := s.q.GetItem(ctx, image.ItemID)
	if err != nil {
		return fmt.Errorf("insert OCR run: load item: %w", err)
	}
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin OCR run transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockStorageQuotaGuards(ctx, tx, item.WorkspaceID); err != nil {
		return fmt.Errorf("insert OCR run: lock storage usage: %w", err)
	}
	queries := s.q.WithTx(tx)
	if _, err := queries.LockItemImageForUseManual(ctx, db.LockItemImageForUseManualParams{
		ID:          *run.ItemImageID,
		WorkspaceID: item.WorkspaceID,
	}); err != nil {
		return fmt.Errorf("insert OCR run: lock item image: %w", err)
	}
	if run.ContextID != nil {
		if _, err := queries.LockContextForUseManual(ctx, db.LockContextForUseManualParams{
			ContextID:   *run.ContextID,
			WorkspaceID: nullableUint64(item.WorkspaceID),
		}); err != nil {
			return fmt.Errorf("insert OCR run: lock context: %w", err)
		}
	}
	before, err := itemImageDurableDatabaseBytes(ctx, queries, item.WorkspaceID, *run.ItemImageID)
	if err != nil {
		return fmt.Errorf("insert OCR run: measure prior durable storage: %w", err)
	}
	if err := insertCurrentOCRRun(ctx, queries, run); err != nil {
		return err
	}
	after, err := itemImageDurableDatabaseBytes(ctx, queries, item.WorkspaceID, *run.ItemImageID)
	if err != nil {
		return fmt.Errorf("insert OCR run: measure durable storage: %w", err)
	}
	if err := applyStorageQuotaUsedDeltaWithLimits(ctx, queries, item.WorkspaceID, before, after, s.quota); err != nil {
		return fmt.Errorf("insert OCR run: account durable storage: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit OCR run: %w", err)
	}
	return nil
}

func insertCurrentOCRRun(ctx context.Context, queries *db.Queries, run OCRRun) error {
	if queries == nil || strings.TrimSpace(run.SessionID) == "" || strings.TrimSpace(run.ImageURL) == "" {
		return fmt.Errorf("insert OCR run: session and image URL are required")
	}
	if run.ItemImageID == nil || *run.ItemImageID == 0 || *run.ItemImageID > math.MaxInt64 {
		return fmt.Errorf("insert OCR run: valid item image is required")
	}
	provider := strings.TrimSpace(run.Provider)
	if provider == "" {
		provider = "unknown"
	}
	if err := queries.InsertOCRRun(ctx, db.InsertOCRRunParams{
		SessionID:    strings.TrimSpace(run.SessionID),
		ItemImageID:  *run.ItemImageID,
		ContextID:    uint64ToNullInt64(run.ContextID),
		ImageURL:     strings.TrimSpace(run.ImageURL),
		Provider:     provider,
		Model:        strings.TrimSpace(run.Model),
		OriginalHocr: run.OriginalHOCR,
		OriginalText: run.OriginalText,
	}); err != nil {
		return fmt.Errorf("insert immutable OCR run: %w", err)
	}
	if err := queries.SetCurrentOCRRunManual(ctx, db.SetCurrentOCRRunManualParams{
		ItemImageID: *run.ItemImageID,
		SessionID:   strings.TrimSpace(run.SessionID),
	}); err != nil {
		return fmt.Errorf("set current OCR run: %w", err)
	}
	return nil
}

func (s *OCRRunStore) Get(ctx context.Context, sessionID string) (OCRRun, error) {
	row, err := s.q.GetOCRRun(ctx, sessionID)
	if err != nil {
		return OCRRun{}, fmt.Errorf("get ocr run: %w", err)
	}
	return rowToOCRRun(row), nil
}

func (s *OCRRunStore) GetByItemImageID(ctx context.Context, itemImageID uint64) (OCRRun, error) {
	row, err := s.q.GetOCRRunByItemImageID(ctx, itemImageID)
	if err != nil {
		return OCRRun{}, fmt.Errorf("get ocr run by item image id: %w", err)
	}
	return rowToOCRRun(row), nil
}

func uint64ToNullInt64(v *uint64) sql.NullInt64 {
	if v == nil {
		return sql.NullInt64{}
	}
	if *v > math.MaxInt64 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{
		Int64: int64(*v),
		Valid: true,
	}
}

func rowToOCRRun(row db.OCRRun) OCRRun {
	run := OCRRun{
		SessionID:           row.SessionID,
		ImageURL:            row.ImageURL,
		Provider:            row.Provider,
		Model:               row.Model,
		OriginalHOCR:        row.OriginalHocr,
		OriginalText:        row.OriginalText,
		LevenshteinDistance: int(row.LevenshteinDistance),
		CreatedAt:           row.CreatedAt,
		UpdatedAt:           row.UpdatedAt,
	}
	if row.ItemImageID > 0 {
		v := row.ItemImageID
		run.ItemImageID = &v
	}
	if row.ContextID.Valid && row.ContextID.Int64 > 0 {
		v := uint64(row.ContextID.Int64)
		run.ContextID = &v
	}
	if row.CanonicalRevision.Valid && row.CanonicalRevision.Int64 > 0 {
		revision := uint64(row.CanonicalRevision.Int64)
		run.CanonicalRevision = &revision
	}
	return run
}
