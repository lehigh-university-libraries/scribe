//go:build remoteocr

package worddetection

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/lehigh-university-libraries/htr/pkg/auth/gcpidtoken"
	"github.com/lehigh-university-libraries/htr/pkg/httpclient"
	"github.com/lehigh-university-libraries/htr/pkg/providers"
	"github.com/lehigh-university-libraries/htr/pkg/remoteocr"
	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/safefile"
	"github.com/lehigh-university-libraries/scribe/internal/uploadlimits"
)

const maxRemoteSegmentResponseBytes int64 = 16 << 20

var remoteIdentityTokens, remoteIdentityTokensErr = gcpidtoken.New(gcpidtoken.Options{})

type remoteProvider struct {
	model string
}

func NewTesseract() *remoteProvider { return &remoteProvider{model: "tesseract"} }

func NewCustom() *remoteProvider { return &remoteProvider{model: "scribe"} }

func NewKraken(modelID string) *remoteProvider {
	model := "kraken"
	if strings.TrimSpace(modelID) != "" {
		model = "kraken:" + strings.TrimSpace(modelID)
	}
	return &remoteProvider{model: model}
}

func (p *remoteProvider) Name() string { return p.model }

func (p *remoteProvider) DetectWords(ctx context.Context, imagePath string) ([]WordBox, error) {
	endpoint, audience, err := p.registeredEndpoint()
	if err != nil {
		return nil, err
	}
	authenticator, err := remoteAuthenticator(audience)
	if err != nil {
		return nil, err
	}
	client, err := remoteocr.NewClient(remoteocr.Options{
		Endpoint:         endpoint,
		Authenticator:    authenticator,
		Timeout:          time.Minute,
		MaxImageBytes:    uploadlimits.MaxImageBytes,
		MaxRequestBytes:  uploadlimits.MaxMultipartBodyBytes,
		MaxResponseBytes: maxRemoteSegmentResponseBytes,
		MaxBoxes:         iiif.MaxAnnotationsPerPage / 2,
	})
	if err != nil {
		return nil, err
	}
	data, err := safefile.ReadFileLimit(imagePath, uploadlimits.MaxImageBytes)
	if err != nil {
		return nil, fmt.Errorf("read segmentation image: %w", err)
	}
	result, err := client.Segment(ctx, providers.Image{
		Data: data, MediaType: remoteImageMediaType(imagePath, data),
	}, p.model)
	if err != nil {
		return nil, err
	}
	words := make([]WordBox, len(result.Words))
	for index, box := range result.Words {
		words[index] = WordBox{X: box.X, Y: box.Y, Width: box.Width, Height: box.Height, Text: box.Text, Confidence: box.Confidence}
	}
	return words, nil
}

func (p *remoteProvider) registeredEndpoint() (string, string, error) {
	cfg := config.Get().Config.Segmentation
	endpoint, audience := cfg.ResolveForModel(p.model)
	if strings.TrimSpace(endpoint) == "" {
		endpoint = cfg.URL
	}
	if strings.TrimSpace(audience) == "" {
		audience = cfg.Audience
	}
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if endpoint == "" {
		return "", "", fmt.Errorf("segmentation_service.url is required when built with remoteocr")
	}
	parsedEndpoint, err := httpclient.ParseEndpoint(endpoint)
	if err != nil {
		return "", "", providers.NewError(providers.ErrorInvalidRequest, 0, false, nil)
	}
	audience = strings.TrimSpace(audience)
	if audience != "" {
		parsedAudience, audienceErr := httpclient.ParseEndpoint(audience)
		if audienceErr != nil || !strings.EqualFold(parsedEndpoint.Scheme, "https") ||
			!strings.EqualFold(parsedEndpoint.Scheme, parsedAudience.Scheme) ||
			!strings.EqualFold(parsedEndpoint.Host, parsedAudience.Host) ||
			(parsedAudience.Path != "" && parsedAudience.Path != "/") {
			return "", "", providers.NewError(providers.ErrorInvalidRequest, 0, false, nil)
		}
	}
	return endpoint, audience, nil
}

func remoteAuthenticator(audience string) (httpclient.Authenticator, error) {
	if audience == "" {
		return httpclient.NoAuth{}, nil
	}
	if remoteIdentityTokensErr != nil {
		return nil, providers.NewError(providers.ErrorAuthentication, 0, false, nil)
	}
	return httpclient.BearerAuthenticator{Source: remoteIdentityTokens, Audience: audience}, nil
}

func remoteImageMediaType(imagePath string, data []byte) string {
	contentType := http.DetectContentType(data)
	if strings.HasPrefix(contentType, "image/") {
		return contentType
	}
	lower := strings.ToLower(imagePath)
	switch {
	case strings.HasSuffix(lower, ".jp2"), strings.HasSuffix(lower, ".j2k"), strings.HasSuffix(lower, ".jpx"):
		return "image/jp2"
	case strings.HasSuffix(lower, ".tif"), strings.HasSuffix(lower, ".tiff"):
		return "image/tiff"
	case strings.HasSuffix(lower, ".webp"):
		return "image/webp"
	default:
		return "image/jpeg"
	}
}
