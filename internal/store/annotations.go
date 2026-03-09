package store

import (
	"context"
	"database/sql"
	"fmt"
)

type AnnotationStore struct {
	pool *sql.DB
}

func NewAnnotationStore(pool *sql.DB) *AnnotationStore {
	return &AnnotationStore{pool: pool}
}

// SearchByCanvas returns all annotation JSON payloads for a canvas URI.
func (s *AnnotationStore) SearchByCanvas(ctx context.Context, canvasURI string) ([]string, error) {
	rows, err := s.pool.QueryContext(ctx,
		`SELECT payload FROM annotations WHERE canvas_uri = ? ORDER BY updated_at ASC`,
		canvasURI,
	)
	if err != nil {
		return nil, fmt.Errorf("search annotations: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		out = append(out, raw)
	}
	return out, rows.Err()
}

// Get returns the payload for a single annotation by its full URI.
func (s *AnnotationStore) Get(ctx context.Context, fullID string) (string, error) {
	var raw string
	err := s.pool.QueryRowContext(ctx,
		`SELECT payload FROM annotations WHERE id = ?`, fullID,
	).Scan(&raw)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("annotation not found: %w", err)
	}
	return raw, err
}

// Upsert stores an annotation (insert or update).
func (s *AnnotationStore) Upsert(ctx context.Context, id, canvasURI, payload string) error {
	_, err := s.pool.ExecContext(ctx, `
INSERT INTO annotations (id, canvas_uri, payload)
VALUES (?, ?, ?)
ON DUPLICATE KEY UPDATE canvas_uri=VALUES(canvas_uri), payload=VALUES(payload)
`, id, canvasURI, payload)
	return err
}

// Update updates an existing annotation. Returns (false, nil) if not found.
func (s *AnnotationStore) Update(ctx context.Context, id, canvasURI, payload string) (bool, error) {
	res, err := s.pool.ExecContext(ctx,
		`UPDATE annotations SET canvas_uri = ?, payload = ? WHERE id = ?`,
		canvasURI, payload, id,
	)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Delete removes an annotation by its full URI.
func (s *AnnotationStore) Delete(ctx context.Context, id string) error {
	_, err := s.pool.ExecContext(ctx, `DELETE FROM annotations WHERE id = ?`, id)
	return err
}
