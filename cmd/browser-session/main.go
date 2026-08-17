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
	"github.com/lehigh-university-libraries/scribe/internal/store"
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

type browserSessionMinter func(
	context.Context,
	*store.IdentityStore,
	string,
	string,
	string,
	string,
) error

type commandRuntime struct {
	newDependencies func(context.Context) (*app.BrowserSessionDependencies, error)
	mint            browserSessionMinter
	mintReserved    browserSessionMinter
	reserve         func(context.Context, string) error
	export          func(context.Context, string, io.Writer) error
	cleanup         func(context.Context, string) error
	cleanupAll      func(context.Context) error
	stdout          io.Writer
}

func defaultCommandRuntime() commandRuntime {
	return commandRuntime{
		newDependencies: app.NewBrowserSessionDependencies,
		mint:            deployauth.MintBrowserSessionFile,
		mintReserved:    deployauth.MintReservedBrowserSessionFile,
		reserve:         deployauth.ReserveBrowserSessionFile,
		export:          deployauth.ExportBrowserSessionFile,
		cleanup:         deployauth.CleanupBrowserSessionFile,
		cleanupAll:      deployauth.CleanupBrowserSessionFiles,
		stdout:          os.Stdout,
	}
}

func run(ctx context.Context, args []string) error {
	return runWithRuntime(ctx, args, defaultCommandRuntime())
}

func runWithRuntime(ctx context.Context, args []string, runtime commandRuntime) (returnErr error) {
	request, err := parseArguments(args)
	if err != nil {
		return err
	}
	if request.cleanupAll {
		return runtime.cleanupAll(ctx)
	}
	if request.cleanupPath != "" {
		return runtime.cleanup(ctx, request.cleanupPath)
	}
	if request.reservePath != "" {
		return runtime.reserve(ctx, request.reservePath)
	}
	if request.exportPath != "" {
		return runtime.export(ctx, request.exportPath, runtime.stdout)
	}
	deps, err := runtime.newDependencies(ctx)
	if err != nil {
		return fmt.Errorf("bootstrap browser session dependencies: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, deps.Close()) }()

	mint := runtime.mint
	if request.reservedOutputPath != "" {
		mint = runtime.mintReserved
	}
	outputPath := request.outputPath
	if request.reservedOutputPath != "" {
		outputPath = request.reservedOutputPath
	}
	if err := mint(
		ctx,
		deps.IdentityStore,
		deps.PublicBaseURL,
		deps.CookieName,
		deps.CookieDomain,
		outputPath,
	); err != nil {
		return fmt.Errorf("mint browser session file: %w", err)
	}
	return nil
}

type commandRequest struct {
	outputPath         string
	reservedOutputPath string
	reservePath        string
	exportPath         string
	cleanupPath        string
	cleanupAll         bool
}

func parseArguments(args []string) (commandRequest, error) {
	flags := flag.NewFlagSet("browser-session", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var request commandRequest
	flags.StringVar(&request.outputPath, "output", "", "new /tmp storage-state path")
	flags.StringVar(&request.reservedOutputPath, "reserved-output", "", "reserved /tmp storage-state path")
	flags.StringVar(&request.reservePath, "reserve", "", "exact /tmp storage-state path to reserve")
	flags.StringVar(&request.exportPath, "export", "", "exact /tmp storage-state path to export")
	flags.StringVar(&request.cleanupPath, "cleanup", "", "exact /tmp storage-state path to remove")
	flags.BoolVar(&request.cleanupAll, "cleanup-all", false, "remove all valid /tmp storage-state paths")
	if err := flags.Parse(args); err != nil {
		return commandRequest{}, fmt.Errorf("parse browser session arguments")
	}
	selected := 0
	if strings.TrimSpace(request.outputPath) != "" {
		selected++
	}
	if strings.TrimSpace(request.cleanupPath) != "" {
		selected++
	}
	if strings.TrimSpace(request.reservedOutputPath) != "" {
		selected++
	}
	if strings.TrimSpace(request.reservePath) != "" {
		selected++
	}
	if strings.TrimSpace(request.exportPath) != "" {
		selected++
	}
	if request.cleanupAll {
		selected++
	}
	if flags.NArg() != 0 || selected != 1 {
		return commandRequest{}, fmt.Errorf("browser session requires exactly one operation")
	}
	return request, nil
}
