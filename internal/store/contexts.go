package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	db "github.com/lehigh-university-libraries/scribe/internal/db"
)

// Context is the store representation of a processing context.
type Context struct {
	ID                    uint64              `json:"id"`
	UserID                *uint64             `json:"user_id,omitempty"`      // creator for non-system contexts
	WorkspaceID           *uint64             `json:"workspace_id,omitempty"` // nil = system
	Name                  string              `json:"name"`
	Description           string              `json:"description,omitempty"`
	IsDefault             bool                `json:"is_default"`
	SegmentationModel     string              `json:"segmentation_model"`
	ImagePreprocessors    []ImagePreprocessor `json:"image_preprocessors,omitempty"`
	TranscriptionProvider string              `json:"transcription_provider"`
	TranscriptionModel    string              `json:"transcription_model"`
	Temperature           *float64            `json:"temperature,omitempty"`
	SystemPrompt          string              `json:"system_prompt,omitempty"`
	PostProcessingSteps   []string            `json:"post_processing_steps,omitempty"`
	CreatedAt             time.Time           `json:"created_at"`
	UpdatedAt             time.Time           `json:"updated_at"`
}

// ImagePreprocessor is a single pre-processing step.
type ImagePreprocessor struct {
	Type   string         `json:"type"`
	Params map[string]any `json:"params,omitempty"`
}

// RuleCondition is a single AND predicate in a selection rule.
type RuleCondition struct {
	Field    string `json:"field"`
	Operator string `json:"operator"` // eq | neq | contains | starts_with | ends_with
	Value    string `json:"value"`
}

// ContextSelectionRule pairs ordered conditions with a target context.
type ContextSelectionRule struct {
	ID         uint64          `json:"id"`
	ContextID  uint64          `json:"context_id"`
	Priority   int32           `json:"priority"`
	Conditions []RuleCondition `json:"conditions"`
	CreatedAt  time.Time       `json:"created_at"`
}

type ContextStore struct {
	q    *db.Queries
	pool *sql.DB
}

func NewContextStore(pool *sql.DB) *ContextStore {
	return &ContextStore{q: db.New(pool), pool: pool}
}

// EnsureDefault seeds a system default context from env config if none exists.
func (s *ContextStore) EnsureDefault(ctx context.Context, defaultCtx Context) error {
	has, err := s.q.HasDefaultContext(ctx)
	if err != nil {
		return fmt.Errorf("check default context: %w", err)
	}
	if has {
		return nil
	}
	preprocessorsJSON := marshalJSON(defaultCtx.ImagePreprocessors)
	postStepsJSON := marshalJSON(defaultCtx.PostProcessingSteps)
	_, err = s.q.CreateContext(ctx, db.CreateContextParams{
		UserID:                nil,
		WorkspaceID:           nil,
		Name:                  defaultCtx.Name,
		Description:           defaultCtx.Description,
		IsDefault:             true,
		SegmentationModel:     defaultCtx.SegmentationModel,
		ImagePreprocessors:    preprocessorsJSON,
		TranscriptionProvider: defaultCtx.TranscriptionProvider,
		TranscriptionModel:    defaultCtx.TranscriptionModel,
		Temperature:           defaultCtx.Temperature,
		SystemPrompt:          defaultCtx.SystemPrompt,
		PostProcessingSteps:   postStepsJSON,
	})
	return err
}

// EnsureSystemContext creates or updates a named system context so built-in
// contexts remain available across restarts.
func (s *ContextStore) EnsureSystemContext(ctx context.Context, desired Context) error {
	desired.UserID = nil
	desired.Name = strings.TrimSpace(desired.Name)
	if desired.Name == "" {
		return fmt.Errorf("system context name is required")
	}

	existing, err := s.List(ctx, true)
	if err != nil {
		return fmt.Errorf("list system contexts: %w", err)
	}
	for _, current := range existing {
		if !strings.EqualFold(strings.TrimSpace(current.Name), desired.Name) {
			continue
		}
		desired.ID = current.ID
		if desired.IsDefault && current.IsDefault {
			// Keep the existing default in place, but refresh its other fields.
			return s.updateSystemContext(ctx, desired)
		}
		if desired.IsDefault && !current.IsDefault {
			desired.IsDefault = false
		}
		return s.updateSystemContext(ctx, desired)
	}

	if desired.IsDefault {
		hasDefault, err := s.q.HasDefaultContext(ctx)
		if err != nil {
			return fmt.Errorf("check default context: %w", err)
		}
		if hasDefault {
			desired.IsDefault = false
		}
	}

	_, err = s.Create(ctx, desired)
	return err
}

func (s *ContextStore) updateSystemContext(ctx context.Context, desired Context) error {
	_, err := s.Update(ctx, desired)
	return err
}

func (s *ContextStore) Create(ctx context.Context, c Context) (Context, error) {
	preprocessorsJSON := marshalJSON(c.ImagePreprocessors)
	postStepsJSON := marshalJSON(c.PostProcessingSteps)
	id, err := s.q.CreateContext(ctx, db.CreateContextParams{
		UserID:                c.UserID,
		WorkspaceID:           c.WorkspaceID,
		Name:                  c.Name,
		Description:           c.Description,
		IsDefault:             c.IsDefault,
		SegmentationModel:     c.SegmentationModel,
		ImagePreprocessors:    preprocessorsJSON,
		TranscriptionProvider: c.TranscriptionProvider,
		TranscriptionModel:    c.TranscriptionModel,
		Temperature:           c.Temperature,
		SystemPrompt:          c.SystemPrompt,
		PostProcessingSteps:   postStepsJSON,
	})
	if err != nil {
		return Context{}, fmt.Errorf("create context: %w", err)
	}
	return s.Get(ctx, id)
}

func (s *ContextStore) Get(ctx context.Context, id uint64) (Context, error) {
	row, err := s.q.GetContext(ctx, id)
	if err != nil {
		return Context{}, fmt.Errorf("get context: %w", err)
	}
	return rowToContext(row), nil
}

func (s *ContextStore) GetForWorkspace(ctx context.Context, id, workspaceID uint64) (Context, error) {
	row, err := s.q.GetContext(ctx, id)
	if err != nil {
		return Context{}, fmt.Errorf("get context: %w", err)
	}
	contextValue := rowToContext(row)
	if contextValue.WorkspaceID == nil || *contextValue.WorkspaceID == workspaceID {
		return contextValue, nil
	}
	return Context{}, sql.ErrNoRows
}

func (s *ContextStore) GetDefault(ctx context.Context) (Context, error) {
	row, err := s.q.GetDefaultContext(ctx)
	if err != nil {
		return Context{}, fmt.Errorf("get default context: %w", err)
	}
	return rowToContext(row), nil
}

func (s *ContextStore) List(ctx context.Context, systemOnly bool) ([]Context, error) {
	rows, err := s.q.ListContexts(ctx, systemOnly)
	if err != nil {
		return nil, fmt.Errorf("list contexts: %w", err)
	}
	out := make([]Context, 0, len(rows))
	for _, row := range rows {
		out = append(out, rowToContext(row))
	}
	return out, nil
}

func (s *ContextStore) ListForWorkspace(ctx context.Context, workspaceID uint64, systemOnly bool) ([]Context, error) {
	query := `
SELECT id, user_id, workspace_id, name, description, is_default,
       segmentation_model, image_preprocessors,
       transcription_provider, transcription_model,
       temperature, system_prompt, post_processing_steps,
       created_at, updated_at
FROM contexts
WHERE workspace_id IS NULL`
	args := []any{}
	if !systemOnly {
		query += ` OR workspace_id = ?`
		args = append(args, workspaceID)
	}
	query += ` ORDER BY is_default DESC, name ASC`

	rows, err := s.pool.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list contexts: %w", err)
	}
	defer rows.Close()

	out := make([]Context, 0)
	for rows.Next() {
		var c db.Context
		if err := rows.Scan(
			&c.ID, &c.UserID, &c.WorkspaceID, &c.Name, &c.Description, &c.IsDefault,
			&c.SegmentationModel, &c.ImagePreprocessors,
			&c.TranscriptionProvider, &c.TranscriptionModel,
			&c.Temperature, &c.SystemPrompt, &c.PostProcessingSteps,
			&c.CreatedAt, &c.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, rowToContext(c))
	}
	return out, rows.Err()
}

func (s *ContextStore) Update(ctx context.Context, c Context) (Context, error) {
	preprocessorsJSON := marshalJSON(c.ImagePreprocessors)
	postStepsJSON := marshalJSON(c.PostProcessingSteps)
	err := s.q.UpdateContext(ctx, db.UpdateContextParams{
		ID:                    c.ID,
		Name:                  c.Name,
		Description:           c.Description,
		IsDefault:             c.IsDefault,
		SegmentationModel:     c.SegmentationModel,
		ImagePreprocessors:    preprocessorsJSON,
		TranscriptionProvider: c.TranscriptionProvider,
		TranscriptionModel:    c.TranscriptionModel,
		Temperature:           c.Temperature,
		SystemPrompt:          c.SystemPrompt,
		PostProcessingSteps:   postStepsJSON,
	})
	if err != nil {
		return Context{}, fmt.Errorf("update context: %w", err)
	}
	return s.Get(ctx, c.ID)
}

func (s *ContextStore) Delete(ctx context.Context, id uint64) error {
	return s.q.DeleteContext(ctx, id)
}

func (s *ContextStore) UpdateForWorkspace(ctx context.Context, c Context, workspaceID uint64, userID uint64) (Context, error) {
	existing, err := s.Get(ctx, c.ID)
	if err != nil {
		return Context{}, err
	}
	if existing.WorkspaceID == nil || *existing.WorkspaceID != workspaceID {
		return Context{}, sql.ErrNoRows
	}
	c.UserID = &userID
	c.WorkspaceID = &workspaceID
	return s.Update(ctx, c)
}

func (s *ContextStore) DeleteForWorkspace(ctx context.Context, id uint64, workspaceID uint64) error {
	res, err := s.pool.ExecContext(ctx, `DELETE FROM contexts WHERE id = ? AND workspace_id = ?`, id, workspaceID)
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

// --- selection rules ---

func (s *ContextStore) CreateRule(ctx context.Context, rule ContextSelectionRule) (ContextSelectionRule, error) {
	condJSON := marshalJSON(rule.Conditions)
	id, err := s.q.CreateSelectionRule(ctx, db.CreateSelectionRuleParams{
		ContextID:  rule.ContextID,
		Priority:   rule.Priority,
		Conditions: condJSON,
	})
	if err != nil {
		return ContextSelectionRule{}, fmt.Errorf("create selection rule: %w", err)
	}
	rows, err := s.q.ListSelectionRules(ctx, 0)
	if err != nil {
		return ContextSelectionRule{}, err
	}
	for _, r := range rows {
		if r.ID == id {
			return rowToRule(r), nil
		}
	}
	return ContextSelectionRule{}, fmt.Errorf("new rule %d not found after insert", id)
}

func (s *ContextStore) ListRules(ctx context.Context, contextID uint64) ([]ContextSelectionRule, error) {
	rows, err := s.q.ListSelectionRules(ctx, contextID)
	if err != nil {
		return nil, fmt.Errorf("list selection rules: %w", err)
	}
	out := make([]ContextSelectionRule, 0, len(rows))
	for _, r := range rows {
		out = append(out, rowToRule(r))
	}
	return out, nil
}

func (s *ContextStore) ListRulesForWorkspace(ctx context.Context, workspaceID, contextID uint64) ([]ContextSelectionRule, error) {
	query := `
SELECT r.id, r.context_id, r.priority, r.conditions, r.created_at
FROM context_selection_rules r
JOIN contexts c ON c.id = r.context_id
WHERE (c.workspace_id IS NULL OR c.workspace_id = ?)`
	args := []any{workspaceID}
	if contextID > 0 {
		query += ` AND r.context_id = ?`
		args = append(args, contextID)
	}
	query += ` ORDER BY r.priority DESC, r.id ASC`

	rows, err := s.pool.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list selection rules: %w", err)
	}
	defer rows.Close()

	out := make([]ContextSelectionRule, 0)
	for rows.Next() {
		var row db.ContextSelectionRule
		if err := rows.Scan(&row.ID, &row.ContextID, &row.Priority, &row.Conditions, &row.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, rowToRule(row))
	}
	return out, rows.Err()
}

func (s *ContextStore) CreateRuleForWorkspace(ctx context.Context, workspaceID uint64, rule ContextSelectionRule) (ContextSelectionRule, error) {
	ok, err := s.WorkspaceCanWriteContext(ctx, workspaceID, rule.ContextID)
	if err != nil {
		return ContextSelectionRule{}, err
	}
	if !ok {
		return ContextSelectionRule{}, sql.ErrNoRows
	}
	return s.CreateRule(ctx, rule)
}

func (s *ContextStore) DeleteRuleForWorkspace(ctx context.Context, workspaceID, ruleID uint64) error {
	res, err := s.pool.ExecContext(ctx, `
DELETE r
FROM context_selection_rules r
JOIN contexts c ON c.id = r.context_id
WHERE r.id = ? AND c.workspace_id = ?
`, ruleID, workspaceID)
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

func (s *ContextStore) DeleteRule(ctx context.Context, id uint64) error {
	return s.q.DeleteSelectionRule(ctx, id)
}

// Resolve evaluates all rules (ordered by priority desc) against the given
// metadata bag and returns the first matching context, or the default.
func (s *ContextStore) Resolve(ctx context.Context, metadata map[string]any) (Context, bool, error) {
	rules, err := s.ListRules(ctx, 0)
	if err != nil {
		return Context{}, false, err
	}
	for _, rule := range rules {
		if matchesAll(rule.Conditions, metadata) {
			c, err := s.Get(ctx, rule.ContextID)
			if err != nil {
				continue
			}
			return c, false, nil
		}
	}
	def, err := s.GetDefault(ctx)
	return def, true, err
}

func (s *ContextStore) ResolveForWorkspace(ctx context.Context, workspaceID uint64, metadata map[string]any) (Context, bool, error) {
	rules, err := s.ListRulesForWorkspace(ctx, workspaceID, 0)
	if err != nil {
		return Context{}, false, err
	}
	for _, rule := range rules {
		if matchesAll(rule.Conditions, metadata) {
			c, err := s.GetForWorkspace(ctx, rule.ContextID, workspaceID)
			if err != nil {
				continue
			}
			return c, false, nil
		}
	}
	def, err := s.GetDefault(ctx)
	return def, true, err
}

func (s *ContextStore) WorkspaceCanReadContext(ctx context.Context, workspaceID, contextID uint64) (bool, error) {
	var count int
	if err := s.pool.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM contexts
WHERE id = ? AND (workspace_id IS NULL OR workspace_id = ?)
`, contextID, workspaceID).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *ContextStore) WorkspaceCanWriteContext(ctx context.Context, workspaceID, contextID uint64) (bool, error) {
	var count int
	if err := s.pool.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM contexts
WHERE id = ? AND workspace_id = ?
`, contextID, workspaceID).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

// matchesAll returns true if all conditions are satisfied by the metadata.
func matchesAll(conditions []RuleCondition, metadata map[string]any) bool {
	for _, cond := range conditions {
		val, ok := metadata[cond.Field]
		if !ok {
			return false
		}
		str := fmt.Sprintf("%v", val)
		switch cond.Operator {
		case "eq":
			if str != cond.Value {
				return false
			}
		case "neq":
			if str == cond.Value {
				return false
			}
		case "contains":
			if !strings.Contains(str, cond.Value) {
				return false
			}
		case "starts_with":
			if !strings.HasPrefix(str, cond.Value) {
				return false
			}
		case "ends_with":
			if !strings.HasSuffix(str, cond.Value) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// --- helpers ---

func rowToContext(row db.Context) Context {
	c := Context{
		ID:                    row.ID,
		Name:                  row.Name,
		IsDefault:             row.IsDefault,
		SegmentationModel:     row.SegmentationModel,
		TranscriptionProvider: row.TranscriptionProvider,
		TranscriptionModel:    row.TranscriptionModel,
		CreatedAt:             row.CreatedAt,
		UpdatedAt:             row.UpdatedAt,
	}
	if row.UserID.Valid {
		uid := uint64(row.UserID.Int64)
		c.UserID = &uid
	}
	if row.WorkspaceID.Valid {
		workspaceID := uint64(row.WorkspaceID.Int64)
		c.WorkspaceID = &workspaceID
	}
	if row.Description.Valid {
		c.Description = row.Description.String
	}
	if len(row.ImagePreprocessors) > 0 {
		var pp []ImagePreprocessor
		if err := json.Unmarshal(row.ImagePreprocessors, &pp); err == nil {
			c.ImagePreprocessors = pp
		}
	}
	if row.Temperature.Valid {
		c.Temperature = &row.Temperature.Float64
	}
	if row.SystemPrompt.Valid {
		c.SystemPrompt = row.SystemPrompt.String
	}
	if len(row.PostProcessingSteps) > 0 {
		var steps []string
		if err := json.Unmarshal(row.PostProcessingSteps, &steps); err == nil {
			c.PostProcessingSteps = steps
		}
	}
	return c
}

func rowToRule(row db.ContextSelectionRule) ContextSelectionRule {
	r := ContextSelectionRule{
		ID:        row.ID,
		ContextID: row.ContextID,
		Priority:  row.Priority,
		CreatedAt: row.CreatedAt,
	}
	if len(row.Conditions) > 0 {
		var conds []RuleCondition
		if err := json.Unmarshal(row.Conditions, &conds); err == nil {
			r.Conditions = conds
		}
	}
	return r
}

func marshalJSON(v any) string {
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil || string(b) == "null" || string(b) == "[]" || string(b) == "{}" {
		return ""
	}
	return string(b)
}
