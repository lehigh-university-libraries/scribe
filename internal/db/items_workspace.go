package db

// Typed adapters in this file compose sqlc-generated workspace queries into the
// domain rows consumed by the store package.

import "context"

func (q *Queries) GetItemForWorkspace(ctx context.Context, id string, workspaceID uint64) (Item, error) {
	row, err := q.GetItemForWorkspaceManual(ctx, GetItemForWorkspaceManualParams{
		ID:          id,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		return Item{}, err
	}
	return Item{
		ID:             row.ID,
		UserID:         row.UserID,
		WorkspaceID:    row.WorkspaceID,
		Name:           row.Name,
		SourceType:     row.SourceType,
		SourceUrl:      row.SourceUrl,
		SourceManifest: row.SourceManifest,
		Metadata:       row.Metadata,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}, nil
}

func (q *Queries) ListItemImagesByWorkspace(ctx context.Context, workspaceID uint64) ([]ItemImage, error) {
	rows, err := q.ListItemImagesByWorkspaceManual(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]ItemImage, 0, len(rows))
	for _, row := range rows {
		out = append(out, ItemImage{
			ID:           row.ID,
			ItemID:       row.ItemID,
			Sequence:     row.Sequence,
			ImageUrl:     row.ImageUrl,
			StorageBytes: row.StorageBytes,
			CanvasUri:    row.CanvasUri,
			Width:        row.Width,
			Height:       row.Height,
			Label:        row.Label,
			HocrUrl:      row.HocrUrl,
			CreatedAt:    row.CreatedAt,
			UpdatedAt:    row.UpdatedAt,
		})
	}
	return out, nil
}

func (q *Queries) GetItemImageForWorkspace(ctx context.Context, id, workspaceID uint64) (ItemImage, error) {
	row, err := q.GetItemImageForWorkspaceManual(ctx, GetItemImageForWorkspaceManualParams{
		ID:          id,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		return ItemImage{}, err
	}
	return ItemImage{
		ID:           row.ID,
		ItemID:       row.ItemID,
		Sequence:     row.Sequence,
		ImageUrl:     row.ImageUrl,
		StorageBytes: row.StorageBytes,
		CanvasUri:    row.CanvasUri,
		Width:        row.Width,
		Height:       row.Height,
		Label:        row.Label,
		HocrUrl:      row.HocrUrl,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}, nil
}

func (q *Queries) WorkspaceOwnsItem(ctx context.Context, workspaceID uint64, itemID string) (bool, error) {
	return q.WorkspaceOwnsItemManual(ctx, WorkspaceOwnsItemManualParams{
		ItemID:      itemID,
		WorkspaceID: workspaceID,
	})
}

func (q *Queries) WorkspaceOwnsItemImage(ctx context.Context, workspaceID, itemImageID uint64) (bool, error) {
	return q.WorkspaceOwnsItemImageManual(ctx, WorkspaceOwnsItemImageManualParams{
		ItemImageID: itemImageID,
		WorkspaceID: workspaceID,
	})
}

func (q *Queries) WorkspaceOwnsImageURL(ctx context.Context, workspaceID uint64, imageURL string) (bool, error) {
	return q.WorkspaceOwnsImageURLManual(ctx, WorkspaceOwnsImageURLManualParams{
		ImageUrl:    imageURL,
		WorkspaceID: workspaceID,
	})
}
