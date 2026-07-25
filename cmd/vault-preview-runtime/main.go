// Command vault-preview-runtime verifies or reconciles the shared Vault
// policy and GCP auth role used by Scribe previews.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/servicehttp"
	"github.com/lehigh-university-libraries/scribe/internal/vaultpreviewruntime"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	googleCloudScope = "https://www.googleapis.com/auth/cloud-platform"
	googleEmailScope = "https://www.googleapis.com/auth/userinfo.email"
	defaultRegion    = "us-east5"
	requestTimeout   = 30 * time.Second
)

type dependencies struct {
	getenv      func(string) string
	httpClient  *http.Client
	tokenSource func(context.Context) (oauth2.TokenSource, error)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Stdout, dependencies{
		getenv:     os.Getenv,
		httpClient: servicehttp.NewClient(requestTimeout),
		tokenSource: func(ctx context.Context) (oauth2.TokenSource, error) {
			return google.DefaultTokenSource(ctx, googleCloudScope, googleEmailScope)
		},
	}); err != nil {
		fmt.Fprintf(os.Stderr, "vault-preview-runtime: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout io.Writer, deps dependencies) error {
	if deps.getenv == nil || deps.tokenSource == nil {
		return errors.New("command dependencies are not configured")
	}
	flags := flag.NewFlagSet("vault-preview-runtime", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	modeFlag := flags.String("mode", string(vaultpreviewruntime.ModeCheck), "check or apply")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return errors.New("invalid arguments")
	}
	mode := vaultpreviewruntime.Mode(*modeFlag)
	if mode != vaultpreviewruntime.ModeCheck && mode != vaultpreviewruntime.ModeApply {
		return errors.New("mode must be check or apply")
	}

	tokenSource, err := deps.tokenSource(ctx)
	if err != nil || tokenSource == nil {
		return errors.New("initialize Google admin token source: failed")
	}
	region := deps.getenv("SCRIBE_REGION")
	if strings.TrimSpace(region) == "" {
		region = defaultRegion
	}
	reconciler, err := vaultpreviewruntime.New(vaultpreviewruntime.Config{
		VaultAddress:     deps.getenv("VAULT_ADDR"),
		VaultToken:       deps.getenv("VAULT_TOKEN"),
		ProjectID:        deps.getenv("GCLOUD_PROJECT"),
		ProjectNumber:    deps.getenv("GCLOUD_PROJECT_NUMBER"),
		Region:           region,
		AdminTokenSource: tokenSource,
		HTTPClient:       deps.httpClient,
	})
	if err != nil {
		return err
	}
	if err := reconciler.Reconcile(ctx, mode); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(stdout, vaultpreviewruntime.SuccessMarker); err != nil {
		return errors.New("write success marker: failed")
	}
	return nil
}
