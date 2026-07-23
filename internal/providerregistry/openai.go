package providerregistry

import (
	"context"
	"strings"

	"github.com/lehigh-university-libraries/htr/pkg/gemini"
	"github.com/lehigh-university-libraries/htr/pkg/openai"
	"github.com/lehigh-university-libraries/htr/pkg/providers"
	"github.com/lehigh-university-libraries/scribe/internal/uploadlimits"
)

const (
	providerAPIKeyField = "api_key"
	openAIAPIKeyField   = providerAPIKeyField
)

func newOpenAIClient(descriptor Provider, model string) (providers.Client, error) {
	endpoint, err := registeredProviderEndpoint(descriptor, model, EndpointVendor)
	if err != nil {
		return nil, err
	}
	client, err := openai.NewClient(openai.Options{
		HTTPClient:       descriptor.vendorHTTPClient,
		Endpoint:         endpoint.URL,
		APIKey:           providerCredentialSource(descriptor),
		Timeout:          descriptor.Limits.Timeout,
		MaxImageBytes:    uploadlimits.MaxImageBytes,
		MaxResponseBytes: descriptor.Limits.MaxResponseBytes,
	})
	return bindRegisteredModel(client, model, err)
}

func newGeminiClient(descriptor Provider, model string) (providers.Client, error) {
	endpoint, err := registeredProviderEndpoint(descriptor, model, EndpointVendor)
	if err != nil {
		return nil, err
	}
	client, err := gemini.NewClient(gemini.Options{
		HTTPClient:              descriptor.vendorHTTPClient,
		Endpoint:                endpoint.URL,
		APIKey:                  providerCredentialSource(descriptor),
		Timeout:                 descriptor.Limits.Timeout,
		MaxImageBytes:           uploadlimits.MaxImageBytes,
		MaxResponseBytes:        descriptor.Limits.MaxResponseBytes,
		MediaResolutionFallback: true,
	})
	return bindRegisteredModel(client, model, err)
}

type registeredModelClient struct {
	client providers.Client
	model  string
}

func bindRegisteredModel(client providers.Client, model string, err error) (providers.Client, error) {
	if err != nil {
		return nil, err
	}
	return registeredModelClient{client: client, model: model}, nil
}

func (c registeredModelClient) Name() string { return c.client.Name() }

func (c registeredModelClient) Extract(ctx context.Context, request providers.Request) (providers.Result, error) {
	if request.Model != c.model {
		return providers.Result{}, providers.NewError(providers.ErrorInvalidRequest, 0, false, nil)
	}
	return c.client.Extract(ctx, request)
}

func providerCredentialSource(descriptor Provider) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		credential := descriptor.Credential(ctx, providerAPIKeyField)
		if strings.TrimSpace(credential) == "" {
			return "", providers.NewError(providers.ErrorAuthentication, 0, false, nil)
		}
		return credential, nil
	}
}

func registeredProviderEndpoint(descriptor Provider, model string, mode EndpointMode) (EndpointPolicy, error) {
	endpoint := descriptor.EndpointForModel(model)
	if endpoint.Mode != mode || !endpoint.ServerOwned || strings.TrimSpace(endpoint.URL) == "" {
		return EndpointPolicy{}, providers.NewError(providers.ErrorInvalidRequest, 0, false, nil)
	}
	return endpoint, nil
}
