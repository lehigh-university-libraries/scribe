package app

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/database"
)

func TestBrowserSessionDependenciesOpenOnlyIdentityDatabaseAndClosePool(t *testing.T) {
	connector := &closeRecordingConnector{}
	pool := sql.OpenDB(connector)
	if err := pool.PingContext(context.Background()); err != nil {
		t.Fatalf("open recording database: %v", err)
	}

	cfg := config.BrowserSessionConfig{
		PublicBaseURL: "https://scribe.example",
		Database: config.DatabaseConfig{
			DSNTemplate: "{{.User}}:{{.Password}}@tcp({{.Host}}:{{.Port}})/{{.Name}}",
			Host:        "database.internal",
			Port:        3306,
			Name:        "scribe",
			User:        "scribe_app",
		},
	}
	loadCalls := 0
	openCalls := 0
	deps, err := newBrowserSessionDependencies(context.Background(), browserSessionBootstrap{
		loadConfig: func() (config.BrowserSessionConfig, error) {
			return cfg, nil
		},
		loadDatabasePassword: func(_ context.Context, loaded config.BrowserSessionVaultConfig) (string, error) {
			loadCalls++
			if loaded != cfg.Vault {
				t.Fatalf("database secret loader received Vault config %+v", loaded)
			}
			return "database-password", nil
		},
		openDatabase: func(openCtx context.Context, dsn string, poolConfig *database.Config) (*sql.DB, error) {
			openCalls++
			if _, ok := openCtx.Deadline(); !ok {
				t.Fatal("database open did not receive the bootstrap deadline")
			}
			wantDSN := "scribe_app:database-password@tcp(database.internal:3306)/scribe"
			if dsn != wantDSN {
				t.Fatalf("database DSN = %q, want %q", dsn, wantDSN)
			}
			if poolConfig == nil || poolConfig.PingTimeout != database.DefaultConfig().PingTimeout {
				t.Fatalf("database pool config = %+v", poolConfig)
			}
			return pool, nil
		},
		bootstrapTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("newBrowserSessionDependencies returned error: %v", err)
	}
	if loadCalls != 1 || openCalls != 1 {
		t.Fatalf("bootstrap calls = secret:%d database:%d, want 1/1", loadCalls, openCalls)
	}
	if deps.PublicBaseURL != cfg.PublicBaseURL || deps.CookieName != "" || deps.CookieDomain != "" ||
		deps.dbPool != pool || deps.IdentityStore == nil {
		t.Fatalf("browser session dependencies = %+v", deps)
	}
	if err := deps.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	if !connector.closed.Load() {
		t.Fatal("Close did not close the database connection")
	}
}

func TestBrowserSessionBootstrapHasInternalDeadline(t *testing.T) {
	_, err := newBrowserSessionDependencies(context.Background(), browserSessionBootstrap{
		loadConfig: func() (config.BrowserSessionConfig, error) {
			return config.BrowserSessionConfig{}, nil
		},
		loadDatabasePassword: func(ctx context.Context, _ config.BrowserSessionVaultConfig) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
		openDatabase: func(context.Context, string, *database.Config) (*sql.DB, error) {
			t.Fatal("database opened after bootstrap deadline")
			return nil, nil
		},
		bootstrapTimeout: 10 * time.Millisecond,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bootstrap error = %v, want deadline exceeded", err)
	}
}

func TestVaultLoadRetryIsBounded(t *testing.T) {
	retryErr := errors.New("retryable")
	attempts := 0
	var delays []time.Duration
	_, err := loadVaultValueWithRetryPolicy(
		context.Background(),
		func(context.Context) (string, error) {
			attempts++
			return "", retryErr
		},
		func(err error) bool { return errors.Is(err, retryErr) },
		func(_ context.Context, delay time.Duration) error {
			delays = append(delays, delay)
			return nil
		},
	)
	if !errors.Is(err, retryErr) {
		t.Fatalf("retry error = %v, want retryable error", err)
	}
	if attempts != vaultLoadMaximumAttempts {
		t.Fatalf("load attempts = %d, want %d", attempts, vaultLoadMaximumAttempts)
	}
	wantDelays := []time.Duration{250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second}
	if !reflect.DeepEqual(delays, wantDelays) {
		t.Fatalf("retry delays = %v, want %v", delays, wantDelays)
	}
}

func TestVaultLoadRetryHonorsCancellation(t *testing.T) {
	retryErr := errors.New("retryable")
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	_, err := loadVaultValueWithRetryPolicy(
		ctx,
		func(context.Context) (string, error) {
			attempts++
			cancel()
			return "", retryErr
		},
		func(error) bool { return true },
		waitForVaultRetry,
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("retry error = %v, want context canceled", err)
	}
	if attempts != 1 {
		t.Fatalf("load attempts after cancellation = %d, want 1", attempts)
	}
}

type closeRecordingConnector struct {
	closed atomic.Bool
}

func (c *closeRecordingConnector) Connect(context.Context) (driver.Conn, error) {
	return &closeRecordingConnection{closed: &c.closed}, nil
}

func (*closeRecordingConnector) Driver() driver.Driver {
	return closeRecordingDriver{}
}

type closeRecordingDriver struct{}

func (closeRecordingDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("direct driver open is unsupported")
}

type closeRecordingConnection struct {
	closed *atomic.Bool
}

func (*closeRecordingConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is unsupported")
}

func (c *closeRecordingConnection) Close() error {
	c.closed.Store(true)
	return nil
}

func (*closeRecordingConnection) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are unsupported")
}

func (*closeRecordingConnection) Ping(context.Context) error {
	return nil
}
