package app

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lehigh-university-libraries/scribe/internal/auth"
	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/database"
	"github.com/lehigh-university-libraries/scribe/internal/jobqueue"
	"github.com/lehigh-university-libraries/scribe/internal/server"
	"github.com/lehigh-university-libraries/scribe/internal/store"
	"github.com/lehigh-university-libraries/scribe/internal/vaultkv"
)

// BootstrapOptions controls which startup actions run for a process.
type BootstrapOptions struct {
	RunMigrations      bool
	SeedSystemContexts bool
}

// Dependencies collects the long-lived stores and shared resources used by the
// API and worker entrypoints.
type Dependencies struct {
	AppContext             context.Context
	Config                 config.Config
	Secrets                config.Secrets
	DBPool                 *sql.DB
	OCRRunStore            *store.OCRRunStore
	ItemStore              *store.ItemStore
	ContextStore           *store.ContextStore
	AnnotationStore        *store.AnnotationStore
	TranscriptionJobStore  *store.TranscriptionJobStore
	ProviderCallAuditStore *store.ProviderCallAuditStore
	IdentityStore          *store.IdentityStore
	APIKeyStore            *store.APIKeyStore
	ProviderSecretStore    *store.ProviderSecretStore
	VaultClient            *vaultkv.Client
	AuthManager            *auth.Manager
	TranscriptionQueue     *jobqueue.PubSubTranscriptionQueue
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
	secrets, err := config.LoadSecrets(ctx, cfg)
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

	deps := &Dependencies{
		AppContext:             ctx,
		Config:                 cfg,
		Secrets:                secrets,
		DBPool:                 dbPool,
		OCRRunStore:            store.NewOCRRunStore(dbPool),
		ItemStore:              store.NewItemStore(dbPool),
		ContextStore:           store.NewContextStore(dbPool),
		AnnotationStore:        store.NewAnnotationStore(dbPool),
		TranscriptionJobStore:  store.NewTranscriptionJobStore(dbPool),
		ProviderCallAuditStore: store.NewProviderCallAuditStore(dbPool),
		IdentityStore:          store.NewIdentityStore(dbPool),
		APIKeyStore:            store.NewAPIKeyStore(dbPool),
		ProviderSecretStore:    store.NewProviderSecretStore(dbPool),
		VaultClient:            vaultkv.New(cfg.Vault.Address, cfg.Vault.Token, cfg.Vault.KVMount, cfg.Vault.GCPAuthRole),
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

	return deps, nil
}

func (d *Dependencies) Close() error {
	if d == nil {
		return nil
	}
	if d.TranscriptionQueue != nil {
		_ = d.TranscriptionQueue.Close()
	}
	if d.DBPool == nil {
		return nil
	}
	return d.DBPool.Close()
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
	h.SetAppContext(d.AppContext)
	return h
}
