package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"

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
	q *db.Queries
}

func NewIdentityStore(pool *sql.DB) *IdentityStore {
	return &IdentityStore{q: db.New(pool)}
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

	var (
		user User
		err  error
	)
	row, err := s.q.GetUserByGoogleSubject(ctx, profile.Subject)
	switch {
	case err == nil:
		if updateErr := s.q.UpdateUserAuthProfile(ctx, db.UpdateUserAuthProfileParams{
			ID:            row.ID,
			Name:          profile.Name,
			Email:         profile.Email,
			GoogleSubject: profile.Subject,
			PictureURL:    profile.PictureURL,
			IsAdmin:       profile.IsAdmin,
		}); updateErr != nil {
			return User{}, Workspace{}, fmt.Errorf("update user auth profile: %w", updateErr)
		}
		user, err = s.GetUser(ctx, row.ID)
	case err == sql.ErrNoRows:
		row, err = s.q.GetUserByEmail(ctx, profile.Email)
		if err == nil {
			if updateErr := s.q.UpdateUserAuthProfile(ctx, db.UpdateUserAuthProfileParams{
				ID:            row.ID,
				Name:          profile.Name,
				Email:         profile.Email,
				GoogleSubject: profile.Subject,
				PictureURL:    profile.PictureURL,
				IsAdmin:       profile.IsAdmin,
			}); updateErr != nil {
				return User{}, Workspace{}, fmt.Errorf("link user auth profile: %w", updateErr)
			}
			user, err = s.GetUser(ctx, row.ID)
			break
		}
		if err != sql.ErrNoRows {
			return User{}, Workspace{}, fmt.Errorf("get user by email: %w", err)
		}
		id, createErr := s.q.CreateUser(ctx, db.CreateUserParams{
			Name:          profile.Name,
			Email:         profile.Email,
			GoogleSubject: profile.Subject,
			PictureURL:    profile.PictureURL,
			IsAdmin:       profile.IsAdmin,
		})
		if createErr != nil {
			return User{}, Workspace{}, fmt.Errorf("create user: %w", createErr)
		}
		user, err = s.GetUser(ctx, id)
	default:
		return User{}, Workspace{}, fmt.Errorf("get user by subject: %w", err)
	}
	if err != nil {
		return User{}, Workspace{}, err
	}

	workspace, err := s.ensurePersonalWorkspace(ctx, user)
	if err != nil {
		return User{}, Workspace{}, err
	}
	return user, workspace, nil
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
	return s.q.CreateAuthSession(ctx, db.CreateAuthSessionParams{
		TokenHash: tokenHash,
		UserID:    userID,
		ExpiresAt: time.Now().UTC().Add(ttl),
		UserAgent: userAgent,
		IPAddress: ipAddress,
	})
}

func (s *IdentityStore) GetSession(ctx context.Context, rawToken string) (IdentitySession, error) {
	sessionRow, err := s.q.GetAuthSessionByTokenHash(ctx, hashSessionToken(rawToken))
	if err != nil {
		return IdentitySession{}, err
	}
	if time.Now().UTC().After(sessionRow.ExpiresAt) {
		_ = s.q.DeleteAuthSessionByTokenHash(ctx, sessionRow.TokenHash)
		return IdentitySession{}, sql.ErrNoRows
	}
	user, err := s.GetUser(ctx, sessionRow.UserID)
	if err != nil {
		return IdentitySession{}, err
	}
	workspace, err := s.ensurePersonalWorkspace(ctx, user)
	if err != nil {
		return IdentitySession{}, err
	}
	access, err := s.GetWorkspaceAccess(ctx, user.ID, workspace.ID)
	if err != nil {
		return IdentitySession{}, err
	}
	_ = s.q.TouchAuthSession(ctx, sessionRow.TokenHash)
	return IdentitySession{User: user, Workspace: workspace, Role: access.Role}, nil
}

func (s *IdentityStore) DeleteSession(ctx context.Context, rawToken string) error {
	return s.q.DeleteAuthSessionByTokenHash(ctx, hashSessionToken(rawToken))
}

func (s *IdentityStore) ensurePersonalWorkspace(ctx context.Context, user User) (Workspace, error) {
	row, err := s.q.GetPersonalWorkspaceByUserID(ctx, user.ID)
	if err == nil {
		return rowToWorkspace(row), nil
	}
	if err != sql.ErrNoRows {
		return Workspace{}, fmt.Errorf("get personal workspace: %w", err)
	}
	ownerUserID := user.ID
	createdByUserID := user.ID
	workspaceID, err := s.q.CreateWorkspace(ctx, db.CreateWorkspaceParams{
		OwnerUserID:     &ownerUserID,
		Name:            personalWorkspaceName(user),
		Slug:            fmt.Sprintf("user-%d-personal", user.ID),
		IsPersonal:      true,
		CreatedByUserID: &createdByUserID,
	})
	if err != nil {
		return Workspace{}, fmt.Errorf("create personal workspace: %w", err)
	}
	if err := s.q.CreateWorkspaceMember(ctx, workspaceID, user.ID, "admin"); err != nil {
		return Workspace{}, fmt.Errorf("create personal workspace membership: %w", err)
	}
	row, err = s.q.GetPersonalWorkspaceByUserID(ctx, user.ID)
	if err != nil {
		return Workspace{}, fmt.Errorf("reload personal workspace: %w", err)
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
		user, err := s.GetUser(ctx, userID)
		if err != nil {
			return WorkspaceAccess{}, err
		}
		workspace, err := s.ensurePersonalWorkspace(ctx, user)
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
	if row.OwnerUserID.Valid {
		ownerUserID := uint64(row.OwnerUserID.Int64)
		workspace.OwnerUserID = &ownerUserID
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
