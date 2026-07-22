package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	db "github.com/lehigh-university-libraries/scribe/internal/db"
)

type User struct {
	ID            uint64    `json:"id"`
	Name          string    `json:"name"`
	Email         string    `json:"email,omitempty"`
	GoogleSubject string    `json:"google_subject,omitempty"`
	PictureURL    string    `json:"picture_url,omitempty"`
	IsAdmin       bool      `json:"is_admin"`
	LastLoginAt   time.Time `json:"last_login_at,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type Workspace struct {
	ID          uint64    `json:"id"`
	Name        string    `json:"name"`
	Slug        string    `json:"slug"`
	IsPersonal  bool      `json:"is_personal"`
	OwnerUserID *uint64   `json:"owner_user_id,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type WorkspaceAccess struct {
	Workspace Workspace `json:"workspace"`
	Role      string    `json:"role"`
}

type GoogleProfile struct {
	Subject    string
	Email      string
	Name       string
	PictureURL string
	IsAdmin    bool
}

type IdentitySession struct {
	User      User
	Workspace Workspace
	Role      string
}

type IdentityStore struct {
	pool *sql.DB
	q    *db.Queries
}

const (
	MaxAuthSessionsPerUser            = 20
	identityConvergenceReleaseTimeout = 5 * time.Second
)

func NewIdentityStore(pool *sql.DB) *IdentityStore {
	return &IdentityStore{
		pool: pool,
		q:    db.New(pool),
	}
}

func (s *IdentityStore) EnsureGoogleUser(ctx context.Context, profile GoogleProfile) (User, Workspace, error) {
	profile.Email = strings.ToLower(strings.TrimSpace(profile.Email))
	profile.Subject = strings.TrimSpace(profile.Subject)
	profile.Name = strings.TrimSpace(profile.Name)
	profile.PictureURL = strings.TrimSpace(profile.PictureURL)
	if profile.Subject == "" {
		return User{}, Workspace{}, fmt.Errorf("google subject is required")
	}
	if profile.Email == "" {
		return User{}, Workspace{}, fmt.Errorf("email is required")
	}
	if profile.Name == "" {
		profile.Name = profile.Email
	}

	for attempt := 0; attempt < 16; attempt++ {
		user, workspace, err := s.ensureGoogleIdentity(ctx, profile)
		if err == nil {
			return user, workspace, nil
		}
		if !isIdentityConvergenceError(err) {
			return User{}, Workspace{}, err
		}
	}
	return User{}, Workspace{}, fmt.Errorf("ensure Google identity: concurrent identity creation did not converge")
}

func (s *IdentityStore) ensureGoogleIdentity(ctx context.Context, profile GoogleProfile) (userResult User, workspaceResult Workspace, returnErr error) {
	if s == nil || s.pool == nil {
		return User{}, Workspace{}, fmt.Errorf("ensure Google identity: store is not configured")
	}
	conn, err := s.pool.Conn(ctx)
	if err != nil {
		return User{}, Workspace{}, fmt.Errorf("ensure Google identity: reserve connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	lockQueries := db.New(conn)
	acquiredLocks := make([]string, 0, 2)
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), identityConvergenceReleaseTimeout)
		defer cancel()
		for index := len(acquiredLocks) - 1; index >= 0; index-- {
			released, err := lockQueries.ReleaseIdentityConvergenceLockManual(releaseCtx, acquiredLocks[index])
			if returnErr == nil && err != nil {
				returnErr = fmt.Errorf("ensure Google identity: release convergence lock: %w", err)
			} else if returnErr == nil && !released {
				returnErr = fmt.Errorf("ensure Google identity: convergence lock was not owned")
			}
		}
	}()
	for _, lockName := range identityConvergenceLockNames(profile) {
		acquired, err := lockQueries.AcquireIdentityConvergenceLockManual(ctx, lockName)
		if err != nil {
			return User{}, Workspace{}, fmt.Errorf("ensure Google identity: acquire convergence lock: %w", err)
		}
		if !acquired {
			return User{}, Workspace{}, fmt.Errorf("ensure Google identity: convergence lock acquisition timed out")
		}
		acquiredLocks = append(acquiredLocks, lockName)
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return User{}, Workspace{}, fmt.Errorf("ensure Google identity: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := db.New(tx)

	subjectRow, err := queries.LockUserByGoogleSubjectForIdentityManual(ctx, nullableString(profile.Subject))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return User{}, Workspace{}, fmt.Errorf("get user by subject: %w", err)
	}
	row := subjectRow.User
	if errors.Is(err, sql.ErrNoRows) {
		emailRow, emailErr := queries.LockUserByEmailForIdentityManual(ctx, nullableString(profile.Email))
		err = emailErr
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return User{}, Workspace{}, fmt.Errorf("get user by email: %w", err)
		}
		row = emailRow.User
	}
	userID := row.ID
	if errors.Is(err, sql.ErrNoRows) {
		userID, err = queries.CreateUser(ctx, db.CreateUserParams{
			Name:          profile.Name,
			Email:         profile.Email,
			GoogleSubject: profile.Subject,
			PictureURL:    profile.PictureURL,
			IsAdmin:       profile.IsAdmin,
		})
		if err != nil {
			return User{}, Workspace{}, fmt.Errorf("create user: %w", err)
		}
	}
	if _, err := queries.LockUserForIdentityAdmissionManual(ctx, userID); err != nil {
		return User{}, Workspace{}, fmt.Errorf("lock Google identity: %w", err)
	}
	if err := queries.UpdateUserAuthProfile(ctx, db.UpdateUserAuthProfileParams{
		ID:            userID,
		Name:          profile.Name,
		Email:         profile.Email,
		GoogleSubject: profile.Subject,
		PictureURL:    profile.PictureURL,
		IsAdmin:       profile.IsAdmin,
	}); err != nil {
		return User{}, Workspace{}, fmt.Errorf("update user auth profile: %w", err)
	}
	userRow, err := queries.GetUser(ctx, userID)
	if err != nil {
		return User{}, Workspace{}, fmt.Errorf("reload Google identity: %w", err)
	}
	workspaceRow, err := ensurePersonalWorkspaceTx(ctx, queries, rowToUser(userRow))
	if err != nil {
		return User{}, Workspace{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, Workspace{}, fmt.Errorf("ensure Google identity: commit: %w", err)
	}
	return rowToUser(userRow), workspaceRow, nil
}

func identityConvergenceLockNames(profile GoogleProfile) []string {
	values := []string{"subject\x00" + profile.Subject, "email\x00" + profile.Email}
	names := make([]string, 0, len(values))
	for _, value := range values {
		sum := sha256.Sum256([]byte(value))
		names = append(names, "scribe:identity:"+hex.EncodeToString(sum[:16]))
	}
	sort.Strings(names)
	return names
}

func isIdentityConvergenceError(err error) bool {
	var mysqlErr *mysql.MySQLError
	if !errors.As(err, &mysqlErr) {
		return false
	}
	switch mysqlErr.Number {
	case 1020, // record changed between a consistent read and a locking read
		1062, // duplicate email, subject, personal workspace, or membership
		1205, // lock wait timeout while another identity transaction commits
		1213: // deadlock victim during absent-key gap-lock convergence
		return true
	default:
		return false
	}
}

func (s *IdentityStore) GetUser(ctx context.Context, id uint64) (User, error) {
	row, err := s.q.GetUser(ctx, id)
	if err != nil {
		return User{}, fmt.Errorf("get user: %w", err)
	}
	return rowToUser(row), nil
}

func (s *IdentityStore) CreateSession(ctx context.Context, userID uint64, rawToken, userAgent, ipAddress string, ttl time.Duration) error {
	tokenHash := hashSessionToken(rawToken)
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("create session: begin admission transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.q.WithTx(tx)
	if _, err := queries.LockUserForIdentityAdmissionManual(ctx, userID); err != nil {
		return fmt.Errorf("create session: lock user: %w", err)
	}
	if err := queries.DeleteExpiredAuthSessionsForUserManual(ctx, userID); err != nil {
		return fmt.Errorf("create session: remove expired sessions: %w", err)
	}
	count, err := queries.CountAuthSessionsForUserManual(ctx, userID)
	if err != nil {
		return fmt.Errorf("create session: count sessions: %w", err)
	}
	for count >= MaxAuthSessionsPerUser {
		result, deleteErr := queries.DeleteOldestAuthSessionForUserManual(ctx, userID)
		if deleteErr != nil {
			return fmt.Errorf("create session: evict oldest session: %w", deleteErr)
		}
		removed, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return fmt.Errorf("create session: verify eviction: %w", rowsErr)
		}
		if removed != 1 {
			return fmt.Errorf("create session: session admission state changed")
		}
		count--
	}
	if err := queries.CreateAuthSession(ctx, db.CreateAuthSessionParams{
		TokenHash: tokenHash,
		UserID:    userID,
		ExpiresAt: time.Now().UTC().Add(ttl),
		UserAgent: userAgent,
		IPAddress: ipAddress,
	}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("create session: commit admission: %w", err)
	}
	return nil
}

func (s *IdentityStore) RetainExpiredSessions(ctx context.Context, cutoff time.Time) error {
	for {
		result, err := s.q.DeleteExpiredAuthSessionsBatchManual(ctx, cutoff.UTC())
		if err != nil {
			return fmt.Errorf("retain expired sessions: %w", err)
		}
		removed, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("retain expired sessions: verify batch: %w", err)
		}
		if removed < 1000 {
			return nil
		}
	}
}

func (s *IdentityStore) GetSession(ctx context.Context, rawToken string) (IdentitySession, error) {
	sessionRow, err := s.q.GetAuthSessionByTokenHash(ctx, hashSessionToken(rawToken))
	if err != nil {
		return IdentitySession{}, err
	}
	if time.Now().UTC().After(sessionRow.ExpiresAt) {
		return IdentitySession{}, sql.ErrNoRows
	}
	user, err := s.GetUser(ctx, sessionRow.UserID)
	if err != nil {
		return IdentitySession{}, err
	}
	workspace, err := s.getPersonalWorkspace(ctx, user.ID)
	if err != nil {
		return IdentitySession{}, err
	}
	access, err := s.GetWorkspaceAccess(ctx, user.ID, workspace.ID)
	if err != nil {
		return IdentitySession{}, err
	}
	return IdentitySession{User: user, Workspace: workspace, Role: access.Role}, nil
}

func (s *IdentityStore) DeleteSession(ctx context.Context, rawToken string) error {
	return s.q.DeleteAuthSessionByTokenHash(ctx, hashSessionToken(rawToken))
}

func ensurePersonalWorkspaceTx(ctx context.Context, queries *db.Queries, user User) (Workspace, error) {
	row, err := queries.GetPersonalWorkspaceByUserID(ctx, user.ID)
	if err == nil {
		if err := queries.EnsureStorageQuotaUsage(ctx, row.ID); err != nil {
			return Workspace{}, fmt.Errorf("repair personal workspace quota row: %w", err)
		}
		if err := queries.CreateWorkspaceMember(ctx, row.ID, user.ID, "admin"); err != nil {
			return Workspace{}, fmt.Errorf("repair personal workspace membership: %w", err)
		}
		return rowToWorkspace(row), nil
	}
	if err != sql.ErrNoRows {
		return Workspace{}, fmt.Errorf("get personal workspace: %w", err)
	}
	ownerUserID := user.ID
	createdByUserID := user.ID
	workspaceID, err := queries.CreateWorkspace(ctx, db.CreateWorkspaceParams{
		OwnerUserID:     &ownerUserID,
		Name:            personalWorkspaceName(user),
		Slug:            fmt.Sprintf("user-%d-personal", user.ID),
		IsPersonal:      true,
		CreatedByUserID: &createdByUserID,
	})
	if err != nil {
		return Workspace{}, fmt.Errorf("create personal workspace: %w", err)
	}
	if err := queries.EnsureStorageQuotaUsage(ctx, workspaceID); err != nil {
		return Workspace{}, fmt.Errorf("create personal workspace quota row: %w", err)
	}
	if err := queries.CreateWorkspaceMember(ctx, workspaceID, user.ID, "admin"); err != nil {
		return Workspace{}, fmt.Errorf("create personal workspace membership: %w", err)
	}
	row, err = queries.GetPersonalWorkspaceByUserID(ctx, user.ID)
	if err != nil {
		return Workspace{}, fmt.Errorf("reload personal workspace: %w", err)
	}
	return rowToWorkspace(row), nil
}

func (s *IdentityStore) getPersonalWorkspace(ctx context.Context, userID uint64) (Workspace, error) {
	row, err := s.q.GetPersonalWorkspaceByUserID(ctx, userID)
	if err != nil {
		return Workspace{}, err
	}
	return rowToWorkspace(row), nil
}

func (s *IdentityStore) GetWorkspaceAccess(ctx context.Context, userID, workspaceID uint64) (WorkspaceAccess, error) {
	row, err := s.q.GetWorkspaceAccess(ctx, workspaceID, userID)
	if err != nil {
		return WorkspaceAccess{}, fmt.Errorf("get workspace access: %w", err)
	}
	return WorkspaceAccess{
		Workspace: rowToWorkspace(row.Workspace),
		Role:      row.Role,
	}, nil
}

func (s *IdentityStore) ResolveWorkspaceAccess(ctx context.Context, userID, requestedWorkspaceID uint64) (WorkspaceAccess, error) {
	if requestedWorkspaceID == 0 {
		workspace, err := s.getPersonalWorkspace(ctx, userID)
		if err != nil {
			return WorkspaceAccess{}, err
		}
		return s.GetWorkspaceAccess(ctx, userID, workspace.ID)
	}
	return s.GetWorkspaceAccess(ctx, userID, requestedWorkspaceID)
}

func (s *IdentityStore) ListWorkspaceAccessByUser(ctx context.Context, userID uint64) ([]WorkspaceAccess, error) {
	rows, err := s.q.ListWorkspaceAccessByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list workspace access: %w", err)
	}
	out := make([]WorkspaceAccess, 0, len(rows))
	for _, row := range rows {
		out = append(out, WorkspaceAccess{
			Workspace: rowToWorkspace(row.Workspace),
			Role:      row.Role,
		})
	}
	return out, nil
}

func personalWorkspaceName(user User) string {
	if name := strings.TrimSpace(user.Name); name != "" && !strings.EqualFold(name, user.Email) {
		return fmt.Sprintf("%s Workspace", name)
	}
	if email := strings.TrimSpace(user.Email); email != "" {
		return fmt.Sprintf("%s Workspace", email)
	}
	return fmt.Sprintf("User %d Workspace", user.ID)
}

func hashSessionToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func rowToUser(row db.User) User {
	user := User{
		ID:        row.ID,
		Name:      row.Name,
		IsAdmin:   row.IsAdmin,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}
	if row.Email.Valid {
		user.Email = row.Email.String
	}
	if row.GoogleSubject.Valid {
		user.GoogleSubject = row.GoogleSubject.String
	}
	if row.PictureUrl.Valid {
		user.PictureURL = row.PictureUrl.String
	}
	if row.LastLoginAt.Valid {
		user.LastLoginAt = row.LastLoginAt.Time
	}
	return user
}

func rowToWorkspace(row db.Workspace) Workspace {
	workspace := Workspace{
		ID:         row.ID,
		Name:       row.Name,
		Slug:       row.Slug,
		IsPersonal: row.IsPersonal,
		CreatedAt:  row.CreatedAt,
		UpdatedAt:  row.UpdatedAt,
	}
	if ownerUserID, ok := uint64PtrFromNullInt64(row.OwnerUserID); ok {
		workspace.OwnerUserID = ownerUserID
	}
	return workspace
}

var slugCleaner = regexp.MustCompile(`[^a-z0-9]+`)

func Slugify(raw string) string {
	slug := strings.ToLower(strings.TrimSpace(raw))
	slug = slugCleaner.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		return "workspace"
	}
	return slug
}
