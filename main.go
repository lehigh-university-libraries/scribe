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

	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/database"
	"github.com/lehigh-university-libraries/scribe/internal/server"
	"github.com/lehigh-university-libraries/scribe/internal/store"
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

	ocrRunStore := store.NewOCRRunStore(dbPool)
	itemStore := store.NewItemStore(dbPool)
	contextStore := store.NewContextStore(dbPool)
	annotationStore := store.NewAnnotationStore(dbPool)

	if err := contextStore.EnsureDefault(context.Background(), defaultContextFromEnv()); err != nil {
		slog.Error("failed to seed default context", "err", err)
		os.Exit(1)
	}

	handler := server.NewHandler(ocrRunStore, itemStore, contextStore, annotationStore)

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

func defaultContextFromEnv() store.Context {
	provider := strings.ToLower(strings.TrimSpace(os.Getenv("LLM_PROVIDER")))
	if provider == "" {
		provider = "ollama"
	}
	var model string
	switch provider {
	case "openai":
		model = strings.TrimSpace(os.Getenv("OPENAI_MODEL"))
		if model == "" {
			model = "gpt-4o"
		}
	case "gemini":
		model = strings.TrimSpace(os.Getenv("GEMINI_MODEL"))
		if model == "" {
			model = "gemini-2.0-flash"
		}
	default:
		model = strings.TrimSpace(os.Getenv("OLLAMA_MODEL"))
		if model == "" {
			model = "mistral-small3.2:24b"
		}
	}

	segModel := strings.TrimSpace(os.Getenv("SEGMENTATION_MODEL"))
	if segModel == "" {
		segModel = "tesseract"
	}

	return store.Context{
		Name:                  "Default",
		Description:           "System default context seeded from environment configuration",
		IsDefault:             true,
		SegmentationModel:     segModel,
		TranscriptionProvider: provider,
		TranscriptionModel:    model,
		SystemPrompt:          strings.TrimSpace(os.Getenv("DEFAULT_SYSTEM_PROMPT")),
	}
}
