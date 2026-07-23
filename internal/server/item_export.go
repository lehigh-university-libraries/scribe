package server

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/store"
)

const (
	itemExportTokenVersion       = 1
	itemExportTokenDomain        = "scribe:item-export:v1\x00" // #nosec G101 -- HMAC domain separator, not a credential.
	itemExportTokenTTL           = 5 * time.Minute
	itemExportTokenSignatureSize = sha256.Size
	maxItemExportTokenBytes      = 1024
	maxExportedPageBytes         = 32 << 20
	maxItemExportSourceBytes     = 64 << 20
	maxItemExportOutputBytes     = 128 << 20
	maxPreparedExportDuration    = 90 * time.Second
	exportWriteDeadlineGrace     = 5 * time.Second
	exportCopyBufferBytes        = 32 << 10
)

var (
	errItemExportInvalid          = errors.New("invalid item export")
	errItemExportRevisionConflict = errors.New("item export revision conflict")
	errItemExportSourceLimit      = errors.New("item export source-byte limit exceeded")
	errItemExportOutputLimit      = errors.New("item export output-byte limit exceeded")
	errItemExportStaging          = errors.New("item export staging failed")
)

type canonicalExportPage struct {
	Image store.ItemImage
	Page  store.AnnotationPage
}

type canonicalItemExportPlan struct {
	Item      store.Item
	Pages     []canonicalExportPage
	Format    string
	Digest    string
	Filename  string
	MediaType string
}

type stagedCanonicalItemExport struct {
	File *os.File
	Size int64
}

type itemExportToken struct {
	Version     int    `json:"v"`
	WorkspaceID uint64 `json:"w"`
	ItemID      string `json:"i"`
	Format      string `json:"f"`
	Digest      string `json:"d"`
	ExpiresAt   string `json:"e"`
}

type itemExportTokenCodec struct {
	key []byte
	now func() time.Time
}

func newItemExportTokenCodec(rawKey string) (*itemExportTokenCodec, error) {
	key := strings.TrimSpace(rawKey)
	if key != rawKey || len(key) < minItemPageTokenKeyBytes || len(key) > maxItemPageTokenKeyBytes {
		return nil, fmt.Errorf("export token signing key must contain between %d and %d non-whitespace bytes", minItemPageTokenKeyBytes, maxItemPageTokenKeyBytes)
	}
	return &itemExportTokenCodec{key: append([]byte(nil), key...), now: time.Now}, nil
}

func (c *itemExportTokenCodec) encode(workspaceID uint64, plan canonicalItemExportPlan) (string, time.Time, error) {
	if c == nil || len(c.key) < minItemPageTokenKeyBytes {
		return "", time.Time{}, fmt.Errorf("export token signer is not configured")
	}
	if workspaceID == 0 || strings.TrimSpace(plan.Item.ID) == "" || len(plan.Item.ID) > 64 || len(plan.Pages) == 0 || !validAnnotationExportFormatName(plan.Format) || !validExportDigest(plan.Digest) {
		return "", time.Time{}, fmt.Errorf("invalid item export plan")
	}
	expiresAt := c.now().UTC().Add(itemExportTokenTTL)
	payload, err := json.Marshal(itemExportToken{
		Version:     itemExportTokenVersion,
		WorkspaceID: workspaceID,
		ItemID:      plan.Item.ID,
		Format:      plan.Format,
		Digest:      plan.Digest,
		ExpiresAt:   expiresAt.Format(time.RFC3339Nano),
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("marshal item export token: %w", err)
	}
	mac := hmac.New(sha256.New, c.key)
	_, _ = mac.Write([]byte(itemExportTokenDomain))
	_, _ = mac.Write(payload)
	token := base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if len(token) > maxItemExportTokenBytes {
		return "", time.Time{}, fmt.Errorf("encoded item export token exceeds contract limit")
	}
	return token, expiresAt, nil
}

func (c *itemExportTokenCodec) decode(raw string, workspaceID uint64) (itemExportToken, error) {
	if c == nil || len(c.key) < minItemPageTokenKeyBytes {
		return itemExportToken{}, fmt.Errorf("export token signer is not configured")
	}
	if workspaceID == 0 || len(raw) == 0 || len(raw) > maxItemExportTokenBytes || strings.TrimSpace(raw) != raw {
		return itemExportToken{}, fmt.Errorf("malformed export token")
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return itemExportToken{}, fmt.Errorf("malformed export token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(payload) > maxItemExportTokenBytes || base64.RawURLEncoding.EncodeToString(payload) != parts[0] {
		return itemExportToken{}, fmt.Errorf("decode export token")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(signature) != itemExportTokenSignatureSize || base64.RawURLEncoding.EncodeToString(signature) != parts[1] {
		return itemExportToken{}, fmt.Errorf("decode export token signature")
	}
	mac := hmac.New(sha256.New, c.key)
	_, _ = mac.Write([]byte(itemExportTokenDomain))
	_, _ = mac.Write(payload)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return itemExportToken{}, fmt.Errorf("export token signature is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var parsed itemExportToken
	if err := decoder.Decode(&parsed); err != nil {
		return itemExportToken{}, fmt.Errorf("decode export token payload: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return itemExportToken{}, fmt.Errorf("export token contains trailing data")
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, parsed.ExpiresAt)
	now := c.now().UTC()
	if err != nil || !expiresAt.After(now) || expiresAt.After(now.Add(itemExportTokenTTL+time.Minute)) {
		return itemExportToken{}, fmt.Errorf("export token is expired or invalid")
	}
	if parsed.Version != itemExportTokenVersion || parsed.WorkspaceID != workspaceID || strings.TrimSpace(parsed.ItemID) == "" || len(parsed.ItemID) > 64 || !validAnnotationExportFormatName(parsed.Format) || !validExportDigest(parsed.Digest) {
		return itemExportToken{}, fmt.Errorf("export token does not belong to this workspace")
	}
	return parsed, nil
}

func (h *Handler) loadCanonicalExportPage(ctx context.Context, itemImageID, expectedRevision uint64) (canonicalExportPage, error) {
	if itemImageID == 0 || expectedRevision == 0 || h.items == nil || h.annotations == nil {
		return canonicalExportPage{}, fmt.Errorf("%w: item image and revision are required", errItemExportInvalid)
	}
	image, err := h.itemImageForRequest(ctx, itemImageID)
	if err != nil {
		return canonicalExportPage{}, store.ErrAnnotationPageNotFound
	}
	page, err := h.annotations.LoadPage(ctx, h.currentWorkspaceID(ctx), itemImageID)
	if err != nil {
		return canonicalExportPage{}, err
	}
	if page.Revision != expectedRevision {
		return canonicalExportPage{}, errItemExportRevisionConflict
	}
	return canonicalExportPage{Image: image, Page: page}, nil
}

func (h *Handler) loadCanonicalItemExportSnapshot(ctx context.Context, itemID, format string, expected map[uint64]uint64) (canonicalItemExportPlan, error) {
	if strings.TrimSpace(itemID) == "" || !validAnnotationExportFormatName(format) || h.items == nil || h.annotations == nil {
		return canonicalItemExportPlan{}, fmt.Errorf("%w: item and format are required", errItemExportInvalid)
	}
	item, err := h.itemForRequest(ctx, itemID)
	if err != nil {
		return canonicalItemExportPlan{}, fmt.Errorf("%w: item not found", errItemExportInvalid)
	}
	if len(item.Images) == 0 {
		return canonicalItemExportPlan{}, fmt.Errorf("%w: item has no images", errItemExportInvalid)
	}
	if expected != nil && len(expected) != len(item.Images) {
		return canonicalItemExportPlan{}, fmt.Errorf("%w: expected revision vector must contain every item image exactly once", errItemExportInvalid)
	}
	workspaceID := h.currentWorkspaceID(ctx)
	revisions, err := h.annotations.ListItemRevisions(ctx, workspaceID, item.ID)
	if err != nil {
		return canonicalItemExportPlan{}, err
	}
	if len(revisions) != len(item.Images) {
		return canonicalItemExportPlan{}, fmt.Errorf("%w: every item image must have one committed canonical page", errItemExportInvalid)
	}
	if err := enforceItemExportSourceLimit(revisions, maxItemExportSourceBytes); err != nil {
		return canonicalItemExportPlan{}, err
	}
	plan := canonicalItemExportPlan{Item: item, Format: format, Pages: make([]canonicalExportPage, 0, len(item.Images))}
	for index, image := range item.Images {
		revision := revisions[index]
		if revision.ItemImageID != image.ID || revision.Revision == 0 {
			return canonicalItemExportPlan{}, fmt.Errorf("%w: canonical revision vector does not match item image order", errItemExportInvalid)
		}
		if expected != nil {
			expectedRevision, ok := expected[image.ID]
			if !ok || expectedRevision == 0 {
				return canonicalItemExportPlan{}, fmt.Errorf("%w: expected revision vector does not match the item", errItemExportInvalid)
			}
			if expectedRevision != revision.Revision {
				return canonicalItemExportPlan{}, errItemExportRevisionConflict
			}
		}
		plan.Pages = append(plan.Pages, canonicalExportPage{
			Image: image,
			Page: store.AnnotationPage{
				WorkspaceID: workspaceID,
				ItemImageID: image.ID,
				Revision:    revision.Revision,
			},
		})
	}
	plan.Digest = itemExportRevisionDigest(plan.Pages)
	plan.Filename, plan.MediaType = itemExportMetadata(item, format)
	return plan, nil
}

func enforceItemExportSourceLimit(revisions []store.AnnotationPageRevision, maximum int64) error {
	if maximum <= 0 {
		return fmt.Errorf("%w: source-byte limit must be positive", errItemExportInvalid)
	}
	limit := uint64(maximum)
	var total uint64
	for _, revision := range revisions {
		if revision.PayloadBytes > limit-total {
			return fmt.Errorf("%w: maximum is %d bytes", errItemExportSourceLimit, maximum)
		}
		total += revision.PayloadBytes
	}
	return nil
}

func (h *Handler) loadCanonicalItemExportPages(ctx context.Context, plan canonicalItemExportPlan) (canonicalItemExportPlan, error) {
	pages, err := h.annotations.LoadItemPages(ctx, h.currentWorkspaceID(ctx), plan.Item.ID, maxItemExportSourceBytes)
	if err != nil {
		return canonicalItemExportPlan{}, err
	}
	if len(pages) != len(plan.Pages) {
		return canonicalItemExportPlan{}, errItemExportRevisionConflict
	}
	for index, page := range pages {
		if page.ItemImageID != plan.Pages[index].Image.ID || page.Revision != plan.Pages[index].Page.Revision {
			return canonicalItemExportPlan{}, errItemExportRevisionConflict
		}
		plan.Pages[index].Page = page
	}
	return plan, nil
}

func itemExportRevisionDigest(pages []canonicalExportPage) string {
	hash := sha256.New()
	var value [8]byte
	for _, page := range pages {
		binary.BigEndian.PutUint64(value[:], page.Image.ID)
		_, _ = hash.Write(value[:])
		binary.BigEndian.PutUint64(value[:], page.Page.Revision)
		_, _ = hash.Write(value[:])
	}
	return base64.RawURLEncoding.EncodeToString(hash.Sum(nil))
}

func validExportDigest(value string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func validAnnotationExportFormatName(format string) bool {
	switch format {
	case "txt", "hocr", "pagexml", "alto":
		return true
	default:
		return false
	}
}

func itemExportMetadata(item store.Item, format string) (string, string) {
	safeName := sanitizeFilenamePart(item.Name, "item-"+item.ID)
	if len(item.Images) == 1 {
		_, mediaType, extension, err := emptyAnnotationExportMetadata(format)
		if err == nil {
			return safeName + "." + extension, mediaType
		}
	}
	if format == "txt" {
		return safeName + ".txt", "text/plain; charset=utf-8"
	}
	return safeName + "-" + format + ".zip", "application/zip"
}

func emptyAnnotationExportMetadata(format string) (string, string, string, error) {
	switch format {
	case "hocr":
		return "", "text/vnd.hocr+html; charset=utf-8", "hocr", nil
	case "pagexml":
		return "", "application/vnd.prima.page+xml; charset=utf-8", "xml", nil
	case "alto":
		return "", "application/alto+xml; charset=utf-8", "xml", nil
	case "txt":
		return "", "text/plain; charset=utf-8", "txt", nil
	default:
		return "", "", "", fmt.Errorf("unsupported export format")
	}
}

type canonicalExportPageRenderer func(canonicalExportPage, string) (string, string, string, error)

func stageCanonicalItemExport(ctx context.Context, plan canonicalItemExportPlan) (*stagedCanonicalItemExport, func(), error) {
	return stageCanonicalItemExportWithRenderer(ctx, plan, maxItemExportOutputBytes, renderCanonicalExportPage)
}

func stageCanonicalItemExportWithRenderer(ctx context.Context, plan canonicalItemExportPlan, maximum int64, render canonicalExportPageRenderer) (*stagedCanonicalItemExport, func(), error) {
	if ctx == nil || render == nil || maximum <= 0 || len(plan.Pages) == 0 || !validAnnotationExportFormatName(plan.Format) {
		return nil, nil, fmt.Errorf("%w: context and renderer are required", errItemExportInvalid)
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	file, err := os.CreateTemp("", "scribe-item-export-")
	if err != nil {
		return nil, nil, fmt.Errorf("%w: create file: %v", errItemExportStaging, err)
	}
	filename := file.Name()
	cleanup := func() { _ = file.Close() }
	// Unlink immediately while retaining the open descriptor. A normal return,
	// cancellation, process crash, or SIGKILL can therefore never leave OCR
	// plaintext in the container writable layer.
	if err := os.Remove(filename); err != nil {
		_ = file.Close()
		_ = os.Remove(filename)
		return nil, nil, fmt.Errorf("%w: unlink file: %v", errItemExportStaging, err)
	}
	bounded := &boundedExportWriter{destination: file, maximum: maximum}
	var totalBytes int64
	renderPage := func(page canonicalExportPage) (string, string, error) {
		if err := ctx.Err(); err != nil {
			return "", "", err
		}
		content, _, extension, err := render(page, plan.Format)
		if err != nil {
			if errors.Is(err, errItemExportOutputLimit) {
				return "", "", err
			}
			return "", "", fmt.Errorf("%w: render canonical page", errItemExportInvalid)
		}
		if err := ctx.Err(); err != nil {
			return "", "", err
		}
		totalBytes += int64(len(content))
		if totalBytes > maximum {
			return "", "", fmt.Errorf("%w: maximum is %d bytes", errItemExportOutputLimit, maximum)
		}
		return content, extension, nil
	}
	writeContent := func(destination io.Writer, content string) error {
		_, err := copyExportContent(ctx, destination, strings.NewReader(content))
		return err
	}
	fail := func(err error) (*stagedCanonicalItemExport, func(), error) {
		cleanup()
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded), errors.Is(err, errItemExportOutputLimit), errors.Is(err, errItemExportInvalid):
			return nil, nil, err
		default:
			return nil, nil, fmt.Errorf("%w: write response: %v", errItemExportStaging, err)
		}
	}

	if len(plan.Pages) == 1 {
		content, _, err := renderPage(plan.Pages[0])
		if err != nil {
			return fail(err)
		}
		if err := writeContent(bounded, content); err != nil {
			return fail(err)
		}
	} else if plan.Format == "txt" {
		for index, page := range plan.Pages {
			content, _, err := renderPage(page)
			if err != nil {
				return fail(err)
			}
			if index > 0 {
				if err := writeContent(bounded, "\n\n"); err != nil {
					return fail(err)
				}
			}
			if err := writeContent(bounded, content); err != nil {
				return fail(err)
			}
		}
	} else {
		archive := zip.NewWriter(bounded)
		safeName := sanitizeFilenamePart(plan.Item.Name, "item-"+plan.Item.ID)
		for index, page := range plan.Pages {
			content, extension, err := renderPage(page)
			if err != nil {
				_ = archive.Close()
				return fail(err)
			}
			sequence := page.Image.Sequence
			if sequence == 0 {
				sequence = uint32(index + 1) // #nosec G115 -- item export pages are capped at 1000.
			}
			entry, err := archive.Create(fmt.Sprintf("%s-page-%04d.%s", safeName, sequence, extension))
			if err != nil {
				_ = archive.Close()
				return fail(err)
			}
			if err := writeContent(entry, content); err != nil {
				_ = archive.Close()
				return fail(err)
			}
		}
		if err := archive.Close(); err != nil {
			return fail(err)
		}
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	position, err := file.Seek(0, io.SeekStart)
	if err != nil || position != 0 {
		if err == nil {
			err = fmt.Errorf("unexpected staged export offset %d", position)
		}
		return fail(err)
	}
	return &stagedCanonicalItemExport{File: file, Size: bounded.written}, cleanup, nil
}

type boundedExportWriter struct {
	destination io.Writer
	maximum     int64
	written     int64
}

func (w *boundedExportWriter) Write(content []byte) (int, error) {
	if w == nil || w.destination == nil || w.maximum <= 0 {
		return 0, fmt.Errorf("%w: invalid output writer", errItemExportStaging)
	}
	if int64(len(content)) > w.maximum-w.written {
		return 0, fmt.Errorf("%w: maximum is %d bytes", errItemExportOutputLimit, w.maximum)
	}
	written, err := w.destination.Write(content)
	w.written += int64(written)
	return written, err
}

func copyExportContent(ctx context.Context, destination io.Writer, source io.Reader) (int64, error) {
	buffer := make([]byte, exportCopyBufferBytes)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		count, readErr := source.Read(buffer)
		if count > 0 {
			writeCount, writeErr := destination.Write(buffer[:count])
			written += int64(writeCount)
			if writeErr != nil {
				return written, writeErr
			}
			if writeCount != count {
				return written, io.ErrShortWrite
			}
		}
		if errors.Is(readErr, io.EOF) {
			return written, nil
		}
		if readErr != nil {
			return written, readErr
		}
	}
}
