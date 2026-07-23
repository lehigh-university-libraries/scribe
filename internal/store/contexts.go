package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	db "github.com/lehigh-university-libraries/scribe/internal/db"
)

const (
	DefaultContextPageSize        uint32 = 50
	MaxContextPageSize            uint32 = 100
	DefaultSelectionRulePageSize  uint32 = 50
	MaxSelectionRulePageSize      uint32 = 100
	MaxSelectionRulesPerWorkspace        = 100
	MaxSelectionRuleConditions           = 32
	MaxSelectionRuleFieldBytes           = 255
	MaxSelectionRuleValueBytes           = 4096
	MaxContextMetadataFields             = 64
	MaxContextMetadataKeyBytes           = 255
	MaxContextMetadataScalarBytes        = 4096
	systemContextCatalogLockName         = "scribe:system-context-catalog"
)

var (
	ErrInvalidContextPage       = errors.New("invalid context page")
	ErrInvalidSelectionRulePage = errors.New("invalid selection rule page")
	ErrSelectionRuleLimit       = errors.New("selection rule limit reached")
	ErrContextResolutionLimit   = errors.New("context resolution rule limit exceeded")
	ErrInvalidContextMetadata   = errors.New("invalid context metadata")
)

// Context is the store representation of a processing context.
type Context struct {
	ID                    uint64    `json:"id"`
	UserID                *uint64   `json:"user_id,omitempty"`      // creator for non-system contexts
	WorkspaceID           *uint64   `json:"workspace_id,omitempty"` // nil = system
	Name                  string    `json:"name"`
	Description           string    `json:"description,omitempty"`
	IsDefault             bool      `json:"is_default"`
	SegmentationModel     string    `json:"segmentation_model"`
	TranscriptionProvider string    `json:"transcription_provider"`
	TranscriptionModel    string    `json:"transcription_model"`
	Temperature           *float64  `json:"temperature,omitempty"`
	SystemPrompt          string    `json:"system_prompt,omitempty"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
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

// ContextPageCursor is the exclusive keyset boundary for a workspace context
// catalog. Defaults sort first, followed by system contexts and then ID.
type ContextPageCursor struct {
	ID        uint64
	IsDefault bool
	IsSystem  bool
}

type ContextPage struct {
	Contexts   []Context
	NextCursor *ContextPageCursor
}

// SelectionRulePageCursor is the exclusive priority/id keyset boundary.
type SelectionRulePageCursor struct {
	ID       uint64
	Priority int32
}

type SelectionRulePage struct {
	Rules      []ContextSelectionRule
	NextCursor *SelectionRulePageCursor
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
	defaultCtx.UserID = nil
	defaultCtx.WorkspaceID = nil
	defaultCtx.IsDefault = true
	return s.withSystemContextCatalogLock(ctx, func(queries *db.Queries) error {
		has, err := queries.HasDefaultContext(ctx)
		if err != nil {
			return fmt.Errorf("check default context: %w", err)
		}
		if has {
			return nil
		}
		existingRow, err := queries.GetSystemContextByName(ctx, defaultCtx.Name)
		if err == nil {
			defaultCtx.ID = existingRow.ID
			result, err := queries.UpdateContext(ctx, contextUpdateParams(defaultCtx))
			if err := requireDeletedRow(result, err); err != nil {
				return fmt.Errorf("promote default context: %w", err)
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("get default context by name: %w", err)
		}
		if _, err := queries.CreateContext(ctx, contextCreateParams(defaultCtx)); err != nil {
			return fmt.Errorf("create default context: %w", err)
		}
		return nil
	})
}

// EnsureSystemContext creates or updates a named system context so built-in
// contexts remain available across restarts.
func (s *ContextStore) EnsureSystemContext(ctx context.Context, desired Context) error {
	desired.UserID = nil
	desired.WorkspaceID = nil
	desired.Name = strings.TrimSpace(desired.Name)
	if desired.Name == "" {
		return fmt.Errorf("system context name is required")
	}
	return s.withSystemContextCatalogLock(ctx, func(queries *db.Queries) error {
		existingRow, err := queries.GetSystemContextByName(ctx, desired.Name)
		if err == nil {
			current := rowToContext(existingRow)
			desired.ID = current.ID
			if desired.IsDefault && !current.IsDefault {
				desired.IsDefault = false
			}
			if _, err := queries.UpdateContext(ctx, contextUpdateParams(desired)); err != nil {
				return fmt.Errorf("update system context: %w", err)
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("get system context by name: %w", err)
		}

		if desired.IsDefault {
			hasDefault, err := queries.HasDefaultContext(ctx)
			if err != nil {
				return fmt.Errorf("check default context: %w", err)
			}
			if hasDefault {
				desired.IsDefault = false
			}
		}
		if _, err := queries.CreateContext(ctx, contextCreateParams(desired)); err != nil {
			return fmt.Errorf("create system context: %w", err)
		}
		return nil
	})
}

// withSystemContextCatalogLock makes startup catalog seeding convergent across
// horizontally scaled API instances. A MySQL advisory lock is connection
// scoped, so the transaction and release deliberately use that same reserved
// connection.
func (s *ContextStore) withSystemContextCatalogLock(ctx context.Context, operation func(*db.Queries) error) (returnErr error) {
	if s == nil || s.pool == nil {
		return fmt.Errorf("system context store is not configured")
	}
	conn, err := s.pool.Conn(ctx)
	if err != nil {
		return fmt.Errorf("reserve system context catalog connection: %w", err)
	}
	defer func() { _ = conn.Close() }()
	var acquired sql.NullInt64
	if err := conn.QueryRowContext(ctx, `SELECT GET_LOCK(?, 30)`, systemContextCatalogLockName).Scan(&acquired); err != nil {
		return fmt.Errorf("lock system context catalog: %w", err)
	}
	if !acquired.Valid || acquired.Int64 != 1 {
		return fmt.Errorf("lock system context catalog: lock acquisition timed out")
	}
	defer func() {
		var released sql.NullInt64
		releaseErr := conn.QueryRowContext(context.Background(), `SELECT RELEASE_LOCK(?)`, systemContextCatalogLockName).Scan(&released)
		if releaseErr != nil && returnErr == nil {
			returnErr = fmt.Errorf("release system context catalog lock: %w", releaseErr)
		} else if releaseErr == nil && (!released.Valid || released.Int64 != 1) && returnErr == nil {
			returnErr = fmt.Errorf("release system context catalog lock: lock was not owned")
		}
	}()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin system context catalog transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := operation(db.New(tx)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit system context catalog transaction: %w", err)
	}
	return nil
}

func contextCreateParams(c Context) db.CreateContextParams {
	return db.CreateContextParams{
		UserID:                c.UserID,
		WorkspaceID:           c.WorkspaceID,
		Name:                  c.Name,
		Description:           c.Description,
		IsDefault:             c.IsDefault,
		SegmentationModel:     c.SegmentationModel,
		TranscriptionProvider: c.TranscriptionProvider,
		TranscriptionModel:    c.TranscriptionModel,
		Temperature:           c.Temperature,
		SystemPrompt:          c.SystemPrompt,
	}
}

func contextUpdateParams(c Context) db.UpdateContextParams {
	return db.UpdateContextParams{
		ID:                    c.ID,
		Name:                  c.Name,
		Description:           c.Description,
		IsDefault:             c.IsDefault,
		SegmentationModel:     c.SegmentationModel,
		TranscriptionProvider: c.TranscriptionProvider,
		TranscriptionModel:    c.TranscriptionModel,
		Temperature:           c.Temperature,
		SystemPrompt:          c.SystemPrompt,
	}
}

func (s *ContextStore) Create(ctx context.Context, c Context) (Context, error) {
	if (c.UserID == nil) != (c.WorkspaceID == nil) {
		return Context{}, fmt.Errorf("create context: user and workspace scope must be provided together")
	}
	if c.UserID != nil && (*c.UserID == 0 || *c.WorkspaceID == 0) {
		return Context{}, fmt.Errorf("create context: valid user and workspace scope are required")
	}
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return Context{}, fmt.Errorf("begin create context: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.q.WithTx(tx)
	if c.WorkspaceID != nil {
		if _, err := queries.LockWorkspaceMemberRole(ctx, *c.WorkspaceID, *c.UserID); err != nil {
			return Context{}, fmt.Errorf("create context: lock workspace membership: %w", err)
		}
	}
	if c.IsDefault {
		workspaceID, err := contextWorkspaceParam(c.WorkspaceID)
		if err != nil {
			return Context{}, err
		}
		if err := queries.ClearDefaultContextsForScopeManual(ctx, db.ClearDefaultContextsForScopeManualParams{
			WorkspaceID: workspaceID,
			ExceptID:    0,
		}); err != nil {
			return Context{}, fmt.Errorf("clear existing default context: %w", err)
		}
	}
	id, err := queries.CreateContext(ctx, contextCreateParams(c))
	if err != nil {
		return Context{}, fmt.Errorf("create context: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Context{}, fmt.Errorf("commit create context: %w", err)
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

// GetDefaultForWorkspace returns the workspace default when configured and
// otherwise falls back to the single system default.
func (s *ContextStore) GetDefaultForWorkspace(ctx context.Context, workspaceID uint64) (Context, error) {
	workspaceParam, err := uint64ValueToNullInt64(workspaceID)
	if err != nil {
		return Context{}, err
	}
	row, err := s.q.GetDefaultContextForWorkspaceManual(ctx, workspaceParam)
	if err != nil {
		return Context{}, fmt.Errorf("get workspace default context: %w", err)
	}
	return rowToContext(row), nil
}

func (s *ContextStore) ListPageForWorkspace(ctx context.Context, workspaceID uint64, systemOnly bool, pageSize uint32, cursor *ContextPageCursor) (ContextPage, error) {
	if s == nil || s.q == nil || workspaceID == 0 {
		return ContextPage{}, fmt.Errorf("%w: workspace is required", ErrInvalidContextPage)
	}
	if pageSize == 0 || pageSize > MaxContextPageSize {
		return ContextPage{}, fmt.Errorf("%w: page size must be between 1 and %d", ErrInvalidContextPage, MaxContextPageSize)
	}
	query := db.ListContextsPageParams{
		WorkspaceID: workspaceID,
		SystemOnly:  systemOnly,
		Limit:       int32(pageSize + 1), // #nosec G115 -- pageSize is bounded above.
	}
	if cursor != nil {
		if cursor.ID == 0 {
			return ContextPage{}, fmt.Errorf("%w: cursor ID is required", ErrInvalidContextPage)
		}
		query.CursorID = cursor.ID
		query.CursorIsDefault = cursor.IsDefault
		query.CursorIsSystem = cursor.IsSystem
	}
	rows, err := s.q.ListContextsPageForWorkspace(ctx, query)
	if err != nil {
		return ContextPage{}, fmt.Errorf("list context page: %w", err)
	}
	hasMore := len(rows) > int(pageSize)
	if hasMore {
		rows = rows[:pageSize]
	}
	page := ContextPage{Contexts: make([]Context, 0, len(rows))}
	for _, row := range rows {
		page.Contexts = append(page.Contexts, rowToContext(row))
	}
	if hasMore {
		last := page.Contexts[len(page.Contexts)-1]
		page.NextCursor = &ContextPageCursor{ID: last.ID, IsDefault: last.IsDefault, IsSystem: last.WorkspaceID == nil}
	}
	return page, nil
}

func (s *ContextStore) Update(ctx context.Context, c Context) (Context, error) {
	return s.updateContext(ctx, c, 0)
}

func (s *ContextStore) updateContext(ctx context.Context, c Context, workspaceID uint64) (Context, error) {
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return Context{}, fmt.Errorf("begin update context: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.q.WithTx(tx)
	if workspaceID == 0 {
		if _, err := queries.LockContextForDeleteManual(ctx, c.ID); err != nil {
			return Context{}, fmt.Errorf("lock context before update: %w", err)
		}
	} else {
		if _, err := queries.LockContextForWorkspaceDeleteManual(ctx, db.LockContextForWorkspaceDeleteManualParams{
			ID:          c.ID,
			WorkspaceID: nullableUint64(workspaceID),
		}); err != nil {
			return Context{}, fmt.Errorf("lock workspace context before update: %w", err)
		}
	}
	existing, err := queries.GetContext(ctx, c.ID)
	if err != nil {
		return Context{}, fmt.Errorf("get context before update: %w", err)
	}
	if c.IsDefault {
		workspaceID, err := contextWorkspaceParamFromRow(existing.WorkspaceID)
		if err != nil {
			return Context{}, err
		}
		if err := queries.ClearDefaultContextsForScopeManual(ctx, db.ClearDefaultContextsForScopeManualParams{
			WorkspaceID: workspaceID,
			ExceptID:    c.ID,
		}); err != nil {
			return Context{}, fmt.Errorf("clear existing default context: %w", err)
		}
	}
	result, err := queries.UpdateContext(ctx, contextUpdateParams(c))
	if err := requireDeletedRow(result, err); err != nil {
		return Context{}, fmt.Errorf("update context: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Context{}, fmt.Errorf("commit update context: %w", err)
	}
	return s.Get(ctx, c.ID)
}

func (s *ContextStore) Delete(ctx context.Context, id uint64) error {
	return s.deleteContext(ctx, id, 0)
}

func (s *ContextStore) UpdateForWorkspace(ctx context.Context, c Context, workspaceID uint64, userID uint64) (Context, error) {
	if workspaceID == 0 || userID == 0 {
		return Context{}, sql.ErrNoRows
	}
	c.UserID = &userID
	c.WorkspaceID = &workspaceID
	return s.updateContext(ctx, c, workspaceID)
}

func (s *ContextStore) DeleteForWorkspace(ctx context.Context, id uint64, workspaceID uint64) error {
	if workspaceID == 0 {
		return sql.ErrNoRows
	}
	return s.deleteContext(ctx, id, workspaceID)
}

func (s *ContextStore) deleteContext(ctx context.Context, id, workspaceID uint64) error {
	if s == nil || s.pool == nil || id == 0 {
		return sql.ErrNoRows
	}
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin context deletion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.q.WithTx(tx)
	if workspaceID == 0 {
		if _, err := queries.LockContextForDeleteManual(ctx, id); err != nil {
			return err
		}
	} else if _, err := queries.LockContextForWorkspaceDeleteManual(ctx, db.LockContextForWorkspaceDeleteManualParams{
		ID:          id,
		WorkspaceID: nullableUint64(workspaceID),
	}); err != nil {
		return err
	}
	if err := queries.DeleteSelectionRulesForContextManual(ctx, id); err != nil {
		return fmt.Errorf("delete context selection rules: %w", err)
	}
	contextID := nullableUint64(id)
	if err := queries.ClearOCRRunContextLinksManual(ctx, contextID); err != nil {
		return fmt.Errorf("clear OCR run context links: %w", err)
	}
	if err := queries.ClearTranscriptionJobContextLinksManual(ctx, contextID); err != nil {
		return fmt.Errorf("clear transcription job context links: %w", err)
	}
	if err := queries.ClearUploadBatchContextLinksManual(ctx, contextID); err != nil {
		return fmt.Errorf("clear upload batch context links: %w", err)
	}
	if err := queries.ClearProviderAuditContextLinksManual(ctx, contextID); err != nil {
		return fmt.Errorf("clear provider audit context links: %w", err)
	}
	var result sql.Result
	if workspaceID == 0 {
		result, err = queries.DeleteContextManual(ctx, id)
	} else {
		workspaceParam, paramErr := uint64ValueToNullInt64(workspaceID)
		if paramErr != nil {
			return paramErr
		}
		result, err = queries.DeleteContextForWorkspaceManual(ctx, db.DeleteContextForWorkspaceManualParams{
			ID:          id,
			WorkspaceID: workspaceParam,
		})
	}
	if err := requireDeletedRow(result, err); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit context deletion: %w", err)
	}
	return nil
}

// --- selection rules ---

func (s *ContextStore) CreateRule(ctx context.Context, rule ContextSelectionRule) (ContextSelectionRule, error) {
	if err := validateSelectionRule(rule); err != nil {
		return ContextSelectionRule{}, err
	}
	condJSON, err := json.Marshal(rule.Conditions)
	if err != nil {
		return ContextSelectionRule{}, fmt.Errorf("marshal selection rule conditions: %w", err)
	}
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return ContextSelectionRule{}, fmt.Errorf("begin selection rule creation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.q.WithTx(tx)
	if _, err := queries.LockContextByIDForUseManual(ctx, rule.ContextID); err != nil {
		return ContextSelectionRule{}, fmt.Errorf("lock selection rule context: %w", err)
	}
	id, err := queries.CreateSelectionRule(ctx, db.CreateSelectionRuleParams{
		ContextID:  rule.ContextID,
		Priority:   rule.Priority,
		Conditions: string(condJSON),
	})
	if err != nil {
		return ContextSelectionRule{}, fmt.Errorf("create selection rule: %w", err)
	}
	row, err := queries.GetSelectionRuleByID(ctx, id)
	if err != nil {
		return ContextSelectionRule{}, fmt.Errorf("reload selection rule: %w", err)
	}
	created, err := rowToRule(row)
	if err != nil {
		return ContextSelectionRule{}, err
	}
	if err := tx.Commit(); err != nil {
		return ContextSelectionRule{}, fmt.Errorf("commit selection rule creation: %w", err)
	}
	return created, nil
}

func (s *ContextStore) GetRule(ctx context.Context, id uint64) (ContextSelectionRule, error) {
	row, err := s.q.GetSelectionRuleByID(ctx, id)
	if err != nil {
		return ContextSelectionRule{}, err
	}
	return rowToRule(row)
}

func (s *ContextStore) ListRulePageForWorkspace(ctx context.Context, workspaceID, contextID uint64, pageSize uint32, cursor *SelectionRulePageCursor) (SelectionRulePage, error) {
	if s == nil || s.q == nil || workspaceID == 0 {
		return SelectionRulePage{}, fmt.Errorf("%w: workspace is required", ErrInvalidSelectionRulePage)
	}
	if pageSize == 0 || pageSize > MaxSelectionRulePageSize {
		return SelectionRulePage{}, fmt.Errorf("%w: page size must be between 1 and %d", ErrInvalidSelectionRulePage, MaxSelectionRulePageSize)
	}
	query := db.ListSelectionRulesPageParams{
		WorkspaceID: workspaceID,
		ContextID:   contextID,
		Limit:       int32(pageSize + 1), // #nosec G115 -- pageSize is bounded above.
	}
	if cursor != nil {
		if cursor.ID == 0 {
			return SelectionRulePage{}, fmt.Errorf("%w: cursor ID is required", ErrInvalidSelectionRulePage)
		}
		query.CursorID = cursor.ID
		query.CursorPriority = cursor.Priority
	}
	rows, err := s.q.ListSelectionRulesPageForWorkspace(ctx, query)
	if err != nil {
		return SelectionRulePage{}, fmt.Errorf("list selection rule page: %w", err)
	}
	hasMore := len(rows) > int(pageSize)
	if hasMore {
		rows = rows[:pageSize]
	}
	page := SelectionRulePage{Rules: make([]ContextSelectionRule, 0, len(rows))}
	for _, row := range rows {
		rule, err := rowToRule(row)
		if err != nil {
			return SelectionRulePage{}, err
		}
		page.Rules = append(page.Rules, rule)
	}
	if hasMore {
		last := page.Rules[len(page.Rules)-1]
		page.NextCursor = &SelectionRulePageCursor{ID: last.ID, Priority: last.Priority}
	}
	return page, nil
}

func (s *ContextStore) CreateRuleForWorkspace(ctx context.Context, workspaceID uint64, rule ContextSelectionRule) (ContextSelectionRule, error) {
	if s == nil || s.pool == nil || workspaceID == 0 {
		return ContextSelectionRule{}, fmt.Errorf("workspace selection rule store is not configured")
	}
	if err := validateSelectionRule(rule); err != nil {
		return ContextSelectionRule{}, err
	}
	if rule.Conditions == nil {
		rule.Conditions = make([]RuleCondition, 0)
	}
	conditions, err := json.Marshal(rule.Conditions)
	if err != nil {
		return ContextSelectionRule{}, fmt.Errorf("marshal selection rule conditions: %w", err)
	}
	workspaceParam, err := uint64ValueToNullInt64(workspaceID)
	if err != nil {
		return ContextSelectionRule{}, err
	}
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return ContextSelectionRule{}, fmt.Errorf("begin selection rule admission: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.q.WithTx(tx)
	if _, err := queries.LockWorkspaceForSelectionRuleAdmissionManual(ctx, workspaceID); err != nil {
		return ContextSelectionRule{}, fmt.Errorf("lock selection rule admission: %w", err)
	}
	canWrite, err := queries.WorkspaceCanWriteContextManual(ctx, db.WorkspaceCanWriteContextManualParams{
		ContextID:   rule.ContextID,
		WorkspaceID: workspaceParam,
	})
	if err != nil {
		return ContextSelectionRule{}, fmt.Errorf("authorize selection rule context: %w", err)
	}
	if !canWrite {
		return ContextSelectionRule{}, sql.ErrNoRows
	}
	if _, err := queries.LockContextForUseManual(ctx, db.LockContextForUseManualParams{
		ContextID:   rule.ContextID,
		WorkspaceID: nullableUint64(workspaceID),
	}); err != nil {
		return ContextSelectionRule{}, fmt.Errorf("lock selection rule context: %w", err)
	}
	ruleCount, err := queries.CountSelectionRulesForWorkspace(ctx, workspaceID)
	if err != nil {
		return ContextSelectionRule{}, fmt.Errorf("count workspace selection rules: %w", err)
	}
	if ruleCount >= MaxSelectionRulesPerWorkspace {
		return ContextSelectionRule{}, fmt.Errorf("%w: a workspace may evaluate at most %d rules", ErrSelectionRuleLimit, MaxSelectionRulesPerWorkspace)
	}
	id, err := queries.CreateSelectionRule(ctx, db.CreateSelectionRuleParams{
		ContextID:  rule.ContextID,
		Priority:   rule.Priority,
		Conditions: string(conditions),
	})
	if err != nil {
		return ContextSelectionRule{}, fmt.Errorf("create selection rule: %w", err)
	}
	row, err := queries.GetSelectionRuleForWorkspace(ctx, workspaceID, id)
	if err != nil {
		return ContextSelectionRule{}, fmt.Errorf("reload workspace selection rule: %w", err)
	}
	created, err := rowToRule(row)
	if err != nil {
		return ContextSelectionRule{}, err
	}
	if err := tx.Commit(); err != nil {
		return ContextSelectionRule{}, fmt.Errorf("commit selection rule admission: %w", err)
	}
	return created, nil
}

func (s *ContextStore) DeleteRuleForWorkspace(ctx context.Context, workspaceID, ruleID uint64) error {
	return s.q.DeleteSelectionRuleForWorkspace(ctx, workspaceID, ruleID)
}

// WorkspaceOwnsSelectionRule is the side-effect-free authorization lookup for
// rule mutations. System rules are never workspace-owned and cannot be deleted
// through a tenant credential.
func (s *ContextStore) WorkspaceOwnsSelectionRule(ctx context.Context, workspaceID, ruleID uint64) (bool, error) {
	if s == nil || s.q == nil || workspaceID == 0 || ruleID == 0 {
		return false, nil
	}
	workspaceParam, err := uint64ValueToNullInt64(workspaceID)
	if err != nil {
		return false, err
	}
	return s.q.WorkspaceOwnsSelectionRuleManual(ctx, db.WorkspaceOwnsSelectionRuleManualParams{
		WorkspaceID: workspaceParam,
		RuleID:      ruleID,
	})
}

func (s *ContextStore) DeleteRule(ctx context.Context, id uint64) error {
	return s.q.DeleteSelectionRule(ctx, id)
}

// Resolve evaluates a bounded rule set (ordered by priority desc) against a
// pre-normalized metadata bag and returns the first match, or the default.
func (s *ContextStore) Resolve(ctx context.Context, metadata map[string]any) (Context, bool, error) {
	normalized, err := normalizeContextMetadata(metadata)
	if err != nil {
		return Context{}, false, err
	}
	rows, err := s.q.ListSelectionRulesForResolutionManual(ctx, int32(MaxSelectionRulesPerWorkspace+1))
	if err != nil {
		return Context{}, false, err
	}
	rules, err := boundedSelectionRules(rows)
	if err != nil {
		return Context{}, false, err
	}
	for _, rule := range rules {
		if matchesAll(rule.Conditions, normalized) {
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

func contextWorkspaceParam(workspaceID *uint64) (sql.NullInt64, error) {
	if workspaceID == nil {
		return sql.NullInt64{}, nil
	}
	return uint64ValueToNullInt64(*workspaceID)
}

func contextWorkspaceParamFromRow(workspaceID sql.NullInt64) (sql.NullInt64, error) {
	if !workspaceID.Valid {
		return sql.NullInt64{}, nil
	}
	if workspaceID.Int64 < 0 {
		return sql.NullInt64{}, fmt.Errorf("context workspace id is negative")
	}
	return workspaceID, nil
}

func (s *ContextStore) ResolveForWorkspace(ctx context.Context, workspaceID uint64, metadata map[string]any) (Context, bool, error) {
	if workspaceID == 0 {
		return Context{}, false, fmt.Errorf("workspace is required")
	}
	normalized, err := normalizeContextMetadata(metadata)
	if err != nil {
		return Context{}, false, err
	}
	rows, err := s.q.ListSelectionRulesForWorkspaceResolution(ctx, workspaceID, int32(MaxSelectionRulesPerWorkspace+1))
	if err != nil {
		return Context{}, false, err
	}
	rules, err := boundedSelectionRules(rows)
	if err != nil {
		return Context{}, false, err
	}
	for _, rule := range rules {
		if matchesAll(rule.Conditions, normalized) {
			c, err := s.GetForWorkspace(ctx, rule.ContextID, workspaceID)
			if err != nil {
				continue
			}
			return c, false, nil
		}
	}
	def, err := s.GetDefaultForWorkspace(ctx, workspaceID)
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

func validateSelectionRule(rule ContextSelectionRule) error {
	if rule.ContextID == 0 {
		return fmt.Errorf("selection rule context is required")
	}
	if len(rule.Conditions) > MaxSelectionRuleConditions {
		return fmt.Errorf("selection rule may contain at most %d conditions", MaxSelectionRuleConditions)
	}
	for _, condition := range rule.Conditions {
		if !utf8.ValidString(condition.Field) || strings.TrimSpace(condition.Field) == "" || len(condition.Field) > MaxSelectionRuleFieldBytes {
			return fmt.Errorf("selection rule field must contain at most %d valid UTF-8 bytes", MaxSelectionRuleFieldBytes)
		}
		if !utf8.ValidString(condition.Value) || len(condition.Value) > MaxSelectionRuleValueBytes {
			return fmt.Errorf("selection rule value must contain at most %d valid UTF-8 bytes", MaxSelectionRuleValueBytes)
		}
		switch condition.Operator {
		case "eq", "neq", "contains", "starts_with", "ends_with":
		default:
			return fmt.Errorf("selection rule operator is invalid")
		}
	}
	return nil
}

func boundedSelectionRules(rows []db.ContextSelectionRule) ([]ContextSelectionRule, error) {
	if len(rows) > MaxSelectionRulesPerWorkspace {
		return nil, fmt.Errorf("%w: at most %d rules may be evaluated", ErrContextResolutionLimit, MaxSelectionRulesPerWorkspace)
	}
	rules := make([]ContextSelectionRule, 0, len(rows))
	for _, row := range rows {
		rule, err := rowToRule(row)
		if err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

// normalizeContextMetadata bounds the Cartesian rule-evaluation cost and
// formats each scalar once. Nested JSON is deliberately rejected because rule
// fields address a flat metadata bag, not an unbounded object graph.
func normalizeContextMetadata(metadata map[string]any) (map[string]string, error) {
	if len(metadata) > MaxContextMetadataFields {
		return nil, fmt.Errorf("%w: at most %d fields are allowed", ErrInvalidContextMetadata, MaxContextMetadataFields)
	}
	normalized := make(map[string]string, len(metadata))
	for field, raw := range metadata {
		if !utf8.ValidString(field) || strings.TrimSpace(field) == "" || len(field) > MaxContextMetadataKeyBytes {
			return nil, fmt.Errorf("%w: field names must contain at most %d valid UTF-8 bytes", ErrInvalidContextMetadata, MaxContextMetadataKeyBytes)
		}
		value, err := normalizeContextMetadataScalar(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: field %q: %v", ErrInvalidContextMetadata, field, err)
		}
		if !utf8.ValidString(value) || len(value) > MaxContextMetadataScalarBytes {
			return nil, fmt.Errorf("%w: scalar values must contain at most %d valid UTF-8 bytes", ErrInvalidContextMetadata, MaxContextMetadataScalarBytes)
		}
		normalized[field] = value
	}
	return normalized, nil
}

func normalizeContextMetadataScalar(value any) (string, error) {
	switch typed := value.(type) {
	case nil:
		return "null", nil
	case string:
		return typed, nil
	case bool:
		return strconv.FormatBool(typed), nil
	case json.Number:
		if _, err := typed.Float64(); err != nil {
			return "", fmt.Errorf("number is invalid")
		}
		return typed.String(), nil
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return "", fmt.Errorf("number must be finite")
		}
		return strconv.FormatFloat(typed, 'g', -1, 64), nil
	case float32:
		if math.IsNaN(float64(typed)) || math.IsInf(float64(typed), 0) {
			return "", fmt.Errorf("number must be finite")
		}
		return strconv.FormatFloat(float64(typed), 'g', -1, 32), nil
	case int:
		return strconv.FormatInt(int64(typed), 10), nil
	case int8:
		return strconv.FormatInt(int64(typed), 10), nil
	case int16:
		return strconv.FormatInt(int64(typed), 10), nil
	case int32:
		return strconv.FormatInt(int64(typed), 10), nil
	case int64:
		return strconv.FormatInt(typed, 10), nil
	case uint:
		return strconv.FormatUint(uint64(typed), 10), nil
	case uint8:
		return strconv.FormatUint(uint64(typed), 10), nil
	case uint16:
		return strconv.FormatUint(uint64(typed), 10), nil
	case uint32:
		return strconv.FormatUint(uint64(typed), 10), nil
	case uint64:
		return strconv.FormatUint(typed, 10), nil
	default:
		return "", fmt.Errorf("value must be a string, number, boolean, or null")
	}
}

// matchesAll returns true if all conditions are satisfied by metadata that was
// normalized once before rule evaluation.
func matchesAll(conditions []RuleCondition, metadata map[string]string) bool {
	for _, cond := range conditions {
		value, ok := metadata[cond.Field]
		if !ok {
			return false
		}
		switch cond.Operator {
		case "eq":
			if value != cond.Value {
				return false
			}
		case "neq":
			if value == cond.Value {
				return false
			}
		case "contains":
			if !strings.Contains(value, cond.Value) {
				return false
			}
		case "starts_with":
			if !strings.HasPrefix(value, cond.Value) {
				return false
			}
		case "ends_with":
			if !strings.HasSuffix(value, cond.Value) {
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
	if uid, ok := uint64PtrFromNullInt64(row.UserID); ok {
		c.UserID = uid
	}
	if workspaceID, ok := uint64PtrFromNullInt64(row.WorkspaceID); ok {
		c.WorkspaceID = workspaceID
	}
	if row.Description.Valid {
		c.Description = row.Description.String
	}
	if row.Temperature.Valid {
		c.Temperature = &row.Temperature.Float64
	}
	if row.SystemPrompt.Valid {
		c.SystemPrompt = row.SystemPrompt.String
	}
	return c
}

func lockContextSnapshotForWorkspace(ctx context.Context, queries *db.Queries, contextID, workspaceID uint64) (Context, json.RawMessage, error) {
	row, err := queries.LockContextForUseManual(ctx, db.LockContextForUseManualParams{
		ContextID: contextID, WorkspaceID: nullableUint64(workspaceID),
	})
	if err != nil {
		return Context{}, nil, err
	}
	locked := rowToContext(row)
	snapshot, err := json.Marshal(locked)
	if err != nil {
		return Context{}, nil, fmt.Errorf("encode locked context snapshot: %w", err)
	}
	return locked, snapshot, nil
}

func rowToRule(row db.ContextSelectionRule) (ContextSelectionRule, error) {
	r := ContextSelectionRule{
		ID:        row.ID,
		ContextID: row.ContextID,
		Priority:  row.Priority,
		CreatedAt: row.CreatedAt,
	}
	if len(row.Conditions) == 0 {
		return ContextSelectionRule{}, fmt.Errorf("selection rule %d has empty conditions JSON", row.ID)
	}
	if err := json.Unmarshal(row.Conditions, &r.Conditions); err != nil {
		return ContextSelectionRule{}, fmt.Errorf("decode selection rule %d conditions: %w", row.ID, err)
	}
	if r.Conditions == nil {
		return ContextSelectionRule{}, fmt.Errorf("selection rule %d conditions must be a JSON array", row.ID)
	}
	if err := validateSelectionRule(r); err != nil {
		return ContextSelectionRule{}, fmt.Errorf("selection rule %d is invalid: %w", row.ID, err)
	}
	return r, nil
}
