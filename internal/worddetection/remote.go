//go:build remoteocr

package worddetection

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
	"strings"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/serviceauth"
)

type remoteProvider struct {
	model string
}

type remoteSegmentResponse struct {
	Provider string    `json:"provider"`
	Words    []WordBox `json:"words"`
}

func NewTesseract() *remoteProvider {
	return &remoteProvider{model: "tesseract"}
}

func NewCustom() *remoteProvider {
	return &remoteProvider{model: "scribe"}
}

func NewKraken(modelID string) *remoteProvider {
	model := "kraken"
	if strings.TrimSpace(modelID) != "" {
		model = "kraken:" + strings.TrimSpace(modelID)
	}
	return &remoteProvider{model: model}
}

func (p *remoteProvider) Name() string {
	return p.model
}

func (p *remoteProvider) DetectWords(ctx context.Context, imagePath string) ([]WordBox, error) {
	cfg := config.Get().Config.Segmentation
	baseURL, audience := cfg.ResolveForModel(p.model)
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = strings.TrimRight(strings.TrimSpace(cfg.URL), "/")
	}
	if audience == "" {
		audience = strings.TrimSpace(cfg.Audience)
	}
	if baseURL == "" {
		return nil, fmt.Errorf("segmentation_service.url is required when built with remoteocr")
	}

	imageData, err := os.ReadFile(imagePath)
	if err != nil {
		return nil, fmt.Errorf("read image %s: %w", imagePath, err)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("model", p.model); err != nil {
		return nil, err
	}
	part, err := writer.CreateFormFile("image", filepath.Base(imagePath))
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(imageData); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}

	httpClient := &http.Client{Timeout: 60 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v1/segment", &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	auth := serviceauth.NewCloudRunTokenSource(httpClient)
	header, err := auth.AuthorizationHeader(ctx, audience)
	if err != nil {
		return nil, err
	}
	if header != "" {
		req.Header.Set("Authorization", header)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("segmentor status %d: %s", resp.StatusCode, string(respBody))
	}

	var parsed remoteSegmentResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("parse segmentor response: %w", err)
	}
	return parsed.Words, nil
}
