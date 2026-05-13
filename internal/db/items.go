package db

// Compatibility wrappers in this file preserve the older store-facing API while
// delegating SQL execution to sqlc-generated queries in items.sql.

import (
	"context"
	"encoding/json"
)

type CreateItemParams struct {
	ID          string
	UserID      uint64
	WorkspaceID uint64
	Name        string
	SourceType  string
	SourceURL   string
	Metadata    string
}

func (q *Queries) CreateItem(ctx context.Context, arg CreateItemParams) error {
	return q.CreateItemManual(ctx, CreateItemManualParams{
		ID:          arg.ID,
		UserID:      arg.UserID,
		WorkspaceID: arg.WorkspaceID,
		Name:        arg.Name,
		SourceType:  ItemsSourceType(arg.SourceType),
		SourceUrl:   compatNullableString(arg.SourceURL),
		Metadata:    compatRawJSON(arg.Metadata),
	})
}

func (q *Queries) GetItem(ctx context.Context, id string) (Item, error) {
	row, err := q.GetItemManual(ctx, id)
	if err != nil {
		return Item{}, err
	}
	return Item{
		ID:          row.ID,
		UserID:      row.UserID,
		WorkspaceID: row.WorkspaceID,
		Name:        row.Name,
		SourceType:  row.SourceType,
		SourceUrl:   row.SourceUrl,
		Metadata:    row.Metadata,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}, nil
}

func (q *Queries) ListItems(ctx context.Context, workspaceID uint64) ([]Item, error) {
	rows, err := q.ListItemsManual(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]Item, 0, len(rows))
	for _, row := range rows {
		out = append(out, Item{
			ID:          row.ID,
			UserID:      row.UserID,
			WorkspaceID: row.WorkspaceID,
			Name:        row.Name,
			SourceType:  row.SourceType,
			SourceUrl:   row.SourceUrl,
			Metadata:    row.Metadata,
			CreatedAt:   row.CreatedAt,
			UpdatedAt:   row.UpdatedAt,
		})
	}
	return out, nil
}

func (q *Queries) DeleteItem(ctx context.Context, id string) error {
	return q.DeleteItemManual(ctx, id)
}

func (q *Queries) UpdateItemMetadata(ctx context.Context, id string, metadata map[string]any) error {
	body, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	return q.UpdateItemMetadataManual(ctx, UpdateItemMetadataManualParams{
		ID:       id,
		Metadata: body,
	})
}

type CreateItemImageParams struct {
	ItemID    string
	Sequence  uint32
	ImageURL  string
	CanvasURI string
	Label     string
	HocrURL   string
}

func (q *Queries) CreateItemImage(ctx context.Context, arg CreateItemImageParams) (uint64, error) {
	res, err := q.CreateItemImageManual(ctx, CreateItemImageManualParams{
		ItemID:    arg.ItemID,
		Sequence:  arg.Sequence,
		ImageUrl:  arg.ImageURL,
		CanvasUri: compatNullableString(arg.CanvasURI),
		Label:     compatNullableString(arg.Label),
		HocrUrl:   compatNullableString(arg.HocrURL),
	})
	if err != nil {
		return 0, err
	}
	return compatLastInsertID(res)
}

func (q *Queries) GetItemImage(ctx context.Context, id uint64) (ItemImage, error) {
	row, err := q.GetItemImageManual(ctx, id)
	if err != nil {
		return ItemImage{}, err
	}
	return ItemImage{
		ID:        row.ID,
		ItemID:    row.ItemID,
		Sequence:  row.Sequence,
		ImageUrl:  row.ImageUrl,
		CanvasUri: row.CanvasUri,
		Label:     row.Label,
		HocrUrl:   row.HocrUrl,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}, nil
}

func (q *Queries) ListItemImages(ctx context.Context, itemID string) ([]ItemImage, error) {
	rows, err := q.ListItemImagesManual(ctx, itemID)
	if err != nil {
		return nil, err
	}
	out := make([]ItemImage, 0, len(rows))
	for _, row := range rows {
		out = append(out, ItemImage{
			ID:        row.ID,
			ItemID:    row.ItemID,
			Sequence:  row.Sequence,
			ImageUrl:  row.ImageUrl,
			CanvasUri: row.CanvasUri,
			Label:     row.Label,
			HocrUrl:   row.HocrUrl,
			CreatedAt: row.CreatedAt,
			UpdatedAt: row.UpdatedAt,
		})
	}
	return out, nil
}

func (q *Queries) GetItemImageByCanvasURI(ctx context.Context, canvasURI string) (ItemImage, error) {
	row, err := q.GetItemImageByCanvasURIManual(ctx, compatNullableString(canvasURI))
	if err != nil {
		return ItemImage{}, err
	}
	return ItemImage{
		ID:        row.ID,
		ItemID:    row.ItemID,
		Sequence:  row.Sequence,
		ImageUrl:  row.ImageUrl,
		CanvasUri: row.CanvasUri,
		Label:     row.Label,
		HocrUrl:   row.HocrUrl,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}, nil
}

func (q *Queries) UpdateItemImageCanvasURI(ctx context.Context, id uint64, canvasURI string) error {
	return q.UpdateItemImageCanvasURIManual(ctx, UpdateItemImageCanvasURIManualParams{
		ID:        id,
		CanvasUri: compatNullableString(canvasURI),
	})
}
