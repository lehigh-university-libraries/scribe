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
	UserID                *uint64             `json:"user_id,omitempty"` // nil = system
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

func (s *ContextStore) Create(ctx context.Context, c Context) (Context, error) {
	preprocessorsJSON := marshalJSON(c.ImagePreprocessors)
	postStepsJSON := marshalJSON(c.PostProcessingSteps)
	id, err := s.q.CreateContext(ctx, db.CreateContextParams{
		UserID:                c.UserID,
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
	if row.Description.Valid {
		c.Description = row.Description.String
	}
	if row.ImagePreprocessors.Valid && row.ImagePreprocessors.String != "" {
		var pp []ImagePreprocessor
		if err := json.Unmarshal([]byte(row.ImagePreprocessors.String), &pp); err == nil {
			c.ImagePreprocessors = pp
		}
	}
	if row.Temperature.Valid {
		c.Temperature = &row.Temperature.Float64
	}
	if row.SystemPrompt.Valid {
		c.SystemPrompt = row.SystemPrompt.String
	}
	if row.PostProcessingSteps.Valid && row.PostProcessingSteps.String != "" {
		var steps []string
		if err := json.Unmarshal([]byte(row.PostProcessingSteps.String), &steps); err == nil {
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
	if row.Conditions != "" {
		var conds []RuleCondition
		if err := json.Unmarshal([]byte(row.Conditions), &conds); err == nil {
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
