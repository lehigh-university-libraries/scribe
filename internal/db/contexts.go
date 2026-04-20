package db

// Compatibility wrappers in this file preserve the older store-facing API while
// delegating SQL execution to sqlc-generated queries in contexts.sql.

import (
	"context"
	"database/sql"
)

type CreateContextParams struct {
	UserID                *uint64
	WorkspaceID           *uint64
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

func (q *Queries) CreateContext(ctx context.Context, arg CreateContextParams) (uint64, error) {
	res, err := q.CreateContextManual(ctx, CreateContextManualParams{
		UserID:                compatNullUint64(arg.UserID),
		WorkspaceID:           compatNullUint64(arg.WorkspaceID),
		Name:                  arg.Name,
		Description:           compatNullableString(arg.Description),
		IsDefault:             arg.IsDefault,
		SegmentationModel:     arg.SegmentationModel,
		ImagePreprocessors:    compatRawJSON(arg.ImagePreprocessors),
		TranscriptionProvider: arg.TranscriptionProvider,
		TranscriptionModel:    arg.TranscriptionModel,
		Temperature: func() sql.NullFloat64 {
			if arg.Temperature == nil {
				return sql.NullFloat64{}
			}
			return sql.NullFloat64{Float64: *arg.Temperature, Valid: true}
		}(),
		SystemPrompt:        compatNullableString(arg.SystemPrompt),
		PostProcessingSteps: compatRawJSON(arg.PostProcessingSteps),
	})
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	return uint64(id), err
}

func (q *Queries) GetContext(ctx context.Context, id uint64) (Context, error) {
	row, err := q.GetContextManual(ctx, id)
	if err != nil {
		return Context{}, err
	}
	return Context{
		ID:                    row.ID,
		UserID:                row.UserID,
		WorkspaceID:           row.WorkspaceID,
		Name:                  row.Name,
		Description:           row.Description,
		IsDefault:             row.IsDefault,
		SegmentationModel:     row.SegmentationModel,
		ImagePreprocessors:    row.ImagePreprocessors,
		TranscriptionProvider: row.TranscriptionProvider,
		TranscriptionModel:    row.TranscriptionModel,
		Temperature:           row.Temperature,
		SystemPrompt:          row.SystemPrompt,
		PostProcessingSteps:   row.PostProcessingSteps,
		CreatedAt:             row.CreatedAt,
		UpdatedAt:             row.UpdatedAt,
	}, nil
}

func (q *Queries) GetDefaultContext(ctx context.Context) (Context, error) {
	row, err := q.GetDefaultContextManual(ctx)
	if err != nil {
		return Context{}, err
	}
	return Context{
		ID:                    row.ID,
		UserID:                row.UserID,
		WorkspaceID:           row.WorkspaceID,
		Name:                  row.Name,
		Description:           row.Description,
		IsDefault:             row.IsDefault,
		SegmentationModel:     row.SegmentationModel,
		ImagePreprocessors:    row.ImagePreprocessors,
		TranscriptionProvider: row.TranscriptionProvider,
		TranscriptionModel:    row.TranscriptionModel,
		Temperature:           row.Temperature,
		SystemPrompt:          row.SystemPrompt,
		PostProcessingSteps:   row.PostProcessingSteps,
		CreatedAt:             row.CreatedAt,
		UpdatedAt:             row.UpdatedAt,
	}, nil
}

func (q *Queries) ListContexts(ctx context.Context, systemOnly bool) ([]Context, error) {
	query := `
SELECT
  id,
  user_id,
  workspace_id,
  name,
  description,
  is_default,
  segmentation_model,
  image_preprocessors,
  transcription_provider,
  transcription_model,
  temperature,
  system_prompt,
  post_processing_steps,
  created_at,
  updated_at
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

	out := make([]Context, 0)
	for rows.Next() {
		var row Context
		if err := rows.Scan(
			&row.ID,
			&row.UserID,
			&row.WorkspaceID,
			&row.Name,
			&row.Description,
			&row.IsDefault,
			&row.SegmentationModel,
			&row.ImagePreprocessors,
			&row.TranscriptionProvider,
			&row.TranscriptionModel,
			&row.Temperature,
			&row.SystemPrompt,
			&row.PostProcessingSteps,
			&row.CreatedAt,
			&row.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, row)
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
	return q.UpdateContextManual(ctx, UpdateContextManualParams{
		ID:                    arg.ID,
		Name:                  arg.Name,
		Description:           compatNullableString(arg.Description),
		IsDefault:             arg.IsDefault,
		SegmentationModel:     arg.SegmentationModel,
		ImagePreprocessors:    compatRawJSON(arg.ImagePreprocessors),
		TranscriptionProvider: arg.TranscriptionProvider,
		TranscriptionModel:    arg.TranscriptionModel,
		Temperature: func() sql.NullFloat64 {
			if arg.Temperature == nil {
				return sql.NullFloat64{}
			}
			return sql.NullFloat64{Float64: *arg.Temperature, Valid: true}
		}(),
		SystemPrompt:        compatNullableString(arg.SystemPrompt),
		PostProcessingSteps: compatRawJSON(arg.PostProcessingSteps),
	})
}

func (q *Queries) DeleteContext(ctx context.Context, id uint64) error {
	return q.DeleteContextManual(ctx, id)
}

func (q *Queries) HasDefaultContext(ctx context.Context) (bool, error) {
	return q.HasDefaultContextManual(ctx)
}

type CreateSelectionRuleParams struct {
	ContextID  uint64
	Priority   int32
	Conditions string
}

func (q *Queries) CreateSelectionRule(ctx context.Context, arg CreateSelectionRuleParams) (uint64, error) {
	res, err := q.CreateSelectionRuleManual(ctx, CreateSelectionRuleManualParams{
		ContextID:  arg.ContextID,
		Priority:   arg.Priority,
		Conditions: compatRawJSON(arg.Conditions),
	})
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	return uint64(id), err
}

func (q *Queries) ListSelectionRules(ctx context.Context, contextID uint64) ([]ContextSelectionRule, error) {
	return q.ListSelectionRulesManual(ctx, ListSelectionRulesManualParams{
		ContextID: contextID,
	})
}

func (q *Queries) DeleteSelectionRule(ctx context.Context, id uint64) error {
	return q.DeleteSelectionRuleManual(ctx, id)
}
