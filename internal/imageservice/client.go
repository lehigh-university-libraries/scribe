// Package imageservice is the single Scribe client boundary for Triplet's
// IIIF Image API. Scribe never implements or proxies the IIIF Image protocol.
package imageservice

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/safefile"
	"github.com/lehigh-university-libraries/scribe/internal/servicehttp"
	"github.com/lehigh-university-libraries/scribe/internal/uploadblob"
	"github.com/lehigh-university-libraries/scribe/internal/uploadlimits"
	"github.com/lehigh-university-libraries/scribe/internal/uploadref"
)

const (
	maxTripletResponseBytes int64 = uploadlimits.MaxImageBytes
	maxDecodedDimension           = uploadlimits.MaxImageDimension
	maxDecodedPixels        int64 = uploadlimits.MaxImagePixels
	stagedUploadsDirectory        = "uploads"
)

type Box struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

// Client calls a trusted Triplet deployment. sourceBase is the exact Scribe
// raw-upload collection Triplet may dereference; sourceReadToken has no other
// application permissions.
type Client struct {
	http            *http.Client
	internalBase    string
	sourceBase      string
	sourceReadToken string
}

func New() *Client {
	cfg := config.Get().Config.IIIF
	return &Client{
		http:            servicehttp.NewClient(2 * time.Minute),
		internalBase:    strings.TrimRight(strings.TrimSpace(cfg.InternalBase), "/"),
		sourceBase:      strings.TrimRight(strings.TrimSpace(cfg.SourceBase), "/"),
		sourceReadToken: strings.TrimSpace(cfg.SourceReadToken),
	}
}

func (c *Client) Enabled() bool {
	return c != nil && c.http != nil && c.internalBase != "" && c.sourceBase != "" && c.sourceReadToken != ""
}

// Crop asks Triplet for one exact source region.
func (c *Client) Crop(ctx context.Context, imagePath string, box Box) (result []byte, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateBox(box); err != nil {
		return nil, err
	}
	source, err := c.sourceForPath(ctx, imagePath)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, source.cleanup())
		if err != nil {
			result = nil
		}
	}()
	region := fmt.Sprintf("%d,%d,%d,%d", box.X, box.Y, box.Width, box.Height)
	return c.getImage(ctx, source.url, region+"/max/0/default.jpg")
}

// FullJPEG asks Triplet to normalize a trusted local pipeline image. This is
// also used by the segmentor so every normalization follows the same source
// identity and authentication policy.
func (c *Client) FullJPEG(ctx context.Context, imagePath string) (result []byte, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	source, err := c.sourceForPath(ctx, imagePath)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, source.cleanup())
		if err != nil {
			result = nil
		}
	}()
	return c.getImage(ctx, source.url, "full/max/0/default.jpg")
}

func (c *Client) StitchHorizontal(ctx context.Context, imagePath string, boxes []Box, padding int) (result []byte, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(boxes) == 0 {
		return nil, fmt.Errorf("no boxes to stitch")
	}
	if len(boxes) > iiif.MaxAnnotationsPerPage/2 {
		return nil, fmt.Errorf("box count %d exceeds processing limit %d", len(boxes), iiif.MaxAnnotationsPerPage/2)
	}
	if padding < 0 || padding > maxDecodedDimension {
		return nil, fmt.Errorf("padding must be between 0 and %d", maxDecodedDimension)
	}
	source, err := c.sourceForPath(ctx, imagePath)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, source.cleanup())
		if err != nil {
			result = nil
		}
	}()

	crops := make([]image.Image, 0, len(boxes))
	totalWidth := 0
	maxHeight := 0
	for _, original := range boxes {
		if err := validateBox(original); err != nil {
			return nil, err
		}
		box := Box{
			X:      max(0, original.X-padding),
			Y:      max(0, original.Y-padding),
			Width:  original.Width + padding*2,
			Height: original.Height + padding*2,
		}
		if err := validateBox(box); err != nil {
			return nil, err
		}
		region := fmt.Sprintf("%d,%d,%d,%d", box.X, box.Y, box.Width, box.Height)
		data, err := c.getImage(ctx, source.url, region+"/max/0/default.jpg")
		if err != nil {
			return nil, err
		}
		cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("inspect Triplet crop: %w", err)
		}
		if err := validateDimensions("Triplet crop", cfg.Width, cfg.Height); err != nil {
			return nil, err
		}
		if cfg.Width > maxDecodedDimension-totalWidth {
			return nil, fmt.Errorf("stitched width exceeds %d pixels", maxDecodedDimension)
		}
		nextWidth := totalWidth + cfg.Width
		nextHeight := max(maxHeight, cfg.Height)
		if int64(nextWidth)*int64(nextHeight) > maxDecodedPixels {
			return nil, fmt.Errorf("stitched image exceeds %d decoded pixels", maxDecodedPixels)
		}
		img, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("decode Triplet crop: %w", err)
		}
		crops = append(crops, img)
		totalWidth = nextWidth
		maxHeight = nextHeight
	}

	dst := image.NewRGBA(image.Rect(0, 0, totalWidth, maxHeight))
	offsetX := 0
	for _, crop := range crops {
		bounds := crop.Bounds()
		target := image.Rect(offsetX, 0, offsetX+bounds.Dx(), bounds.Dy())
		draw.Draw(dst, target, crop, bounds.Min, draw.Src)
		offsetX += bounds.Dx()
	}
	var output bytes.Buffer
	if err := jpeg.Encode(&output, dst, &jpeg.Options{Quality: 95}); err != nil {
		return nil, fmt.Errorf("encode stitched image: %w", err)
	}
	return output.Bytes(), nil
}

// Normalize stages bytes behind Scribe's constrained raw-source route, asks
// Triplet for a JPEG, and removes both local and shared temporary copies.
func (c *Client) Normalize(ctx context.Context, imageBytes []byte, contentType string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !c.Enabled() {
		return nil, fmt.Errorf("triplet image client is not configured")
	}
	if len(imageBytes) == 0 {
		return nil, fmt.Errorf("image is empty")
	}
	if err := uploadlimits.ValidateImageSize(int64(len(imageBytes))); err != nil {
		return nil, err
	}
	source, err := c.stageSource(ctx, imageBytes, contentType, "")
	if err != nil {
		return nil, err
	}
	normalized, normalizeErr := c.getImage(ctx, source.url, "full/max/0/default.jpg")
	cleanupErr := source.cleanup()
	if normalizeErr != nil || cleanupErr != nil {
		return nil, errors.Join(normalizeErr, cleanupErr)
	}
	return normalized, nil
}

type sourceReference struct {
	url     string
	cleanup func() error
}

func (c *Client) sourceForPath(ctx context.Context, imagePath string) (sourceReference, error) {
	if !c.Enabled() {
		return sourceReference{}, fmt.Errorf("triplet image client is not configured")
	}
	name := filepath.Base(strings.TrimSpace(imagePath))
	if uploadref.IsImmutableName(name) {
		return sourceReference{url: c.sourceURL(name), cleanup: func() error { return nil }}, nil
	}
	data, err := readBoundedImageFile(imagePath)
	if err != nil {
		return sourceReference{}, fmt.Errorf("read image for Triplet: %w", err)
	}
	return c.stageSource(ctx, data, http.DetectContentType(data), filepath.Ext(imagePath))
}

func (c *Client) stageSource(ctx context.Context, data []byte, contentType, extensionHint string) (sourceReference, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	extension, mediaType, err := canonicalImageType(contentType, extensionHint, data)
	if err != nil {
		return sourceReference{}, err
	}
	digest := sha256.Sum256(data)
	name := hex.EncodeToString(digest[:]) + "-" + uuid.NewString() + extension
	if !uploadref.IsImmutableName(name) {
		return sourceReference{}, fmt.Errorf("refusing noncanonical staged upload name")
	}
	file, err := createStagedUpload(name)
	if err != nil {
		return sourceReference{}, fmt.Errorf("create staged Triplet source: %w", err)
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		_ = removeStagedUpload(name)
		if writeErr != nil {
			return sourceReference{}, errors.Join(fmt.Errorf("write staged Triplet source: %w", writeErr), closeErr)
		}
		return sourceReference{}, fmt.Errorf("close staged Triplet source: %w", closeErr)
	}
	cleanup := func() error {
		removeErr := removeStagedUpload(name)
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		return errors.Join(removeErr, uploadblob.Delete(cleanupCtx, name))
	}
	if err := uploadblob.Put(ctx, name, data, mediaType); err != nil {
		return sourceReference{}, errors.Join(fmt.Errorf("persist staged Triplet source: %w", err), cleanup())
	}
	return sourceReference{url: c.sourceURL(name), cleanup: cleanup}, nil
}

func createStagedUpload(name string) (file *os.File, err error) {
	if !uploadref.IsImmutableName(name) {
		return nil, fmt.Errorf("refusing noncanonical staged upload name")
	}
	if err := os.MkdirAll(stagedUploadsDirectory, 0o750); err != nil {
		return nil, fmt.Errorf("create uploads directory: %w", err)
	}
	root, err := os.OpenRoot(stagedUploadsDirectory)
	if err != nil {
		return nil, fmt.Errorf("open uploads root: %w", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close uploads root: %w", closeErr))
			if file != nil {
				err = errors.Join(err, file.Close())
				file = nil
			}
		}
	}()
	file, err = root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	return file, err
}

func removeStagedUpload(name string) (err error) {
	if !uploadref.IsImmutableName(name) {
		return fmt.Errorf("refusing noncanonical staged upload name")
	}
	root, err := os.OpenRoot(stagedUploadsDirectory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open uploads root: %w", err)
	}
	defer func() {
		err = errors.Join(err, root.Close())
	}()
	err = root.Remove(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (c *Client) sourceURL(name string) string {
	return c.sourceBase + "/" + name
}

func (c *Client) getImage(ctx context.Context, sourceURL, suffix string) ([]byte, error) {
	requestURL := c.internalBase + "/" + url.PathEscape(sourceURL) + "/" + suffix
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create Triplet image request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+c.sourceReadToken)
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("call Triplet image API: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.CopyN(io.Discard, response.Body, 4096)
		return nil, fmt.Errorf("triplet image request returned status %d", response.StatusCode)
	}
	return readClientResponse(response.Body)
}

func canonicalImageType(contentType, extensionHint string, data []byte) (string, string, error) {
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	if mediaType == "" || mediaType == "application/octet-stream" {
		mediaType = strings.ToLower(strings.TrimSpace(strings.Split(http.DetectContentType(data), ";")[0]))
	}
	if extension, ok := canonicalExtensionForMediaType(mediaType); ok {
		return extension, mediaType, nil
	}
	switch strings.ToLower(strings.TrimSpace(extensionHint)) {
	case ".jpg", ".jpeg":
		return ".jpg", "image/jpeg", nil
	case ".png":
		return ".png", "image/png", nil
	case ".gif":
		return ".gif", "image/gif", nil
	case ".jp2", ".j2k", ".jpx":
		return ".jp2", "image/jp2", nil
	case ".tif", ".tiff":
		return ".tiff", "image/tiff", nil
	case ".webp":
		return ".webp", "image/webp", nil
	default:
		return "", "", fmt.Errorf("unsupported image content type %q", contentType)
	}
}

func canonicalExtensionForMediaType(mediaType string) (string, bool) {
	switch mediaType {
	case "image/jpeg", "image/jpg":
		return ".jpg", true
	case "image/png":
		return ".png", true
	case "image/gif":
		return ".gif", true
	case "image/jp2", "image/jpeg2000", "image/jpx":
		return ".jp2", true
	case "image/tiff", "image/tif":
		return ".tiff", true
	case "image/webp":
		return ".webp", true
	default:
		return "", false
	}
}

func validateBox(box Box) error {
	if box.X < 0 || box.Y < 0 || box.Width <= 0 || box.Height <= 0 ||
		box.Width > maxDecodedDimension || box.Height > maxDecodedDimension ||
		int64(box.Width)*int64(box.Height) > maxDecodedPixels ||
		box.X > iiif.MaxPixelCoordinate-box.Width || box.Y > iiif.MaxPixelCoordinate-box.Height {
		return fmt.Errorf("invalid box geometry (%d,%d,%d,%d)", box.X, box.Y, box.Width, box.Height)
	}
	return nil
}

func validateDimensions(label string, width, height int) error {
	if width <= 0 || height <= 0 || width > maxDecodedDimension || height > maxDecodedDimension || int64(width)*int64(height) > maxDecodedPixels {
		return fmt.Errorf("%s dimensions %dx%d exceed processing limits", label, width, height)
	}
	return nil
}

func readBoundedImageFile(path string) ([]byte, error) {
	file, err := safefile.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("image input must be a regular file")
	}
	if err := uploadlimits.ValidateImageSize(info.Size()); err != nil {
		return nil, err
	}
	return readClientResponse(file)
}

func readClientResponse(reader io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maxTripletResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxTripletResponseBytes {
		return nil, fmt.Errorf("triplet image response exceeds %d bytes", maxTripletResponseBytes)
	}
	return body, nil
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
