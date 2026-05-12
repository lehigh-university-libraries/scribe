package segmentor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/safefile"
	"github.com/lehigh-university-libraries/scribe/internal/serviceauth"
	"github.com/lehigh-university-libraries/scribe/internal/worddetection"
)

type Client struct {
	http *http.Client
	auth *serviceauth.CloudRunTokenSource
	base string
	aud  string
}

type detectResponse struct {
	Provider string                  `json:"provider"`
	Words    []worddetection.WordBox `json:"words"`
}

type transcribeResponse struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Text     string `json:"text"`
}

func NewClient() *Client {
	cfg := config.Get().Config.Segmentation
	return newClient(cfg.URL, cfg.Audience)
}

func NewSegmentationModelClient(model string) *Client {
	cfg := config.Get().Config.Segmentation
	baseURL, audience := cfg.ResolveForModel(model)
	if baseURL == "" {
		baseURL = strings.TrimSpace(cfg.URL)
	}
	if audience == "" {
		audience = strings.TrimSpace(cfg.Audience)
	}
	return newClient(baseURL, audience)
}

func NewKrakenClient(model, overrideBaseURL, overrideAudience string) *Client {
	cfg := config.Get().Config
	baseURL := strings.TrimSpace(overrideBaseURL)
	audience := strings.TrimSpace(overrideAudience)
	if baseURL == "" {
		baseURL, audience = cfg.LLM.Kraken.ResolveForModel(model)
	}
	if baseURL == "" {
		baseURL = strings.TrimSpace(cfg.LLM.Kraken.URL)
	}
	if baseURL == "" {
		baseURL, audience = cfg.Segmentation.ResolveForModel(model)
	}
	if audience == "" {
		audience = strings.TrimSpace(cfg.LLM.Kraken.Audience)
	}
	if audience == "" {
		audience = strings.TrimSpace(cfg.Segmentation.Audience)
	}
	return newClient(baseURL, audience)
}

func newClient(baseURL, audience string) *Client {
	httpClient := &http.Client{Timeout: 120 * time.Second}
	return &Client{
		http: httpClient,
		auth: serviceauth.NewCloudRunTokenSource(httpClient),
		base: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		aud:  strings.TrimSpace(audience),
	}
}

func (c *Client) Enabled() bool {
	return c != nil && c.base != ""
}

func (c *Client) DetectWords(ctx context.Context, imagePath, model string) ([]worddetection.WordBox, string, error) {
	body, contentType, err := c.newMultipartBody(imagePath, map[string]string{
		"model": strings.TrimSpace(model),
	})
	if err != nil {
		return nil, "", err
	}
	respBody, err := c.post(ctx, "/v1/segment", body, contentType)
	if err != nil {
		return nil, "", err
	}
	var parsed detectResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, "", fmt.Errorf("parse segmentor response: %w", err)
	}
	return parsed.Words, strings.TrimSpace(parsed.Provider), nil
}

func (c *Client) Transcribe(ctx context.Context, imagePath, model string) (string, string, error) {
	body, contentType, err := c.newMultipartBody(imagePath, map[string]string{
		"model": strings.TrimSpace(model),
	})
	if err != nil {
		return "", "", err
	}
	respBody, err := c.post(ctx, "/v1/transcribe", body, contentType)
	if err != nil {
		return "", "", err
	}
	var parsed transcribeResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", "", fmt.Errorf("parse segmentor transcription response: %w", err)
	}
	return strings.TrimSpace(parsed.Text), strings.TrimSpace(parsed.Model), nil
}

func (c *Client) newMultipartBody(imagePath string, fields map[string]string) (*bytes.Buffer, string, error) {
	if !c.Enabled() {
		return nil, "", fmt.Errorf("segmentor service is not configured")
	}
	imageData, err := safefile.ReadFile(imagePath)
	if err != nil {
		return nil, "", fmt.Errorf("read image %s: %w", imagePath, err)
	}

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	for key, value := range fields {
		if strings.TrimSpace(value) == "" {
			continue
		}
		if err := writer.WriteField(key, value); err != nil {
			return nil, "", err
		}
	}
	part, err := writer.CreateFormFile("image", filepath.Base(imagePath))
	if err != nil {
		return nil, "", err
	}
	if _, err := part.Write(imageData); err != nil {
		return nil, "", err
	}
	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	return &buf, writer.FormDataContentType(), nil
}

func (c *Client) post(ctx context.Context, path string, body *bytes.Buffer, contentType string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	if err := c.authorize(ctx, req); err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("segmentor %s status %d: %s", path, resp.StatusCode, string(respBody))
	}
	return respBody, nil
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
