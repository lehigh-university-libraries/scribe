package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// Item maps to the items table.
type Item struct {
	ID         string
	UserID     uint64
	Name       string
	SourceType string
	SourceURL  sql.NullString
	Metadata   sql.NullString // JSON blob
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// ItemImage maps to the item_images table.
type ItemImage struct {
	ID        uint64
	ItemID    string
	Sequence  uint32
	ImageURL  string
	CanvasURI sql.NullString
	Label     sql.NullString
	HocrURL   sql.NullString
	CreatedAt time.Time
	UpdatedAt time.Time
}

// --- items ---

type CreateItemParams struct {
	ID         string
	UserID     uint64
	Name       string
	SourceType string
	SourceURL  string
	Metadata   string // JSON; empty string means NULL
}

func (q *Queries) CreateItem(ctx context.Context, arg CreateItemParams) error {
	var srcURL sql.NullString
	if arg.SourceURL != "" {
		srcURL = sql.NullString{String: arg.SourceURL, Valid: true}
	}
	var meta sql.NullString
	if arg.Metadata != "" {
		meta = sql.NullString{String: arg.Metadata, Valid: true}
	}
	_, err := q.db.ExecContext(ctx, `
INSERT INTO items (id, user_id, name, source_type, source_url, metadata)
VALUES (?, ?, ?, ?, ?, ?)
`, arg.ID, arg.UserID, arg.Name, arg.SourceType, srcURL, meta)
	return err
}

func (q *Queries) GetItem(ctx context.Context, id string) (Item, error) {
	var it Item
	err := q.db.QueryRowContext(ctx, `
SELECT id, user_id, name, source_type, source_url, metadata, created_at, updated_at
FROM items WHERE id = ?
`, id).Scan(
		&it.ID, &it.UserID, &it.Name, &it.SourceType,
		&it.SourceURL, &it.Metadata, &it.CreatedAt, &it.UpdatedAt,
	)
	return it, err
}

func (q *Queries) ListItems(ctx context.Context, userID uint64) ([]Item, error) {
	rows, err := q.db.QueryContext(ctx, `
SELECT id, user_id, name, source_type, source_url, metadata, created_at, updated_at
FROM items WHERE user_id = ?
ORDER BY created_at DESC
`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Item
	for rows.Next() {
		var it Item
		if err := rows.Scan(
			&it.ID, &it.UserID, &it.Name, &it.SourceType,
			&it.SourceURL, &it.Metadata, &it.CreatedAt, &it.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (q *Queries) DeleteItem(ctx context.Context, id string) error {
	_, err := q.db.ExecContext(ctx, `DELETE FROM items WHERE id = ?`, id)
	return err
}

// UpdateItemMetadata merges the given metadata JSON into the item's metadata bag.
func (q *Queries) UpdateItemMetadata(ctx context.Context, id string, metadata map[string]any) error {
	b, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	_, err = q.db.ExecContext(ctx, `UPDATE items SET metadata = ? WHERE id = ?`, string(b), id)
	return err
}

// --- item_images ---

type CreateItemImageParams struct {
	ItemID    string
	Sequence  uint32
	ImageURL  string
	CanvasURI string
	Label     string
	HocrURL   string
}

func (q *Queries) CreateItemImage(ctx context.Context, arg CreateItemImageParams) (uint64, error) {
	var canvasURI sql.NullString
	if arg.CanvasURI != "" {
		canvasURI = sql.NullString{String: arg.CanvasURI, Valid: true}
	}
	var label sql.NullString
	if arg.Label != "" {
		label = sql.NullString{String: arg.Label, Valid: true}
	}
	var hocrURL sql.NullString
	if arg.HocrURL != "" {
		hocrURL = sql.NullString{String: arg.HocrURL, Valid: true}
	}
	res, err := q.db.ExecContext(ctx, `
INSERT INTO item_images (item_id, sequence, image_url, canvas_uri, label, hocr_url)
VALUES (?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
  image_url  = VALUES(image_url),
  canvas_uri = VALUES(canvas_uri),
  label      = VALUES(label),
  hocr_url   = VALUES(hocr_url)
`, arg.ItemID, arg.Sequence, arg.ImageURL, canvasURI, label, hocrURL)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	return uint64(id), err
}

func (q *Queries) GetItemImage(ctx context.Context, id uint64) (ItemImage, error) {
	var img ItemImage
	err := q.db.QueryRowContext(ctx, `
SELECT id, item_id, sequence, image_url, canvas_uri, label, hocr_url, created_at, updated_at
FROM item_images WHERE id = ?
`, id).Scan(
		&img.ID, &img.ItemID, &img.Sequence, &img.ImageURL,
		&img.CanvasURI, &img.Label, &img.HocrURL, &img.CreatedAt, &img.UpdatedAt,
	)
	return img, err
}

func (q *Queries) ListItemImages(ctx context.Context, itemID string) ([]ItemImage, error) {
	rows, err := q.db.QueryContext(ctx, `
SELECT id, item_id, sequence, image_url, canvas_uri, label, hocr_url, created_at, updated_at
FROM item_images WHERE item_id = ?
ORDER BY sequence ASC
`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ItemImage
	for rows.Next() {
		var img ItemImage
		if err := rows.Scan(
			&img.ID, &img.ItemID, &img.Sequence, &img.ImageURL,
			&img.CanvasURI, &img.Label, &img.HocrURL, &img.CreatedAt, &img.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, img)
	}
	return out, rows.Err()
}

func (q *Queries) GetItemImageByCanvasURI(ctx context.Context, canvasURI string) (ItemImage, error) {
	var img ItemImage
	err := q.db.QueryRowContext(ctx, `
SELECT id, item_id, sequence, image_url, canvas_uri, label, hocr_url, created_at, updated_at
FROM item_images WHERE canvas_uri = ? LIMIT 1
`, canvasURI).Scan(
		&img.ID, &img.ItemID, &img.Sequence, &img.ImageURL,
		&img.CanvasURI, &img.Label, &img.HocrURL, &img.CreatedAt, &img.UpdatedAt,
	)
	return img, err
}

func (q *Queries) UpdateItemImageCanvasURI(ctx context.Context, id uint64, canvasURI string) error {
	_, err := q.db.ExecContext(ctx, `UPDATE item_images SET canvas_uri = ? WHERE id = ?`, canvasURI, id)
	return err
}
