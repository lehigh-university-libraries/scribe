package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type OCRRun struct {
	SessionID           string     `json:"session_id"`
	ImageURL            string     `json:"image_url"`
	Model               string     `json:"model"`
	OriginalHOCR        string     `json:"original_hocr"`
	OriginalText        string     `json:"original_text"`
	CorrectedHOCR       *string    `json:"corrected_hocr,omitempty"`
	CorrectedText       *string    `json:"corrected_text,omitempty"`
	EditCount           int        `json:"edit_count"`
	LevenshteinDistance int        `json:"levenshtein_distance"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

type OCRRunStore struct {
	db *sql.DB
}

func NewOCRRunStore(db *sql.DB) *OCRRunStore {
	return &OCRRunStore{db: db}
}

func (s *OCRRunStore) Create(ctx context.Context, run OCRRun) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO ocr_runs (
  session_id, image_url, model, original_hocr, original_text
) VALUES (?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
  image_url = VALUES(image_url),
  model = VALUES(model),
  original_hocr = VALUES(original_hocr),
  original_text = VALUES(original_text)
`, run.SessionID, run.ImageURL, run.Model, run.OriginalHOCR, run.OriginalText)
	if err != nil {
		return fmt.Errorf("insert ocr run: %w", err)
	}
	return nil
}

func (s *OCRRunStore) Get(ctx context.Context, sessionID string) (OCRRun, error) {
	var run OCRRun
	var correctedHOCR, correctedText sql.NullString
	err := s.db.QueryRowContext(ctx, `
SELECT
  session_id, image_url, model, original_hocr, original_text,
  corrected_hocr, corrected_text, edit_count, levenshtein_distance,
  created_at, updated_at
FROM ocr_runs
WHERE session_id = ?
`, sessionID).Scan(
		&run.SessionID,
		&run.ImageURL,
		&run.Model,
		&run.OriginalHOCR,
		&run.OriginalText,
		&correctedHOCR,
		&correctedText,
		&run.EditCount,
		&run.LevenshteinDistance,
		&run.CreatedAt,
		&run.UpdatedAt,
	)
	if err != nil {
		return OCRRun{}, fmt.Errorf("get ocr run: %w", err)
	}

	if correctedHOCR.Valid {
		run.CorrectedHOCR = &correctedHOCR.String
	}
	if correctedText.Valid {
		run.CorrectedText = &correctedText.String
	}

	return run, nil
}

func (s *OCRRunStore) SaveEdits(ctx context.Context, sessionID, correctedHOCR, correctedText string, editCount, levenshteinDistance int) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE ocr_runs
SET corrected_hocr = ?, corrected_text = ?, edit_count = ?, levenshtein_distance = ?
WHERE session_id = ?
`, correctedHOCR, correctedText, editCount, levenshteinDistance, sessionID)
	if err != nil {
		return fmt.Errorf("update ocr run edits: %w", err)
	}
	return nil
}
