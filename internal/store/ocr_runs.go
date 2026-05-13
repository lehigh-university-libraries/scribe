package store

import (
	"context"
	"database/sql"
	"fmt"
	"math"
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
	CorrectedHOCR       *string   `json:"corrected_hocr,omitempty"`
	CorrectedText       *string   `json:"corrected_text,omitempty"`
	EditCount           int       `json:"edit_count"`
	LevenshteinDistance int       `json:"levenshtein_distance"`
	BoxEditCount        int       `json:"box_edit_count"`
	BoxesAdded          int       `json:"boxes_added"`
	BoxesDeleted        int       `json:"boxes_deleted"`
	BoxChangeScore      float64   `json:"box_change_score"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

type OCRRunStore struct {
	q *db.Queries
}

func NewOCRRunStore(pool *sql.DB) *OCRRunStore {
	return &OCRRunStore{q: db.New(pool)}
}

// ContextMetrics holds aggregate statistics for all OCR runs belonging to a context.
type ContextMetrics struct {
	ContextID              uint64  `json:"context_id"`
	TotalRuns              int64   `json:"total_runs"`
	CorrectedRuns          int64   `json:"corrected_runs"`
	AvgLevenshteinDistance float64 `json:"avg_levenshtein_distance"`
	AvgEditCount           float64 `json:"avg_edit_count"`
	AvgBoxChangeScore      float64 `json:"avg_box_change_score"`
}

// GetContextMetrics returns aggregate metrics for all OCR runs in the given context.
func (s *OCRRunStore) GetContextMetrics(ctx context.Context, contextID uint64) (ContextMetrics, error) {
	metricsRow, err := s.q.GetContextOCRRunMetrics(ctx, contextID)
	if err != nil {
		return ContextMetrics{}, fmt.Errorf("get context metrics: %w", err)
	}
	return ContextMetrics{
		ContextID:              contextID,
		TotalRuns:              metricsRow.TotalRuns,
		CorrectedRuns:          metricsRow.CorrectedRuns,
		AvgLevenshteinDistance: metricsRow.AvgLevenshteinDistance,
		AvgEditCount:           metricsRow.AvgEditCount,
		AvgBoxChangeScore:      metricsRow.AvgBoxChangeScore,
	}, nil
}

func (s *OCRRunStore) Create(ctx context.Context, run OCRRun) error {
	provider := run.Provider
	if provider == "" {
		provider = "unknown"
	}

	err := s.q.UpsertOCRRun(ctx, db.UpsertOCRRunParams{
		SessionID:    run.SessionID,
		ItemImageID:  uint64ToNullInt64(run.ItemImageID),
		ContextID:    uint64ToNullInt64(run.ContextID),
		ImageURL:     run.ImageURL,
		Provider:     provider,
		Model:        run.Model,
		OriginalHocr: run.OriginalHOCR,
		OriginalText: run.OriginalText,
	})
	if err != nil {
		return fmt.Errorf("insert ocr run: %w", err)
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

func (s *OCRRunStore) SaveEdits(
	ctx context.Context,
	sessionID, correctedHOCR, correctedText string,
	editCount, levenshteinDistance, boxEditCount, boxesAdded, boxesDeleted int,
	boxChangeScore float64,
) error {
	editCount32, err := int32FromInt(editCount)
	if err != nil {
		return err
	}
	levenshteinDistance32, err := int32FromInt(levenshteinDistance)
	if err != nil {
		return err
	}
	boxEditCount32, err := int32FromInt(boxEditCount)
	if err != nil {
		return err
	}
	boxesAdded32, err := int32FromInt(boxesAdded)
	if err != nil {
		return err
	}
	boxesDeleted32, err := int32FromInt(boxesDeleted)
	if err != nil {
		return err
	}
	err = s.q.SaveOCREdits(ctx, db.SaveOCREditsParams{
		CorrectedHocr:       correctedHOCR,
		CorrectedText:       correctedText,
		EditCount:           editCount32,
		LevenshteinDistance: levenshteinDistance32,
		BoxEditCount:        boxEditCount32,
		BoxesAdded:          boxesAdded32,
		BoxesDeleted:        boxesDeleted32,
		BoxChangeScore:      boxChangeScore,
		SessionID:           sessionID,
	})
	if err != nil {
		return fmt.Errorf("update ocr run edits: %w", err)
	}
	return nil
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
		EditCount:           int(row.EditCount),
		LevenshteinDistance: int(row.LevenshteinDistance),
		BoxEditCount:        int(row.BoxEditCount),
		BoxesAdded:          int(row.BoxesAdded),
		BoxesDeleted:        int(row.BoxesDeleted),
		BoxChangeScore:      row.BoxChangeScore,
		CreatedAt:           row.CreatedAt,
		UpdatedAt:           row.UpdatedAt,
	}
	if row.ItemImageID.Valid && row.ItemImageID.Int64 > 0 {
		v := uint64(row.ItemImageID.Int64)
		run.ItemImageID = &v
	}
	if row.ContextID.Valid && row.ContextID.Int64 > 0 {
		v := uint64(row.ContextID.Int64)
		run.ContextID = &v
	}
	if row.CorrectedHocr.Valid {
		run.CorrectedHOCR = &row.CorrectedHocr.String
	}
	if row.CorrectedText.Valid {
		run.CorrectedText = &row.CorrectedText.String
	}
	return run
}
