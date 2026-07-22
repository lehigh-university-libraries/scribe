package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/providersecret"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

const (
	providerSecretCleanupBatchSize = 100
	providerSecretPendingGrace     = 5 * time.Minute
	providerSecretCleanupInterval  = time.Minute
)

type inactiveProviderSecretDeleter interface {
	DeleteInactive(context.Context, uint64, uint64) error
}

func cleanupTrackedProviderSecret(
	ctx context.Context,
	vaultPrefix string,
	vault vaultClient,
	metadata inactiveProviderSecretDeleter,
	secret store.ProviderSecret,
) error {
	if vault == nil || metadata == nil || secret.ID == 0 || secret.WorkspaceID == 0 {
		return fmt.Errorf("provider secret cleanup is not configured")
	}
	if secret.LifecycleState != store.ProviderSecretCleanupPending {
		return fmt.Errorf("provider secret is not scheduled for cleanup")
	}
	if err := providersecret.ValidateVaultPath(vaultPrefix, secret.WorkspaceID, secret.VaultPath); err != nil {
		return fmt.Errorf("provider secret cleanup path is invalid")
	}
	if err := vault.Delete(ctx, secret.VaultPath); err != nil {
		return fmt.Errorf("delete provider secret material: %w", err)
	}
	if err := metadata.DeleteInactive(ctx, secret.ID, secret.WorkspaceID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("delete provider secret locator: %w", err)
	}
	return nil
}

func (m *Manager) scheduleProviderSecretCleanup(ctx context.Context, secret *store.ProviderSecret) {
	if m == nil || m.providerSecrets == nil || secret == nil {
		return
	}
	if err := m.providerSecrets.MarkPendingCleanup(ctx, secret.ID, secret.WorkspaceID); err != nil {
		// The existing pending/active locator is the only durable cross-system
		// reference. Do not touch Vault until the hidden cleanup transition is
		// confirmed; a later request or stale-pending reconciliation can retry.
		return
	}
	secret.LifecycleState = store.ProviderSecretCleanupPending
	// Best-effort immediate cleanup reduces residue after ordinary failures. If
	// either system is unavailable, the durable inactive locator remains for the
	// worker reconciler; it is never returned by provider-secret read queries.
	_ = m.cleanupProviderSecret(ctx, *secret)
}

func (m *Manager) cleanupProviderSecret(ctx context.Context, secret store.ProviderSecret) error {
	return cleanupTrackedProviderSecret(ctx, m.providerSecretVaultPrefix(), m.vault, m.providerSecrets, secret)
}

// ReconcileProviderSecretCleanups retries cleanup_pending rows immediately and
// treats stale pending_write rows as interrupted creates. Vault deletion and
// inactive-row deletion are both idempotent, so concurrent workers are safe.
func (m *Manager) ReconcileProviderSecretCleanups(ctx context.Context) (int, error) {
	if m == nil || m.providerSecrets == nil || m.vault == nil {
		return 0, fmt.Errorf("provider secret cleanup is not configured")
	}
	candidates, err := m.providerSecrets.ListCleanupCandidates(ctx, time.Now().UTC().Add(-providerSecretPendingGrace), providerSecretCleanupBatchSize)
	if err != nil {
		return 0, fmt.Errorf("list provider secret cleanup candidates: %w", err)
	}
	cleaned := 0
	failed := 0
	for _, secret := range candidates {
		if secret.LifecycleState == store.ProviderSecretPendingWrite {
			if err := m.providerSecrets.MarkPendingCleanup(ctx, secret.ID, secret.WorkspaceID); err != nil {
				failed++
				continue
			}
			secret.LifecycleState = store.ProviderSecretCleanupPending
		}
		if err := m.cleanupProviderSecret(ctx, secret); err != nil {
			failed++
			continue
		}
		cleaned++
	}
	if failed > 0 {
		return cleaned, fmt.Errorf("%d provider secret cleanup attempts failed", failed)
	}
	return cleaned, nil
}

// StartProviderSecretCleanupDispatcher owns the durable Vault/DB reconciliation
// loop. It belongs in cmd/worker, alongside the other outbox and retention
// consumers, and stops promptly when the worker context is canceled.
func (m *Manager) StartProviderSecretCleanupDispatcher(ctx context.Context) {
	if m == nil || m.providerSecrets == nil || m.vault == nil {
		return
	}
	m.backgroundMu.Lock()
	if m.providerCleanupStarted || m.backgroundWaiting {
		m.backgroundMu.Unlock()
		return
	}
	m.providerCleanupStarted = true
	m.backgroundWG.Add(1)
	m.backgroundMu.Unlock()
	go func() {
		defer m.backgroundWG.Done()
		run := func() {
			operationCtx, cancel := context.WithTimeout(ctx, authBackgroundOperationTimeout)
			defer cancel()
			cleaned, err := m.ReconcileProviderSecretCleanups(operationCtx)
			if err != nil && ctx.Err() == nil {
				slog.Warn("provider secret cleanup reconciliation incomplete", "cleaned", cleaned)
			}
		}
		run()
		ticker := time.NewTicker(providerSecretCleanupInterval)
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
