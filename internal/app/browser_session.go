package app

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/database"
	"github.com/lehigh-university-libraries/scribe/internal/store"
)

const browserSessionBootstrapTimeout = 90 * time.Second

// BrowserSessionDependencies contains the narrow database-backed identity
// boundary required by the trusted browser-session mint command.
type BrowserSessionDependencies struct {
	PublicBaseURL string
	CookieName    string
	CookieDomain  string
	IdentityStore *store.IdentityStore
	dbPool        *sql.DB
}

type browserSessionBootstrap struct {
	loadConfig           func() (config.BrowserSessionConfig, error)
	loadDatabasePassword func(context.Context, config.BrowserSessionVaultConfig) (string, error)
	openDatabase         func(context.Context, string, *database.Config) (*sql.DB, error)
	bootstrapTimeout     time.Duration
}

// NewBrowserSessionDependencies validates configuration, reads only the
// database Vault secret, and opens the identity store used to mint a browser
// session. It does not initialize the full application runtime.
func NewBrowserSessionDependencies(ctx context.Context) (*BrowserSessionDependencies, error) {
	return newBrowserSessionDependencies(ctx, browserSessionBootstrap{
		loadConfig:           config.LoadBrowserSessionConfig,
		loadDatabasePassword: loadDatabasePasswordWithRetry,
		openDatabase:         database.NewPoolContext,
		bootstrapTimeout:     browserSessionBootstrapTimeout,
	})
}

func newBrowserSessionDependencies(ctx context.Context, bootstrap browserSessionBootstrap) (*BrowserSessionDependencies, error) {
	bootstrapCtx, cancel := context.WithTimeout(ctx, bootstrap.bootstrapTimeout)
	defer cancel()

	cfg, err := bootstrap.loadConfig()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	databasePassword, err := bootstrap.loadDatabasePassword(bootstrapCtx, cfg.Vault)
	if err != nil {
		return nil, fmt.Errorf("load database secret: %w", err)
	}
	databaseDSN := cfg.Database.BuildDSN(databasePassword)

	dbPool, err := bootstrap.openDatabase(bootstrapCtx, databaseDSN, database.DefaultConfig())
	if err != nil {
		return nil, fmt.Errorf("connect database: %w", err)
	}
	return &BrowserSessionDependencies{
		PublicBaseURL: cfg.PublicBaseURL,
		CookieName:    cfg.CookieName,
		CookieDomain:  cfg.CookieDomain,
		IdentityStore: store.NewIdentityStore(dbPool),
		dbPool:        dbPool,
	}, nil
}

// Close releases the browser-session database pool.
func (d *BrowserSessionDependencies) Close() error {
	if d == nil || d.dbPool == nil {
		return nil
	}
	if err := d.dbPool.Close(); err != nil {
		return fmt.Errorf("close database: %w", err)
	}
	return nil
}
