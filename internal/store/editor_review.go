package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/lehigh-university-libraries/scribe/internal/db"
)

const (
	MaxActiveEditorReviewTokensPerWorkspace   = 100
	MaxActiveEditorReviewSessionsPerWorkspace = 100
)

var (
	ErrEditorReviewTokenLimit   = errors.New("workspace editor review token limit reached")
	ErrEditorReviewTokenInvalid = errors.New("editor review token is invalid or expired")
)

// EditorReviewGrant is the durable, one-time half of an integration-to-browser
// handoff. TokenHash and ReviewerSubjectHash are irreversible SHA-256 digests;
// callers must never persist the raw URL token or external subject here.
type EditorReviewGrant struct {
	ID                  string
	TokenHash           string
	WorkspaceID         uint64
	ItemID              string
	ItemImageID         uint64
	IssuedByUserID      uint64
	ReviewerSubjectHash string
	ReviewerName        string
	ReviewerEmail       string
	SessionTTL          time.Duration
	ExpiresAt           time.Time
}

// EditorReviewSession is an item-image-scoped browser credential. The issuer
// remains the canonical Scribe user used for mutation attribution, while the
// caller-vouched display identity is retained only as bounded session metadata.
type EditorReviewSession struct {
	ID                  uint64
	ReviewTokenID       string
	Workspace           Workspace
	ItemID              string
	ItemImageID         uint64
	IssuedBy            User
	IssuerRole          string
	ReviewerSubjectHash string
	ReviewerName        string
	ReviewerEmail       string
	ExpiresAt           time.Time
}

type RedeemEditorReviewGrantParams struct {
	TokenHash       string
	GrantID         string
	WorkspaceID     uint64
	ItemID          string
	ItemImageID     uint64
	RawSessionToken string
	UserAgent       string
	IPAddress       string
}

// CreateEditorReviewGrant serializes active-token admission with the owning
// workspace and rechecks the complete workspace/item/image/issuer tuple in the
// same transaction as the grant write.
func (s *IdentityStore) CreateEditorReviewGrant(ctx context.Context, grant EditorReviewGrant) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("create editor review grant: identity store is not configured")
	}
	if err := validateEditorReviewGrant(grant); err != nil {
		return err
	}
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("create editor review grant: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.q.WithTx(tx)
	if _, err := queries.LockWorkspaceManual(ctx, grant.WorkspaceID); err != nil {
		return fmt.Errorf("create editor review grant: lock workspace: %w", err)
	}
	access, err := queries.GetWorkspaceAccess(ctx, grant.WorkspaceID, grant.IssuedByUserID)
	if err != nil || !reviewIssuerRoleCanWrite(access.Role) {
		return fmt.Errorf("%w: issuer is not a current workspace writer", ErrEditorReviewTokenInvalid)
	}
	image, err := queries.GetItemImageForWorkspace(ctx, grant.ItemImageID, grant.WorkspaceID)
	if err != nil || image.ItemID != grant.ItemID {
		return fmt.Errorf("%w: item image is not in the grant workspace", ErrEditorReviewTokenInvalid)
	}
	active, err := queries.CountActiveEditorReviewTokensForWorkspaceManual(ctx, grant.WorkspaceID)
	if err != nil {
		return fmt.Errorf("create editor review grant: count active grants: %w", err)
	}
	if active >= MaxActiveEditorReviewTokensPerWorkspace {
		return ErrEditorReviewTokenLimit
	}
	if err := queries.CreateEditorReviewTokenManual(ctx, db.CreateEditorReviewTokenManualParams{
		ID:                  grant.ID,
		TokenHash:           grant.TokenHash,
		WorkspaceID:         grant.WorkspaceID,
		ItemID:              grant.ItemID,
		ItemImageID:         grant.ItemImageID,
		IssuedByUserID:      grant.IssuedByUserID,
		ReviewerSubjectHash: grant.ReviewerSubjectHash,
		ReviewerName:        grant.ReviewerName,
		ReviewerEmail:       nullableString(grant.ReviewerEmail),
		SessionTtlSeconds:   uint32(grant.SessionTTL / time.Second), // #nosec G115 -- validation above bounds this to 300..28800.
		ExpiresAt:           grant.ExpiresAt.UTC(),
	}); err != nil {
		return fmt.Errorf("create editor review grant: persist: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("create editor review grant: commit: %w", err)
	}
	return nil
}

// RedeemEditorReviewGrant consumes a grant and creates the scoped browser
// session atomically. A concurrent replay can observe neither an unconsumed
// grant nor a partially created session.
func (s *IdentityStore) RedeemEditorReviewGrant(ctx context.Context, params RedeemEditorReviewGrantParams) (EditorReviewSession, error) {
	if s == nil || s.pool == nil || strings.TrimSpace(params.RawSessionToken) == "" {
		return EditorReviewSession{}, ErrEditorReviewTokenInvalid
	}
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return EditorReviewSession{}, fmt.Errorf("redeem editor review grant: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.q.WithTx(tx)
	if _, err := queries.LockWorkspaceManual(ctx, params.WorkspaceID); err != nil {
		return EditorReviewSession{}, ErrEditorReviewTokenInvalid
	}
	row, err := queries.LockEditorReviewTokenByHashManual(ctx, params.TokenHash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return EditorReviewSession{}, ErrEditorReviewTokenInvalid
		}
		return EditorReviewSession{}, fmt.Errorf("redeem editor review grant: load: %w", err)
	}
	now := time.Now().UTC()
	if row.RedeemedAt.Valid || !now.Before(row.ExpiresAt) ||
		row.ID != params.GrantID || row.WorkspaceID != params.WorkspaceID ||
		row.ItemID != params.ItemID || row.ItemImageID != params.ItemImageID {
		return EditorReviewSession{}, ErrEditorReviewTokenInvalid
	}
	access, err := queries.GetWorkspaceAccess(ctx, row.WorkspaceID, row.IssuedByUserID)
	if err != nil || !reviewIssuerRoleCanWrite(access.Role) {
		return EditorReviewSession{}, ErrEditorReviewTokenInvalid
	}
	image, err := queries.GetItemImageForWorkspace(ctx, row.ItemImageID, row.WorkspaceID)
	if err != nil || image.ItemID != row.ItemID {
		return EditorReviewSession{}, ErrEditorReviewTokenInvalid
	}
	result, err := queries.MarkEditorReviewTokenRedeemedManual(ctx, row.ID)
	if err != nil {
		return EditorReviewSession{}, fmt.Errorf("redeem editor review grant: consume: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return EditorReviewSession{}, ErrEditorReviewTokenInvalid
	}
	if err := queries.DeleteExpiredEditorReviewSessionsForWorkspaceManual(ctx, row.WorkspaceID); err != nil {
		return EditorReviewSession{}, fmt.Errorf("redeem editor review grant: remove expired sessions: %w", err)
	}
	activeSessions, err := queries.CountActiveEditorReviewSessionsForWorkspaceManual(ctx, row.WorkspaceID)
	if err != nil {
		return EditorReviewSession{}, fmt.Errorf("redeem editor review grant: count sessions: %w", err)
	}
	for activeSessions >= MaxActiveEditorReviewSessionsPerWorkspace {
		result, deleteErr := queries.DeleteOldestEditorReviewSessionForWorkspaceManual(ctx, row.WorkspaceID)
		if deleteErr != nil {
			return EditorReviewSession{}, fmt.Errorf("redeem editor review grant: evict oldest session: %w", deleteErr)
		}
		removed, rowsErr := result.RowsAffected()
		if rowsErr != nil || removed != 1 {
			return EditorReviewSession{}, fmt.Errorf("redeem editor review grant: session admission state changed")
		}
		activeSessions--
	}
	sessionExpiresAt := now.Add(time.Duration(row.SessionTtlSeconds) * time.Second)
	if err := queries.CreateEditorReviewSessionManual(ctx, db.CreateEditorReviewSessionManualParams{
		TokenHash:           hashSessionToken(params.RawSessionToken),
		ReviewTokenID:       row.ID,
		WorkspaceID:         row.WorkspaceID,
		ItemID:              row.ItemID,
		ItemImageID:         row.ItemImageID,
		IssuedByUserID:      row.IssuedByUserID,
		ReviewerSubjectHash: row.ReviewerSubjectHash,
		ReviewerName:        row.ReviewerName,
		ReviewerEmail:       row.ReviewerEmail,
		ExpiresAt:           sessionExpiresAt,
		UserAgent:           nullableString(boundedEditorReviewAuditValue(params.UserAgent, 1024)),
		IpAddress:           nullableString(boundedEditorReviewAuditValue(params.IPAddress, 255)),
	}); err != nil {
		return EditorReviewSession{}, fmt.Errorf("redeem editor review grant: create session: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return EditorReviewSession{}, fmt.Errorf("redeem editor review grant: commit: %w", err)
	}
	return s.GetEditorReviewSession(ctx, params.RawSessionToken)
}

// GetEditorReviewSession is intentionally read-only. Current issuer membership
// is checked on every request, so removing or demoting the integration account
// immediately constrains every outstanding review session.
func (s *IdentityStore) GetEditorReviewSession(ctx context.Context, rawToken string) (EditorReviewSession, error) {
	if s == nil || s.q == nil || strings.TrimSpace(rawToken) == "" {
		return EditorReviewSession{}, sql.ErrNoRows
	}
	row, err := s.q.GetEditorReviewSessionByTokenHashManual(ctx, hashSessionToken(rawToken))
	if err != nil {
		return EditorReviewSession{}, err
	}
	if !time.Now().UTC().Before(row.ExpiresAt) {
		return EditorReviewSession{}, sql.ErrNoRows
	}
	user, err := s.q.GetUser(ctx, row.IssuedByUserID)
	if err != nil {
		return EditorReviewSession{}, err
	}
	access, err := s.q.GetWorkspaceAccess(ctx, row.WorkspaceID, row.IssuedByUserID)
	if err != nil {
		return EditorReviewSession{}, err
	}
	return editorReviewSessionFromRows(row, user, access), nil
}

func (s *IdentityStore) DeleteEditorReviewSession(ctx context.Context, rawToken string) error {
	if s == nil || s.q == nil || strings.TrimSpace(rawToken) == "" {
		return nil
	}
	return s.q.DeleteEditorReviewSessionByTokenHashManual(ctx, hashSessionToken(rawToken))
}

func (s *IdentityStore) retainExpiredEditorReviewState(ctx context.Context, cutoff time.Time) error {
	for {
		result, err := s.q.DeleteExpiredEditorReviewSessionsBatchManual(ctx, cutoff.UTC())
		if err != nil {
			return fmt.Errorf("retain editor review sessions: %w", err)
		}
		removed, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("retain editor review sessions: verify batch: %w", err)
		}
		if removed < 1000 {
			break
		}
	}
	for {
		result, err := s.q.DeleteRetainedEditorReviewTokensBatchManual(ctx, cutoff.UTC())
		if err != nil {
			return fmt.Errorf("retain editor review tokens: %w", err)
		}
		removed, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("retain editor review tokens: verify batch: %w", err)
		}
		if removed < 1000 {
			return nil
		}
	}
}

func validateEditorReviewGrant(grant EditorReviewGrant) error {
	if strings.TrimSpace(grant.ID) == "" || !isSHA256Hex(grant.TokenHash) ||
		!isSHA256Hex(grant.ReviewerSubjectHash) || grant.WorkspaceID == 0 ||
		grant.IssuedByUserID == 0 || grant.ItemImageID == 0 || strings.TrimSpace(grant.ItemID) == "" || len(grant.ItemID) > 64 {
		return fmt.Errorf("%w: incomplete grant identity", ErrEditorReviewTokenInvalid)
	}
	name := strings.TrimSpace(grant.ReviewerName)
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 255 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return fmt.Errorf("%w: invalid reviewer name", ErrEditorReviewTokenInvalid)
	}
	if !utf8.ValidString(grant.ReviewerEmail) || utf8.RuneCountInString(grant.ReviewerEmail) > 320 || strings.IndexFunc(grant.ReviewerEmail, unicode.IsControl) >= 0 {
		return fmt.Errorf("%w: invalid reviewer email", ErrEditorReviewTokenInvalid)
	}
	if grant.SessionTTL < 5*time.Minute || grant.SessionTTL > 8*time.Hour ||
		!grant.ExpiresAt.After(time.Now().UTC()) || grant.ExpiresAt.After(time.Now().UTC().Add(11*time.Minute)) {
		return fmt.Errorf("%w: unsafe grant lifetime", ErrEditorReviewTokenInvalid)
	}
	return nil
}

func isSHA256Hex(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func boundedEditorReviewAuditValue(value string, maximumRunes int) string {
	if maximumRunes <= 0 {
		return ""
	}
	value = strings.ToValidUTF8(value, "")
	value = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return -1
		}
		return character
	}, value)
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) <= maximumRunes {
		return value
	}
	runes := []rune(value)
	return strings.TrimSpace(string(runes[:maximumRunes]))
}

func reviewIssuerRoleCanWrite(role string) bool {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "admin", "write":
		return true
	default:
		return false
	}
}

func editorReviewSessionFromRows(row db.EditorReviewSession, user db.User, access db.WorkspaceAccess) EditorReviewSession {
	session := EditorReviewSession{
		ID:                  row.ID,
		ReviewTokenID:       row.ReviewTokenID,
		Workspace:           rowToWorkspace(access.Workspace),
		ItemID:              row.ItemID,
		ItemImageID:         row.ItemImageID,
		IssuedBy:            rowToUser(user),
		IssuerRole:          access.Role,
		ReviewerSubjectHash: row.ReviewerSubjectHash,
		ReviewerName:        row.ReviewerName,
		ExpiresAt:           row.ExpiresAt,
	}
	if row.ReviewerEmail.Valid {
		session.ReviewerEmail = row.ReviewerEmail.String
	}
	return session
}
