package auth

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

const (
	authSessionRetentionInterval   = time.Hour
	authBackgroundOperationTimeout = 30 * time.Second
)

// StartSessionRetentionDispatcher bounds expired authentication state without
// introducing writes into the request authentication path.
func (m *Manager) StartSessionRetentionDispatcher(ctx context.Context) {
	if m == nil || m.identities == nil {
		return
	}
	m.backgroundMu.Lock()
	if m.sessionRetentionStarted || m.backgroundWaiting {
		m.backgroundMu.Unlock()
		return
	}
	m.sessionRetentionStarted = true
	m.backgroundWG.Add(1)
	m.backgroundMu.Unlock()
	go func() {
		defer m.backgroundWG.Done()
		run := func() {
			operationCtx, cancel := context.WithTimeout(ctx, authBackgroundOperationTimeout)
			defer cancel()
			if err := m.identities.RetainExpiredSessions(operationCtx, time.Now().UTC()); err != nil && ctx.Err() == nil {
				slog.Warn("authentication session retention incomplete")
			}
		}
		run()
		ticker := time.NewTicker(authSessionRetentionInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				run()
			}
		}
	}()
}

func (m *Manager) WaitForBackgroundWorkers(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.backgroundMu.Lock()
	m.backgroundWaiting = true
	m.backgroundMu.Unlock()
	done := make(chan struct{})
	go func() {
		m.backgroundWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for auth background workers: %w", ctx.Err())
	}
}
