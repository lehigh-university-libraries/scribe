package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/app"
	"github.com/lehigh-university-libraries/scribe/internal/httplimits"
	"github.com/lehigh-university-libraries/scribe/internal/httprun"
	"github.com/lehigh-university-libraries/scribe/internal/safelog"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		slog.Error("api stopped", "error_type", safelog.ErrorType(err), "category", safelog.ErrorCategory(err))
		os.Exit(1)
	}
}

func run(ctx context.Context) (returnErr error) {
	deps, err := app.NewDependencies(ctx, app.BootstrapOptions{
		RunMigrations:      true,
		SeedSystemContexts: true,
	})
	if err != nil {
		return fmt.Errorf("bootstrap API dependencies: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, deps.Close()) }()

	handler := deps.NewHandler()

	httpServer := &http.Server{
		Addr:              deps.Config.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		// Upload RPCs accept images up to the documented body limit. Give slow
		// but legitimate clients enough time to transmit them; per-request body
		// and decoded-pixel limits still bound memory and CPU cost.
		ReadTimeout:    2 * time.Minute,
		WriteTimeout:   0,
		IdleTimeout:    60 * time.Second,
		MaxHeaderBytes: httplimits.MaxHeaderBytes,
	}

	slog.Info("api listening", "addr", deps.Config.ListenAddr)
	if err := httprun.Serve(ctx, httpServer, 10*time.Second); err != nil {
		return fmt.Errorf("run API server: %w", err)
	}
	return nil
}
