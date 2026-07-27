package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/lehigh-university-libraries/scribe/internal/db"
)

const (
	MaxWebhookSubscriptionsPerWorkspace = 100
	MinWebhookSigningSecretBytes        = 32
	MaxWebhookSigningSecretBytes        = 1024
)

var (
	ErrWebhookSubscriptionExists = errors.New("webhook target is already subscribed")
	ErrWebhookSubscriptionLimit  = errors.New("workspace webhook subscription limit reached")
)

// WebhookSubscription is one workspace-scoped target. SigningSecret is loaded
// only for delivery and must never be returned from an API or written to logs.
type WebhookSubscription struct {
	ID            uint64
	WorkspaceID   uint64
	TargetURL     string
	SigningSecret []byte `json:"-"`
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type WebhookSubscriptionStore struct {
	pool *sql.DB
	q    *db.Queries
}

func NewWebhookSubscriptionStore(pool *sql.DB) *WebhookSubscriptionStore {
	return &WebhookSubscriptionStore{pool: pool, q: db.New(pool)}
}

func (s *WebhookSubscriptionStore) Create(ctx context.Context, workspaceID uint64, targetURL, secret string) (WebhookSubscription, error) {
	if s == nil || s.pool == nil || workspaceID == 0 {
		return WebhookSubscription{}, fmt.Errorf("create webhook subscription: store and workspace are required")
	}
	targetURL = strings.TrimSpace(targetURL)
	if targetURL == "" || len(targetURL) > 2048 {
		return WebhookSubscription{}, fmt.Errorf("create webhook subscription: target URL is invalid")
	}
	if strings.TrimSpace(secret) != secret || len([]byte(secret)) < MinWebhookSigningSecretBytes || len([]byte(secret)) > MaxWebhookSigningSecretBytes {
		return WebhookSubscription{}, fmt.Errorf("create webhook subscription: signing secret must contain between %d and %d bytes without surrounding whitespace", MinWebhookSigningSecretBytes, MaxWebhookSigningSecretBytes)
	}

	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return WebhookSubscription{}, fmt.Errorf("create webhook subscription: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.q.WithTx(tx)
	if _, err := queries.LockWorkspaceManual(ctx, workspaceID); err != nil {
		return WebhookSubscription{}, fmt.Errorf("create webhook subscription: lock workspace: %w", err)
	}
	count, err := queries.CountWebhookSubscriptionsForWorkspaceManual(ctx, workspaceID)
	if err != nil {
		return WebhookSubscription{}, fmt.Errorf("create webhook subscription: count subscriptions: %w", err)
	}
	if count >= MaxWebhookSubscriptionsPerWorkspace {
		return WebhookSubscription{}, ErrWebhookSubscriptionLimit
	}
	digest := sha256.Sum256([]byte(targetURL))
	result, err := queries.CreateWebhookSubscriptionManual(ctx, db.CreateWebhookSubscriptionManualParams{
		WorkspaceID:   workspaceID,
		TargetUrl:     targetURL,
		TargetHash:    hex.EncodeToString(digest[:]),
		SigningSecret: []byte(secret),
	})
	if err != nil {
		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
			return WebhookSubscription{}, ErrWebhookSubscriptionExists
		}
		return WebhookSubscription{}, fmt.Errorf("create webhook subscription: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil || id <= 0 {
		return WebhookSubscription{}, fmt.Errorf("create webhook subscription: read inserted identity")
	}
	row, err := queries.GetWebhookSubscriptionManual(ctx, db.GetWebhookSubscriptionManualParams{ID: uint64(id), WorkspaceID: workspaceID})
	if err != nil {
		return WebhookSubscription{}, fmt.Errorf("create webhook subscription: reload: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return WebhookSubscription{}, fmt.Errorf("create webhook subscription: commit: %w", err)
	}
	return webhookSubscriptionFromRow(row), nil
}

func (s *WebhookSubscriptionStore) List(ctx context.Context, workspaceID uint64) ([]WebhookSubscription, error) {
	if s == nil || s.q == nil || workspaceID == 0 {
		return nil, fmt.Errorf("list webhook subscriptions: store and workspace are required")
	}
	rows, err := s.q.ListWebhookSubscriptionsManual(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list webhook subscriptions: %w", err)
	}
	result := make([]WebhookSubscription, 0, len(rows))
	for _, row := range rows {
		result = append(result, webhookSubscriptionFromListRow(row))
	}
	return result, nil
}

func (s *WebhookSubscriptionStore) Delete(ctx context.Context, workspaceID, subscriptionID uint64) error {
	if s == nil || s.pool == nil || workspaceID == 0 || subscriptionID == 0 {
		return sql.ErrNoRows
	}
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("delete webhook subscription: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.q.WithTx(tx)
	if _, err := queries.LockWorkspaceManual(ctx, workspaceID); err != nil {
		return fmt.Errorf("delete webhook subscription: lock workspace: %w", err)
	}
	if _, err := queries.LockWebhookSubscriptionManual(ctx, db.LockWebhookSubscriptionManualParams{ID: subscriptionID, WorkspaceID: workspaceID}); err != nil {
		return err
	}
	if err := queries.DeleteWebhookDeliveriesForSubscriptionManual(ctx, subscriptionID); err != nil {
		return fmt.Errorf("delete webhook subscription: delete pending deliveries: %w", err)
	}
	result, err := queries.DeleteWebhookSubscriptionManual(ctx, db.DeleteWebhookSubscriptionManualParams{ID: subscriptionID, WorkspaceID: workspaceID})
	if err != nil {
		return fmt.Errorf("delete webhook subscription: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return sql.ErrNoRows
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("delete webhook subscription: commit: %w", err)
	}
	return nil
}

func webhookSubscriptionFromRow(row db.GetWebhookSubscriptionManualRow) WebhookSubscription {
	return WebhookSubscription{ID: row.ID, WorkspaceID: row.WorkspaceID, TargetURL: row.TargetUrl, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}

func webhookSubscriptionFromListRow(row db.ListWebhookSubscriptionsManualRow) WebhookSubscription {
	return WebhookSubscription{ID: row.ID, WorkspaceID: row.WorkspaceID, TargetURL: row.TargetUrl, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}
