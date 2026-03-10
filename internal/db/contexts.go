package db

import (
	"context"
	"database/sql"
	"time"
)

// Context maps to the contexts table.
type Context struct {
	ID                    uint64
	UserID                sql.NullInt64 // NULL = system context
	Name                  string
	Description           sql.NullString
	IsDefault             bool
	SegmentationModel     string
	ImagePreprocessors    sql.NullString // JSON array
	TranscriptionProvider string
	TranscriptionModel    string
	Temperature           sql.NullFloat64
	SystemPrompt          sql.NullString
	PostProcessingSteps   sql.NullString // JSON array
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

// ContextSelectionRule maps to context_selection_rules.
type ContextSelectionRule struct {
	ID         uint64
	ContextID  uint64
	Priority   int32
	Conditions string // JSON array of RuleCondition objects
	CreatedAt  time.Time
}

// --- contexts ---

type CreateContextParams struct {
	UserID                *uint64 // nil = system context
	Name                  string
	Description           string
	IsDefault             bool
	SegmentationModel     string
	ImagePreprocessors    string // JSON; empty = NULL
	TranscriptionProvider string
	TranscriptionModel    string
	Temperature           *float64 // nil = use provider default
	SystemPrompt          string
	PostProcessingSteps   string // JSON; empty = NULL
}

func (q *Queries) CreateContext(ctx context.Context, arg CreateContextParams) (uint64, error) {
	var userID sql.NullInt64
	if arg.UserID != nil {
		userID = sql.NullInt64{Int64: int64(*arg.UserID), Valid: true}
	}
	var desc sql.NullString
	if arg.Description != "" {
		desc = sql.NullString{String: arg.Description, Valid: true}
	}
	var preprocessors sql.NullString
	if arg.ImagePreprocessors != "" {
		preprocessors = sql.NullString{String: arg.ImagePreprocessors, Valid: true}
	}
	var temp sql.NullFloat64
	if arg.Temperature != nil {
		temp = sql.NullFloat64{Float64: *arg.Temperature, Valid: true}
	}
	var sysPrompt sql.NullString
	if arg.SystemPrompt != "" {
		sysPrompt = sql.NullString{String: arg.SystemPrompt, Valid: true}
	}
	var postSteps sql.NullString
	if arg.PostProcessingSteps != "" {
		postSteps = sql.NullString{String: arg.PostProcessingSteps, Valid: true}
	}
	res, err := q.db.ExecContext(ctx, `
INSERT INTO contexts (
  user_id, name, description, is_default,
  segmentation_model, image_preprocessors,
  transcription_provider, transcription_model,
  temperature, system_prompt, post_processing_steps
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, userID, arg.Name, desc, arg.IsDefault,
		arg.SegmentationModel, preprocessors,
		arg.TranscriptionProvider, arg.TranscriptionModel,
		temp, sysPrompt, postSteps,
	)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	return uint64(id), err
}

func (q *Queries) GetContext(ctx context.Context, id uint64) (Context, error) {
	var c Context
	err := q.db.QueryRowContext(ctx, `
SELECT id, user_id, name, description, is_default,
       segmentation_model, image_preprocessors,
       transcription_provider, transcription_model,
       temperature, system_prompt, post_processing_steps,
       created_at, updated_at
FROM contexts WHERE id = ?
`, id).Scan(
		&c.ID, &c.UserID, &c.Name, &c.Description, &c.IsDefault,
		&c.SegmentationModel, &c.ImagePreprocessors,
		&c.TranscriptionProvider, &c.TranscriptionModel,
		&c.Temperature, &c.SystemPrompt, &c.PostProcessingSteps,
		&c.CreatedAt, &c.UpdatedAt,
	)
	return c, err
}

func (q *Queries) GetDefaultContext(ctx context.Context) (Context, error) {
	var c Context
	err := q.db.QueryRowContext(ctx, `
SELECT id, user_id, name, description, is_default,
       segmentation_model, image_preprocessors,
       transcription_provider, transcription_model,
       temperature, system_prompt, post_processing_steps,
       created_at, updated_at
FROM contexts
WHERE is_default = TRUE AND user_id IS NULL
LIMIT 1
`).Scan(
		&c.ID, &c.UserID, &c.Name, &c.Description, &c.IsDefault,
		&c.SegmentationModel, &c.ImagePreprocessors,
		&c.TranscriptionProvider, &c.TranscriptionModel,
		&c.Temperature, &c.SystemPrompt, &c.PostProcessingSteps,
		&c.CreatedAt, &c.UpdatedAt,
	)
	return c, err
}

func (q *Queries) ListContexts(ctx context.Context, systemOnly bool) ([]Context, error) {
	query := `
SELECT id, user_id, name, description, is_default,
       segmentation_model, image_preprocessors,
       transcription_provider, transcription_model,
       temperature, system_prompt, post_processing_steps,
       created_at, updated_at
FROM contexts`
	if systemOnly {
		query += " WHERE user_id IS NULL"
	}
	query += " ORDER BY is_default DESC, name ASC"

	rows, err := q.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Context
	for rows.Next() {
		var c Context
		if err := rows.Scan(
			&c.ID, &c.UserID, &c.Name, &c.Description, &c.IsDefault,
			&c.SegmentationModel, &c.ImagePreprocessors,
			&c.TranscriptionProvider, &c.TranscriptionModel,
			&c.Temperature, &c.SystemPrompt, &c.PostProcessingSteps,
			&c.CreatedAt, &c.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

type UpdateContextParams struct {
	ID                    uint64
	Name                  string
	Description           string
	IsDefault             bool
	SegmentationModel     string
	ImagePreprocessors    string
	TranscriptionProvider string
	TranscriptionModel    string
	Temperature           *float64
	SystemPrompt          string
	PostProcessingSteps   string
}

func (q *Queries) UpdateContext(ctx context.Context, arg UpdateContextParams) error {
	var desc sql.NullString
	if arg.Description != "" {
		desc = sql.NullString{String: arg.Description, Valid: true}
	}
	var preprocessors sql.NullString
	if arg.ImagePreprocessors != "" {
		preprocessors = sql.NullString{String: arg.ImagePreprocessors, Valid: true}
	}
	var temp sql.NullFloat64
	if arg.Temperature != nil {
		temp = sql.NullFloat64{Float64: *arg.Temperature, Valid: true}
	}
	var sysPrompt sql.NullString
	if arg.SystemPrompt != "" {
		sysPrompt = sql.NullString{String: arg.SystemPrompt, Valid: true}
	}
	var postSteps sql.NullString
	if arg.PostProcessingSteps != "" {
		postSteps = sql.NullString{String: arg.PostProcessingSteps, Valid: true}
	}
	_, err := q.db.ExecContext(ctx, `
UPDATE contexts SET
  name=?, description=?, is_default=?,
  segmentation_model=?, image_preprocessors=?,
  transcription_provider=?, transcription_model=?,
  temperature=?, system_prompt=?, post_processing_steps=?
WHERE id=?
`, arg.Name, desc, arg.IsDefault,
		arg.SegmentationModel, preprocessors,
		arg.TranscriptionProvider, arg.TranscriptionModel,
		temp, sysPrompt, postSteps,
		arg.ID,
	)
	return err
}

func (q *Queries) DeleteContext(ctx context.Context, id uint64) error {
	_, err := q.db.ExecContext(ctx, `DELETE FROM contexts WHERE id = ?`, id)
	return err
}

// HasDefaultContext returns true if any system default context exists.
func (q *Queries) HasDefaultContext(ctx context.Context) (bool, error) {
	var count int
	err := q.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM contexts WHERE is_default = TRUE AND user_id IS NULL
`).Scan(&count)
	return count > 0, err
}

// --- context_selection_rules ---

type CreateSelectionRuleParams struct {
	ContextID  uint64
	Priority   int32
	Conditions string // JSON
}

func (q *Queries) CreateSelectionRule(ctx context.Context, arg CreateSelectionRuleParams) (uint64, error) {
	res, err := q.db.ExecContext(ctx, `
INSERT INTO context_selection_rules (context_id, priority, conditions)
VALUES (?, ?, ?)
`, arg.ContextID, arg.Priority, arg.Conditions)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	return uint64(id), err
}

func (q *Queries) ListSelectionRules(ctx context.Context, contextID uint64) ([]ContextSelectionRule, error) {
	query := `
SELECT id, context_id, priority, conditions, created_at
FROM context_selection_rules`
	var args []any
	if contextID > 0 {
		query += " WHERE context_id = ?"
		args = append(args, contextID)
	}
	query += " ORDER BY priority DESC, id ASC"

	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ContextSelectionRule
	for rows.Next() {
		var r ContextSelectionRule
		if err := rows.Scan(&r.ID, &r.ContextID, &r.Priority, &r.Conditions, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (q *Queries) DeleteSelectionRule(ctx context.Context, id uint64) error {
	_, err := q.db.ExecContext(ctx, `DELETE FROM context_selection_rules WHERE id = ?`, id)
	return err
}
