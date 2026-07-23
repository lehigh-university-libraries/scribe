package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/httplimits"
	"github.com/lehigh-university-libraries/scribe/internal/httprun"
	"github.com/lehigh-university-libraries/scribe/internal/safelog"
	"github.com/lehigh-university-libraries/scribe/internal/segmentor"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := checkSegmentorHealth(ctx, "http://127.0.0.1:8080/healthz"); err != nil {
			fmt.Fprintln(os.Stderr, "segmentor healthcheck failed")
			os.Exit(1)
		}
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	addr := strings.TrimSpace(os.Getenv("SEGMENTOR_LISTEN_ADDR"))
	if addr == "" {
		addr = ":8080"
	}

	server := newSegmentorHTTPServer(addr)

	slog.Info("segmentor listening", "addr", addr)
	if err := httprun.Serve(ctx, server, 10*time.Second); err != nil {
		slog.Error("segmentor failed", "error_type", safelog.ErrorType(err), "category", safelog.ErrorCategory(err))
		os.Exit(1)
	}
}

func newSegmentorHTTPServer(addr string) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           segmentor.NewHandler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      segmentor.InferenceServerWriteTimeout,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    httplimits.MaxHeaderBytes,
	}
}

func checkSegmentorHealth(ctx context.Context, endpoint string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("construct health request: %w", err)
	}
	client := &http.Client{
		Timeout: 2 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return fmt.Errorf("health endpoint redirected")
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("request health endpoint: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("health endpoint returned status %d", response.StatusCode)
	}
	return nil
}
