package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"

	db "github.com/lehigh-university-libraries/scribe/internal/db"
)

type AnnotationStore struct {
	q *db.Queries
}

var internalItemImageCanvasPattern = regexp.MustCompile(`(?:https?://[^/]+)?(/v1/item-images/[0-9]+/manifest/canvas/[^?#]+)`)

func NewAnnotationStore(pool *sql.DB) *AnnotationStore {
	return &AnnotationStore{q: db.New(pool)}
}

func normalizeCanvasURIKey(canvasURI string) string {
	if matches := internalItemImageCanvasPattern.FindStringSubmatch(canvasURI); len(matches) >= 2 {
		return matches[1]
	}
	return canvasURI
}

// SearchByCanvas returns all annotation JSON payloads for a canvas URI.
func (s *AnnotationStore) SearchByCanvas(ctx context.Context, canvasURI string) ([]string, error) {
	normalized := normalizeCanvasURIKey(canvasURI)
	rows, err := s.q.SearchAnnotationsByCanvas(ctx, canvasURI, normalized)
	if err != nil {
		return nil, fmt.Errorf("search annotations: %w", err)
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Payload)
	}
	return out, nil
}

// Get returns the payload for a single annotation by its full URI.
func (s *AnnotationStore) Get(ctx context.Context, fullID string) (string, error) {
	row, err := s.q.GetAnnotation(ctx, fullID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("annotation not found: %w", err)
	}
	if err != nil {
		return "", err
	}
	return row.Payload, nil
}

// Upsert stores an annotation (insert or update).
func (s *AnnotationStore) Upsert(ctx context.Context, id, canvasURI, payload string) error {
	canvasURI = normalizeCanvasURIKey(canvasURI)
	return s.q.UpsertAnnotation(ctx, id, canvasURI, payload)
}

// Update updates an existing annotation. Returns (false, nil) if not found.
func (s *AnnotationStore) Update(ctx context.Context, id, canvasURI, payload string) (bool, error) {
	canvasURI = normalizeCanvasURIKey(canvasURI)
	return s.q.UpdateAnnotation(ctx, id, canvasURI, payload)
}

// Delete removes an annotation by its full URI.
func (s *AnnotationStore) Delete(ctx context.Context, id string) error {
	return s.q.DeleteAnnotation(ctx, id)
}

func (s *AnnotationStore) DeleteByCanvas(ctx context.Context, canvasURI string) error {
	canvasURI = normalizeCanvasURIKey(canvasURI)
	return s.q.DeleteAnnotationsByCanvas(ctx, canvasURI)
}
