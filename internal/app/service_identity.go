package app

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/gcpidentity"
	"golang.org/x/sync/errgroup"
)

const (
	serviceIdentityPreflightTimeout = 15 * time.Second
	serviceIdentityPreflightLimit   = 4
)

var errServiceIdentityUnavailable = errors.New("service identity unavailable")

type serviceIdentityTokenSource interface {
	Token(context.Context, string) (string, error)
}

// preflightServiceIdentity mints each configured outbound identity token before
// the API or worker begins listening. This makes the existing Compose and
// deployment readiness gates cover the same credential path used by requests.
func preflightServiceIdentity(ctx context.Context, cfg config.Config) error {
	audiences := configuredServiceAudiences(cfg)
	if len(audiences) == 0 {
		return nil
	}
	source, err := gcpidentity.Default()
	if err != nil {
		return errServiceIdentityUnavailable
	}
	return preflightServiceIdentityWithSource(ctx, source, audiences)
}

func preflightServiceIdentityWithSource(ctx context.Context, source serviceIdentityTokenSource, audiences []string) error {
	if len(audiences) == 0 {
		return nil
	}
	preflightCtx, cancel := context.WithTimeout(ctx, serviceIdentityPreflightTimeout)
	defer cancel()
	if err := warmServiceIdentity(preflightCtx, source, audiences); err != nil {
		if parentErr := ctx.Err(); parentErr != nil {
			return parentErr
		}
		return errServiceIdentityUnavailable
	}
	return nil
}

func warmServiceIdentity(ctx context.Context, source serviceIdentityTokenSource, audiences []string) error {
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(serviceIdentityPreflightLimit)
	for _, audience := range audiences {
		audience := audience
		group.Go(func() error {
			_, err := source.Token(groupCtx, audience)
			return err
		})
	}
	return group.Wait()
}

func configuredServiceAudiences(cfg config.Config) []string {
	seen := make(map[string]struct{})
	add := func(audience string) {
		audience = strings.TrimSpace(audience)
		if audience != "" {
			seen[audience] = struct{}{}
		}
	}
	addEndpoints := func(endpoints map[string]config.ModelEndpoint) {
		for _, endpoint := range endpoints {
			add(endpoint.Audience)
		}
	}

	add(cfg.Segmentation.Audience)
	addEndpoints(cfg.Segmentation.ModelEndpoints)
	add(cfg.LLM.Kraken.Audience)
	addEndpoints(cfg.LLM.Kraken.ModelEndpoints)
	add(cfg.LLM.Ollama.Audience)
	addEndpoints(cfg.LLM.Ollama.ModelEndpoints)

	audiences := make([]string, 0, len(seen))
	for audience := range seen {
		audiences = append(audiences, audience)
	}
	sort.Strings(audiences)
	return audiences
}
