package gcpidentity

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSDKDebugLoggingCannotExposeTokenExchange(t *testing.T) {
	const helperEnvironment = "SCRIBE_GCP_IDENTITY_LOG_HELPER"
	token := testJWT(map[string]any{
		"aud":    "https://service.example",
		"exp":    int64(4102444800),
		"marker": "sdk-debug-token-marker",
	})
	if os.Getenv(helperEnvironment) == "1" {
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, map[string]any{"id_token": token}), nil
		})}
		source, err := New(Options{
			CredentialsFile: writeServiceAccount(t, googleTokenEndpoint),
			HTTPClient:      client,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := source.Token(context.Background(), "https://service.example"); err != nil {
			t.Fatal(err)
		}
		return
	}

	command := exec.Command(os.Args[0], "-test.run=^TestSDKDebugLoggingCannotExposeTokenExchange$")
	commandEnvironment := make([]string, 0, len(os.Environ())+2)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, helperEnvironment+"=") &&
			!strings.HasPrefix(value, "GOOGLE_SDK_GO_LOGGING_LEVEL=") {
			commandEnvironment = append(commandEnvironment, value)
		}
	}
	command.Env = append(
		commandEnvironment,
		helperEnvironment+"=1",
		"GOOGLE_SDK_GO_LOGGING_LEVEL=debug",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("logging helper failed: %v: %s", err, output)
	}
	for _, secretMarker := range []string{token, "assertion=", "grant_type="} {
		if strings.Contains(string(output), secretMarker) {
			t.Fatalf("SDK debug logging disclosed token-exchange material matching %q", secretMarker)
		}
	}
}

func TestCredentialFileBindsAndCachesProvidersPerAudience(t *testing.T) {
	var tokenCalls atomic.Int32
	var metadataCalls atomic.Int32
	seenAudiences := make(chan string, 2)
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host == "metadata.google.internal" {
			metadataCalls.Add(1)
			return nil, errors.New("metadata must not be called")
		}
		if request.URL.String() != googleTokenEndpoint {
			return nil, fmt.Errorf("unexpected token endpoint")
		}
		tokenCalls.Add(1)
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		form, err := url.ParseQuery(string(body))
		if err != nil {
			return nil, err
		}
		var claims map[string]any
		if err := decodeJWTPayload(form.Get("assertion"), &claims); err != nil {
			return nil, err
		}
		if claims["aud"] != googleTokenEndpoint ||
			claims["iss"] != "scribe@test-project.iam.gserviceaccount.com" {
			return nil, fmt.Errorf("unexpected assertion identity")
		}
		if scope, present := claims["scope"]; present && scope != "" {
			return nil, fmt.Errorf("unexpected assertion scope")
		}
		audience, _ := claims["target_audience"].(string)
		seenAudiences <- audience
		token := testJWT(map[string]any{
			"aud": audience,
			"exp": time.Now().Add(time.Hour).Unix(),
		})
		return jsonResponse(http.StatusOK, map[string]any{"id_token": token}), nil
	})}
	credentialsFile := writeServiceAccount(t, googleTokenEndpoint)
	source, err := New(Options{
		CredentialsFile:  credentialsFile,
		HTTPClient:       client,
		MetadataEndpoint: "http://metadata.google.internal/identity",
	})
	if err != nil {
		t.Fatal(err)
	}

	first, err := source.Token(context.Background(), "https://one.example")
	if err != nil {
		t.Fatal(err)
	}
	cached, err := source.Token(context.Background(), "https://one.example")
	if err != nil {
		t.Fatal(err)
	}
	second, err := source.Token(context.Background(), "https://two.example")
	if err != nil {
		t.Fatal(err)
	}
	if first != cached || first == second {
		t.Fatalf("unexpected cached tokens: first=%q cached=%q second=%q", first, cached, second)
	}
	if got := tokenCalls.Load(); got != 2 {
		t.Fatalf("token endpoint calls = %d, want 2", got)
	}
	if got := metadataCalls.Load(); got != 0 {
		t.Fatalf("metadata calls = %d, want 0", got)
	}
	close(seenAudiences)
	var got []string
	for audience := range seenAudiences {
		got = append(got, audience)
	}
	if len(got) != 2 || got[0] != "https://one.example" || got[1] != "https://two.example" {
		t.Fatalf("bound audiences = %#v", got)
	}
}

func TestImpersonatedCredentialBindsAndCachesProvidersPerAudience(t *testing.T) {
	const target = "scribe-dev-external@test-project.iam.gserviceaccount.com"
	var oauthCalls atomic.Int32
	var iamCalls atomic.Int32
	seenAudiences := make(chan string, 2)
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var response *http.Response
		switch request.URL.String() {
		case googleTokenEndpoint:
			oauthCalls.Add(1)
			response = jsonResponse(http.StatusOK, map[string]any{
				"access_token": "source-access-token",
				"expires_in":   3600,
				"token_type":   "Bearer",
			})
		case "https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/" + target + ":generateIdToken":
			iamCalls.Add(1)
			if got := request.Header.Get("Authorization"); got != "Bearer source-access-token" {
				return nil, fmt.Errorf("unexpected IAM authorization")
			}
			var body struct {
				Audience     string `json:"audience"`
				IncludeEmail bool   `json:"includeEmail"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				return nil, err
			}
			if !body.IncludeEmail {
				return nil, fmt.Errorf("IAM token must include the target email")
			}
			seenAudiences <- body.Audience
			response = jsonResponse(http.StatusOK, map[string]any{
				"token": testJWT(map[string]any{
					"aud": body.Audience,
					"exp": time.Now().Add(time.Hour).Unix(),
				}),
			})
		default:
			return nil, fmt.Errorf("unexpected identity endpoint")
		}
		response.Request = request
		return response, nil
	})}
	source, err := New(Options{
		CredentialsFile: writeImpersonatedServiceAccount(t, target),
		HTTPClient:      client,
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := source.Token(context.Background(), "https://one.example")
	if err != nil {
		t.Fatal(err)
	}
	cached, err := source.Token(context.Background(), "https://one.example")
	if err != nil {
		t.Fatal(err)
	}
	second, err := source.Token(context.Background(), "https://two.example")
	if err != nil {
		t.Fatal(err)
	}
	if first != cached || first == second {
		t.Fatalf("unexpected cached tokens: first=%q cached=%q second=%q", first, cached, second)
	}
	if got := oauthCalls.Load(); got != 1 {
		t.Fatalf("OAuth refresh calls = %d, want 1", got)
	}
	if got := iamCalls.Load(); got != 2 {
		t.Fatalf("IAM ID-token calls = %d, want 2", got)
	}
	close(seenAudiences)
	var got []string
	for audience := range seenAudiences {
		got = append(got, audience)
	}
	if len(got) != 2 || got[0] != "https://one.example" || got[1] != "https://two.example" {
		t.Fatalf("bound audiences = %#v", got)
	}
}

func TestCredentialFileRejectsRedirectAndRedactsProviderFailure(t *testing.T) {
	var destinationCalls atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host == "redirect.example" {
			destinationCalls.Add(1)
			return jsonResponse(http.StatusOK, map[string]any{}), nil
		}
		return &http.Response{
			StatusCode: http.StatusTemporaryRedirect,
			Header:     http.Header{"Location": []string{"https://redirect.example/private-token"}},
			Body:       io.NopCloser(strings.NewReader("credential-secret")),
			Request:    request,
		}, nil
	})}
	source, err := New(Options{
		CredentialsFile: writeServiceAccount(t, googleTokenEndpoint),
		HTTPClient:      client,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.Token(context.Background(), "https://service.example")
	if !errors.Is(err, ErrTokenUnavailable) {
		t.Fatalf("Token() error = %v, want ErrTokenUnavailable", err)
	}
	if destinationCalls.Load() != 0 {
		t.Fatal("token request followed a redirect")
	}
	if strings.Contains(err.Error(), "redirect.example") || strings.Contains(err.Error(), "credential-secret") {
		t.Fatalf("token error disclosed provider detail: %q", err)
	}
}

func TestCredentialFileTokenExchangeIsTimeoutBounded(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}
	source, err := New(Options{
		CredentialsFile: writeServiceAccount(t, googleTokenEndpoint),
		HTTPClient:      client,
		Timeout:         20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, err = source.Token(context.Background(), "https://service.example")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Token() error = %v, want context deadline", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("bounded token request took %s", elapsed)
	}
}

func TestCredentialFileTokenResponseIsByteBounded(t *testing.T) {
	token := testJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	body := &trackingReadCloser{
		reader: strings.NewReader(
			fmt.Sprintf(`{"id_token":%q}`, token) +
				strings.Repeat(" ", maxTokenResponseBytes),
		),
	}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       body,
			Request:    request,
		}, nil
	})}
	source, err := New(Options{
		CredentialsFile: writeServiceAccount(t, googleTokenEndpoint),
		HTTPClient:      client,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.Token(context.Background(), "https://service.example")
	if !errors.Is(err, ErrTokenUnavailable) {
		t.Fatalf("Token() error = %v, want ErrTokenUnavailable", err)
	}
	if body.read > maxTokenResponseBytes+1 {
		t.Fatalf("token response bytes read = %d, limit = %d", body.read, maxTokenResponseBytes+1)
	}
}

func TestNewRejectsInvalidCredentialConfiguration(t *testing.T) {
	temporary := t.TempDir()
	write := func(name string, value any) string {
		t.Helper()
		path := filepath.Join(temporary, name)
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	secret := "private-configuration-value"
	tests := map[string]string{
		"whitespace path": " " + filepath.Join(temporary, "missing"),
		"missing file":    filepath.Join(temporary, secret),
		"malformed JSON":  write("malformed.json", secret),
		"wrong type": write("external.json", map[string]any{
			"type":      "external_account",
			"token_uri": googleTokenEndpoint,
			"secret":    secret,
		}),
		"nonstandard token endpoint": write("endpoint.json", map[string]any{
			"type":      "service_account",
			"token_uri": "https://attacker.example/" + secret,
		}),
		"untrusted impersonation endpoint": write("impersonation-endpoint.json", map[string]any{
			"type":                              "impersonated_service_account",
			"service_account_impersonation_url": "https://attacker.example/" + secret,
			"source_credentials": map[string]any{
				"type":          "authorized_user",
				"client_id":     "client",
				"client_secret": "client-secret",
				"refresh_token": "refresh-token",
			},
		}),
		"nested service account source": write("impersonation-source.json", map[string]any{
			"type":                              "impersonated_service_account",
			"service_account_impersonation_url": "https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/scribe-dev-external@test-project.iam.gserviceaccount.com:generateAccessToken",
			"source_credentials": map[string]any{
				"type":        "service_account",
				"private_key": secret,
			},
		}),
		"impersonation delegate": write("impersonation-delegate.json", map[string]any{
			"type":                              "impersonated_service_account",
			"service_account_impersonation_url": "https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/scribe-dev-external@test-project.iam.gserviceaccount.com:generateAccessToken",
			"delegates":                         []string{"delegate@test-project.iam.gserviceaccount.com"},
			"source_credentials": map[string]any{
				"type":          "authorized_user",
				"client_id":     "client",
				"client_secret": "client-secret",
				"refresh_token": "refresh-token",
			},
		}),
		"source endpoint injection": write("impersonation-source-endpoint.json", map[string]any{
			"type":                              "impersonated_service_account",
			"service_account_impersonation_url": "https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/scribe-dev-external@test-project.iam.gserviceaccount.com:generateAccessToken",
			"source_credentials": map[string]any{
				"type":          "authorized_user",
				"client_id":     "client",
				"client_secret": "client-secret",
				"refresh_token": "refresh-token",
				"token_url":     "https://attacker.example/" + secret,
			},
		}),
	}
	for name, path := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := New(Options{CredentialsFile: path})
			if !errors.Is(err, ErrInvalidConfiguration) {
				t.Fatalf("New() error = %v, want ErrInvalidConfiguration", err)
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), path) {
				t.Fatalf("configuration error disclosed input: %q", err)
			}
		})
	}

	oversized := filepath.Join(temporary, "oversized.json")
	if err := os.WriteFile(oversized, []byte(strings.Repeat("x", maxCredentialBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Options{CredentialsFile: oversized}); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("oversized New() error = %v, want ErrInvalidConfiguration", err)
	}
}

func TestMetadataFallbackOnlyWhenCredentialFileIsAbsent(t *testing.T) {
	var calls atomic.Int32
	token := testJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.Header.Get("Metadata-Flavor") != "Google" ||
			request.URL.Query().Get("audience") != "https://service.example" ||
			request.URL.Query().Get("format") != "full" {
			t.Errorf("unexpected metadata request: %s headers=%v", request.URL, request.Header)
		}
		response.Header().Set("Metadata-Flavor", "Google")
		_, _ = response.Write([]byte(token))
	}))
	defer server.Close()
	source, err := New(Options{MetadataEndpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		got, tokenErr := source.Token(context.Background(), "https://service.example")
		if tokenErr != nil {
			t.Fatal(tokenErr)
		}
		if got != token {
			t.Fatalf("Token() = %q, want metadata token", got)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("metadata calls = %d, want 1", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type trackingReadCloser struct {
	reader io.Reader
	read   int
}

func (r *trackingReadCloser) Read(buffer []byte) (int, error) {
	count, err := r.reader.Read(buffer)
	r.read += count
	return count, err
}

func (*trackingReadCloser) Close() error {
	return nil
}

func writeServiceAccount(t *testing.T, tokenURI string) string {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privateKeyBytes, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{
		"type":           "service_account",
		"project_id":     "test-project",
		"private_key_id": "test-key",
		"private_key": string(pem.EncodeToMemory(&pem.Block{
			Type:  "PRIVATE KEY",
			Bytes: privateKeyBytes,
		})),
		"client_email": "scribe@test-project.iam.gserviceaccount.com",
		"token_uri":    tokenURI,
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "service-account.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeImpersonatedServiceAccount(t *testing.T, target string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"type": "impersonated_service_account",
		"service_account_impersonation_url": "https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/" +
			target + ":generateAccessToken",
		"source_credentials": map[string]any{
			"type":             "authorized_user",
			"client_id":        "test-client-id",
			"client_secret":    "test-client-secret",
			"refresh_token":    "test-refresh-token",
			"quota_project_id": "test-project",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "impersonated-service-account.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func testJWT(claims map[string]any) string {
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	payload, _ := json.Marshal(claims)
	return base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

func decodeJWTPayload(token string, target any) error {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return fmt.Errorf("unexpected assertion")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return err
	}
	return json.Unmarshal(payload, target)
}

func jsonResponse(status int, value any) *http.Response {
	raw, _ := json.Marshal(value)
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(string(raw))),
	}
}
