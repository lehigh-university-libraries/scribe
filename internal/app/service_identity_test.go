package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/config"
)

func TestConfiguredServiceAudiencesAreExactUniqueAndOutboundOnly(t *testing.T) {
	cfg := config.Config{
		Segmentation: config.ServiceEndpointConfig{
			Audience: " https://segment.example ",
			ModelEndpoints: map[string]config.ModelEndpoint{
				"default": {Audience: "https://segment.example"},
				"other":   {Audience: "https://segment-other.example"},
			},
		},
		LLM: config.LLMConfig{
			Kraken: config.KrakenConfig{
				Audience: "https://kraken.example",
				ModelEndpoints: map[string]config.ModelEndpoint{
					"default": {Audience: "https://kraken.example"},
				},
			},
			Ollama: config.OllamaConfig{
				ModelEndpoints: map[string]config.ModelEndpoint{
					"default": {Audience: "https://ollama.example"},
					"empty":   {},
				},
			},
		},
		Auth: config.AuthConfig{
			ExternalJWTIssuers: []config.ExternalJWTIssuerConfig{
				{Audience: "https://inbound-must-not-be-minted.example"},
			},
		},
	}

	got := configuredServiceAudiences(cfg)
	want := []string{
		"https://kraken.example",
		"https://ollama.example",
		"https://segment-other.example",
		"https://segment.example",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("configuredServiceAudiences() = %#v, want %#v", got, want)
	}
}

func TestWarmServiceIdentityUsesEachAudienceAndBoundsConcurrency(t *testing.T) {
	source := &recordingIdentitySource{
		release: make(chan struct{}),
		started: make(chan struct{}, 8),
	}
	audiences := []string{
		"https://a.example",
		"https://b.example",
		"https://c.example",
		"https://d.example",
		"https://e.example",
		"https://f.example",
	}
	done := make(chan error, 1)
	go func() {
		done <- warmServiceIdentity(context.Background(), source, audiences)
	}()

	for range serviceIdentityPreflightLimit {
		select {
		case <-source.started:
		case <-time.After(time.Second):
			t.Fatal("preflight did not start the configured concurrency")
		}
	}
	select {
	case <-source.started:
		t.Fatal("preflight exceeded its concurrency limit")
	case <-time.After(20 * time.Millisecond):
	}
	close(source.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := source.maxConcurrent(); got != serviceIdentityPreflightLimit {
		t.Fatalf("maximum concurrent token calls = %d, want %d", got, serviceIdentityPreflightLimit)
	}
	got := source.audiences()
	slices.Sort(got)
	slices.Sort(audiences)
	if !slices.Equal(got, audiences) {
		t.Fatalf("warmed audiences = %#v, want %#v", got, audiences)
	}
}

func TestWarmServiceIdentityEmptyIsNoOp(t *testing.T) {
	if err := warmServiceIdentity(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestWarmServiceIdentityFailureIsRedactedByPreflightBoundary(t *testing.T) {
	secret := "private-provider-response"
	source := &recordingIdentitySource{err: errors.New(secret)}
	err := preflightServiceIdentityWithSource(context.Background(), source, []string{"https://service.example"})
	if !errors.Is(err, errServiceIdentityUnavailable) {
		t.Fatalf("preflightServiceIdentityWithSource() error = %v, want unavailable", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("preflight error disclosed provider detail: %q", err)
	}

	parent, cancel := context.WithCancel(context.Background())
	cancel()
	if err := preflightServiceIdentityWithSource(parent, source, []string{"https://service.example"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled preflight error = %v, want context canceled", err)
	}
}

type recordingIdentitySource struct {
	mu        sync.Mutex
	calls     []string
	active    int
	maxActive int
	release   chan struct{}
	started   chan struct{}
	err       error
}

func (s *recordingIdentitySource) Token(ctx context.Context, audience string) (string, error) {
	s.mu.Lock()
	s.calls = append(s.calls, audience)
	s.active++
	if s.active > s.maxActive {
		s.maxActive = s.active
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.active--
		s.mu.Unlock()
	}()
	if s.started != nil {
		s.started <- struct{}{}
	}
	if s.release != nil {
		select {
		case <-s.release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if s.err != nil {
		return "", s.err
	}
	return "token", nil
}

func (s *recordingIdentitySource) audiences() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

func (s *recordingIdentitySource) maxConcurrent() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.maxActive
}
