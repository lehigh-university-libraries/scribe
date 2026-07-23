package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	db "github.com/lehigh-university-libraries/scribe/internal/db"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/uploadref"
)

const (
	AnonymousUserID      uint64 = 1
	AnonymousWorkspaceID uint64 = 1
	// DefaultItemPageSize bounds ListItems when the caller omits page_size.
	DefaultItemPageSize uint32 = 50
	// MaxItemPageSize is the largest page accepted by the store and API.
	MaxItemPageSize uint32 = 100
	// MaxItemFilterRunes bounds the literal library search term.
	MaxItemFilterRunes = 200
)

// ErrInvalidItemPage identifies an invalid internal pagination request.
var ErrInvalidItemPage = errors.New("invalid item page")

type Item struct {
	ID             string         `json:"id"`
	UserID         uint64         `json:"user_id"`
	WorkspaceID    uint64         `json:"workspace_id"`
	Name           string         `json:"name"`
	SourceType     string         `json:"source_type"`
	SourceURL      string         `json:"source_url,omitempty"`
	SourceManifest string         `json:"-"`
	Metadata       map[string]any `json:"metadata,omitempty"`
	Images         []ItemImage    `json:"images,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

type ItemImage struct {
	ID           uint64    `json:"id"`
	ItemID       string    `json:"item_id"`
	Sequence     uint32    `json:"sequence"`
	ImageURL     string    `json:"image_url"`
	StorageBytes uint64    `json:"storage_bytes"`
	CanvasURI    string    `json:"canvas_uri,omitempty"`
	Width        uint32    `json:"width,omitempty"`
	Height       uint32    `json:"height,omitempty"`
	Label        string    `json:"label,omitempty"`
	HocrURL      string    `json:"hocr_url,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// ItemSummary is the bounded library representation of an item. PreviewImage
// contains at most the first ordered image; Get returns the complete image set.
type ItemSummary struct {
	ID           string
	Name         string
	SourceType   string
	ImageCount   uint64
	PreviewImage *ItemImage
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// ItemPageCursor is the exclusive keyset boundary for a ListItems page.
type ItemPageCursor struct {
	CreatedAt time.Time
	ID        string
}

// ItemPage is one bounded workspace-scoped result page.
type ItemPage struct {
	Items      []ItemSummary
	NextCursor *ItemPageCursor
}

type ItemStore struct {
	q    *db.Queries
	pool *sql.DB
}

func NewItemStore(pool *sql.DB) *ItemStore {
	return &ItemStore{q: db.New(pool), pool: pool}
}

// Ping verifies that the persistence dependency is ready to serve requests.
func (s *ItemStore) Ping(ctx context.Context) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("item store is not configured")
	}
	return s.pool.PingContext(ctx)
}

func (s *ItemStore) Create(ctx context.Context, params db.CreateItemParams) (Item, error) {
	if s == nil || s.pool == nil || params.WorkspaceID == 0 {
		return Item{}, fmt.Errorf("create item: store and workspace are required")
	}
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return Item{}, fmt.Errorf("create item: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockStorageQuotaGuards(ctx, tx, params.WorkspaceID); err != nil {
		return Item{}, fmt.Errorf("create item: lock storage usage: %w", err)
	}
	queries := s.q.WithTx(tx)
	if _, err := queries.LockWorkspaceMemberRole(ctx, params.WorkspaceID, params.UserID); err != nil {
		return Item{}, fmt.Errorf("create item: lock workspace membership: %w", err)
	}
	if err := queries.CreateItem(ctx, params); err != nil {
		return Item{}, fmt.Errorf("create item: %w", err)
	}
	if err := addStorageQuotaUsed(ctx, queries, params.WorkspaceID, StorageQuotaRequest{Items: 1}); err != nil {
		return Item{}, fmt.Errorf("create item: account item: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Item{}, fmt.Errorf("create item: commit: %w", err)
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
		return Item{}, fmt.Errorf("get item images: %w", err)
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
		return Item{}, fmt.Errorf("get item images: %w", err)
	}
	storeItem.Images = make([]ItemImage, 0, len(imgs))
	for _, img := range imgs {
		storeItem.Images = append(storeItem.Images, rowToItemImage(img))
	}
	return storeItem, nil
}

// ListPage returns a stable descending (created_at, id) keyset page. Items and
// their images are read from the same repeatable-read snapshot.
func (s *ItemStore) ListPage(ctx context.Context, workspaceID uint64, pageSize uint32, filter string, cursor *ItemPageCursor) (ItemPage, error) {
	if s == nil || s.pool == nil {
		return ItemPage{}, fmt.Errorf("item store is not configured")
	}
	if workspaceID == 0 {
		return ItemPage{}, fmt.Errorf("%w: workspace is required", ErrInvalidItemPage)
	}
	if pageSize == 0 || pageSize > MaxItemPageSize {
		return ItemPage{}, fmt.Errorf("%w: page size must be between 1 and %d", ErrInvalidItemPage, MaxItemPageSize)
	}
	if !utf8.ValidString(filter) || strings.TrimSpace(filter) != filter || utf8.RuneCountInString(filter) > MaxItemFilterRunes {
		return ItemPage{}, fmt.Errorf("%w: invalid item filter", ErrInvalidItemPage)
	}
	filterPattern := literalItemFilterPattern(filter)

	cursorCreatedAt := sql.NullTime{}
	cursorID := ""
	if cursor != nil {
		if cursor.CreatedAt.IsZero() || cursor.ID == "" {
			return ItemPage{}, fmt.Errorf("%w: cursor requires created_at and id", ErrInvalidItemPage)
		}
		cursorCreatedAt = sql.NullTime{Time: cursor.CreatedAt.UTC(), Valid: true}
		cursorID = cursor.ID
	}

	tx, err := s.pool.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return ItemPage{}, fmt.Errorf("begin item page read: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.q.WithTx(tx)
	rows, err := queries.ListItemsPage(ctx, db.ListItemsPageParams{
		WorkspaceID:     workspaceID,
		FilterPattern:   filterPattern,
		CursorCreatedAt: cursorCreatedAt,
		CursorID:        cursorID,
		PageLimit:       int32(pageSize + 1), // #nosec G115 -- bounded by MaxItemPageSize.
	})
	if err != nil {
		return ItemPage{}, fmt.Errorf("list items: %w", err)
	}
	hasMore := len(rows) > int(pageSize)
	if hasMore {
		rows = rows[:pageSize]
	}

	previews := []db.ItemPreview{}
	if len(rows) > 0 {
		previews, err = queries.ListItemPreviewsForItemsPage(ctx, db.ListItemsPageParams{
			WorkspaceID:     workspaceID,
			FilterPattern:   filterPattern,
			CursorCreatedAt: cursorCreatedAt,
			CursorID:        cursorID,
			PageLimit:       int32(len(rows)), // #nosec G115 -- len(rows) is bounded by MaxItemPageSize.
		})
		if err != nil {
			return ItemPage{}, fmt.Errorf("list item previews: %w", err)
		}
	}
	previewsByItemID := make(map[string]db.ItemPreview, len(previews))
	for _, preview := range previews {
		previewsByItemID[preview.Image.ItemID] = preview
	}
	out := make([]ItemSummary, 0, len(rows))
	for _, row := range rows {
		summary := ItemSummary{
			ID:         row.ID,
			Name:       row.Name,
			SourceType: string(row.SourceType),
			CreatedAt:  row.CreatedAt,
			UpdatedAt:  row.UpdatedAt,
		}
		if preview, ok := previewsByItemID[row.ID]; ok {
			image := rowToItemImage(preview.Image)
			summary.ImageCount = preview.ImageCount
			summary.PreviewImage = &image
		}
		out = append(out, summary)
	}
	if err := tx.Commit(); err != nil {
		return ItemPage{}, fmt.Errorf("commit item page read: %w", err)
	}
	page := ItemPage{Items: out}
	if hasMore {
		last := rows[len(rows)-1]
		page.NextCursor = &ItemPageCursor{CreatedAt: last.CreatedAt.UTC(), ID: last.ID}
	}
	return page, nil
}

func literalItemFilterPattern(filter string) string {
	escaped := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(filter)
	return "%" + escaped + "%"
}

func (s *ItemStore) DeleteForWorkspace(ctx context.Context, id string, workspaceID uint64) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("item store is not configured")
	}
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin item deletion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockStorageQuotaGuards(ctx, tx, workspaceID); err != nil {
		return fmt.Errorf("lock item deletion quota guards: %w", err)
	}
	queries := s.q.WithTx(tx)
	if _, err := queries.LockItemForCleanup(ctx, db.LockItemForCleanupParams{ID: id, WorkspaceID: workspaceID}); err != nil {
		return err
	}
	images, err := queries.ListItemImagesForCleanup(ctx, db.ListItemImagesForCleanupParams{ItemID: id, WorkspaceID: workspaceID})
	if err != nil {
		return fmt.Errorf("lock item images for cleanup: %w", err)
	}
	if err := enqueueImageCleanups(ctx, queries, images); err != nil {
		return err
	}
	if err := enqueueTripletItemCleanup(ctx, queries, id, workspaceID, time.Now().UTC()); err != nil {
		return err
	}
	durableBytes, err := itemDurableDatabaseBytes(ctx, queries, workspaceID, id)
	if err != nil {
		return fmt.Errorf("load item durable storage: %w", err)
	}
	if err := deleteItemResourceGraph(ctx, queries, workspaceID, id, 0); err != nil {
		return fmt.Errorf("delete item resource graph: %w", err)
	}
	result, err := queries.DeleteItemForWorkspaceManual(ctx, db.DeleteItemForWorkspaceManualParams{ID: id, WorkspaceID: workspaceID})
	if err := requireDeletedRow(result, err); err != nil {
		return err
	}
	if err := subtractStorageQuotaUsed(ctx, queries, workspaceID, StorageQuotaRequest{
		DurableBytes: durableBytes,
		Items:        1,
		Images:       uint64(len(images)),
	}); err != nil {
		return fmt.Errorf("account item deletion: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit item deletion: %w", err)
	}
	return nil
}

// DeleteItemImageForWorkspace removes one image only when it belongs to the
// workspace. External cleanup and every owned relational row are committed in
// the same transaction before the image parent is removed.
func (s *ItemStore) DeleteItemImageForWorkspace(ctx context.Context, id, workspaceID uint64) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("item store is not configured")
	}
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin item image deletion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockStorageQuotaGuards(ctx, tx, workspaceID); err != nil {
		return fmt.Errorf("lock item image deletion quota guards: %w", err)
	}
	queries := s.q.WithTx(tx)
	image, err := queries.LockItemImageForCleanup(ctx, db.LockItemImageForCleanupParams{ID: id, WorkspaceID: workspaceID})
	if err != nil {
		return err
	}
	if err := enqueueImageCleanup(ctx, queries, image.ID, image.ImageUrl, image.WorkspaceID, image.StorageBytes, time.Now().UTC()); err != nil {
		return err
	}
	if err := enqueueTripletItemCleanup(ctx, queries, image.ItemID, image.WorkspaceID, time.Now().UTC()); err != nil {
		return err
	}
	durableBytes, err := itemImageDurableDatabaseBytes(ctx, queries, workspaceID, id)
	if err != nil {
		return fmt.Errorf("load item image durable storage: %w", err)
	}
	if err := deleteItemResourceGraph(ctx, queries, workspaceID, image.ItemID, id); err != nil {
		return fmt.Errorf("delete item image resource graph: %w", err)
	}
	if err := subtractStorageQuotaUsed(ctx, queries, workspaceID, StorageQuotaRequest{DurableBytes: durableBytes, Images: 1}); err != nil {
		return fmt.Errorf("account item image deletion: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit item image deletion: %w", err)
	}
	return nil
}

// deleteItemResourceGraph removes every database-owned descendant of an item
// or one of its images. Callers must lock the item/image and quota guards first.
// imageID == 0 selects the complete item graph; a positive ID selects one image.
func deleteItemResourceGraph(ctx context.Context, queries *db.Queries, workspaceID uint64, itemID string, imageID uint64) error {
	if queries == nil || workspaceID == 0 || strings.TrimSpace(itemID) == "" {
		return fmt.Errorf("delete item resource graph: workspace and item are required")
	}
	itemID = strings.TrimSpace(itemID)
	if err := queries.DeleteExternalRequestsForItemResourceGraph(ctx, db.DeleteExternalRequestsForItemResourceGraphParams{
		WorkspaceID:    workspaceID,
		ItemImageID:    imageID,
		NullableItemID: nullableString(itemID),
		ItemID:         itemID,
	}); err != nil {
		return fmt.Errorf("delete external requests: %w", err)
	}
	if imageID == 0 {
		if err := queries.DeleteUploadBatchFilesForItemResourceGraph(ctx, db.DeleteUploadBatchFilesForItemResourceGraphParams{
			WorkspaceID: workspaceID,
			ItemID:      itemID,
		}); err != nil {
			return fmt.Errorf("delete upload batch files: %w", err)
		}
	} else if err := queries.DetachUploadBatchFilesForItemImageResourceGraph(ctx, db.DetachUploadBatchFilesForItemImageResourceGraphParams{
		WorkspaceID: workspaceID,
		ItemID:      itemID,
		ItemImageID: imageID,
	}); err != nil {
		return fmt.Errorf("detach upload batch file resources: %w", err)
	}
	if err := queries.DeleteTranscriptionAttemptsForItemResourceGraph(ctx, db.DeleteTranscriptionAttemptsForItemResourceGraphParams{
		WorkspaceID: workspaceID,
		ItemID:      itemID,
		ItemImageID: imageID,
	}); err != nil {
		return fmt.Errorf("delete transcription attempts: %w", err)
	}
	if err := queries.DeleteTranscriptionJobsForItemResourceGraph(ctx, db.DeleteTranscriptionJobsForItemResourceGraphParams{
		WorkspaceID: workspaceID,
		ItemID:      itemID,
		ItemImageID: imageID,
	}); err != nil {
		return fmt.Errorf("delete transcription jobs: %w", err)
	}
	if err := queries.DeleteProviderAuditsForItemResourceGraph(ctx, db.DeleteProviderAuditsForItemResourceGraphParams{
		WorkspaceID: workspaceID,
		ItemID:      itemID,
		ItemImageID: imageID,
	}); err != nil {
		return fmt.Errorf("delete provider audits: %w", err)
	}
	if err := queries.DeleteCurrentOCRRunsForItemResourceGraph(ctx, db.DeleteCurrentOCRRunsForItemResourceGraphParams{
		WorkspaceID: workspaceID,
		ItemID:      itemID,
		ItemImageID: imageID,
	}); err != nil {
		return fmt.Errorf("delete current OCR runs: %w", err)
	}
	if err := queries.DeleteOCRRunsForItemResourceGraph(ctx, db.DeleteOCRRunsForItemResourceGraphParams{
		WorkspaceID: workspaceID,
		ItemID:      itemID,
		ItemImageID: imageID,
	}); err != nil {
		return fmt.Errorf("delete OCR runs: %w", err)
	}
	if err := queries.DeleteAnnotationMirrorsForItemResourceGraph(ctx, db.DeleteAnnotationMirrorsForItemResourceGraphParams{
		WorkspaceID: workspaceID,
		ItemID:      itemID,
		ItemImageID: imageID,
	}); err != nil {
		return fmt.Errorf("delete annotation mirrors: %w", err)
	}
	if err := queries.DeleteAnnotationMirrorTombstonesForItemResourceGraph(ctx, db.DeleteAnnotationMirrorTombstonesForItemResourceGraphParams{
		WorkspaceID: workspaceID,
		ItemID:      itemID,
		ItemImageID: imageID,
	}); err != nil {
		return fmt.Errorf("delete annotation mirror tombstones: %w", err)
	}
	if err := queries.DeletePublishedPagesForItemResourceGraph(ctx, db.DeletePublishedPagesForItemResourceGraphParams{
		WorkspaceID: workspaceID,
		ItemID:      itemID,
		ItemImageID: imageID,
	}); err != nil {
		return fmt.Errorf("delete published pages: %w", err)
	}
	if err := queries.DeleteAnnotationIndexForItemResourceGraph(ctx, db.DeleteAnnotationIndexForItemResourceGraphParams{
		WorkspaceID: workspaceID,
		ItemID:      itemID,
		ItemImageID: imageID,
	}); err != nil {
		return fmt.Errorf("delete annotation index: %w", err)
	}
	if err := queries.DeleteAnnotationPagesForItemResourceGraph(ctx, db.DeleteAnnotationPagesForItemResourceGraphParams{
		WorkspaceID: workspaceID,
		ItemID:      itemID,
		ItemImageID: imageID,
	}); err != nil {
		return fmt.Errorf("delete annotation pages: %w", err)
	}
	if imageID == 0 {
		if err := queries.DeleteUploadBatchesForItemResourceGraph(ctx, db.DeleteUploadBatchesForItemResourceGraphParams{
			WorkspaceID: workspaceID,
			ItemID:      itemID,
		}); err != nil {
			return fmt.Errorf("delete upload batches: %w", err)
		}
		if err := queries.DeleteItemImagesForItemResourceGraph(ctx, db.DeleteItemImagesForItemResourceGraphParams{
			WorkspaceID: workspaceID,
			ItemID:      itemID,
		}); err != nil {
			return fmt.Errorf("delete item images: %w", err)
		}
		return nil
	}
	result, err := queries.DeleteItemImageForWorkspaceManual(ctx, db.DeleteItemImageForWorkspaceManualParams{
		ID:          imageID,
		WorkspaceID: workspaceID,
	})
	return requireDeletedRow(result, err)
}

// EnqueueUploadCleanup durably schedules an orphaned upload produced before an
// item_image transaction could commit. Non-Scribe URLs are deliberately
// ignored; imported remote resources are never cleanup targets.
func (s *ItemStore) EnqueueUploadCleanup(ctx context.Context, workspaceID uint64, imageURL string, storageBytes uint64) error {
	name, ok := uploadref.ImmutableNameFromURL(imageURL)
	if !ok {
		if _, localButNonCanonical := uploadref.NameFromURL(imageURL); localButNonCanonical {
			return fmt.Errorf("enqueue upload cleanup: local upload identity is not immutable")
		}
		return nil
	}
	if workspaceID == 0 {
		return fmt.Errorf("enqueue upload cleanup: workspace is required")
	}
	return s.q.UpsertResourceCleanup(ctx, db.UpsertResourceCleanupParams{
		Kind:          db.ResourceCleanupOutboxKindUploadBlob,
		ResourceKey:   name,
		WorkspaceID:   workspaceID,
		StorageBytes:  storageBytes,
		NextAttemptAt: time.Now().UTC(),
	})
}

const tripletDeleteGracePeriod = 90 * time.Second

func enqueueImageCleanups(ctx context.Context, queries *db.Queries, images []db.ListItemImagesForCleanupRow) error {
	type uploadAccounting struct {
		workspaceID  uint64
		storageBytes uint64
	}
	uploads := make(map[string]uploadAccounting, len(images))
	now := time.Now().UTC()
	for _, image := range images {
		name, isUpload := uploadref.ImmutableNameFromURL(image.ImageUrl)
		if isUpload {
			accounting := uploads[name]
			if image.StorageBytes > accounting.storageBytes {
				accounting.storageBytes = image.StorageBytes
			}
			accounting.workspaceID = image.WorkspaceID
			uploads[name] = accounting
		}
		if err := enqueueTripletImageCleanup(ctx, queries, image.ID, image.WorkspaceID, now); err != nil {
			return err
		}
	}
	names := make([]string, 0, len(uploads))
	for name := range uploads {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		accounting := uploads[name]
		if err := queries.UpsertResourceCleanup(ctx, db.UpsertResourceCleanupParams{
			Kind:          db.ResourceCleanupOutboxKindUploadBlob,
			ResourceKey:   name,
			WorkspaceID:   accounting.workspaceID,
			StorageBytes:  accounting.storageBytes,
			NextAttemptAt: now,
		}); err != nil {
			return fmt.Errorf("enqueue upload cleanup: %w", err)
		}
	}
	return nil
}

func enqueueImageCleanup(ctx context.Context, queries *db.Queries, imageID uint64, imageURL string, workspaceID, storageBytes uint64, now time.Time) error {
	if name, ok := uploadref.ImmutableNameFromURL(imageURL); ok {
		if err := queries.UpsertResourceCleanup(ctx, db.UpsertResourceCleanupParams{
			Kind:          db.ResourceCleanupOutboxKindUploadBlob,
			ResourceKey:   name,
			WorkspaceID:   workspaceID,
			StorageBytes:  storageBytes,
			NextAttemptAt: now,
		}); err != nil {
			return fmt.Errorf("enqueue upload cleanup: %w", err)
		}
	}
	return enqueueTripletImageCleanup(ctx, queries, imageID, workspaceID, now)
}

func enqueueTripletImageCleanup(ctx context.Context, queries *db.Queries, imageID, workspaceID uint64, now time.Time) error {
	if err := queries.UpsertResourceCleanup(ctx, db.UpsertResourceCleanupParams{
		Kind:        db.ResourceCleanupOutboxKindTripletPresentationImage,
		ResourceKey: strconv.FormatUint(imageID, 10),
		WorkspaceID: workspaceID,
		// A mirror PUT already in flight is bounded by a shorter HTTP timeout.
		// Deferring DELETE makes the terminal remote operation win that race.
		NextAttemptAt: now.Add(tripletDeleteGracePeriod),
	}); err != nil {
		return fmt.Errorf("enqueue Triplet cleanup: %w", err)
	}
	return nil
}

func enqueueTripletItemCleanup(ctx context.Context, queries *db.Queries, itemID string, workspaceID uint64, now time.Time) error {
	itemID = strings.TrimSpace(itemID)
	if itemID == "" || workspaceID == 0 {
		return fmt.Errorf("enqueue Triplet item cleanup: item and workspace are required")
	}
	if err := queries.UpsertResourceCleanup(ctx, db.UpsertResourceCleanupParams{
		Kind:          db.ResourceCleanupOutboxKindTripletPresentationItem,
		ResourceKey:   itemID,
		WorkspaceID:   workspaceID,
		NextAttemptAt: now.Add(tripletDeleteGracePeriod),
	}); err != nil {
		return fmt.Errorf("enqueue Triplet item cleanup: %w", err)
	}
	return nil
}

func requireDeletedRow(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *ItemStore) UpdateMetadata(ctx context.Context, id string, metadata map[string]any) error {
	return s.q.UpdateItemMetadata(ctx, id, metadata)
}

// AddImage creates a new item_image row and returns its ID.
func (s *ItemStore) AddImage(ctx context.Context, params db.CreateItemImageParams) (ItemImage, error) {
	if err := validateImageStorageReference(params.ImageURL, params.StorageBytes); err != nil {
		return ItemImage{}, err
	}
	params.CanvasURI = strings.TrimSpace(params.CanvasURI)
	if err := iiif.ValidateCanvasURI(params.CanvasURI); err != nil {
		return ItemImage{}, fmt.Errorf("add item image: %w", err)
	}
	item, err := s.q.GetItem(ctx, params.ItemID)
	if err != nil {
		return ItemImage{}, fmt.Errorf("add item image: load item: %w", err)
	}
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return ItemImage{}, fmt.Errorf("add item image: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockStorageQuotaGuards(ctx, tx, item.WorkspaceID); err != nil {
		return ItemImage{}, fmt.Errorf("add item image: lock storage usage: %w", err)
	}
	queries := s.q.WithTx(tx)
	if _, err := queries.LockItemForUseManual(ctx, db.LockItemForUseManualParams{
		ID:          params.ItemID,
		WorkspaceID: item.WorkspaceID,
	}); err != nil {
		return ItemImage{}, fmt.Errorf("add item image: lock item: %w", err)
	}
	uploadBytes := uint64(0)
	if name, isUpload := uploadref.ImmutableNameFromURL(params.ImageURL); isUpload {
		_, staged, cleanupErr := lockUploadCleanupForReference(ctx, queries, item.WorkspaceID, name)
		if cleanupErr != nil {
			return ItemImage{}, fmt.Errorf("add item image: inspect staged upload: %w", cleanupErr)
		}
		if !staged {
			uploadBytes = params.StorageBytes
		}
	}
	id, err := queries.CreateItemImage(ctx, params)
	if err != nil {
		return ItemImage{}, fmt.Errorf("add item image: %w", err)
	}
	row, err := queries.GetItemImage(ctx, id)
	if err != nil {
		return ItemImage{}, fmt.Errorf("get new item image: %w", err)
	}
	if err := addStorageQuotaUsed(ctx, queries, item.WorkspaceID, StorageQuotaRequest{Bytes: uploadBytes, Images: 1}); err != nil {
		return ItemImage{}, fmt.Errorf("add item image: account image: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ItemImage{}, fmt.Errorf("add item image: commit: %w", err)
	}
	return rowToItemImage(row), nil
}

func validateImageStorageReference(imageURL string, storageBytes uint64) error {
	_, recognizedUpload := uploadref.NameFromURL(imageURL)
	_, isUpload := uploadref.ImmutableNameFromURL(imageURL)
	switch {
	case recognizedUpload && !isUpload:
		return fmt.Errorf("local upload identity is not immutable")
	case isUpload && storageBytes == 0:
		return fmt.Errorf("local upload storage size is required")
	case !isUpload && storageBytes != 0:
		return fmt.Errorf("storage size is only valid for local uploads")
	default:
		return nil
	}
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
	if s == nil || s.q == nil || workspaceID == 0 || strings.TrimSpace(imageURL) == "" {
		return false, nil
	}
	return s.q.WorkspaceOwnsImageURL(ctx, workspaceID, strings.TrimSpace(imageURL))
}

// UserCanReadImageURL reports whether the user is a member of a workspace
// containing an exact reference to imageURL. The caller remains responsible
// for checking the principal's annotation-read permission before using this
// membership lookup; delegated credentials must use WorkspaceOwnsImageURL so
// their fixed workspace scope cannot expand to all memberships of their user.
func (s *ItemStore) UserCanReadImageURL(ctx context.Context, userID uint64, imageURL string) (bool, error) {
	if s == nil || s.q == nil || userID == 0 || strings.TrimSpace(imageURL) == "" {
		return false, nil
	}
	return s.q.UserCanReadImageURLManual(ctx, db.UserCanReadImageURLManualParams{
		ImageUrl: strings.TrimSpace(imageURL),
		UserID:   userID,
	})
}

func (s *ItemStore) UpdateImageCanvasURI(ctx context.Context, id uint64, canvasURI string) error {
	return s.q.UpdateItemImageCanvasURI(ctx, id, canvasURI)
}

// EnsureImageCanvasURI atomically assigns a generated Canvas URI only when an
// imported URI is not already present, then returns the authoritative row.
// This keeps public manifest IDs stable when concurrent readers initialize the
// same greenfield item image.
func (s *ItemStore) EnsureImageCanvasURI(ctx context.Context, id uint64, canvasURI string) (ItemImage, error) {
	if _, err := s.q.SetItemImageCanvasURIIfMissing(ctx, id, canvasURI); err != nil {
		return ItemImage{}, fmt.Errorf("set item image canvas uri: %w", err)
	}
	return s.GetImage(ctx, id)
}

func (s *ItemStore) WorkspaceOwnsItem(ctx context.Context, workspaceID uint64, itemID string) (bool, error) {
	return s.q.WorkspaceOwnsItem(ctx, workspaceID, itemID)
}

func (s *ItemStore) WorkspaceOwnsItemImage(ctx context.Context, workspaceID uint64, itemImageID uint64) (bool, error) {
	return s.q.WorkspaceOwnsItemImage(ctx, workspaceID, itemImageID)
}

// ImageURLReferenceCount returns how many item images still reference a blob.
func (s *ItemStore) ImageURLReferenceCount(ctx context.Context, imageURL string) (int64, error) {
	count, err := s.q.CountItemImagesByURL(ctx, imageURL)
	if err != nil {
		return 0, fmt.Errorf("count item images by URL: %w", err)
	}
	return count, nil
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
	if row.SourceManifest.Valid {
		it.SourceManifest = row.SourceManifest.String
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
		ID:           row.ID,
		ItemID:       row.ItemID,
		Sequence:     row.Sequence,
		ImageURL:     row.ImageUrl,
		StorageBytes: row.StorageBytes,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}
	if row.Width.Valid && row.Width.Int32 > 0 {
		img.Width = uint32(row.Width.Int32)
	}
	if row.Height.Valid && row.Height.Int32 > 0 {
		img.Height = uint32(row.Height.Int32)
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
