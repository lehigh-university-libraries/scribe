package store

import (
  "context"
  "database/sql"
  "errors"
  "fmt"
  "time"
)

type Session struct {
  ID        string    `json:"id"`
  Name      string    `json:"name"`
  CreatedAt time.Time `json:"created_at"`
  UpdatedAt time.Time `json:"updated_at"`
}

type SessionStore struct {
  db *sql.DB
}

func NewSessionStore(db *sql.DB) *SessionStore {
  return &SessionStore{db: db}
}

func (s *SessionStore) List(ctx context.Context) ([]Session, error) {
  rows, err := s.db.QueryContext(ctx, `
SELECT id, name, created_at, updated_at
FROM sessions
ORDER BY created_at DESC
`)
  if err != nil {
    return nil, fmt.Errorf("query sessions: %w", err)
  }
  defer rows.Close()

  var out []Session
  for rows.Next() {
    var session Session
    if err := rows.Scan(&session.ID, &session.Name, &session.CreatedAt, &session.UpdatedAt); err != nil {
      return nil, fmt.Errorf("scan session: %w", err)
    }
    out = append(out, session)
  }
  if err := rows.Err(); err != nil {
    return nil, fmt.Errorf("iterate sessions: %w", err)
  }

  return out, nil
}

func (s *SessionStore) Create(ctx context.Context, id, name string) (Session, error) {
  if id == "" || name == "" {
    return Session{}, errors.New("id and name are required")
  }

  if _, err := s.db.ExecContext(ctx, `
INSERT INTO sessions (id, name)
VALUES (?, ?)
`, id, name); err != nil {
    return Session{}, fmt.Errorf("insert session: %w", err)
  }

  return s.Get(ctx, id)
}

func (s *SessionStore) Get(ctx context.Context, id string) (Session, error) {
  var session Session
  if err := s.db.QueryRowContext(ctx, `
SELECT id, name, created_at, updated_at
FROM sessions
WHERE id = ?
`, id).Scan(&session.ID, &session.Name, &session.CreatedAt, &session.UpdatedAt); err != nil {
    return Session{}, fmt.Errorf("get session: %w", err)
  }

  return session, nil
}
