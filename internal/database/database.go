package database

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

type Config struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
	PingTimeout     time.Duration
}

func DefaultConfig() *Config {
	return &Config{
		MaxOpenConns:    10,
		MaxIdleConns:    10,
		ConnMaxLifetime: 5 * time.Minute,
		ConnMaxIdleTime: 1 * time.Minute,
		PingTimeout:     5 * time.Second,
	}
}

func NewPool(dsn string, cfg *Config) (*sql.DB, error) {
	return NewPoolContext(context.Background(), dsn, cfg)
}

// NewPoolContext opens and verifies a database pool while honoring the caller's
// cancellation in addition to the configured ping timeout.
func NewPoolContext(ctx context.Context, dsn string, cfg *Config) (*sql.DB, error) {
	if cfg == nil {
		cfg = DefaultConfig()
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}

	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	db.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)

	pingCtx, cancel := context.WithTimeout(ctx, cfg.PingTimeout)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping mysql: %w", err)
	}

	return db, nil
}
