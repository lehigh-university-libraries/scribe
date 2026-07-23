// Package worklimit bounds expensive HTTP work independently from cheap
// liveness endpoints and platform-level request concurrency.
package worklimit

import (
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
)

type Limiter struct {
	slots chan struct{}
}

// FromEnvironment returns a limiter configured by a positive integer. Invalid
// or excessive values fail safely to the documented default or maximum.
func FromEnvironment(name string, fallback, maximum int) *Limiter {
	if fallback < 1 {
		fallback = 1
	}
	if maximum < fallback {
		maximum = fallback
	}
	limit := fallback
	if raw := strings.TrimSpace(os.Getenv(name)); raw != "" {
		parsed, err := strconv.Atoi(raw)
		switch {
		case err != nil || parsed < 1:
			slog.Warn("invalid work concurrency; using default", "environment", name, "value", raw, "default", fallback)
		case parsed > maximum:
			limit = maximum
			slog.Warn("work concurrency exceeds safety cap; clamping", "environment", name, "value", parsed, "maximum", maximum)
		default:
			limit = parsed
		}
	}
	return &Limiter{slots: make(chan struct{}, limit)}
}

// Wrap queues requests until one bounded work slot is available. Cancellation
// removes queued requests without starting expensive work.
func (l *Limiter) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if l == nil {
			next.ServeHTTP(w, r)
			return
		}
		select {
		case l.slots <- struct{}{}:
			defer func() { <-l.slots }()
			next.ServeHTTP(w, r)
		case <-r.Context().Done():
			return
		}
	})
}
