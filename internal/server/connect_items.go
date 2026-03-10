package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/lehigh-university-libraries/scribe/internal/db"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	scribev1 "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
)

// --- ItemService Connect handlers ---

func (h *Handler) ListItems(ctx context.Context, _ *connect.Request[scribev1.ListItemsRequest]) (*connect.Response[scribev1.ListItemsResponse], error) {
	items, err := h.items.List(ctx, store.AnonymousUserID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &scribev1.ListItemsResponse{
		Items: make([]*scribev1.Item, 0, len(items)),
	}
	for _, it := range items {
		resp.Items = append(resp.Items, storeItemToProto(it))
	}
	return connect.NewResponse(resp), nil
}

func (h *Handler) GetItem(ctx context.Context, req *connect.Request[scribev1.GetItemRequest]) (*connect.Response[scribev1.GetItemResponse], error) {
	it, err := h.items.Get(ctx, req.Msg.GetItemId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("item not found"))
	}
	return connect.NewResponse(&scribev1.GetItemResponse{Item: storeItemToProto(it)}), nil
}

func (h *Handler) CreateItem(ctx context.Context, req *connect.Request[scribev1.CreateItemRequest]) (*connect.Response[scribev1.CreateItemResponse], error) {
	name := strings.TrimSpace(req.Msg.GetName())
	srcType := strings.TrimSpace(req.Msg.GetSourceType())
	if srcType == "" {
		srcType = "url"
	}
	manifestURL := strings.TrimSpace(req.Msg.GetSourceUrl())

	// For manifest ingestion, fetch the manifest upfront so we can use its
	// label as the item name and avoid fetching it a second time below.
	var prefetchedManifest map[string]any
	if srcType == "manifest" && manifestURL != "" {
		if m, err := fetchIIIFManifest(ctx, manifestURL); err == nil {
			prefetchedManifest = m
			if name == "" || name == manifestURL {
				if label := extractManifestLabel(m); label != "" {
					name = label
				}
			}
		}
	}
	if name == "" {
		name = "Untitled Item"
	}

	itemID := time.Now().UTC().Format("20060102150405")
	it, err := h.items.Create(ctx, db.CreateItemParams{
		ID:         itemID,
		UserID:     store.AnonymousUserID,
		Name:       name,
		SourceType: srcType,
		SourceURL:  manifestURL,
		Metadata:   strings.TrimSpace(req.Msg.GetMetadata()),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// Ingest canvases and hOCR from the manifest.
	if srcType == "manifest" && manifestURL != "" {
		if prefetchedManifest != nil {
			_, _ = h.ingestParsedManifest(ctx, it.ID, prefetchedManifest)
		} else {
			_, _ = h.ingestManifest(ctx, it.ID, manifestURL)
		}
		it, _ = h.items.Get(ctx, it.ID)
	}

	return connect.NewResponse(&scribev1.CreateItemResponse{Item: storeItemToProto(it)}), nil
}

func (h *Handler) UploadItemImage(ctx context.Context, req *connect.Request[scribev1.UploadItemImageRequest]) (*connect.Response[scribev1.UploadItemImageResponse], error) {
	itemID := strings.TrimSpace(req.Msg.GetItemId())
	if itemID == "" {
		// Create a new item for this upload.
		name := strings.TrimSpace(req.Msg.GetName())
		if name == "" {
			name = strings.TrimSuffix(req.Msg.GetFilename(), "")
			if name == "" {
				name = "Untitled Item"
			}
		}
		itemID = time.Now().UTC().Format("20060102150405")
		if _, err := h.items.Create(ctx, db.CreateItemParams{
			ID:         itemID,
			UserID:     store.AnonymousUserID,
			Name:       name,
			SourceType: "upload",
		}); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}

	filename := strings.TrimSpace(req.Msg.GetFilename())
	if filename == "" {
		filename = "upload.jpg"
	}
	imageURL, err := h.ocr.StoreUploadedImage(filename, req.Msg.GetImageData())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	img, err := h.items.AddImage(ctx, db.CreateItemImageParams{
		ItemID:   itemID,
		Sequence: req.Msg.GetSequence(),
		ImageURL: imageURL,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	it, _ := h.items.Get(ctx, itemID)
	return connect.NewResponse(&scribev1.UploadItemImageResponse{
		Item:  storeItemToProto(it),
		Image: storeItemImageToProto(img),
	}), nil
}

func (h *Handler) DeleteItem(ctx context.Context, req *connect.Request[scribev1.DeleteItemRequest]) (*connect.Response[scribev1.DeleteItemResponse], error) {
	if err := h.items.Delete(ctx, req.Msg.GetItemId()); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&scribev1.DeleteItemResponse{}), nil
}

// ingestManifest fetches and processes a IIIF manifest by URL.
func (h *Handler) ingestManifest(ctx context.Context, itemID, manifestURL string) (uint64, error) {
	manifest, err := fetchIIIFManifest(ctx, manifestURL)
	if err != nil {
		return 0, fmt.Errorf("fetch manifest: %w", err)
	}
	return h.ingestParsedManifest(ctx, itemID, manifest)
}

// ingestParsedManifest creates item_image rows for each canvas in an already-fetched
// manifest, and imports hOCR from seeAlso when present. Returns the first item_image
// ID that has an OCR run.
func (h *Handler) ingestParsedManifest(ctx context.Context, itemID string, manifest map[string]any) (uint64, error) {
	canvases, err := extractCanvasesFromManifest(manifest)
	if err != nil {
		return 0, fmt.Errorf("extract canvases: %w", err)
	}
	var firstWithHOCR uint64
	for seq, canvas := range canvases {
		img, err := h.items.AddImage(ctx, db.CreateItemImageParams{
			ItemID:    itemID,
			Sequence:  uint32(seq),
			ImageURL:  canvas.imageURL,
			CanvasURI: canvas.canvasURI,
			Label:     canvas.label,
			HocrURL:   canvas.hocrURL,
		})
		if err != nil {
			return 0, fmt.Errorf("add canvas %d: %w", seq, err)
		}
		if canvas.hocrURL == "" {
			continue
		}
		hocrXML, err := fetchHOCRContent(ctx, canvas.hocrURL)
		if err != nil || hocrXML == "" {
			slog.Warn("hOCR fetch failed; will retry on first annotation request", "hocr_url", canvas.hocrURL, "error", err)
			continue
		}
		plainText := hocrToPlainTextLenient(hocrXML)
		sessionID := fmt.Sprintf("%s-seq%d", itemID, seq)
		if err := h.ocrRuns.Create(ctx, store.OCRRun{
			SessionID:    sessionID,
			ItemImageID:  &img.ID,
			ImageURL:     canvas.imageURL,
			Provider:     "manifest",
			Model:        "imported",
			OriginalHOCR: hocrXML,
			OriginalText: plainText,
		}); err != nil {
			continue
		}
		_ = writeSessionHOCR(sessionID, "original.hocr", hocrXML)
		if firstWithHOCR == 0 {
			firstWithHOCR = img.ID
		}
	}
	return firstWithHOCR, nil
}

func fetchHOCRContent(ctx context.Context, hocrURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, hocrURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("fetch hocr: status %d", resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// --- proto conversion helpers ---

func storeItemToProto(it store.Item) *scribev1.Item {
	metaJSON := ""
	if it.Metadata != nil {
		if b, err := json.Marshal(it.Metadata); err == nil {
			metaJSON = string(b)
		}
	}
	proto := &scribev1.Item{
		Id:         it.ID,
		UserId:     it.UserID,
		Name:       it.Name,
		SourceType: it.SourceType,
		SourceUrl:  it.SourceURL,
		Metadata:   metaJSON,
		CreatedAt:  it.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:  it.UpdatedAt.UTC().Format(time.RFC3339),
		Images:     make([]*scribev1.ItemImage, 0, len(it.Images)),
	}
	for _, img := range it.Images {
		proto.Images = append(proto.Images, storeItemImageToProto(img))
	}
	return proto
}

func storeItemImageToProto(img store.ItemImage) *scribev1.ItemImage {
	return &scribev1.ItemImage{
		Id:        img.ID,
		ItemId:    img.ItemID,
		Sequence:  img.Sequence,
		ImageUrl:  img.ImageURL,
		CanvasUri: img.CanvasURI,
		Label:     img.Label,
	}
}
