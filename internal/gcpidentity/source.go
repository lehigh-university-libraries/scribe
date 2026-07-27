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
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"cloud.google.com/go/auth"
	"cloud.google.com/go/auth/credentials"
	"cloud.google.com/go/auth/credentials/idtoken"
	"cloud.google.com/go/auth/credentials/impersonate"
	"cloud.google.com/go/auth/httptransport"
	"github.com/lehigh-university-libraries/htr/pkg/auth/gcpidtoken"
	"github.com/lehigh-university-libraries/htr/pkg/httpclient"
)

const (
	credentialsEnvironment = "GOOGLE_APPLICATION_CREDENTIALS"
	googleTokenEndpoint    = "https://oauth2.googleapis.com/token"
	googleIAMHost          = "iamcredentials.googleapis.com"
	cloudPlatformScope     = "https://www.googleapis.com/auth/cloud-platform"
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
	serviceAccountEmail = regexp.MustCompile(`^[a-z][a-z0-9-]{4,29}@[a-z][a-z0-9-]{4,61}\.iam\.gserviceaccount\.com$`)
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
// uses either one validated configured credential file or metadata, never both.
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

type impersonatedServiceAccountConfig struct {
	targetPrincipal   string
	sourceCredentials []byte
}

type credentialFileConfig struct {
	credentialType credentials.CredType
	serviceAccount serviceAccountConfig
	impersonated   impersonatedServiceAccountConfig
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
	switch config.credentialType {
	case credentials.ServiceAccount:
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
				Email:        config.serviceAccount.clientEmail,
				PrivateKey:   []byte(config.serviceAccount.privateKey),
				PrivateKeyID: config.serviceAccount.privateKeyID,
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
	case credentials.ImpersonatedServiceAccount:
		baseCredentials, err := credentials.NewCredentialsFromJSON(
			credentials.AuthorizedUser,
			config.impersonated.sourceCredentials,
			&credentials.DetectOptions{
				Scopes:   []string{cloudPlatformScope},
				TokenURL: googleTokenEndpoint,
				Client:   client,
				Logger:   silentSDKLogger,
			},
		)
		if err != nil {
			return nil, ErrInvalidConfiguration
		}
		authenticatedClient, err := httptransport.NewClient(&httptransport.Options{
			BaseRoundTripper: client.Transport,
			Credentials:      baseCredentials,
			DisableTelemetry: true,
			Logger:           silentSDKLogger,
		})
		if err != nil {
			return nil, ErrInvalidConfiguration
		}
		authenticatedClient.Timeout = client.Timeout
		authenticatedClient.CheckRedirect = client.CheckRedirect
		source.newProvider = func(audience string) (auth.TokenProvider, error) {
			credential, err := impersonate.NewIDTokenCredentials(&impersonate.IDTokenOptions{
				Audience:        audience,
				TargetPrincipal: config.impersonated.targetPrincipal,
				IncludeEmail:    true,
				Client:          authenticatedClient,
				Logger:          silentSDKLogger,
			})
			if err != nil {
				return nil, ErrInvalidConfiguration
			}
			return credential.TokenProvider, nil
		}
	default:
		return nil, ErrInvalidConfiguration
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

func readCredentialJSON(path string) (credentialFileConfig, []byte, error) {
	if strings.TrimSpace(path) != path || path == "" ||
		strings.IndexFunc(path, unicode.IsControl) >= 0 {
		return credentialFileConfig{}, nil, ErrInvalidConfiguration
	}
	// #nosec G304 -- the path is trusted operator configuration; the file is
	// bounded below and accepted only as one of the explicitly validated Google
	// credential shapes below.
	file, err := os.Open(path)
	if err != nil {
		return credentialFileConfig{}, nil, ErrInvalidConfiguration
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxCredentialBytes {
		return credentialFileConfig{}, nil, ErrInvalidConfiguration
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxCredentialBytes+1))
	if err != nil || len(raw) == 0 || len(raw) > maxCredentialBytes {
		return credentialFileConfig{}, nil, ErrInvalidConfiguration
	}
	var descriptor struct {
		Type                           string          `json:"type"`
		TokenURI                       string          `json:"token_uri"`
		ClientEmail                    string          `json:"client_email"`
		PrivateKey                     string          `json:"private_key"`
		PrivateKeyID                   string          `json:"private_key_id"`
		ServiceAccountImpersonationURL string          `json:"service_account_impersonation_url"`
		SourceCredentials              json.RawMessage `json:"source_credentials"`
		Delegates                      []string        `json:"delegates"`
		Scopes                         []string        `json:"scopes"`
		UniverseDomain                 string          `json:"universe_domain"`
	}
	if err := json.Unmarshal(raw, &descriptor); err != nil {
		return credentialFileConfig{}, nil, ErrInvalidConfiguration
	}
	switch credentials.CredType(descriptor.Type) {
	case credentials.ServiceAccount:
		if descriptor.TokenURI != googleTokenEndpoint {
			return credentialFileConfig{}, nil, ErrInvalidConfiguration
		}
		return credentialFileConfig{
			credentialType: credentials.ServiceAccount,
			serviceAccount: serviceAccountConfig{
				clientEmail:  descriptor.ClientEmail,
				privateKey:   descriptor.PrivateKey,
				privateKeyID: descriptor.PrivateKeyID,
			},
		}, raw, nil
	case credentials.ImpersonatedServiceAccount:
		impersonated, validateErr := validateImpersonatedServiceAccount(raw, descriptor)
		if validateErr != nil {
			return credentialFileConfig{}, nil, ErrInvalidConfiguration
		}
		return credentialFileConfig{
			credentialType: credentials.ImpersonatedServiceAccount,
			impersonated:   impersonated,
		}, raw, nil
	default:
		return credentialFileConfig{}, nil, ErrInvalidConfiguration
	}
}

func validateImpersonatedServiceAccount(raw []byte, descriptor struct {
	Type                           string          `json:"type"`
	TokenURI                       string          `json:"token_uri"`
	ClientEmail                    string          `json:"client_email"`
	PrivateKey                     string          `json:"private_key"`
	PrivateKeyID                   string          `json:"private_key_id"`
	ServiceAccountImpersonationURL string          `json:"service_account_impersonation_url"`
	SourceCredentials              json.RawMessage `json:"source_credentials"`
	Delegates                      []string        `json:"delegates"`
	Scopes                         []string        `json:"scopes"`
	UniverseDomain                 string          `json:"universe_domain"`
}) (impersonatedServiceAccountConfig, error) {
	if len(descriptor.Delegates) != 0 || len(descriptor.Scopes) != 0 ||
		(descriptor.UniverseDomain != "" && descriptor.UniverseDomain != "googleapis.com") {
		return impersonatedServiceAccountConfig{}, ErrInvalidConfiguration
	}
	var outerFields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &outerFields); err != nil || !onlyJSONFields(outerFields,
		"type", "service_account_impersonation_url", "source_credentials",
		"delegates", "scopes", "universe_domain",
	) {
		return impersonatedServiceAccountConfig{}, ErrInvalidConfiguration
	}
	target, err := targetPrincipalFromImpersonationURL(descriptor.ServiceAccountImpersonationURL)
	if err != nil {
		return impersonatedServiceAccountConfig{}, ErrInvalidConfiguration
	}
	var source struct {
		Type           string `json:"type"`
		ClientID       string `json:"client_id"`
		ClientSecret   string `json:"client_secret"`
		RefreshToken   string `json:"refresh_token"`
		QuotaProjectID string `json:"quota_project_id"`
		UniverseDomain string `json:"universe_domain"`
		Account        string `json:"account"`
	}
	var sourceFields map[string]json.RawMessage
	if len(descriptor.SourceCredentials) == 0 ||
		json.Unmarshal(descriptor.SourceCredentials, &source) != nil ||
		json.Unmarshal(descriptor.SourceCredentials, &sourceFields) != nil ||
		!onlyJSONFields(sourceFields,
			"type", "client_id", "client_secret", "refresh_token",
			"quota_project_id", "universe_domain", "account",
		) ||
		source.Type != string(credentials.AuthorizedUser) ||
		source.ClientID == "" || source.ClientSecret == "" || source.RefreshToken == "" ||
		len(source.ClientID) > 64<<10 || len(source.ClientSecret) > 64<<10 || len(source.RefreshToken) > 64<<10 ||
		(source.UniverseDomain != "" && source.UniverseDomain != "googleapis.com") {
		return impersonatedServiceAccountConfig{}, ErrInvalidConfiguration
	}
	return impersonatedServiceAccountConfig{
		targetPrincipal:   target,
		sourceCredentials: append([]byte(nil), descriptor.SourceCredentials...),
	}, nil
}

func onlyJSONFields(fields map[string]json.RawMessage, allowed ...string) bool {
	allowedFields := make(map[string]struct{}, len(allowed))
	for _, field := range allowed {
		allowedFields[field] = struct{}{}
	}
	for field := range fields {
		if _, ok := allowedFields[field]; !ok {
			return false
		}
	}
	return true
}

func targetPrincipalFromImpersonationURL(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host != googleIAMHost ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" {
		return "", ErrInvalidConfiguration
	}
	const prefix = "/v1/projects/-/serviceAccounts/"
	const suffix = ":generateAccessToken"
	if !strings.HasPrefix(parsed.Path, prefix) || !strings.HasSuffix(parsed.Path, suffix) {
		return "", ErrInvalidConfiguration
	}
	target := strings.TrimSuffix(strings.TrimPrefix(parsed.Path, prefix), suffix)
	if !serviceAccountEmail.MatchString(target) {
		return "", ErrInvalidConfiguration
	}
	return target, nil
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
