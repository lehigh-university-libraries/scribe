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
	TranscriptionBaseURL  string
	TranscriptionAudience string
	Temperature           *float64
	SystemPrompt          string
	PostProcessingSteps   string
}

func (q *Queries) CreateContext(ctx context.Context, arg CreateContextParams) (uint64, error) {
	userID, err := compatNullUint64(arg.UserID)
	if err != nil {
		return 0, err
	}
	workspaceID, err := compatNullUint64(arg.WorkspaceID)
	if err != nil {
		return 0, err
	}
	res, err := q.CreateContextManual(ctx, CreateContextManualParams{
		UserID:                userID,
		WorkspaceID:           workspaceID,
		Name:                  arg.Name,
		Description:           compatNullableString(arg.Description),
		IsDefault:             arg.IsDefault,
		SegmentationModel:     arg.SegmentationModel,
		ImagePreprocessors:    compatRawJSON(arg.ImagePreprocessors),
		TranscriptionProvider: arg.TranscriptionProvider,
		TranscriptionModel:    arg.TranscriptionModel,
		TranscriptionBaseUrl:  compatNullableString(arg.TranscriptionBaseURL),
		TranscriptionAudience: compatNullableString(arg.TranscriptionAudience),
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
	return compatLastInsertID(res)
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
		TranscriptionBaseUrl:  row.TranscriptionBaseUrl,
		TranscriptionAudience: row.TranscriptionAudience,
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
		TranscriptionBaseUrl:  row.TranscriptionBaseUrl,
		TranscriptionAudience: row.TranscriptionAudience,
		Temperature:           row.Temperature,
		SystemPrompt:          row.SystemPrompt,
		PostProcessingSteps:   row.PostProcessingSteps,
		CreatedAt:             row.CreatedAt,
		UpdatedAt:             row.UpdatedAt,
	}, nil
}

func (q *Queries) ListContexts(ctx context.Context, systemOnly bool) ([]Context, error) {
	return q.ListContextsManual(ctx, systemOnly)
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
	TranscriptionBaseURL  string
	TranscriptionAudience string
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
		TranscriptionBaseUrl:  compatNullableString(arg.TranscriptionBaseURL),
		TranscriptionAudience: compatNullableString(arg.TranscriptionAudience),
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

func (q *Queries) DeleteContextForWorkspace(ctx context.Context, id, workspaceID uint64) error {
	workspaceIDParam, err := compatUint64ToInt64(workspaceID)
	if err != nil {
		return err
	}
	res, err := q.DeleteContextForWorkspaceManual(ctx, DeleteContextForWorkspaceManualParams{
		ID:          id,
		WorkspaceID: sql.NullInt64{Int64: workspaceIDParam, Valid: true},
	})
	return requireAffectedRow(res, err)
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
	return compatLastInsertID(res)
}

func (q *Queries) ListSelectionRules(ctx context.Context, contextID uint64) ([]ContextSelectionRule, error) {
	return q.ListSelectionRulesManual(ctx, ListSelectionRulesManualParams{
		ContextID: contextID,
	})
}

func (q *Queries) ListSelectionRulesForWorkspace(ctx context.Context, workspaceID, contextID uint64) ([]ContextSelectionRule, error) {
	workspaceIDParam, err := compatUint64ToInt64(workspaceID)
	if err != nil {
		return nil, err
	}
	return q.ListSelectionRulesForWorkspaceManual(ctx, ListSelectionRulesForWorkspaceManualParams{
		WorkspaceID: sql.NullInt64{Int64: workspaceIDParam, Valid: true},
		ContextID:   contextID,
	})
}

func (q *Queries) DeleteSelectionRule(ctx context.Context, id uint64) error {
	return q.DeleteSelectionRuleManual(ctx, id)
}

func (q *Queries) DeleteSelectionRuleForWorkspace(ctx context.Context, workspaceID, id uint64) error {
	workspaceIDParam, err := compatUint64ToInt64(workspaceID)
	if err != nil {
		return err
	}
	res, err := q.DeleteSelectionRuleForWorkspaceManual(ctx, DeleteSelectionRuleForWorkspaceManualParams{
		ID:          id,
		WorkspaceID: sql.NullInt64{Int64: workspaceIDParam, Valid: true},
	})
	return requireAffectedRow(res, err)
}
