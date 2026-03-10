package db

import (
	"context"
	"database/sql"
	"time"
)

type DBTX interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type Queries struct {
	db DBTX
}

func New(db DBTX) *Queries {
	return &Queries{db: db}
}

type Session struct {
	ID        string
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type CreateSessionParams struct {
	ID   string
	Name string
}

func (q *Queries) ListSessions(ctx context.Context) ([]Session, error) {
	rows, err := q.db.QueryContext(ctx, `
SELECT id, name, created_at, updated_at
FROM sessions
ORDER BY created_at DESC
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Session, 0)
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.ID, &s.Name, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (q *Queries) GetSession(ctx context.Context, id string) (Session, error) {
	var s Session
	err := q.db.QueryRowContext(ctx, `
SELECT id, name, created_at, updated_at
FROM sessions
WHERE id = ?
`, id).Scan(&s.ID, &s.Name, &s.CreatedAt, &s.UpdatedAt)
	return s, err
}

func (q *Queries) CreateSession(ctx context.Context, arg CreateSessionParams) error {
	_, err := q.db.ExecContext(ctx, `
INSERT INTO sessions (id, name)
VALUES (?, ?)
`, arg.ID, arg.Name)
	return err
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
	_, err := q.db.ExecContext(ctx, `
INSERT INTO ocr_runs (
  session_id, item_image_id, context_id, image_url, provider, model, original_hocr, original_text
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
  item_image_id = COALESCE(VALUES(item_image_id), item_image_id),
  context_id = COALESCE(VALUES(context_id), context_id),
  image_url = VALUES(image_url),
  provider = VALUES(provider),
  model = VALUES(model),
  original_hocr = VALUES(original_hocr),
  original_text = VALUES(original_text)
`, arg.SessionID, arg.ItemImageID, arg.ContextID, arg.ImageURL, arg.Provider, arg.Model, arg.OriginalHocr, arg.OriginalText)
	return err
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
	var run OCRRun
	err := q.db.QueryRowContext(ctx, `
SELECT
  session_id, item_image_id, context_id, image_url, provider, model, original_hocr, original_text,
  corrected_hocr, corrected_text, edit_count, levenshtein_distance,
  box_edit_count, boxes_added, boxes_deleted, box_change_score,
  created_at, updated_at
FROM ocr_runs
WHERE session_id = ?
`, sessionID).Scan(
		&run.SessionID,
		&run.ItemImageID,
		&run.ContextID,
		&run.ImageURL,
		&run.Provider,
		&run.Model,
		&run.OriginalHocr,
		&run.OriginalText,
		&run.CorrectedHocr,
		&run.CorrectedText,
		&run.EditCount,
		&run.LevenshteinDistance,
		&run.BoxEditCount,
		&run.BoxesAdded,
		&run.BoxesDeleted,
		&run.BoxChangeScore,
		&run.CreatedAt,
		&run.UpdatedAt,
	)
	return run, err
}

func (q *Queries) GetOCRRunByItemImageID(ctx context.Context, itemImageID uint64) (OCRRun, error) {
	var run OCRRun
	err := q.db.QueryRowContext(ctx, `
SELECT
  session_id, item_image_id, context_id, image_url, provider, model, original_hocr, original_text,
  corrected_hocr, corrected_text, edit_count, levenshtein_distance,
  box_edit_count, boxes_added, boxes_deleted, box_change_score,
  created_at, updated_at
FROM ocr_runs
WHERE item_image_id = ?
`, itemImageID).Scan(
		&run.SessionID,
		&run.ItemImageID,
		&run.ContextID,
		&run.ImageURL,
		&run.Provider,
		&run.Model,
		&run.OriginalHocr,
		&run.OriginalText,
		&run.CorrectedHocr,
		&run.CorrectedText,
		&run.EditCount,
		&run.LevenshteinDistance,
		&run.BoxEditCount,
		&run.BoxesAdded,
		&run.BoxesDeleted,
		&run.BoxChangeScore,
		&run.CreatedAt,
		&run.UpdatedAt,
	)
	return run, err
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
	res, err := q.db.ExecContext(ctx, `
UPDATE ocr_runs
SET corrected_hocr = ?, corrected_text = ?, edit_count = ?, levenshtein_distance = ?,
    box_edit_count = ?, boxes_added = ?, boxes_deleted = ?, box_change_score = ?
WHERE session_id = ?
`, arg.CorrectedHocr, arg.CorrectedText, arg.EditCount, arg.LevenshteinDistance, arg.BoxEditCount, arg.BoxesAdded, arg.BoxesDeleted, arg.BoxChangeScore, arg.SessionID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}
