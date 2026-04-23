package main

import (
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/segmentor"
)

func main() {
	addr := strings.TrimSpace(os.Getenv("SEGMENTOR_LISTEN_ADDR"))
	if addr == "" {
		addr = ":8080"
	}

	server := &http.Server{
		Addr:              addr,
		Handler:           segmentor.NewHandler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	slog.Info("segmentor listening", "addr", addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("segmentor failed", "err", err)
		os.Exit(1)
	}
}
