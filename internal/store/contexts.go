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
	TranscriptionBaseURL  string              `json:"transcription_base_url,omitempty"`
	TranscriptionAudience string              `json:"transcription_audience,omitempty"`
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
	q *db.Queries
}

func NewContextStore(pool *sql.DB) *ContextStore {
	return &ContextStore{q: db.New(pool)}
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
		TranscriptionBaseURL:  defaultCtx.TranscriptionBaseURL,
		TranscriptionAudience: defaultCtx.TranscriptionAudience,
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
		TranscriptionBaseURL:  c.TranscriptionBaseURL,
		TranscriptionAudience: c.TranscriptionAudience,
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
	workspaceIDParam, err := uint64ValueToNullInt64(workspaceID)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListContextsForWorkspaceManual(ctx, db.ListContextsForWorkspaceManualParams{
		WorkspaceID: workspaceIDParam,
		SystemOnly:  systemOnly,
	})
	if err != nil {
		return nil, fmt.Errorf("list contexts: %w", err)
	}
	out := make([]Context, 0, len(rows))
	for _, row := range rows {
		out = append(out, rowToContext(row))
	}
	return out, nil
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
		TranscriptionBaseURL:  c.TranscriptionBaseURL,
		TranscriptionAudience: c.TranscriptionAudience,
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
	return s.q.DeleteContextForWorkspace(ctx, id, workspaceID)
}

// --- selection rules ---

func (s *ContextStore) CreateRule(ctx context.Context, rule ContextSelectionRule) (ContextSelectionRule, error) {
	condJSON := marshalJSON(rule.Conditions)
	if condJSON == "" {
		condJSON = "[]"
	}
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
	rows, err := s.q.ListSelectionRulesForWorkspace(ctx, workspaceID, contextID)
	if err != nil {
		return nil, fmt.Errorf("list selection rules: %w", err)
	}
	out := make([]ContextSelectionRule, 0, len(rows))
	for _, row := range rows {
		out = append(out, rowToRule(row))
	}
	return out, nil
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
	return s.q.DeleteSelectionRuleForWorkspace(ctx, workspaceID, ruleID)
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
	workspaceIDParam, err := uint64ValueToNullInt64(workspaceID)
	if err != nil {
		return false, err
	}
	return s.q.WorkspaceCanReadContextManual(ctx, db.WorkspaceCanReadContextManualParams{
		ContextID:   contextID,
		WorkspaceID: workspaceIDParam,
	})
}

func (s *ContextStore) WorkspaceCanWriteContext(ctx context.Context, workspaceID, contextID uint64) (bool, error) {
	workspaceIDParam, err := uint64ValueToNullInt64(workspaceID)
	if err != nil {
		return false, err
	}
	return s.q.WorkspaceCanWriteContextManual(ctx, db.WorkspaceCanWriteContextManualParams{
		ContextID:   contextID,
		WorkspaceID: workspaceIDParam,
	})
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
		TranscriptionBaseURL:  row.TranscriptionBaseUrl.String,
		TranscriptionAudience: row.TranscriptionAudience.String,
		CreatedAt:             row.CreatedAt,
		UpdatedAt:             row.UpdatedAt,
	}
	if uid, ok := uint64PtrFromNullInt64(row.UserID); ok {
		c.UserID = uid
	}
	if workspaceID, ok := uint64PtrFromNullInt64(row.WorkspaceID); ok {
		c.WorkspaceID = workspaceID
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
