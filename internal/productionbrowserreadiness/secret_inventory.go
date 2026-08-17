package productionbrowserreadiness

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"

	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	"google.golang.org/api/option/internaloption"
	secretmanagerapi "google.golang.org/api/secretmanager/v1"
	googlehttptransport "google.golang.org/api/transport/http"
)

const (
	activeSecretVersionFilter          = "state:(ENABLED OR DISABLED)"
	maximumSecretInventoryResponseSize = 64 << 10
)

type secretInventoryAPI interface {
	ListSecretVersions(context.Context, string) (*secretmanagerapi.ListSecretVersionsResponse, error)
}

type googleSecretInventoryAPI struct {
	service *secretmanagerapi.Service
}

func newSecretInventoryAPI(ctx context.Context) (secretInventoryAPI, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	httpClient, _, err := googlehttptransport.NewClient(
		ctx,
		option.WithScopes(secretmanagerapi.CloudPlatformScope),
		option.WithLogger(logger),
		option.WithTelemetryDisabled(),
		// Match the generated Secret Manager client: credential discovery uses
		// the bounded construction context, while token exchange uses each API
		// request's context after construction returns.
		internaloption.EnableNewAuthLibrary(),
	)
	if err != nil {
		return nil, errCommandFailed
	}
	base := httpClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	httpClient.Transport = &secretInventoryResponseTransport{base: base}
	service, err := secretmanagerapi.NewService(
		ctx,
		option.WithHTTPClient(httpClient),
		option.WithLogger(logger),
		option.WithTelemetryDisabled(),
	)
	if err != nil {
		return nil, errCommandFailed
	}
	return &googleSecretInventoryAPI{service: service}, nil
}

type secretInventoryResponseTransport struct {
	base http.RoundTripper
}

func (transport *secretInventoryResponseTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if transport == nil || transport.base == nil {
		return nil, errCommandFailed
	}
	response, err := transport.base.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	if response == nil || response.Body == nil {
		return nil, errCommandFailed
	}
	data, readErr := io.ReadAll(io.LimitReader(response.Body, maximumSecretInventoryResponseSize+1))
	closeErr := response.Body.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(data) > maximumSecretInventoryResponseSize {
		return nil, errOutputLimit
	}
	response.Body = io.NopCloser(bytes.NewReader(data))
	response.ContentLength = int64(len(data))
	return response, nil
}

func (api *googleSecretInventoryAPI) ListSecretVersions(ctx context.Context, parent string) (*secretmanagerapi.ListSecretVersionsResponse, error) {
	if api == nil || api.service == nil {
		return nil, errCommandFailed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	callContext, cancel := context.WithTimeout(ctx, secretCommandTimeout)
	defer cancel()
	return api.service.Projects.Secrets.Versions.List(parent).
		Filter(activeSecretVersionFilter).
		PageSize(maximumSecretVersions + 1).
		Fields("nextPageToken,versions(name,state)").
		Context(callContext).
		Do()
}

func listSecretVersions(ctx context.Context, api secretInventoryAPI, request Request) ([]SecretVersion, error) {
	if api == nil || !projectPattern.MatchString(request.Project) || !secretPattern.MatchString(request.Secret) {
		return nil, errCommandFailed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	parent := "projects/" + request.Project + "/secrets/" + request.Secret
	response, err := api.ListSecretVersions(ctx, parent)
	if err != nil {
		if errors.Is(err, errOutputLimit) || errors.Is(err, errCommandFailed) {
			return nil, errCommandFailed
		}
		if ctx.Err() == nil && transientSecretInventoryError(err) {
			return nil, errSecretInventoryUnavailable
		}
		return nil, errCommandFailed
	}
	return parseSecretInventoryResponse(response, request)
}

func transientSecretInventoryError(err error) bool {
	var apiError *googleapi.Error
	if errors.As(err, &apiError) {
		return apiError.Code == 403 || apiError.Code == 404 || apiError.Code == 408 || apiError.Code == 429 || apiError.Code >= 500 && apiError.Code <= 599
	}
	var urlError *url.Error
	return errors.As(err, &urlError) || errors.Is(err, context.DeadlineExceeded)
}

func parseSecretInventoryResponse(response *secretmanagerapi.ListSecretVersionsResponse, request Request) ([]SecretVersion, error) {
	if response == nil || response.NextPageToken != "" || len(response.Versions) > maximumSecretVersions {
		return nil, errCommandFailed
	}
	versions := make([]SecretVersion, 0, len(response.Versions))
	seen := make(map[string]struct{}, len(response.Versions))
	for _, record := range response.Versions {
		if record == nil {
			return nil, errCommandFailed
		}
		version, err := parseSecretResource(record.Name, request)
		state := SecretState(record.State)
		if err != nil || state != SecretEnabled && state != SecretDisabled {
			return nil, errCommandFailed
		}
		if _, duplicate := seen[version]; duplicate {
			return nil, errCommandFailed
		}
		seen[version] = struct{}{}
		versions = append(versions, SecretVersion{Version: version, State: state})
	}
	return versions, nil
}
