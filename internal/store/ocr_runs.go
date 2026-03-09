package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	db "github.com/lehigh-university-libraries/hOCRedit/internal/db"
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

	return run, nil
}

func (s *OCRRunStore) GetByItemImageID(ctx context.Context, itemImageID uint64) (OCRRun, error) {
	row, err := s.q.GetOCRRunByItemImageID(ctx, itemImageID)
	if err != nil {
		return OCRRun{}, fmt.Errorf("get ocr run by item image id: %w", err)
	}

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
	return run, nil
}

func (s *OCRRunStore) SaveEdits(
	ctx context.Context,
	sessionID, correctedHOCR, correctedText string,
	editCount, levenshteinDistance, boxEditCount, boxesAdded, boxesDeleted int,
	boxChangeScore float64,
) error {
	err := s.q.SaveOCREdits(ctx, db.SaveOCREditsParams{
		CorrectedHocr:       correctedHOCR,
		CorrectedText:       correctedText,
		EditCount:           int32(editCount),
		LevenshteinDistance: int32(levenshteinDistance),
		BoxEditCount:        int32(boxEditCount),
		BoxesAdded:          int32(boxesAdded),
		BoxesDeleted:        int32(boxesDeleted),
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
	return sql.NullInt64{
		Int64: int64(*v),
		Valid: true,
	}
}
