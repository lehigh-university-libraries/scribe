package imageservice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/serviceauth"
)

type Box struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

type Client struct {
	http  *http.Client
	auth  *serviceauth.CloudRunTokenSource
	base  string
	aud   string
}

func New() *Client {
	httpClient := &http.Client{Timeout: 60 * time.Second}
	cfg := config.Get().Config.ImageService
	return &Client{
		http: httpClient,
		auth: serviceauth.NewCloudRunTokenSource(httpClient),
		base: strings.TrimRight(strings.TrimSpace(cfg.URL), "/"),
		aud:  strings.TrimSpace(cfg.Audience),
	}
}

func (c *Client) Enabled() bool {
	return c != nil && c.base != ""
}

func (c *Client) Crop(ctx context.Context, imagePath string, box Box) ([]byte, error) {
	return c.postMultipartImage(ctx, "/v1/crop", imagePath, map[string]string{
		"x":      strconv.Itoa(box.X),
		"y":      strconv.Itoa(box.Y),
		"width":  strconv.Itoa(box.Width),
		"height": strconv.Itoa(box.Height),
	})
}

func (c *Client) StitchHorizontal(ctx context.Context, imagePath string, boxes []Box, padding int) ([]byte, error) {
	payload, err := json.Marshal(boxes)
	if err != nil {
		return nil, fmt.Errorf("marshal stitch boxes: %w", err)
	}
	return c.postMultipartImage(ctx, "/v1/stitch-horizontal", imagePath, map[string]string{
		"boxes_json": string(payload),
		"padding":    strconv.Itoa(padding),
	})
}

func (c *Client) Normalize(ctx context.Context, image []byte, contentType string) ([]byte, error) {
	if !c.Enabled() {
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

func (c *Client) postMultipartImage(ctx context.Context, path, imagePath string, fields map[string]string) ([]byte, error) {
	if !c.Enabled() {
		return nil, fmt.Errorf("image service is not configured")
	}
	image, err := os.ReadFile(imagePath)
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
