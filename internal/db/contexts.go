package db

// Store query adapters in this file are the sole mapping boundary from
// domain-shaped context values to sqlc-generated queries in contexts.sql.

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
	TranscriptionProvider string
	TranscriptionModel    string
	Temperature           *float64
	SystemPrompt          string
}

func (q *Queries) CreateContext(ctx context.Context, arg CreateContextParams) (uint64, error) {
	userID, err := nullUint64(arg.UserID)
	if err != nil {
		return 0, err
	}
	workspaceID, err := nullUint64(arg.WorkspaceID)
	if err != nil {
		return 0, err
	}
	res, err := q.CreateContextManual(ctx, CreateContextManualParams{
		UserID:                userID,
		WorkspaceID:           workspaceID,
		Name:                  arg.Name,
		Description:           nullableString(arg.Description),
		IsDefault:             arg.IsDefault,
		SegmentationModel:     arg.SegmentationModel,
		TranscriptionProvider: arg.TranscriptionProvider,
		TranscriptionModel:    arg.TranscriptionModel,
		Temperature: func() sql.NullFloat64 {
			if arg.Temperature == nil {
				return sql.NullFloat64{}
			}
			return sql.NullFloat64{Float64: *arg.Temperature, Valid: true}
		}(),
		SystemPrompt: nullableString(arg.SystemPrompt),
	})
	if err != nil {
		return 0, err
	}
	return lastInsertID(res)
}

func (q *Queries) GetContext(ctx context.Context, id uint64) (Context, error) {
	row, err := q.GetContextManual(ctx, id)
	if err != nil {
		return Context{}, err
	}
	return row, nil
}

func (q *Queries) GetDefaultContext(ctx context.Context) (Context, error) {
	row, err := q.GetDefaultContextManual(ctx)
	if err != nil {
		return Context{}, err
	}
	return row, nil
}

func (q *Queries) GetSystemContextByName(ctx context.Context, name string) (Context, error) {
	return q.GetSystemContextByNameManual(ctx, name)
}

type ListContextsPageParams struct {
	WorkspaceID     uint64
	SystemOnly      bool
	CursorID        uint64
	CursorIsDefault bool
	CursorIsSystem  bool
	Limit           int32
}

func (q *Queries) ListContextsPageForWorkspace(ctx context.Context, arg ListContextsPageParams) ([]Context, error) {
	workspaceID, err := uint64ToInt64(arg.WorkspaceID)
	if err != nil {
		return nil, err
	}
	cursorIsSystem := int64(0)
	if arg.CursorIsSystem {
		cursorIsSystem = 1
	}
	return q.ListContextsPageForWorkspaceManual(ctx, ListContextsPageForWorkspaceManualParams{
		SystemOnly:      arg.SystemOnly,
		WorkspaceID:     sql.NullInt64{Int64: workspaceID, Valid: true},
		CursorID:        arg.CursorID,
		CursorIsDefault: arg.CursorIsDefault,
		CursorIsSystem:  sql.NullInt64{Int64: cursorIsSystem, Valid: true},
		Limit:           arg.Limit,
	})
}

type UpdateContextParams struct {
	ID                    uint64
	Name                  string
	Description           string
	IsDefault             bool
	SegmentationModel     string
	TranscriptionProvider string
	TranscriptionModel    string
	Temperature           *float64
	SystemPrompt          string
}

func (q *Queries) UpdateContext(ctx context.Context, arg UpdateContextParams) (sql.Result, error) {
	return q.UpdateContextManual(ctx, UpdateContextManualParams{
		ID:                    arg.ID,
		Name:                  arg.Name,
		Description:           nullableString(arg.Description),
		IsDefault:             arg.IsDefault,
		SegmentationModel:     arg.SegmentationModel,
		TranscriptionProvider: arg.TranscriptionProvider,
		TranscriptionModel:    arg.TranscriptionModel,
		Temperature: func() sql.NullFloat64 {
			if arg.Temperature == nil {
				return sql.NullFloat64{}
			}
			return sql.NullFloat64{Float64: *arg.Temperature, Valid: true}
		}(),
		SystemPrompt: nullableString(arg.SystemPrompt),
	})
}

func (q *Queries) DeleteContext(ctx context.Context, id uint64) error {
	res, err := q.DeleteContextManual(ctx, id)
	return requireAffectedRow(res, err)
}

func (q *Queries) DeleteContextForWorkspace(ctx context.Context, id, workspaceID uint64) error {
	workspaceIDParam, err := uint64ToInt64(workspaceID)
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
		Conditions: rawJSON(arg.Conditions),
	})
	if err != nil {
		return 0, err
	}
	return lastInsertID(res)
}

func (q *Queries) GetSelectionRuleByID(ctx context.Context, id uint64) (ContextSelectionRule, error) {
	return q.GetSelectionRuleByIDManual(ctx, id)
}

func (q *Queries) GetSelectionRuleForWorkspace(ctx context.Context, workspaceID, id uint64) (ContextSelectionRule, error) {
	workspaceIDParam, err := uint64ToInt64(workspaceID)
	if err != nil {
		return ContextSelectionRule{}, err
	}
	return q.GetSelectionRuleForWorkspaceManual(ctx, GetSelectionRuleForWorkspaceManualParams{
		ID:          id,
		WorkspaceID: sql.NullInt64{Int64: workspaceIDParam, Valid: true},
	})
}

type ListSelectionRulesPageParams struct {
	WorkspaceID    uint64
	ContextID      uint64
	CursorID       uint64
	CursorPriority int32
	Limit          int32
}

func (q *Queries) ListSelectionRulesPageForWorkspace(ctx context.Context, arg ListSelectionRulesPageParams) ([]ContextSelectionRule, error) {
	workspaceIDParam, err := uint64ToInt64(arg.WorkspaceID)
	if err != nil {
		return nil, err
	}
	return q.ListSelectionRulesPageForWorkspaceManual(ctx, ListSelectionRulesPageForWorkspaceManualParams{
		WorkspaceID:    sql.NullInt64{Int64: workspaceIDParam, Valid: true},
		ContextID:      arg.ContextID,
		CursorID:       arg.CursorID,
		CursorPriority: arg.CursorPriority,
		Limit:          arg.Limit,
	})
}

func (q *Queries) ListSelectionRulesForWorkspaceResolution(ctx context.Context, workspaceID uint64, limit int32) ([]ContextSelectionRule, error) {
	workspaceIDParam, err := uint64ToInt64(workspaceID)
	if err != nil {
		return nil, err
	}
	return q.ListSelectionRulesForWorkspaceResolutionManual(ctx, ListSelectionRulesForWorkspaceResolutionManualParams{
		WorkspaceID: sql.NullInt64{Int64: workspaceIDParam, Valid: true},
		Limit:       limit,
	})
}

func (q *Queries) CountSelectionRulesForWorkspace(ctx context.Context, workspaceID uint64) (int64, error) {
	workspaceIDParam, err := uint64ToInt64(workspaceID)
	if err != nil {
		return 0, err
	}
	return q.CountSelectionRulesForWorkspaceManual(ctx, sql.NullInt64{Int64: workspaceIDParam, Valid: true})
}

func (q *Queries) DeleteSelectionRule(ctx context.Context, id uint64) error {
	return q.DeleteSelectionRuleManual(ctx, id)
}

func (q *Queries) DeleteSelectionRuleForWorkspace(ctx context.Context, workspaceID, id uint64) error {
	workspaceIDParam, err := uint64ToInt64(workspaceID)
	if err != nil {
		return err
	}
	res, err := q.DeleteSelectionRuleForWorkspaceManual(ctx, DeleteSelectionRuleForWorkspaceManualParams{
		ID:          id,
		WorkspaceID: sql.NullInt64{Int64: workspaceIDParam, Valid: true},
	})
	return requireAffectedRow(res, err)
}
