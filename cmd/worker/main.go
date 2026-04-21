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
	ctx := context.Background()
	deps, err := app.NewDependencies(ctx, app.BootstrapOptions{
		RunMigrations:      false,
		SeedSystemContexts: false,
	})
	if err != nil {
		slog.Error("failed to bootstrap dependencies", "err", err)
		os.Exit(1)
	}
	defer deps.Close()

	handler := deps.NewHandler()

	workerCtx, workerCancel := context.WithCancel(context.Background())
	defer workerCancel()
	handler.StartTranscriptionWorker(workerCtx)

	healthMux := http.NewServeMux()
	healthMux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	httpServer := &http.Server{
		Addr:         deps.Config.ListenAddr,
		Handler:      healthMux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		slog.Info("worker health endpoint listening", "addr", deps.Config.ListenAddr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("worker health server failed", "err", err)
			os.Exit(1)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	workerCancel()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		slog.Error("worker graceful shutdown failed", "err", err)
		os.Exit(1)
	}
}
