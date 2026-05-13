package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	db "github.com/lehigh-university-libraries/scribe/internal/db"
)

const (
	AnonymousUserID      uint64 = 1
	AnonymousWorkspaceID uint64 = 1
)

type Item struct {
	ID          string         `json:"id"`
	UserID      uint64         `json:"user_id"`
	WorkspaceID uint64         `json:"workspace_id"`
	Name        string         `json:"name"`
	SourceType  string         `json:"source_type"`
	SourceURL   string         `json:"source_url,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	Images      []ItemImage    `json:"images,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

type ItemImage struct {
	ID        uint64    `json:"id"`
	ItemID    string    `json:"item_id"`
	Sequence  uint32    `json:"sequence"`
	ImageURL  string    `json:"image_url"`
	CanvasURI string    `json:"canvas_uri,omitempty"`
	Label     string    `json:"label,omitempty"`
	HocrURL   string    `json:"hocr_url,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ItemStore struct {
	q *db.Queries
}

func NewItemStore(pool *sql.DB) *ItemStore {
	return &ItemStore{q: db.New(pool)}
}

func (s *ItemStore) Create(ctx context.Context, params db.CreateItemParams) (Item, error) {
	if err := s.q.CreateItem(ctx, params); err != nil {
		return Item{}, fmt.Errorf("create item: %w", err)
	}
	return s.Get(ctx, params.ID)
}

func (s *ItemStore) Get(ctx context.Context, id string) (Item, error) {
	row, err := s.q.GetItem(ctx, id)
	if err != nil {
		return Item{}, fmt.Errorf("get item: %w", err)
	}
	item := rowToItem(row)

	imgs, err := s.q.ListItemImages(ctx, id)
	if err != nil {
		return item, nil // non-fatal
	}
	item.Images = make([]ItemImage, 0, len(imgs))
	for _, img := range imgs {
		item.Images = append(item.Images, rowToItemImage(img))
	}
	return item, nil
}

func (s *ItemStore) GetForWorkspace(ctx context.Context, id string, workspaceID uint64) (Item, error) {
	item, err := s.q.GetItemForWorkspace(ctx, id, workspaceID)
	if err != nil {
		return Item{}, fmt.Errorf("get item: %w", err)
	}
	storeItem := rowToItem(item)

	imgs, err := s.q.ListItemImages(ctx, storeItem.ID)
	if err != nil {
		return storeItem, nil
	}
	storeItem.Images = make([]ItemImage, 0, len(imgs))
	for _, img := range imgs {
		storeItem.Images = append(storeItem.Images, rowToItemImage(img))
	}
	return storeItem, nil
}

func (s *ItemStore) List(ctx context.Context, workspaceID uint64) ([]Item, error) {
	rows, err := s.q.ListItems(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list items: %w", err)
	}
	images, err := s.q.ListItemImagesByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list item images: %w", err)
	}
	imagesByItemID := make(map[string][]ItemImage, len(rows))
	for _, img := range images {
		imagesByItemID[img.ItemID] = append(imagesByItemID[img.ItemID], rowToItemImage(img))
	}
	out := make([]Item, 0, len(rows))
	for _, row := range rows {
		it := rowToItem(row)
		it.Images = imagesByItemID[it.ID]
		out = append(out, it)
	}
	return out, nil
}

func (s *ItemStore) Delete(ctx context.Context, id string) error {
	return s.q.DeleteItem(ctx, id)
}

func (s *ItemStore) DeleteForWorkspace(ctx context.Context, id string, workspaceID uint64) error {
	return s.q.DeleteItemForWorkspace(ctx, id, workspaceID)
}

func (s *ItemStore) UpdateMetadata(ctx context.Context, id string, metadata map[string]any) error {
	return s.q.UpdateItemMetadata(ctx, id, metadata)
}

// AddImage creates a new item_image row and returns its ID.
func (s *ItemStore) AddImage(ctx context.Context, params db.CreateItemImageParams) (ItemImage, error) {
	id, err := s.q.CreateItemImage(ctx, params)
	if err != nil {
		return ItemImage{}, fmt.Errorf("add item image: %w", err)
	}
	row, err := s.q.GetItemImage(ctx, id)
	if err != nil {
		return ItemImage{}, fmt.Errorf("get new item image: %w", err)
	}
	return rowToItemImage(row), nil
}

func (s *ItemStore) GetImage(ctx context.Context, id uint64) (ItemImage, error) {
	row, err := s.q.GetItemImage(ctx, id)
	if err != nil {
		return ItemImage{}, fmt.Errorf("get item image: %w", err)
	}
	return rowToItemImage(row), nil
}

func (s *ItemStore) GetImageForWorkspace(ctx context.Context, id uint64, workspaceID uint64) (ItemImage, error) {
	img, err := s.q.GetItemImageForWorkspace(ctx, id, workspaceID)
	if err != nil {
		return ItemImage{}, err
	}
	return rowToItemImage(img), nil
}

func (s *ItemStore) WorkspaceOwnsImageURL(ctx context.Context, workspaceID uint64, imageURL string) (bool, error) {
	if s == nil {
		return false, nil
	}
	rows, err := s.q.ListItemImagesByWorkspace(ctx, workspaceID)
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		if row.ImageUrl == imageURL {
			return true, nil
		}
	}
	return false, nil
}

func (s *ItemStore) GetImageByCanvasURI(ctx context.Context, canvasURI string) (ItemImage, error) {
	row, err := s.q.GetItemImageByCanvasURI(ctx, canvasURI)
	if err != nil {
		return ItemImage{}, err
	}
	return rowToItemImage(row), nil
}

func (s *ItemStore) GetImageByCanvasURIForWorkspace(ctx context.Context, canvasURI string, workspaceID uint64) (ItemImage, error) {
	img, err := s.q.GetItemImageByCanvasURIForWorkspace(ctx, canvasURI, workspaceID)
	if err != nil {
		return ItemImage{}, err
	}
	return rowToItemImage(img), nil
}

func (s *ItemStore) UpdateImageCanvasURI(ctx context.Context, id uint64, canvasURI string) error {
	return s.q.UpdateItemImageCanvasURI(ctx, id, canvasURI)
}

func (s *ItemStore) WorkspaceOwnsItem(ctx context.Context, workspaceID uint64, itemID string) (bool, error) {
	return s.q.WorkspaceOwnsItem(ctx, workspaceID, itemID)
}

func (s *ItemStore) WorkspaceOwnsItemImage(ctx context.Context, workspaceID uint64, itemImageID uint64) (bool, error) {
	return s.q.WorkspaceOwnsItemImage(ctx, workspaceID, itemImageID)
}

// --- helpers ---

func rowToItem(row db.Item) Item {
	it := Item{
		ID:          row.ID,
		UserID:      row.UserID,
		WorkspaceID: row.WorkspaceID,
		Name:        row.Name,
		SourceType:  string(row.SourceType),
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}
	if row.SourceUrl.Valid {
		it.SourceURL = row.SourceUrl.String
	}
	if len(row.Metadata) > 0 {
		var m map[string]any
		if err := json.Unmarshal(row.Metadata, &m); err == nil {
			it.Metadata = m
		}
	}
	return it
}

func rowToItemImage(row db.ItemImage) ItemImage {
	img := ItemImage{
		ID:        row.ID,
		ItemID:    row.ItemID,
		Sequence:  row.Sequence,
		ImageURL:  row.ImageUrl,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}
	if row.CanvasUri.Valid {
		img.CanvasURI = row.CanvasUri.String
	}
	if row.Label.Valid {
		img.Label = row.Label.String
	}
	if row.HocrUrl.Valid {
		img.HocrURL = row.HocrUrl.String
	}
	return img
}
