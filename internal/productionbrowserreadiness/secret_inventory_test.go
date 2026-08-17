package productionbrowserreadiness

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	secretmanagerapi "google.golang.org/api/secretmanager/v1"
)

func TestGoogleSecretInventoryAPIUsesExactBoundedRequest(t *testing.T) {
	t.Parallel()
	type requestRecord struct {
		method string
		path   string
		query  url.Values
	}
	requests := make(chan requestRecord, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests <- requestRecord{method: request.Method, path: request.URL.Path, query: request.URL.Query()}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"versions":[{"name":"projects/123456789012/secrets/scribe-browser-session-acde1234/versions/1","state":"ENABLED"},{"name":"projects/123456789012/secrets/scribe-browser-session-acde1234/versions/42","state":"DISABLED"}]}`)
	}))
	t.Cleanup(server.Close)
	service := newSecretInventoryTestService(t, server)
	client := &GCloudClient{secretInventory: &googleSecretInventoryAPI{service: service}}
	request := Request{Project: "scribe-test", Secret: "scribe-browser-session-acde1234"}
	versions, err := client.ListSecretVersions(context.Background(), request)
	if err != nil {
		t.Fatalf("ListSecretVersions() error = %v", err)
	}
	wantVersions := []SecretVersion{{Version: "1", State: SecretEnabled}, {Version: "42", State: SecretDisabled}}
	if !reflect.DeepEqual(versions, wantVersions) {
		t.Fatalf("versions = %#v, want %#v", versions, wantVersions)
	}
	gotRequest := <-requests
	if gotRequest.method != http.MethodGet || gotRequest.path != "/v1/projects/scribe-test/secrets/scribe-browser-session-acde1234/versions" {
		t.Fatalf("request = %s %s", gotRequest.method, gotRequest.path)
	}
	if gotRequest.query.Get("filter") != activeSecretVersionFilter || gotRequest.query.Get("pageSize") != "65" || gotRequest.query.Get("fields") != "nextPageToken,versions(name,state)" {
		t.Fatalf("query = %v", gotRequest.query)
	}
}

func TestParseSecretInventoryResponseRejectsInvalidMetadata(t *testing.T) {
	t.Parallel()
	request := Request{Project: "scribe-test", Secret: "scribe-browser-session-acde1234"}
	resource := func(version string) string {
		return "projects/123456789012/secrets/" + request.Secret + "/versions/" + version
	}
	if versions, err := parseSecretInventoryResponse(&secretmanagerapi.ListSecretVersionsResponse{}, request); err != nil || len(versions) != 0 {
		t.Fatalf("empty canonical inventory = %#v, %v", versions, err)
	}
	overBound := make([]*secretmanagerapi.SecretVersion, maximumSecretVersions+1)
	for index := range overBound {
		overBound[index] = &secretmanagerapi.SecretVersion{Name: resource(fmt.Sprint(index + 1)), State: string(SecretEnabled)}
	}
	tests := map[string]*secretmanagerapi.ListSecretVersionsResponse{
		"nil response":    nil,
		"next page":       {NextPageToken: "more"},
		"over bound":      {Versions: overBound},
		"nil record":      {Versions: []*secretmanagerapi.SecretVersion{nil}},
		"lowercase state": {Versions: []*secretmanagerapi.SecretVersion{{Name: resource("1"), State: "enabled"}}},
		"destroyed state": {Versions: []*secretmanagerapi.SecretVersion{{Name: resource("1"), State: string(SecretDestroyed)}}},
		"wrong secret":    {Versions: []*secretmanagerapi.SecretVersion{{Name: "projects/123456789012/secrets/other/versions/1", State: string(SecretEnabled)}}},
		"wrong project":   {Versions: []*secretmanagerapi.SecretVersion{{Name: "projects/other-project/secrets/" + request.Secret + "/versions/1", State: string(SecretEnabled)}}},
		"invalid version": {Versions: []*secretmanagerapi.SecretVersion{{Name: resource("0"), State: string(SecretEnabled)}}},
		"duplicate": {Versions: []*secretmanagerapi.SecretVersion{
			{Name: resource("1"), State: string(SecretEnabled)},
			{Name: resource("1"), State: string(SecretDisabled)},
		}},
	}
	for name, response := range tests {
		t.Run(name, func(t *testing.T) {
			if versions, err := parseSecretInventoryResponse(response, request); err == nil {
				t.Fatalf("parseSecretInventoryResponse() = %#v, want error", versions)
			}
		})
	}
}

func TestSecretInventoryErrorClassification(t *testing.T) {
	t.Parallel()
	request := Request{Project: "scribe-test", Secret: "scribe-browser-session-acde1234"}
	tests := map[string]struct {
		err       error
		transient bool
	}{
		"forbidden":            {err: &googleapi.Error{Code: 403}, transient: true},
		"not found":            {err: &googleapi.Error{Code: 404}, transient: true},
		"timeout":              {err: &googleapi.Error{Code: 408}, transient: true},
		"rate limit":           {err: &googleapi.Error{Code: 429}, transient: true},
		"server error":         {err: &googleapi.Error{Code: 503}, transient: true},
		"transport":            {err: &url.Error{Op: "GET", URL: "redacted", Err: io.ErrUnexpectedEOF}, transient: true},
		"deadline":             {err: context.DeadlineExceeded, transient: true},
		"bad request":          {err: &googleapi.Error{Code: 400}},
		"unauthenticated":      {err: &googleapi.Error{Code: 401}},
		"conflict":             {err: &googleapi.Error{Code: 409}},
		"local boundary":       {err: &url.Error{Op: "GET", URL: "redacted", Err: errCommandFailed}},
		"malformed successful": {err: errors.New("invalid response")},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			api := &fakeSecretInventoryAPI{err: test.err}
			_, err := listSecretVersions(context.Background(), api, request)
			if errors.Is(err, errSecretInventoryUnavailable) != test.transient {
				t.Fatalf("error = %v, transient = %t", err, test.transient)
			}
			if !test.transient && !errors.Is(err, errCommandFailed) {
				t.Fatalf("error = %v, want command failure", err)
			}
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := listSecretVersions(ctx, &fakeSecretInventoryAPI{err: context.Canceled}, request); errors.Is(err, errSecretInventoryUnavailable) {
		t.Fatalf("canceled caller error = %v, want non-retryable", err)
	}
}

func TestGoogleSecretInventoryMalformedSuccessFailsImmediately(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"versions":[`)
	}))
	t.Cleanup(server.Close)
	service := newSecretInventoryTestService(t, server)
	request := Request{Project: "scribe-test", Secret: "scribe-browser-session-acde1234"}
	_, err := listSecretVersions(context.Background(), &googleSecretInventoryAPI{service: service}, request)
	if !errors.Is(err, errCommandFailed) || errors.Is(err, errSecretInventoryUnavailable) {
		t.Fatalf("error = %v, want immediate malformed-response failure", err)
	}
}

func TestGoogleSecretInventoryOversizedResponseFailsImmediately(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, strings.Repeat("x", maximumSecretInventoryResponseSize+1))
	}))
	t.Cleanup(server.Close)
	service := newSecretInventoryTestService(t, server)
	request := Request{Project: "scribe-test", Secret: "scribe-browser-session-acde1234"}
	_, err := listSecretVersions(context.Background(), &googleSecretInventoryAPI{service: service}, request)
	if !errors.Is(err, errCommandFailed) || errors.Is(err, errSecretInventoryUnavailable) {
		t.Fatalf("error = %v, want immediate output-limit failure", err)
	}
}

func newSecretInventoryTestService(t *testing.T, server *httptest.Server) *secretmanagerapi.Service {
	t.Helper()
	client := server.Client()
	client.Transport = &secretInventoryResponseTransport{base: client.Transport}
	service, err := secretmanagerapi.NewService(context.Background(), option.WithHTTPClient(client))
	if err != nil {
		t.Fatal(err)
	}
	service.BasePath = server.URL + "/"
	return service
}

type fakeSecretInventoryAPI struct {
	response *secretmanagerapi.ListSecretVersionsResponse
	err      error
}

func (api *fakeSecretInventoryAPI) ListSecretVersions(context.Context, string) (*secretmanagerapi.ListSecretVersionsResponse, error) {
	return api.response, api.err
}
