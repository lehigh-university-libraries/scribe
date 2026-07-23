package db

// Store query adapters in this file are the sole mapping boundary from
// domain-shaped item values to sqlc-generated queries in items.sql.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"time"
)

type CreateItemParams struct {
	ID             string
	UserID         uint64
	WorkspaceID    uint64
	Name           string
	SourceType     string
	SourceURL      string
	SourceManifest string
	Metadata       string
}

func (q *Queries) CreateItem(ctx context.Context, arg CreateItemParams) error {
	return q.CreateItemManual(ctx, CreateItemManualParams{
		ID:             arg.ID,
		UserID:         arg.UserID,
		WorkspaceID:    arg.WorkspaceID,
		Name:           arg.Name,
		SourceType:     ItemsSourceType(arg.SourceType),
		SourceUrl:      nullableString(arg.SourceURL),
		SourceManifest: nullableString(arg.SourceManifest),
		Metadata:       rawJSON(arg.Metadata),
	})
}

func (q *Queries) GetItem(ctx context.Context, id string) (Item, error) {
	row, err := q.GetItemManual(ctx, id)
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

// ListItemsPageParams identifies one stable, workspace-scoped keyset page.
// CursorCreatedAt is invalid for the first page.
type ListItemsPageParams struct {
	WorkspaceID     uint64
	FilterPattern   string
	CursorCreatedAt sql.NullTime
	CursorID        string
	PageLimit       int32
}

// ItemSummary contains only the bounded scalar fields needed by ListItems.
type ItemSummary struct {
	ID         string
	Name       string
	SourceType ItemsSourceType
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// ListItemsPage returns bounded item summaries in descending creation/id order.
func (q *Queries) ListItemsPage(ctx context.Context, arg ListItemsPageParams) ([]ItemSummary, error) {
	rows, err := q.ListItemSummariesPageManual(ctx, ListItemSummariesPageManualParams{
		WorkspaceID:     arg.WorkspaceID,
		FilterPattern:   arg.FilterPattern,
		CursorCreatedAt: arg.CursorCreatedAt,
		CursorID:        arg.CursorID,
		Limit:           arg.PageLimit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]ItemSummary, 0, len(rows))
	for _, row := range rows {
		out = append(out, ItemSummary(row))
	}
	return out, nil
}

// ItemPreview is the first ordered image and total image count for one item.
type ItemPreview struct {
	Image      ItemImage
	ImageCount uint64
}

// ListItemPreviewsForItemsPage returns at most one image row per requested item.
func (q *Queries) ListItemPreviewsForItemsPage(ctx context.Context, arg ListItemsPageParams) ([]ItemPreview, error) {
	rows, err := q.ListItemPreviewsForItemsPageManual(ctx, ListItemPreviewsForItemsPageManualParams{
		WorkspaceID:     arg.WorkspaceID,
		FilterPattern:   arg.FilterPattern,
		CursorCreatedAt: arg.CursorCreatedAt,
		CursorID:        arg.CursorID,
		Limit:           arg.PageLimit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]ItemPreview, 0, len(rows))
	for _, row := range rows {
		if row.ImageCount < 0 {
			return nil, fmt.Errorf("negative image count for item %q", row.ItemID)
		}
		out = append(out, ItemPreview{
			Image: ItemImage{
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
			},
			ImageCount: uint64(row.ImageCount),
		})
	}
	return out, nil
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
	ItemID       string
	Sequence     uint32
	ImageURL     string
	StorageBytes uint64
	CanvasURI    string
	Width        uint32
	Height       uint32
	Label        string
	HocrURL      string
}

func (q *Queries) CreateItemImage(ctx context.Context, arg CreateItemImageParams) (uint64, error) {
	width, err := nullableImageDimension(arg.Width)
	if err != nil {
		return 0, err
	}
	height, err := nullableImageDimension(arg.Height)
	if err != nil {
		return 0, err
	}
	res, err := q.CreateItemImageManual(ctx, CreateItemImageManualParams{
		ItemID:       arg.ItemID,
		Sequence:     arg.Sequence,
		ImageUrl:     arg.ImageURL,
		StorageBytes: arg.StorageBytes,
		CanvasUri:    nullableString(arg.CanvasURI),
		Width:        width,
		Height:       height,
		Label:        nullableString(arg.Label),
		HocrUrl:      nullableString(arg.HocrURL),
	})
	if err != nil {
		return 0, err
	}
	return lastInsertID(res)
}

func (q *Queries) GetItemImage(ctx context.Context, id uint64) (ItemImage, error) {
	row, err := q.GetItemImageManual(ctx, id)
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

func (q *Queries) ListItemImages(ctx context.Context, itemID string) ([]ItemImage, error) {
	rows, err := q.ListItemImagesManual(ctx, itemID)
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

func nullableImageDimension(value uint32) (sql.NullInt32, error) {
	if value == 0 {
		return sql.NullInt32{}, nil
	}
	if value > math.MaxInt32 {
		return sql.NullInt32{}, fmt.Errorf("image dimension %d exceeds database range", value)
	}
	return sql.NullInt32{Int32: int32(value), Valid: true}, nil
}

func (q *Queries) UpdateItemImageCanvasURI(ctx context.Context, id uint64, canvasURI string) error {
	return q.UpdateItemImageCanvasURIManual(ctx, UpdateItemImageCanvasURIManualParams{
		ID:        id,
		CanvasUri: nullableString(canvasURI),
	})
}

func (q *Queries) SetItemImageCanvasURIIfMissing(ctx context.Context, id uint64, canvasURI string) (int64, error) {
	return q.SetItemImageCanvasURIIfMissingManual(ctx, SetItemImageCanvasURIIfMissingManualParams{
		ID:        id,
		CanvasUri: nullableString(canvasURI),
	})
}

func (q *Queries) CountItemImagesByURL(ctx context.Context, imageURL string) (int64, error) {
	return q.CountItemImagesByURLManual(ctx, imageURL)
}
