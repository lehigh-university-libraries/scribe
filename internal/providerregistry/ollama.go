package providerregistry

import (
	"net/url"
	"strings"

	"github.com/lehigh-university-libraries/htr/pkg/auth/gcpidtoken"
	"github.com/lehigh-university-libraries/htr/pkg/httpclient"
	"github.com/lehigh-university-libraries/htr/pkg/ollama"
	"github.com/lehigh-university-libraries/htr/pkg/providers"
	"github.com/lehigh-university-libraries/scribe/internal/uploadlimits"
)

var providerIdentityTokens, providerIdentityTokensErr = gcpidtoken.New(gcpidtoken.Options{})

func newOllamaClient(descriptor Provider, model string) (providers.Client, error) {
	endpoint, err := registeredProviderEndpoint(descriptor, model, EndpointExactOrigin)
	if err != nil {
		return nil, err
	}
	audience, err := validateOllamaAudience(endpoint.URL, endpoint.Audience)
	if err != nil {
		return nil, err
	}
	var authenticator httpclient.Authenticator = httpclient.NoAuth{}
	if audience != "" {
		if providerIdentityTokensErr != nil {
			return nil, providers.NewError(providers.ErrorAuthentication, 0, false, nil)
		}
		authenticator = httpclient.BearerAuthenticator{Source: providerIdentityTokens, Audience: audience}
	}
	client, err := ollama.NewClient(ollama.Options{
		Endpoint:         endpoint.URL,
		Authenticator:    authenticator,
		Timeout:          descriptor.Limits.Timeout,
		MaxImageBytes:    uploadlimits.MaxImageBytes,
		MaxResponseBytes: descriptor.Limits.MaxResponseBytes,
	})
	return bindRegisteredModel(client, model, err)
}

func validateOllamaAudience(endpointRaw, audienceRaw string) (string, error) {
	audienceRaw = strings.TrimSpace(audienceRaw)
	if audienceRaw == "" {
		return "", nil
	}
	endpoint, endpointErr := url.Parse(strings.TrimSpace(endpointRaw))
	audience, audienceErr := url.Parse(audienceRaw)
	if endpointErr != nil || audienceErr != nil || endpoint == nil || audience == nil ||
		endpoint.Host == "" || audience.Host == "" || !strings.EqualFold(audience.Scheme, "https") ||
		(audience.Path != "" && audience.Path != "/") || audience.Opaque != "" || audience.User != nil ||
		audience.RawQuery != "" || audience.Fragment != "" || audience.RawPath != "" ||
		!strings.EqualFold(endpoint.Scheme, audience.Scheme) || !strings.EqualFold(endpoint.Host, audience.Host) {
		return "", providers.NewError(providers.ErrorInvalidRequest, 0, false, nil)
	}
	return strings.TrimRight(audience.String(), "/"), nil
}
