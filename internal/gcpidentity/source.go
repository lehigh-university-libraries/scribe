// Package gcpidentity provides process-wide, audience-bound Google identity
// tokens for calls to private Cloud Run services.
package gcpidentity

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"

	"cloud.google.com/go/auth"
	"cloud.google.com/go/auth/credentials"
	"cloud.google.com/go/auth/credentials/idtoken"
	"github.com/lehigh-university-libraries/htr/pkg/auth/gcpidtoken"
	"github.com/lehigh-university-libraries/htr/pkg/httpclient"
)

const (
	credentialsEnvironment = "GOOGLE_APPLICATION_CREDENTIALS"
	googleTokenEndpoint    = "https://oauth2.googleapis.com/token"
	validationAudience     = "https://scribe.invalid"
	defaultTimeout         = 10 * time.Second
	defaultMaxAudiences    = 128
	maxCredentialBytes     = 1 << 20
	maxAudienceBytes       = 4 << 10
	maxTokenBytes          = 16 << 10
	maxTokenResponseBytes  = 64 << 10
)

var (
	// ErrInvalidConfiguration reports an invalid identity configuration without
	// disclosing a credential path, credential content, or provider error.
	ErrInvalidConfiguration = errors.New("invalid google identity configuration")
	// ErrInvalidAudience reports an empty or unsafe identity-token audience.
	ErrInvalidAudience = errors.New("invalid google identity audience")
	// ErrTokenUnavailable reports a redacted token acquisition failure.
	ErrTokenUnavailable = errors.New("google identity token unavailable")
	errResponseTooLarge = errors.New("google identity response too large")
	silentSDKLogger     = slog.New(slog.NewTextHandler(io.Discard, nil))
)

// Options configures a Source. An empty CredentialsFile selects the bounded
// metadata source; callers must not use an invalid credential file as a signal
// to fall back to metadata.
type Options struct {
	CredentialsFile  string
	HTTPClient       *http.Client
	MetadataEndpoint string
	Timeout          time.Duration
	MaxAudiences     int
}

// Source caches one Google token provider for each exact audience. A source
// uses either the configured service-account file or metadata, never both.
type Source struct {
	metadata     *gcpidtoken.Source
	newProvider  func(string) (auth.TokenProvider, error)
	maxAudiences int

	mu        sync.Mutex
	providers map[string]auth.TokenProvider
}

type serviceAccountConfig struct {
	clientEmail  string
	privateKey   string
	privateKeyID string
}

var (
	defaultOnce   sync.Once
	defaultSource *Source
	defaultErr    error
)

// Default returns the process-wide source selected from
// GOOGLE_APPLICATION_CREDENTIALS. The environment is read exactly once so all
// consumers share the same identity and per-audience cache.
func Default() (*Source, error) {
	defaultOnce.Do(func() {
		credentialsFile, configured := os.LookupEnv(credentialsEnvironment)
		if configured && credentialsFile == "" {
			defaultErr = ErrInvalidConfiguration
			return
		}
		defaultSource, defaultErr = New(Options{CredentialsFile: credentialsFile})
	})
	return defaultSource, defaultErr
}

// New constructs a source without performing network I/O.
func New(options Options) (*Source, error) {
	if options.Timeout < 0 || options.MaxAudiences < 0 {
		return nil, ErrInvalidConfiguration
	}
	timeout := options.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	maxAudiences := options.MaxAudiences
	if maxAudiences == 0 {
		maxAudiences = defaultMaxAudiences
	}
	client := boundedHTTPClient(options.HTTPClient, timeout)

	source := &Source{
		maxAudiences: maxAudiences,
		providers:    make(map[string]auth.TokenProvider),
	}
	if options.CredentialsFile == "" {
		metadata, err := gcpidtoken.New(gcpidtoken.Options{
			HTTPClient:   client,
			Endpoint:     options.MetadataEndpoint,
			Timeout:      timeout,
			MaxAudiences: maxAudiences,
		})
		if err != nil {
			return nil, ErrInvalidConfiguration
		}
		source.metadata = metadata
		return source, nil
	}

	config, credentialJSON, err := readCredentialJSON(options.CredentialsFile)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	defer clear(credentialJSON)
	if _, err := idtoken.NewCredentialsFromJSON(
		credentials.ServiceAccount,
		credentialJSON,
		&idtoken.Options{
			Audience: validationAudience,
			Client:   client,
			Logger:   silentSDKLogger,
		},
	); err != nil {
		return nil, ErrInvalidConfiguration
	}
	source.newProvider = func(audience string) (auth.TokenProvider, error) {
		// idtoken.NewCredentialsFromJSON validates its Client option but the
		// service-account 2LO path does not pass that client to the token
		// exchange. Build the equivalent provider explicitly so redirects and
		// total request duration remain bounded.
		provider, err := auth.New2LOTokenProvider(&auth.Options2LO{
			Email:        config.clientEmail,
			PrivateKey:   []byte(config.privateKey),
			PrivateKeyID: config.privateKeyID,
			TokenURL:     googleTokenEndpoint,
			PrivateClaims: map[string]any{
				"target_audience": audience,
			},
			Client:     client,
			UseIDToken: true,
			Logger:     silentSDKLogger,
		})
		if err != nil {
			return nil, ErrInvalidConfiguration
		}
		return auth.NewCachedTokenProvider(provider, nil), nil
	}
	if _, err := source.newProvider(validationAudience); err != nil {
		return nil, ErrInvalidConfiguration
	}
	return source, nil
}

// Token returns an identity token bound to the exact audience.
func (s *Source) Token(ctx context.Context, audience string) (string, error) {
	if err := validateAudience(audience); err != nil {
		return "", err
	}
	if ctx == nil {
		return "", ErrTokenUnavailable
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if s == nil {
		return "", ErrTokenUnavailable
	}
	if s.metadata != nil {
		token, err := s.metadata.Token(ctx, audience)
		return checkedToken(ctx, token, err)
	}

	provider, err := s.provider(audience)
	if err != nil {
		return "", err
	}
	token, err := provider.Token(ctx)
	if err != nil {
		return "", redactedTokenError(ctx, err)
	}
	if token == nil {
		return "", ErrTokenUnavailable
	}
	return checkedToken(ctx, token.Value, nil)
}

func (s *Source) provider(audience string) (auth.TokenProvider, error) {
	if s.newProvider == nil {
		return nil, ErrTokenUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if provider := s.providers[audience]; provider != nil {
		return provider, nil
	}
	if len(s.providers) >= s.maxAudiences {
		return nil, ErrTokenUnavailable
	}
	provider, err := s.newProvider(audience)
	if err != nil {
		return nil, ErrTokenUnavailable
	}
	s.providers[audience] = provider
	return provider, nil
}

func readCredentialJSON(path string) (serviceAccountConfig, []byte, error) {
	if strings.TrimSpace(path) != path || path == "" ||
		strings.IndexFunc(path, unicode.IsControl) >= 0 {
		return serviceAccountConfig{}, nil, ErrInvalidConfiguration
	}
	// #nosec G304 -- the path is trusted operator configuration; the file is
	// bounded below and accepted only as an exact service-account credential.
	file, err := os.Open(path)
	if err != nil {
		return serviceAccountConfig{}, nil, ErrInvalidConfiguration
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxCredentialBytes {
		return serviceAccountConfig{}, nil, ErrInvalidConfiguration
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxCredentialBytes+1))
	if err != nil || len(raw) == 0 || len(raw) > maxCredentialBytes {
		return serviceAccountConfig{}, nil, ErrInvalidConfiguration
	}
	var descriptor struct {
		Type         string `json:"type"`
		TokenURI     string `json:"token_uri"`
		ClientEmail  string `json:"client_email"`
		PrivateKey   string `json:"private_key"`
		PrivateKeyID string `json:"private_key_id"`
	}
	if err := json.Unmarshal(raw, &descriptor); err != nil ||
		descriptor.Type != string(credentials.ServiceAccount) ||
		descriptor.TokenURI != googleTokenEndpoint {
		return serviceAccountConfig{}, nil, ErrInvalidConfiguration
	}
	return serviceAccountConfig{
		clientEmail:  descriptor.ClientEmail,
		privateKey:   descriptor.PrivateKey,
		privateKeyID: descriptor.PrivateKeyID,
	}, raw, nil
}

func validateAudience(audience string) error {
	if audience == "" || len(audience) > maxAudienceBytes ||
		strings.TrimSpace(audience) != audience ||
		strings.IndexFunc(audience, unicode.IsControl) >= 0 {
		return ErrInvalidAudience
	}
	return nil
}

func checkedToken(ctx context.Context, token string, err error) (string, error) {
	if err != nil {
		return "", redactedTokenError(ctx, err)
	}
	if token == "" || len(token) > maxTokenBytes ||
		strings.TrimSpace(token) != token ||
		strings.IndexFunc(token, unicode.IsControl) >= 0 {
		return "", ErrTokenUnavailable
	}
	return token, nil
}

func redactedTokenError(ctx context.Context, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return ErrTokenUnavailable
}

func boundedHTTPClient(client *http.Client, timeout time.Duration) *http.Client {
	secured := httpclient.Secure(client, timeout)
	transport := secured.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	secured.Transport = boundedTransport{base: transport}
	return secured
}

type boundedTransport struct {
	base http.RoundTripper
}

func (t boundedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(request)
	if err != nil || response == nil || response.Body == nil {
		return response, err
	}
	response.Body = &boundedReadCloser{
		reader: &io.LimitedReader{
			R: response.Body,
			N: maxTokenResponseBytes + 1,
		},
		closer: response.Body,
	}
	return response, nil
}

type boundedReadCloser struct {
	reader *io.LimitedReader
	closer io.Closer
}

func (r *boundedReadCloser) Read(buffer []byte) (int, error) {
	count, err := r.reader.Read(buffer)
	if r.reader.N == 0 {
		return count, errResponseTooLarge
	}
	return count, err
}

func (r *boundedReadCloser) Close() error {
	return r.closer.Close()
}
