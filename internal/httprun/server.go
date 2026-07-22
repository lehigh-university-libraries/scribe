// Package httprun provides the shared lifecycle used by Scribe HTTP services.
package httprun

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// Serve listens synchronously, serves in the background, and gracefully drains
// in-flight requests when ctx is canceled. Binding before starting the goroutine
// makes startup failures observable and avoids a shutdown-before-listen race.
func Serve(ctx context.Context, server *http.Server, shutdownTimeout time.Duration) error {
	if ctx == nil {
		return fmt.Errorf("serve HTTP: context is required")
	}
	if server == nil {
		return fmt.Errorf("serve HTTP: server is required")
	}
	if shutdownTimeout <= 0 {
		return fmt.Errorf("serve HTTP: shutdown timeout must be positive")
	}

	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", server.Addr, err)
	}
	serveErrors := make(chan error, 1)
	go func() {
		serveErrors <- server.Serve(listener)
	}()

	select {
	case err := <-serveErrors:
		return normalizeServeError(err)
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		// Force the listener closed after the bounded graceful period so this
		// process cannot hang indefinitely during a rollout.
		_ = server.Close()
		<-serveErrors
		return fmt.Errorf("shutdown HTTP server: %w", err)
	}
	return normalizeServeError(<-serveErrors)
}

func normalizeServeError(err error) error {
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("serve HTTP: %w", err)
}
