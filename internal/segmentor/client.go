package segmentor

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/lehigh-university-libraries/htr/pkg/httpclient"
	"github.com/lehigh-university-libraries/htr/pkg/providers"
	"github.com/lehigh-university-libraries/htr/pkg/remoteocr"
	"github.com/lehigh-university-libraries/scribe/internal/gcpidentity"
	"github.com/lehigh-university-libraries/scribe/internal/iiif"
	"github.com/lehigh-university-libraries/scribe/internal/imageservice"
	"github.com/lehigh-university-libraries/scribe/internal/safefile"
	"github.com/lehigh-university-libraries/scribe/internal/uploadlimits"
	"github.com/lehigh-university-libraries/scribe/internal/worddetection"
)

const maxSegmentorResponseBytes int64 = 16 << 20

// InferenceRequestTimeout is the end-to-end deadline shared by Scribe callers
// and deployment readiness. The server keeps bounded cleanup and response
// margins above this budget while remaining below the configured Cloud Run
// service request timeout.
const InferenceRequestTimeout = 240 * time.Second

const (
	// InferenceHandlerTimeout is the server-side fallback for clients whose
	// disconnect is not propagated promptly through the Cloud Run proxy.
	InferenceHandlerTimeout = InferenceRequestTimeout + 15*time.Second
	// InferenceServerWriteTimeout leaves the handler time to publish its
	// redacted timeout response before the HTTP server closes the connection.
	InferenceServerWriteTimeout = InferenceHandlerTimeout + 15*time.Second
)

// Client prepares Scribe-owned image bytes and delegates the remote protocol
// to HTR's generic OCR client.
type Client struct {
	remote *remoteocr.Client
	images tripletImageClient
}

type tripletImageClient interface {
	Enabled() bool
	FullJPEG(context.Context, string) ([]byte, error)
	Normalize(context.Context, []byte, string) ([]byte, error)
}

// NewClientForEndpoint constructs a segmentor client from an endpoint already
// resolved by the trusted provider registry. Request data must never be passed
// as either argument.
func NewClientForEndpoint(baseURL, audience string) (*Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("segmentation endpoint is not configured")
	}
	authenticator, err := segmentorAuthenticator(baseURL, audience)
	if err != nil {
		return nil, err
	}
	remote, err := remoteocr.NewClient(remoteocr.Options{
		Endpoint:         baseURL,
		Authenticator:    authenticator,
		Timeout:          InferenceRequestTimeout,
		MaxImageBytes:    uploadlimits.MaxImageBytes,
		MaxRequestBytes:  uploadlimits.MaxMultipartBodyBytes,
		MaxResponseBytes: maxSegmentorResponseBytes,
		MaxBoxes:         iiif.MaxAnnotationsPerPage / 2,
	})
	if err != nil {
		return nil, err
	}
	return &Client{remote: remote, images: imageservice.New()}, nil
}

// Enabled reports whether the HTR remote client is ready.
func (c *Client) Enabled() bool { return c != nil && c.remote != nil }

// Name implements providers.Client for registered remote transcription models.
func (c *Client) Name() string { return "remoteocr" }

// Extract implements providers.Client by delegating to HTR's generic remote
// transcription operation. Scribe's provider registry binds the approved
// model before exposing this client to the processing pipeline.
func (c *Client) Extract(ctx context.Context, request providers.Request) (providers.Result, error) {
	if !c.Enabled() {
		return providers.Result{}, providers.NewError(providers.ErrorInvalidRequest, 0, false, nil)
	}
	image, err := c.prepareProviderImage(ctx, request.Image)
	if err != nil {
		return providers.Result{}, err
	}
	result, err := c.remote.Transcribe(ctx, image, request.Model)
	if err != nil {
		return providers.Result{}, err
	}
	return providers.Result{
		Text:           result.Text,
		EffectiveModel: result.EffectiveModel,
	}, nil
}

func (c *Client) prepareProviderImage(ctx context.Context, image providers.Image) (providers.Image, error) {
	if !needsTripletNormalize(image.Filename) {
		return image, nil
	}
	if c.images == nil || !c.images.Enabled() {
		return providers.Image{}, providers.NewError(providers.ErrorInvalidRequest, 0, false, nil)
	}
	data, err := c.images.Normalize(ctx, image.Data, image.MediaType)
	if err != nil {
		return providers.Image{}, providers.ErrorForRequest(ctx, err)
	}
	return providers.Image{Data: data, MediaType: "image/jpeg", Filename: "image.jpg"}, nil
}

// DetectWords sends prepared image bytes to HTR's segmentation operation.
func (c *Client) DetectWords(ctx context.Context, imagePath, model string) ([]worddetection.WordBox, string, error) {
	image, err := c.prepareImage(ctx, imagePath)
	if err != nil {
		return nil, "", err
	}
	result, err := c.remote.Segment(ctx, image, strings.TrimSpace(model))
	if err != nil {
		return nil, "", err
	}
	words := make([]worddetection.WordBox, len(result.Words))
	for index, box := range result.Words {
		words[index] = worddetection.WordBox{
			X: box.X, Y: box.Y, Width: box.Width, Height: box.Height,
			Text: box.Text, Confidence: box.Confidence,
		}
	}
	return words, result.Provider, nil
}

// Transcribe sends prepared image bytes to HTR's transcription operation.
func (c *Client) Transcribe(ctx context.Context, imagePath, model string) (string, string, error) {
	image, err := c.prepareImage(ctx, imagePath)
	if err != nil {
		return "", "", err
	}
	result, err := c.remote.Transcribe(ctx, image, strings.TrimSpace(model))
	if err != nil {
		return "", "", err
	}
	return strings.TrimSpace(result.Text), result.EffectiveModel, nil
}

func segmentorAuthenticator(endpointRaw, audienceRaw string) (httpclient.Authenticator, error) {
	audienceRaw = strings.TrimSpace(audienceRaw)
	if audienceRaw == "" {
		return httpclient.NoAuth{}, nil
	}
	endpoint, endpointErr := httpclient.ParseEndpoint(endpointRaw)
	audience, audienceErr := httpclient.ParseEndpoint(audienceRaw)
	if endpointErr != nil || audienceErr != nil || !strings.EqualFold(endpoint.Scheme, "https") ||
		!strings.EqualFold(endpoint.Scheme, audience.Scheme) || !strings.EqualFold(endpoint.Host, audience.Host) ||
		audience.Path != "" {
		return nil, providers.NewError(providers.ErrorInvalidRequest, 0, false, nil)
	}
	identityTokens, err := gcpidentity.Default()
	if err != nil {
		return nil, providers.NewError(providers.ErrorAuthentication, 0, false, nil)
	}
	return httpclient.BearerAuthenticator{Source: identityTokens, Audience: audience.String()}, nil
}

func (c *Client) prepareImage(ctx context.Context, imagePath string) (providers.Image, error) {
	if !c.Enabled() {
		return providers.Image{}, providers.NewError(providers.ErrorInvalidRequest, 0, false, nil)
	}
	if needsTripletNormalize(imagePath) {
		data, err := c.fetchTripletJPEG(ctx, imagePath)
		if err != nil {
			return providers.Image{}, err
		}
		return providers.Image{Data: data, MediaType: "image/jpeg"}, nil
	}
	data, err := safefile.ReadFileLimit(imagePath, uploadlimits.MaxImageBytes)
	if err != nil {
		return providers.Image{}, err
	}
	return providers.Image{Data: data, MediaType: imageMediaType(imagePath, data)}, nil
}

func imageMediaType(imagePath string, data []byte) string {
	detected := http.DetectContentType(data)
	if strings.HasPrefix(detected, "image/") {
		return detected
	}
	switch strings.ToLower(filepath.Ext(imagePath)) {
	case ".jp2", ".j2k", ".jpx":
		return "image/jp2"
	case ".tif", ".tiff":
		return "image/tiff"
	case ".webp":
		return "image/webp"
	default:
		return "image/jpeg"
	}
}

func needsTripletNormalize(imagePath string) bool {
	switch strings.ToLower(filepath.Ext(imagePath)) {
	case ".tif", ".tiff", ".jp2", ".j2k", ".jpx", ".webp":
		return true
	}
	return false
}

func (c *Client) fetchTripletJPEG(ctx context.Context, imagePath string) ([]byte, error) {
	if c == nil || c.images == nil || !c.images.Enabled() {
		return nil, fmt.Errorf("triplet image client is not configured")
	}
	return c.images.FullJPEG(ctx, imagePath)
}
