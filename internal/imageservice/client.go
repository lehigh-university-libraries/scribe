package imageservice

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/safefile"
	"github.com/lehigh-university-libraries/scribe/internal/serviceauth"
	"github.com/lehigh-university-libraries/scribe/internal/uploadblob"
)

type Box struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

type Client struct {
	http *http.Client
	auth *serviceauth.CloudRunTokenSource
	base string
	iiif string
	aud  string
}

func New() *Client {
	httpClient := &http.Client{Timeout: 60 * time.Second}
	cfg := config.Get().Config.ImageService
	return &Client{
		http: httpClient,
		auth: serviceauth.NewCloudRunTokenSource(httpClient),
		base: strings.TrimRight(strings.TrimSpace(cfg.URL), "/"),
		iiif: strings.TrimRight(strings.TrimSpace(config.Get().Config.IIIF.InternalBase), "/"),
		aud:  strings.TrimSpace(cfg.Audience),
	}
}

func (c *Client) Enabled() bool {
	return c != nil && (c.base != "" || c.iiif != "")
}

func (c *Client) Crop(ctx context.Context, imagePath string, box Box) ([]byte, error) {
	if c != nil && c.iiif != "" {
		identifier, err := iiifIdentifierFromImagePath(imagePath)
		if err != nil {
			return nil, err
		}
		if box.Width <= 0 || box.Height <= 0 {
			return nil, fmt.Errorf("invalid crop dimensions")
		}
		region := fmt.Sprintf("%d,%d,%d,%d", max(0, box.X), max(0, box.Y), box.Width, box.Height)
		return c.getIIIFImage(ctx, fmt.Sprintf("%s/%s/%s/max/0/default.jpg", c.iiif, identifier, region))
	}
	return c.postMultipartImage(ctx, "/v1/crop", imagePath, map[string]string{
		"x":      strconv.Itoa(box.X),
		"y":      strconv.Itoa(box.Y),
		"width":  strconv.Itoa(box.Width),
		"height": strconv.Itoa(box.Height),
	})
}

func (c *Client) StitchHorizontal(ctx context.Context, imagePath string, boxes []Box, padding int) ([]byte, error) {
	if c != nil && c.iiif != "" {
		return c.stitchHorizontalFromIIIF(ctx, imagePath, boxes, padding)
	}
	payload, err := json.Marshal(boxes)
	if err != nil {
		return nil, fmt.Errorf("marshal stitch boxes: %w", err)
	}
	return c.postMultipartImage(ctx, "/v1/stitch-horizontal", imagePath, map[string]string{
		"boxes_json": string(payload),
		"padding":    strconv.Itoa(padding),
	})
}

func (c *Client) getIIIFImage(ctx context.Context, imageURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("iiif image status %d: %s", resp.StatusCode, string(body))
	}
	return body, nil
}

func (c *Client) stitchHorizontalFromIIIF(ctx context.Context, imagePath string, boxes []Box, padding int) ([]byte, error) {
	if len(boxes) == 0 {
		return nil, fmt.Errorf("no boxes to stitch")
	}
	crops := make([]image.Image, 0, len(boxes))
	totalWidth := 0
	maxHeight := 0
	for _, box := range boxes {
		box.X = max(0, box.X-padding)
		box.Y = max(0, box.Y-padding)
		box.Width += padding * 2
		box.Height += padding * 2
		data, err := c.Crop(ctx, imagePath, box)
		if err != nil {
			return nil, err
		}
		img, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("decode iiif crop: %w", err)
		}
		b := img.Bounds()
		crops = append(crops, img)
		totalWidth += b.Dx()
		if b.Dy() > maxHeight {
			maxHeight = b.Dy()
		}
	}
	if len(crops) == 0 || totalWidth <= 0 || maxHeight <= 0 {
		return nil, fmt.Errorf("no valid boxes to stitch")
	}
	dst := image.NewRGBA(image.Rect(0, 0, totalWidth, maxHeight))
	offsetX := 0
	for _, crop := range crops {
		b := crop.Bounds()
		target := image.Rect(offsetX, 0, offsetX+b.Dx(), b.Dy())
		draw.Draw(dst, target, crop, b.Min, draw.Src)
		offsetX += b.Dx()
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 95}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func iiifIdentifierFromImagePath(imagePath string) (string, error) {
	name := filepath.Base(strings.TrimSpace(imagePath))
	if name == "" || name == "." || name == string(filepath.Separator) || strings.Contains(name, "/") || strings.Contains(name, "\\") {
		return "", fmt.Errorf("invalid image path %q", imagePath)
	}
	return url.PathEscape(name), nil
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (c *Client) Normalize(ctx context.Context, image []byte, contentType string) ([]byte, error) {
	if c != nil && c.iiif != "" {
		data, err := c.normalizeViaIIIF(ctx, image, contentType)
		if err == nil {
			return data, nil
		}
		if c.base == "" {
			return nil, err
		}
	}
	if c == nil || c.base == "" {
		return nil, fmt.Errorf("image service is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/v1/normalize", bytes.NewReader(image))
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(contentType) != "" {
		req.Header.Set("Content-Type", contentType)
	} else {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	if err := c.authorize(ctx, req); err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("image service normalize status %d: %s", resp.StatusCode, string(body))
	}
	return body, nil
}

func (c *Client) normalizeViaIIIF(ctx context.Context, image []byte, contentType string) ([]byte, error) {
	if len(image) == 0 {
		return nil, fmt.Errorf("image is empty")
	}
	sum := sha256.Sum256(image)
	name := hex.EncodeToString(sum[:]) + extensionForNormalizeContentType(contentType)
	if uploadblob.Enabled() {
		if err := uploadblob.Put(ctx, name, image, contentType); err != nil {
			return nil, fmt.Errorf("write image for iiif normalize: %w", err)
		}
	} else {
		if err := os.MkdirAll("uploads", 0o750); err != nil {
			return nil, fmt.Errorf("create uploads dir: %w", err)
		}
		path := filepath.Join("uploads", name)
		if err := os.WriteFile(path, image, 0o600); err != nil {
			return nil, fmt.Errorf("write image for iiif normalize: %w", err)
		}
	}
	identifier := url.PathEscape(name)
	return c.getIIIFImage(ctx, fmt.Sprintf("%s/%s/full/max/0/default.jpg", c.iiif, identifier))
}

func extensionForNormalizeContentType(contentType string) string {
	switch strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0])) {
	case "image/jp2", "image/jpeg2000", "image/jpx":
		return ".jp2"
	case "image/tiff", "image/tif":
		return ".tif"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	default:
		return ".img"
	}
}

func (c *Client) postMultipartImage(ctx context.Context, path, imagePath string, fields map[string]string) ([]byte, error) {
	if c == nil || c.base == "" {
		return nil, fmt.Errorf("image service is not configured")
	}
	image, err := safefile.ReadFile(imagePath)
	if err != nil {
		return nil, fmt.Errorf("read image %s: %w", imagePath, err)
	}

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			return nil, err
		}
	}
	part, err := writer.CreateFormFile("image", filepath.Base(imagePath))
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(image); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if err := c.authorize(ctx, req); err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("image service %s status %d: %s", path, resp.StatusCode, string(body))
	}
	return body, nil
}

func (c *Client) authorize(ctx context.Context, req *http.Request) error {
	header, err := c.auth.AuthorizationHeader(ctx, c.aud)
	if err != nil {
		return err
	}
	if header != "" {
		req.Header.Set("Authorization", header)
	}
	return nil
}
