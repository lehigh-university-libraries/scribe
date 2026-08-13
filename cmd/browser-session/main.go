// Command browser-session creates a short-lived browser credential for a
// trusted deployment smoke test running on the backend host.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/lehigh-university-libraries/scribe/internal/app"
	"github.com/lehigh-university-libraries/scribe/internal/deployauth"
	"github.com/lehigh-university-libraries/scribe/internal/safelog"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		// Never log the returned error: a future storage implementation could
		// include credential-bearing state. Categorical diagnostics are enough
		// for the protected workflow to fail closed.
		slog.Error(
			"browser smoke session mint failed",
			"error_type", safelog.ErrorType(err),
			"category", safelog.ErrorCategory(err),
		)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) (returnErr error) {
	outputPath, err := parseArguments(args)
	if err != nil {
		return err
	}
	deps, err := app.NewDependencies(ctx, app.BootstrapOptions{
		TelemetryServiceName: "scribe-browser-session",
	})
	if err != nil {
		return fmt.Errorf("bootstrap browser session dependencies: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, deps.Close()) }()

	if err := deployauth.MintBrowserSessionFile(
		ctx,
		deps.IdentityStore,
		deps.Config.PublicBaseURL,
		deps.Config.Auth.CookieName,
		deps.Config.Auth.CookieDomain,
		outputPath,
	); err != nil {
		return fmt.Errorf("mint browser session file: %w", err)
	}
	return nil
}

func parseArguments(args []string) (string, error) {
	flags := flag.NewFlagSet("browser-session", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var outputPath string
	flags.StringVar(&outputPath, "output", "", "new /tmp storage-state path")
	if err := flags.Parse(args); err != nil {
		return "", fmt.Errorf("parse browser session arguments")
	}
	if flags.NArg() != 0 || strings.TrimSpace(outputPath) == "" {
		return "", fmt.Errorf("browser session requires exactly --output /tmp/scribe-browser-session-<run-id>.json")
	}
	return outputPath, nil
}
