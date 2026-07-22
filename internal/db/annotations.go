package db

import (
	"context"
	"database/sql"
	"strings"
)

const annotationIndexInsertChunkSize = 250

// AnnotationPageResourceExists verifies that the image belongs to the same
// workspace as the canonical page key.
func (q *Queries) AnnotationPageResourceExists(ctx context.Context, workspaceID, itemImageID uint64) (bool, error) {
	return q.AnnotationPageResourceExistsManual(ctx, AnnotationPageResourceExistsManualParams{
		WorkspaceID: workspaceID,
		ItemImageID: itemImageID,
	})
}

// GetAnnotationPage returns the canonical page for one tenant-scoped image.
func (q *Queries) GetAnnotationPage(ctx context.Context, workspaceID, itemImageID uint64) (AnnotationPage, error) {
	return q.GetAnnotationPageManual(ctx, GetAnnotationPageManualParams{
		WorkspaceID: workspaceID,
		ItemImageID: itemImageID,
	})
}

// CreateAnnotationPage inserts revision one of a canonical page.
func (q *Queries) CreateAnnotationPage(ctx context.Context, page AnnotationPage) error {
	result, err := q.CreateAnnotationPageManual(ctx, CreateAnnotationPageManualParams{
		WorkspaceID:     page.WorkspaceID,
		ItemImageID:     page.ItemImageID,
		PageID:          page.PageID,
		CanvasUri:       page.CanvasUri,
		Payload:         page.Payload,
		UpdatedByUserID: page.UpdatedByUserID,
	})
	return requireAffectedRow(result, err)
}

// UpdateAnnotationPageCAS replaces a page only at the expected revision.
func (q *Queries) UpdateAnnotationPageCAS(ctx context.Context, page AnnotationPage, expectedRevision uint64) (bool, error) {
	result, err := q.UpdateAnnotationPageCASManual(ctx, UpdateAnnotationPageCASManualParams{
		PageID:           page.PageID,
		CanvasUri:        page.CanvasUri,
		Payload:          page.Payload,
		UpdatedByUserID:  page.UpdatedByUserID,
		WorkspaceID:      page.WorkspaceID,
		ItemImageID:      page.ItemImageID,
		ExpectedRevision: expectedRevision,
	})
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
}

type SaveCanonicalOCRCorrectionMetricParams struct {
	CanonicalRevision   uint64
	LevenshteinDistance int32
	ItemImageID         uint64
}

// SaveCanonicalOCRCorrectionMetric is deliberately colocated with canonical
// page persistence and is called only inside the page CAS transaction.
func (q *Queries) SaveCanonicalOCRCorrectionMetric(ctx context.Context, arg SaveCanonicalOCRCorrectionMetricParams) error {
	canonicalRevision, err := uint64ToInt64(arg.CanonicalRevision)
	if err != nil {
		return err
	}
	res, err := q.SaveCanonicalOCRCorrectionMetricManual(ctx, SaveCanonicalOCRCorrectionMetricManualParams{
		CanonicalRevision:   sql.NullInt64{Int64: canonicalRevision, Valid: true},
		LevenshteinDistance: arg.LevenshteinDistance,
		ItemImageID:         arg.ItemImageID,
	})
	return requireAffectedRow(res, err)
}

// ReplaceAnnotationIndex deletes the old derived rows and inserts entries in
// their canonical page order. Callers must provide a transaction-bound Queries.
func (q *Queries) ReplaceAnnotationIndex(ctx context.Context, workspaceID, itemImageID uint64, entries []Annotation) error {
	if err := q.DeleteAnnotationIndexForPageManual(ctx, DeleteAnnotationIndexForPageManualParams{
		WorkspaceID: workspaceID,
		ItemImageID: itemImageID,
	}); err != nil {
		return err
	}
	for start := 0; start < len(entries); start += annotationIndexInsertChunkSize {
		end := min(start+annotationIndexInsertChunkSize, len(entries))
		if err := q.insertAnnotationIndexChunk(ctx, workspaceID, itemImageID, entries[start:end]); err != nil {
			return err
		}
	}
	return nil
}

func (q *Queries) insertAnnotationIndexChunk(ctx context.Context, workspaceID, itemImageID uint64, entries []Annotation) error {
	if len(entries) == 0 {
		return nil
	}
	var query strings.Builder
	query.WriteString(`INSERT INTO annotations
  (workspace_id, item_image_id, id, canvas_uri, text_granularity, position, payload)
VALUES `)
	args := make([]any, 0, len(entries)*7)
	for index, entry := range entries {
		if index > 0 {
			query.WriteByte(',')
		}
		query.WriteString("(?, ?, ?, ?, ?, ?, ?)")
		args = append(args,
			workspaceID,
			itemImageID,
			entry.ID,
			entry.CanvasUri,
			entry.TextGranularity,
			entry.Position,
			entry.Payload,
		)
	}
	_, err := q.db.ExecContext(ctx, query.String(), args...)
	return err
}

// SearchAnnotationIndex returns the ordered derived rows for one page.
func (q *Queries) SearchAnnotationIndex(ctx context.Context, workspaceID, itemImageID uint64) ([]Annotation, error) {
	return q.SearchAnnotationIndexManual(ctx, SearchAnnotationIndexManualParams{
		WorkspaceID: workspaceID,
		ItemImageID: itemImageID,
	})
}

// GetAnnotationIndexEntry returns an indexed annotation within a workspace.
func (q *Queries) GetAnnotationIndexEntry(ctx context.Context, workspaceID uint64, id string) (Annotation, error) {
	return q.GetAnnotationIndexEntryManual(ctx, GetAnnotationIndexEntryManualParams{
		WorkspaceID: workspaceID,
		ID:          id,
	})
}
