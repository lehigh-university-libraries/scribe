package db

// Compatibility wrappers in this file preserve the older store-facing API while
// delegating SQL execution to sqlc-generated queries in annotations.sql.

import "context"

func (q *Queries) SearchAnnotationsByCanvas(ctx context.Context, canvasURI, normalizedCanvasURI string) ([]Annotation, error) {
	return q.SearchAnnotationsByCanvasManual(ctx, SearchAnnotationsByCanvasManualParams{
		CanvasUri:           canvasURI,
		NormalizedCanvasUri: normalizedCanvasURI,
	})
}

func (q *Queries) GetAnnotation(ctx context.Context, id string) (Annotation, error) {
	return q.GetAnnotationManual(ctx, id)
}

func (q *Queries) UpsertAnnotation(ctx context.Context, id, canvasURI, payload string) error {
	return q.UpsertAnnotationManual(ctx, UpsertAnnotationManualParams{
		ID:        id,
		CanvasUri: canvasURI,
		Payload:   payload,
	})
}

func (q *Queries) UpdateAnnotation(ctx context.Context, id, canvasURI, payload string) (bool, error) {
	res, err := q.UpdateAnnotationManual(ctx, UpdateAnnotationManualParams{
		ID:        id,
		CanvasUri: canvasURI,
		Payload:   payload,
	})
	if err != nil {
		return false, err
	}
	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return rowsAffected > 0, nil
}

func (q *Queries) DeleteAnnotation(ctx context.Context, id string) error {
	return q.DeleteAnnotationManual(ctx, id)
}

func (q *Queries) DeleteAnnotationsByCanvas(ctx context.Context, canvasURI string) error {
	return q.DeleteAnnotationsByCanvasManual(ctx, canvasURI)
}
