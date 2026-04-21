package db

// Compatibility wrappers in this file preserve the older store-facing API while
// delegating SQL execution to sqlc-generated queries in sessions.sql and
// ocr_runs.sql.

import (
	"context"
	"database/sql"
	"time"
)

type CreateSessionParams struct {
	ID   string
	Name string
}

func (q *Queries) ListSessions(ctx context.Context) ([]Session, error) {
	return q.ListSessionsManual(ctx)
}

func (q *Queries) GetSession(ctx context.Context, id string) (Session, error) {
	return q.GetSessionManual(ctx, id)
}

func (q *Queries) CreateSession(ctx context.Context, arg CreateSessionParams) error {
	return q.CreateSessionManual(ctx, CreateSessionManualParams{
		ID:   arg.ID,
		Name: arg.Name,
	})
}

type UpsertOCRRunParams struct {
	SessionID    string
	ItemImageID  sql.NullInt64
	ContextID    sql.NullInt64
	ImageURL     string
	Provider     string
	Model        string
	OriginalHocr string
	OriginalText string
}

func (q *Queries) UpsertOCRRun(ctx context.Context, arg UpsertOCRRunParams) error {
	return q.UpsertOCRRunManual(ctx, UpsertOCRRunManualParams{
		SessionID:    arg.SessionID,
		ItemImageID:  arg.ItemImageID,
		ContextID:    arg.ContextID,
		ImageUrl:     arg.ImageURL,
		Provider:     arg.Provider,
		Model:        arg.Model,
		OriginalHocr: arg.OriginalHocr,
		OriginalText: arg.OriginalText,
	})
}

type OCRRun struct {
	SessionID           string
	ItemImageID         sql.NullInt64
	ContextID           sql.NullInt64
	ImageURL            string
	Provider            string
	Model               string
	OriginalHocr        string
	OriginalText        string
	CorrectedHocr       sql.NullString
	CorrectedText       sql.NullString
	EditCount           int32
	LevenshteinDistance int32
	BoxEditCount        int32
	BoxesAdded          int32
	BoxesDeleted        int32
	BoxChangeScore      float64
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
		CorrectedHocr:       row.CorrectedHocr,
		CorrectedText:       row.CorrectedText,
		EditCount:           row.EditCount,
		LevenshteinDistance: row.LevenshteinDistance,
		BoxEditCount:        row.BoxEditCount,
		BoxesAdded:          row.BoxesAdded,
		BoxesDeleted:        row.BoxesDeleted,
		BoxChangeScore:      row.BoxChangeScore,
		CreatedAt:           row.CreatedAt,
		UpdatedAt:           row.UpdatedAt,
	}, nil
}

func (q *Queries) GetOCRRunByItemImageID(ctx context.Context, itemImageID uint64) (OCRRun, error) {
	row, err := q.GetOCRRunByItemImageIDManual(ctx, sql.NullInt64{Int64: int64(itemImageID), Valid: true})
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
		CorrectedHocr:       row.CorrectedHocr,
		CorrectedText:       row.CorrectedText,
		EditCount:           row.EditCount,
		LevenshteinDistance: row.LevenshteinDistance,
		BoxEditCount:        row.BoxEditCount,
		BoxesAdded:          row.BoxesAdded,
		BoxesDeleted:        row.BoxesDeleted,
		BoxChangeScore:      row.BoxChangeScore,
		CreatedAt:           row.CreatedAt,
		UpdatedAt:           row.UpdatedAt,
	}, nil
}

type SaveOCREditsParams struct {
	CorrectedHocr       string
	CorrectedText       string
	EditCount           int32
	LevenshteinDistance int32
	BoxEditCount        int32
	BoxesAdded          int32
	BoxesDeleted        int32
	BoxChangeScore      float64
	SessionID           string
}

func (q *Queries) SaveOCREdits(ctx context.Context, arg SaveOCREditsParams) error {
	res, err := q.db.ExecContext(ctx, saveOCREditsManual,
		sql.NullString{String: arg.CorrectedHocr, Valid: true},
		sql.NullString{String: arg.CorrectedText, Valid: true},
		arg.EditCount,
		arg.LevenshteinDistance,
		arg.BoxEditCount,
		arg.BoxesAdded,
		arg.BoxesDeleted,
		arg.BoxChangeScore,
		arg.SessionID,
	)
	if err != nil {
		return err
	}
	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return sql.ErrNoRows
	}
	return nil
}
