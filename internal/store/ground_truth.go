package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lehigh-university-libraries/scribe/internal/db"
)

// GroundTruth is an immutable, explicitly marked canonical revision and its
// model baseline. It has no edit path; corrections continue on annotation_pages.
type GroundTruth = db.AnnotationGroundTruth

var ErrGroundTruthExportLimit = errors.New("ground truth export exceeds 100 pages or 32 MiB; select a smaller context or item")

func (s *AnnotationStore) HasGroundTruth(ctx context.Context, workspaceID, imageID, revision uint64) (bool, error) {
	return s.q.HasGroundTruth(ctx, db.HasGroundTruthParams{WorkspaceID: workspaceID, ItemImageID: imageID, Revision: revision})
}

func (s *AnnotationStore) MarkGroundTruth(ctx context.Context, snapshot GroundTruth, marked bool) error {
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockStorageQuotaGuards(ctx, tx, snapshot.WorkspaceID); err != nil {
		return err
	}
	q := s.q.WithTx(tx)
	if snapshot.MarkedByUserID.Valid {
		if snapshot.MarkedByUserID.Int64 <= 0 {
			return fmt.Errorf("ground truth actor must be positive")
		}
		if _, err := q.LockWorkspaceMemberRole(ctx, snapshot.WorkspaceID, uint64(snapshot.MarkedByUserID.Int64)); err != nil {
			return fmt.Errorf("lock ground truth actor membership: %w", err)
		}
	}
	if _, err := q.LockItemImageForUseManual(ctx, db.LockItemImageForUseManualParams{ID: snapshot.ItemImageID, WorkspaceID: snapshot.WorkspaceID}); err != nil {
		return ErrAnnotationPageNotFound
	}
	page, err := q.LockAnnotationPageForPublication(ctx, db.LockAnnotationPageForPublicationParams{WorkspaceID: snapshot.WorkspaceID, ItemImageID: snapshot.ItemImageID})
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAnnotationPageNotFound
	}
	if err != nil {
		return err
	}
	if marked && page.Revision != snapshot.Revision {
		return ErrAnnotationRevisionConflict
	}
	if marked && page.Payload != snapshot.Payload {
		return ErrAnnotationRevisionConflict
	}
	before, err := itemImageDurableDatabaseBytes(ctx, q, snapshot.WorkspaceID, snapshot.ItemImageID)
	if err != nil {
		return err
	}
	exists, err := q.HasGroundTruth(ctx, db.HasGroundTruthParams{WorkspaceID: snapshot.WorkspaceID, ItemImageID: snapshot.ItemImageID, Revision: snapshot.Revision})
	if err != nil {
		return err
	}
	if marked && !exists {
		err = q.InsertGroundTruth(ctx, db.InsertGroundTruthParams{
			WorkspaceID: snapshot.WorkspaceID, ItemImageID: snapshot.ItemImageID, Revision: snapshot.Revision,
			ContextID: snapshot.ContextID, DocumentType: snapshot.DocumentType, Payload: snapshot.Payload,
			OriginalPage: snapshot.OriginalPage, ImageUrl: snapshot.ImageUrl, Transcript: snapshot.Transcript, MarkedByUserID: snapshot.MarkedByUserID,
		})
	} else if !marked {
		err = q.DeleteGroundTruth(ctx, db.DeleteGroundTruthParams{WorkspaceID: snapshot.WorkspaceID, ItemImageID: snapshot.ItemImageID, Revision: snapshot.Revision})
	}
	if err != nil {
		return err
	}
	after, err := itemImageDurableDatabaseBytes(ctx, q, snapshot.WorkspaceID, snapshot.ItemImageID)
	if err != nil {
		return err
	}
	if err := applyStorageQuotaUsedDeltaWithLimits(ctx, q, snapshot.WorkspaceID, before, after, s.quota); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *AnnotationStore) GroundTruthBundle(ctx context.Context, workspaceID, contextID uint64, itemID string) ([]GroundTruth, error) {
	tx, err := s.pool.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)
	contextKey, err := uint64ValueToNullInt64(contextID)
	if err != nil {
		return nil, err
	}
	refs, err := q.ListGroundTruthRevisions(ctx, db.ListGroundTruthRevisionsParams{WorkspaceID: workspaceID, ContextID: contextKey, ItemID: itemID})
	if err != nil {
		return nil, err
	}
	if len(refs) > 100 {
		return nil, ErrGroundTruthExportLimit
	}
	var bytes int64
	for _, ref := range refs {
		bytes += int64(ref.PayloadBytes)
		if bytes > 32<<20 || ref.PayloadBytes < 0 {
			return nil, ErrGroundTruthExportLimit
		}
	}
	pages := make([]GroundTruth, 0, len(refs))
	for _, ref := range refs {
		page, err := q.GetGroundTruth(ctx, db.GetGroundTruthParams{WorkspaceID: workspaceID, ItemImageID: ref.ItemImageID, Revision: ref.Revision})
		if err != nil {
			return nil, fmt.Errorf("load ground truth revision: %w", err)
		}
		pages = append(pages, page)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return pages, nil
}
