package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	db "github.com/lehigh-university-libraries/hOCRedit/internal/db"
)

type Session struct {
  ID        string    `json:"id"`
  Name      string    `json:"name"`
  CreatedAt time.Time `json:"created_at"`
  UpdatedAt time.Time `json:"updated_at"`
}

type SessionStore struct {
	q *db.Queries
}

func NewSessionStore(pool *sql.DB) *SessionStore {
	return &SessionStore{q: db.New(pool)}
}

func (s *SessionStore) List(ctx context.Context) ([]Session, error) {
	rows, err := s.q.ListSessions(ctx)
	if err != nil {
		return nil, fmt.Errorf("query sessions: %w", err)
	}
	out := make([]Session, 0, len(rows))
	for _, row := range rows {
		out = append(out, Session{
			ID:        row.ID,
			Name:      row.Name,
			CreatedAt: row.CreatedAt,
			UpdatedAt: row.UpdatedAt,
		})
	}
	return out, nil
}

func (s *SessionStore) Create(ctx context.Context, id, name string) (Session, error) {
  if id == "" || name == "" {
    return Session{}, errors.New("id and name are required")
  }

	if err := s.q.CreateSession(ctx, db.CreateSessionParams{
		ID:   id,
		Name: name,
	}); err != nil {
		return Session{}, fmt.Errorf("insert session: %w", err)
	}

  return s.Get(ctx, id)
}

func (s *SessionStore) Get(ctx context.Context, id string) (Session, error) {
	row, err := s.q.GetSession(ctx, id)
	if err != nil {
		return Session{}, fmt.Errorf("get session: %w", err)
	}
	return Session{
		ID:        row.ID,
		Name:      row.Name,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}, nil
}
