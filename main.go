package main

import (
  "context"
  "log/slog"
  "net/http"
  "os"
  "os/signal"
  "syscall"
  "time"

  "github.com/lehigh-university-libraries/hOCRedit/internal/config"
  "github.com/lehigh-university-libraries/hOCRedit/internal/database"
  "github.com/lehigh-university-libraries/hOCRedit/internal/server"
  "github.com/lehigh-university-libraries/hOCRedit/internal/store"
)

func main() {
  cfg := config.FromEnv()

  dbPool, err := database.NewPool(cfg.DatabaseDSN, database.DefaultConfig())
  if err != nil {
    slog.Error("failed to connect to database", "err", err)
    os.Exit(1)
  }
  defer dbPool.Close()

  if err := database.Migrate(dbPool); err != nil {
    slog.Error("failed to run migrations", "err", err)
    os.Exit(1)
  }

	sessionStore := store.NewSessionStore(dbPool)
	ocrRunStore := store.NewOCRRunStore(dbPool)
	handler := server.NewHandler(sessionStore, ocrRunStore)

  httpServer := &http.Server{
    Addr:         cfg.ListenAddr,
    Handler:      handler,
    ReadTimeout:  10 * time.Second,
    WriteTimeout: 30 * time.Second,
    IdleTimeout:  60 * time.Second,
  }

  go func() {
    slog.Info("api listening", "addr", cfg.ListenAddr)
    if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
      slog.Error("server failed", "err", err)
      os.Exit(1)
    }
  }()

  sigCh := make(chan os.Signal, 1)
  signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
  <-sigCh

  ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
  defer cancel()
  if err := httpServer.Shutdown(ctx); err != nil {
    slog.Error("graceful shutdown failed", "err", err)
    os.Exit(1)
  }
}
