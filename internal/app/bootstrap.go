package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/auth"
	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/database"
	"github.com/lehigh-university-libraries/scribe/internal/jobqueue"
	"github.com/lehigh-university-libraries/scribe/internal/safelog"
	"github.com/lehigh-university-libraries/scribe/internal/server"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	"github.com/lehigh-university-libraries/scribe/internal/telemetry"
	"github.com/lehigh-university-libraries/scribe/internal/uploadblob"
	"github.com/lehigh-university-libraries/scribe/internal/vaultkv"
)

// BootstrapOptions controls which startup actions run for a process.
type BootstrapOptions struct {
	RunMigrations             bool
	SeedSystemContexts        bool
	TelemetryServiceName      string
	ObserveTranscriptionQueue bool
}

// Dependencies collects the long-lived stores and shared resources used by the
// API and worker entrypoints.
type Dependencies struct {
	AppContext               context.Context
	Config                   config.Config
	Secrets                  config.Secrets
	DBPool                   *sql.DB
	OCRRunStore              *store.OCRRunStore
	ItemStore                *store.ItemStore
	ContextStore             *store.ContextStore
	AnnotationStore          *store.AnnotationStore
	TranscriptionJobStore    *store.TranscriptionJobStore
	WebhookSubscriptionStore *store.WebhookSubscriptionStore
	ProviderCallAuditStore   *store.ProviderCallAuditStore
	IdentityStore            *store.IdentityStore
	APIKeyStore              *store.APIKeyStore
	ProviderSecretStore      *store.ProviderSecretStore
	VaultClient              *vaultkv.Client
	AuthManager              *auth.Manager
	TranscriptionQueue       *jobqueue.PubSubTranscriptionQueue
	Telemetry                *telemetry.Runtime
}

// NewDependencies loads config + secrets, opens the DB, runs migrations, and
// assembles the long-lived application dependencies. It also installs the
// loaded runtime into the package-level accessor in internal/config so scattered
// request-path callers can reach config values without explicit plumbing.
func NewDependencies(ctx context.Context, opts BootstrapOptions) (*Dependencies, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	if err := preflightServiceIdentity(ctx, cfg); err != nil {
		return nil, fmt.Errorf("preflight service identity: %w", err)
	}
	secrets, err := loadSecretsWithRetry(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("load secrets: %w", err)
	}
	cfg.DatabaseDSN = cfg.Database.BuildDSN(secrets.DatabasePassword)
	config.Init(config.Runtime{Config: cfg, Secrets: secrets})

	dbPool, err := database.NewPool(cfg.DatabaseDSN, database.DefaultConfig())
	if err != nil {
		return nil, fmt.Errorf("connect database: %w", err)
	}

	if opts.RunMigrations {
		if err := database.Migrate(dbPool); err != nil {
			_ = dbPool.Close()
			return nil, fmt.Errorf("run migrations: %w", err)
		}
	}
	jobAdmission, err := store.NewTranscriptionJobAdmissionPolicy(cfg.Transcription.MaxActiveJobsPerWorkspace)
	if err != nil {
		_ = dbPool.Close()
		return nil, fmt.Errorf("configure transcription job admission: %w", err)
	}

	deps := &Dependencies{
		AppContext:               ctx,
		Config:                   cfg,
		Secrets:                  secrets,
		DBPool:                   dbPool,
		OCRRunStore:              store.NewOCRRunStore(dbPool),
		ItemStore:                store.NewItemStore(dbPool),
		ContextStore:             store.NewContextStore(dbPool),
		AnnotationStore:          store.NewAnnotationStoreWithTranscriptionAdmission(dbPool, jobAdmission),
		TranscriptionJobStore:    store.NewTranscriptionJobStoreWithAdmission(dbPool, jobAdmission),
		WebhookSubscriptionStore: store.NewWebhookSubscriptionStore(dbPool),
		ProviderCallAuditStore:   store.NewProviderCallAuditStore(dbPool),
		IdentityStore:            store.NewIdentityStore(dbPool),
		APIKeyStore:              store.NewAPIKeyStore(dbPool),
		ProviderSecretStore:      store.NewProviderSecretStore(dbPool),
		VaultClient:              vaultkv.New(cfg.Vault.Address, cfg.Vault.Token, cfg.Vault.KVMount, cfg.Vault.GCPAuthRole),
	}
	if jobqueue.Enabled(cfg.Transcription.Queue) {
		q, err := jobqueue.NewPubSubTranscriptionQueue(ctx, cfg.Transcription.Queue, cfg.Transcription.JobWorkers)
		if err != nil {
			_ = dbPool.Close()
			return nil, fmt.Errorf("configure transcription queue: %w", err)
		}
		deps.TranscriptionQueue = q
	}

	authManager, err := auth.NewManager(cfg, secrets, deps.IdentityStore, deps.APIKeyStore, deps.ProviderSecretStore, deps.ItemStore, deps.ContextStore, deps.TranscriptionJobStore, deps.VaultClient)
	if err != nil {
		_ = deps.Close()
		return nil, fmt.Errorf("configure auth: %w", err)
	}
	deps.AuthManager = authManager

	if opts.SeedSystemContexts {
		if err := EnsureSystemContexts(ctx, cfg, deps.ContextStore); err != nil {
			_ = deps.Close()
			return nil, fmt.Errorf("seed system contexts: %w", err)
		}
	}

	telemetryOptions := telemetry.Options{ServiceName: opts.TelemetryServiceName}
	if opts.ObserveTranscriptionQueue {
		telemetryOptions.QueueSnapshot = func(snapshotCtx context.Context) (int64, time.Duration, int64, error) {
			snapshot, snapshotErr := deps.TranscriptionJobStore.ClaimableQueueSnapshot(snapshotCtx)
			return snapshot.Depth, snapshot.OldestAge, snapshot.ExpiredLeases, snapshotErr
		}
	}
	deps.Telemetry, err = telemetry.Start(ctx, cfg.Observability, telemetryOptions)
	if err != nil {
		slog.Warn(
			"telemetry initialization failed; continuing without affected exporters",
			"error_type", safelog.ErrorType(err),
			"category", safelog.ErrorCategory(err),
		)
	}

	return deps, nil
}

func loadSecretsWithRetry(ctx context.Context, cfg config.Config) (config.Secrets, error) {
	return loadVaultValueWithRetry(ctx, func(loadCtx context.Context) (config.Secrets, error) {
		return config.LoadSecrets(loadCtx, cfg)
	})
}

func loadDatabasePasswordWithRetry(ctx context.Context, cfg config.BrowserSessionVaultConfig) (string, error) {
	return loadVaultValueWithRetry(ctx, func(loadCtx context.Context) (string, error) {
		return config.LoadDatabasePassword(loadCtx, cfg)
	})
}

const (
	vaultLoadMaximumAttempts = 5
	vaultLoadInitialDelay    = 250 * time.Millisecond
	vaultLoadMaximumDelay    = 4 * time.Second
)

func loadVaultValueWithRetry[T any](ctx context.Context, load func(context.Context) (T, error)) (T, error) {
	return loadVaultValueWithRetryPolicy(ctx, load, vaultkv.IsRetryable, waitForVaultRetry)
}

func loadVaultValueWithRetryPolicy[T any](
	ctx context.Context,
	load func(context.Context) (T, error),
	retryable func(error) bool,
	wait func(context.Context, time.Duration) error,
) (T, error) {
	delay := vaultLoadInitialDelay
	var zero T
	var lastErr error
	for attempt := 1; attempt <= vaultLoadMaximumAttempts; attempt++ {
		value, err := load(ctx)
		if err == nil {
			return value, nil
		}
		lastErr = err
		if !retryable(err) || attempt == vaultLoadMaximumAttempts {
			break
		}
		slog.Warn("vault secrets load failed; retrying", "attempt", attempt, "error_type", safelog.ErrorType(err), "category", safelog.ErrorCategory(err))
		if err := wait(ctx, delay); err != nil {
			return zero, err
		}
		delay *= 2
		if delay > vaultLoadMaximumDelay {
			delay = vaultLoadMaximumDelay
		}
	}
	return zero, lastErr
}

func waitForVaultRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (d *Dependencies) Close() error {
	if d == nil {
		return nil
	}
	var closeErrors []error
	if d.Telemetry != nil {
		if err := d.Telemetry.Close(); err != nil {
			// Telemetry is deliberately outside the process availability contract.
			// A backend outage must not turn an otherwise graceful shutdown into an
			// application failure, and error strings may include client topology.
			slog.Warn(
				"telemetry shutdown did not complete cleanly",
				"error_type", safelog.ErrorType(err),
				"category", safelog.ErrorCategory(err),
			)
		}
	}
	if d.TranscriptionQueue != nil {
		if err := d.TranscriptionQueue.Close(); err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("close transcription queue: %w", err))
		}
	}
	if err := uploadblob.Close(); err != nil {
		closeErrors = append(closeErrors, fmt.Errorf("close upload storage: %w", err))
	}
	if d.DBPool != nil {
		if err := d.DBPool.Close(); err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("close database: %w", err))
		}
	}
	return errors.Join(closeErrors...)
}

func (d *Dependencies) NewHandler() *server.Handler {
	h := server.NewHandler(
		d.OCRRunStore,
		d.ItemStore,
		d.ContextStore,
		d.AnnotationStore,
		d.TranscriptionJobStore,
		d.AuthManager,
		d.ProviderSecretStore,
		d.VaultClient,
		d.ProviderCallAuditStore,
	)
	if d.TranscriptionQueue != nil {
		h.SetTranscriptionJobQueue(d.TranscriptionQueue)
	}
	h.SetWebhookSubscriptionStore(d.WebhookSubscriptionStore)
	h.SetAppContext(d.AppContext)
	return h
}
