package db

// The store query adapters in this file are the sole mapping boundary from
// domain-shaped OCR-run values to sqlc-generated queries in ocr_runs.sql.

import (
	"context"
	"database/sql"
	"time"
)

type InsertOCRRunParams struct {
	SessionID    string
	ItemImageID  uint64
	ContextID    sql.NullInt64
	ImageURL     string
	Provider     string
	Model        string
	OriginalHocr string
	OriginalText string
}

func (q *Queries) InsertOCRRun(ctx context.Context, arg InsertOCRRunParams) error {
	itemImageID, err := uint64ToInt64(arg.ItemImageID)
	if err != nil {
		return err
	}
	res, err := q.InsertOCRRunManual(ctx, InsertOCRRunManualParams{
		SessionID:    arg.SessionID,
		ItemImageID:  sql.NullInt64{Int64: itemImageID, Valid: true},
		ContextID:    arg.ContextID,
		ImageUrl:     arg.ImageURL,
		Provider:     arg.Provider,
		Model:        arg.Model,
		OriginalHocr: arg.OriginalHocr,
		OriginalText: arg.OriginalText,
	})
	return requireAffectedRow(res, err)
}

type OCRRun struct {
	SessionID           string
	ItemImageID         uint64
	ContextID           sql.NullInt64
	ImageURL            string
	Provider            string
	Model               string
	OriginalHocr        string
	OriginalText        string
	CanonicalRevision   sql.NullInt64
	LevenshteinDistance int32
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

func (q *Queries) GetOCRRun(ctx context.Context, sessionID string) (OCRRun, error) {
	row, err := q.GetOCRRunManual(ctx, sessionID)
	if err != nil {
		return OCRRun{}, err
	}
	return OCRRun{
		SessionID:           row.SessionID,
		ItemImageID:         row.ItemImageID,
		ContextID:           row.ContextID,
		ImageURL:            row.ImageUrl,
		Provider:            row.Provider,
		Model:               row.Model,
		OriginalHocr:        row.OriginalHocr,
		OriginalText:        row.OriginalText,
		CanonicalRevision:   row.CanonicalRevision,
		LevenshteinDistance: row.LevenshteinDistance,
		CreatedAt:           row.CreatedAt,
		UpdatedAt:           row.UpdatedAt,
	}, nil
}

func (q *Queries) GetOCRRunByItemImageID(ctx context.Context, itemImageID uint64) (OCRRun, error) {
	row, err := q.GetOCRRunByItemImageIDManual(ctx, itemImageID)
	if err != nil {
		return OCRRun{}, err
	}
	return OCRRun{
		SessionID:           row.SessionID,
		ItemImageID:         row.ItemImageID,
		ContextID:           row.ContextID,
		ImageURL:            row.ImageUrl,
		Provider:            row.Provider,
		Model:               row.Model,
		OriginalHocr:        row.OriginalHocr,
		OriginalText:        row.OriginalText,
		CanonicalRevision:   row.CanonicalRevision,
		LevenshteinDistance: row.LevenshteinDistance,
		CreatedAt:           row.CreatedAt,
		UpdatedAt:           row.UpdatedAt,
	}, nil
}
