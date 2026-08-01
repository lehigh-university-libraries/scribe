package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/app"
	"github.com/lehigh-university-libraries/scribe/internal/httplimits"
	"github.com/lehigh-university-libraries/scribe/internal/httprun"
	"github.com/lehigh-university-libraries/scribe/internal/safelog"
)

type readinessChecker interface {
	PingContext(context.Context) error
}

func workerHealthHandler(checker readinessChecker, draining *atomic.Bool) http.Handler {
	mux := http.NewServeMux()
	liveness := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}
	readiness := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if draining != nil && draining.Load() {
			http.Error(w, "draining", http.StatusServiceUnavailable)
			return
		}
		if checker != nil {
			pingCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			if err := checker.PingContext(pingCtx); err != nil {
				http.Error(w, "database unavailable", http.StatusServiceUnavailable)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}
	mux.HandleFunc("GET /livez", liveness)
	mux.HandleFunc("GET /readyz", readiness)
	mux.HandleFunc("GET /healthz", readiness)
	return mux
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		slog.Error("worker stopped", "error_type", safelog.ErrorType(err), "category", safelog.ErrorCategory(err))
		os.Exit(1)
	}
}

func run(ctx context.Context) (returnErr error) {
	deps, err := app.NewDependencies(ctx, app.BootstrapOptions{
		RunMigrations:             false,
		SeedSystemContexts:        false,
		TelemetryServiceName:      "scribe-worker",
		ObserveTranscriptionQueue: true,
	})
	if err != nil {
		return fmt.Errorf("bootstrap worker dependencies: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, deps.Close()) }()

	handler := deps.NewHandler()

	workerCtx, workerCancel := context.WithCancel(ctx)
	defer workerCancel()
	handler.StartTranscriptionWorker(workerCtx)
	handler.StartWebhookDispatcher(workerCtx)
	handler.StartProviderCallAuditRetention(workerCtx)
	handler.StartExternalRequestRetention(workerCtx)
	handler.StartAnnotationMirrorDispatcher(workerCtx)
	handler.StartResourceCleanupDispatcher(workerCtx)
	deps.AuthManager.StartProviderSecretCleanupDispatcher(workerCtx)
	deps.AuthManager.StartSessionRetentionDispatcher(workerCtx)

	var draining atomic.Bool

	healthAddr := strings.TrimSpace(os.Getenv("WORKER_HEALTH_LISTEN_ADDR"))
	if healthAddr == "" {
		healthAddr = ":8081"
	}
	httpServer := &http.Server{
		Addr:              healthAddr,
		Handler:           workerHealthHandler(deps.DBPool, &draining),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    httplimits.MaxHeaderBytes,
	}

	// Mark readiness as draining at the same cancellation edge that starts the
	// HTTP and worker shutdown paths. If the health listener itself fails,
	// cancel workers below before closing their shared dependencies.
	drainObserved := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			draining.Store(true)
			workerCancel()
		case <-drainObserved:
		}
	}()
	slog.Info("worker health endpoint listening", "addr", healthAddr)
	serveErr := httprun.Serve(ctx, httpServer, 10*time.Second)
	close(drainObserved)
	draining.Store(true)
	workerCancel()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var waitGroup sync.WaitGroup
	var waitErr, authWaitErr error
	waitGroup.Add(2)
	go func() {
		defer waitGroup.Done()
		if err := handler.WaitForBackgroundWorkers(shutdownCtx); err != nil {
			waitErr = fmt.Errorf("wait for worker background operations: %w", err)
		}
	}()
	go func() {
		defer waitGroup.Done()
		if err := deps.AuthManager.WaitForBackgroundWorkers(shutdownCtx); err != nil {
			authWaitErr = fmt.Errorf("wait for auth background operations: %w", err)
		}
	}()
	waitGroup.Wait()
	if serveErr != nil {
		serveErr = fmt.Errorf("run worker health server: %w", serveErr)
	}
	return errors.Join(serveErr, waitErr, authWaitErr)
}
