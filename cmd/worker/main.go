package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/app"
)

func main() {
	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel()
	deps, err := app.NewDependencies(appCtx, app.BootstrapOptions{
		RunMigrations:      false,
		SeedSystemContexts: false,
	})
	if err != nil {
		slog.Error("failed to bootstrap dependencies", "err", err)
		os.Exit(1)
	}
	defer deps.Close()

	handler := deps.NewHandler()

	workerCtx, workerCancel := context.WithCancel(appCtx)
	defer workerCancel()
	handler.StartTranscriptionWorker(workerCtx)
	handler.StartWebhookDispatcher(workerCtx)

	healthMux := http.NewServeMux()
	healthMux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	healthAddr := strings.TrimSpace(os.Getenv("WORKER_HEALTH_LISTEN_ADDR"))
	if healthAddr == "" {
		healthAddr = ":8081"
	}
	httpServer := &http.Server{
		Addr:              healthAddr,
		Handler:           healthMux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		slog.Info("worker health endpoint listening", "addr", healthAddr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("worker health server failed", "err", err)
			os.Exit(1)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	workerCancel()
	appCancel()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		slog.Error("worker graceful shutdown failed", "err", err)
		os.Exit(1)
	}
}
