package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/app"
)

func main() {
	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel()
	deps, err := app.NewDependencies(appCtx, app.BootstrapOptions{
		RunMigrations:      true,
		SeedSystemContexts: true,
	})
	if err != nil {
		slog.Error("failed to bootstrap dependencies", "err", err)
		os.Exit(1)
	}
	defer deps.Close()

	handler := deps.NewHandler()

	httpServer := &http.Server{
		Addr:              deps.Config.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		slog.Info("api listening", "addr", deps.Config.ListenAddr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server failed", "err", err)
			os.Exit(1)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	appCancel()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		slog.Error("graceful shutdown failed", "err", err)
		os.Exit(1)
	}
}
