package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/gcpidentity"
	"github.com/lehigh-university-libraries/scribe/internal/servicehttp"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	for _, origin := range []string{os.Getenv("SCRIBE_API_ORIGIN"), os.Getenv("SCRIBE_WORKER_ORIGIN")} {
		if err := retryProbe(ctx, 2*time.Second, func(ctx context.Context) error { return probe(ctx, origin) }); err != nil {
			fmt.Fprintln(os.Stderr, "managed backend readiness failed")
			os.Exit(1)
		}
	}
}

func retryProbe(ctx context.Context, delay time.Duration, attempt func(context.Context) error) error {
	for {
		if err := attempt(ctx); err == nil {
			return nil
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func probe(ctx context.Context, origin string) error {
	endpoint, err := url.Parse(origin)
	if err != nil || endpoint.Scheme != "https" || !strings.HasSuffix(endpoint.Hostname(), ".run.app") || endpoint.Host != endpoint.Hostname() || endpoint.User != nil || endpoint.Path != "" || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return errors.New("readiness requires a canonical Cloud Run origin")
	}
	source, err := gcpidentity.Default()
	if err != nil {
		return err
	}
	token, err := source.Token(ctx, origin)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/readyz", nil) // #nosec G704 -- Terraform-owned HTTPS run.app origin, validated above; no user-controlled endpoint.
	if err != nil {
		return err
	}
	req.Header.Set("X-Serverless-Authorization", "Bearer "+token)
	response, err := servicehttp.NewClient(60 * time.Second).Do(req) // #nosec G704 -- validated deployment-owned origin; servicehttp forbids redirects and bounds the request.
	if err != nil {
		return err
	}
	defer response.Body.Close()
	return validate(response, os.Getenv("SCRIBE_EXPECTED_API_IMAGE"), os.Getenv("SCRIBE_EXPECTED_PUBLIC_ORIGIN"))
}

func validate(response *http.Response, expectedImage, expectedOrigin string) error {
	var payload struct {
		Status string `json:"status"`
		Image  string `json:"api_image"`
		Origin string `json:"public_origin"`
	}
	if expectedImage == "" || expectedOrigin == "" || response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&payload) != nil || payload.Status != "ready" || payload.Image != expectedImage || payload.Origin != expectedOrigin {
		return errors.New("backend readiness contract mismatch")
	}
	return nil
}
