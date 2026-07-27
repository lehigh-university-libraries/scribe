package database

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
)

func TestBackupRestoreMigrationLedger(t *testing.T) {
	if os.Getenv("SCRIBE_RESTORE_SMOKE") != "1" {
		t.Skip("SCRIBE_RESTORE_SMOKE is not enabled")
	}
	phase := strings.TrimSpace(os.Getenv("SCRIBE_RESTORE_PHASE"))
	if phase != "source" && phase != "restore" {
		t.Fatalf("SCRIBE_RESTORE_PHASE = %q; want source or restore", phase)
	}
	dsn := strings.TrimSpace(os.Getenv("TEST_DSN"))
	if dsn == "" {
		t.Fatal("TEST_DSN is required for the migration restore smoke test")
	}

	db, err := NewPool(dsn, DefaultConfig())
	if err != nil {
		t.Fatalf("open %s database: %v", phase, err)
	}
	defer db.Close()

	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("load embedded migrations: %v", err)
	}
	if phase == "source" {
		installReleasedInitialMigrationFixture(t, db, migrations)
	}
	if phase == "restore" {
		assertMigrationLedger(t, db, migrations, "before restored migration validation")
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate %s database: %v", phase, err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("repeat %s migration: %v", phase, err)
	}
	if phase == "source" {
		assertReleasedInitialMigrationUpgrade(t, db)
	}
	assertMigrationLedger(t, db, migrations, "after migration validation")
}

func installReleasedInitialMigrationFixture(t *testing.T, db *sql.DB, migrations []migration) {
	t.Helper()
	if len(migrations) < 2 || migrations[0].version != "0001_initial" {
		t.Fatalf("upgrade fixture requires released 0001 followed by a later migration: %#v", migrations)
	}
	if migrations[0].checksum != releasedInitialMigrationChecksum {
		t.Fatalf("released 0001 checksum = %q, want %q", migrations[0].checksum, releasedInitialMigrationChecksum)
	}

	var existingTables int
	if err := db.QueryRow(`SELECT COUNT(*)
FROM information_schema.tables
WHERE table_schema = DATABASE()`).Scan(&existingTables); err != nil {
		t.Fatalf("inspect source migration fixture database: %v", err)
	}
	if existingTables != 0 {
		t.Fatalf("source migration fixture database contains %d tables; refusing to replace shared state", existingTables)
	}

	if _, err := db.Exec(`CREATE TABLE scribe_schema_migrations (
  version VARCHAR(255) NOT NULL PRIMARY KEY,
  checksum CHAR(64) NOT NULL,
  dirty BOOLEAN NOT NULL DEFAULT TRUE,
  applied_at DATETIME(6) NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`); err != nil {
		t.Fatalf("create released migration ledger fixture: %v", err)
	}
	for _, statement := range splitStatements(migrations[0].body) {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("apply released 0001 fixture: %v\nstatement: %s", err, statement)
		}
	}
	if _, err := db.Exec(
		`INSERT INTO scribe_schema_migrations (version, checksum, dirty, applied_at)
VALUES (?, ?, FALSE, CURRENT_TIMESTAMP(6))`,
		migrations[0].version,
		migrations[0].checksum,
	); err != nil {
		t.Fatalf("record released 0001 fixture: %v", err)
	}

	const workspaceID = 99002
	if _, err := db.Exec(`INSERT INTO workspaces
  (id, owner_user_id, name, slug, is_personal, created_by_user_id)
VALUES (?, 1, 'Migration upgrade workspace', 'migration-upgrade-workspace', FALSE, 1)`, workspaceID); err != nil {
		t.Fatalf("insert released workspace fixture: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO items
  (id, user_id, workspace_id, name, source_type, source_url, metadata)
VALUES ('migration-upgrade-item', 1, ?, 'Migration upgrade item', 'url', 'https://source.example/upgrade.png', '{"preserved":true}')`, workspaceID); err != nil {
		t.Fatalf("insert released item fixture: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO event_outbox
  (event_id, event_type, workspace_id, subject, body_json)
VALUES ('migration-upgrade-event', 'dev.scribe.transcription.completed', ?, 'item-images/99002', '{"preserved":true}')`, workspaceID); err != nil {
		t.Fatalf("insert released event fixture: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO webhook_deliveries
  (event_id, target_url, target_hash, status)
VALUES ('migration-upgrade-event', 'https://legacy.example/hooks/scribe', ?, 'pending')`, strings.Repeat("a", 64)); err != nil {
		t.Fatalf("insert unsigned legacy delivery fixture: %v", err)
	}
}

func assertReleasedInitialMigrationUpgrade(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()

	var externalReferenceID, callerIdempotencyKey, metadata string
	if err := db.QueryRowContext(ctx, `SELECT external_reference_id, caller_idempotency_key, metadata
FROM items
WHERE id = 'migration-upgrade-item'`).Scan(&externalReferenceID, &callerIdempotencyKey, &metadata); err != nil {
		t.Fatalf("load upgraded item fixture: %v", err)
	}
	if externalReferenceID != "" || callerIdempotencyKey != "" || metadata != `{"preserved":true}` {
		t.Fatalf("upgraded item fields = %q/%q/%q; want empty new identifiers and preserved metadata", externalReferenceID, callerIdempotencyKey, metadata)
	}

	var eventCount, discardedLegacyDeliveryCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_outbox WHERE event_id = 'migration-upgrade-event'`).Scan(&eventCount); err != nil {
		t.Fatalf("count preserved upgrade event: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM webhook_deliveries WHERE event_id = 'migration-upgrade-event'`).Scan(&discardedLegacyDeliveryCount); err != nil {
		t.Fatalf("count transitioned webhook deliveries: %v", err)
	}
	if eventCount != 1 || discardedLegacyDeliveryCount != 0 {
		t.Fatalf("upgrade retained events/deliveries = %d/%d, want 1/0", eventCount, discardedLegacyDeliveryCount)
	}

	var transitionTables, finalColumns int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*)
FROM information_schema.tables
WHERE table_schema = DATABASE()
  AND table_name IN ('editor_review_tokens', 'editor_review_sessions', 'webhook_subscriptions')`).Scan(&transitionTables); err != nil {
		t.Fatalf("inspect upgraded tables: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*)
FROM information_schema.columns
WHERE table_schema = DATABASE()
  AND table_name = 'webhook_deliveries'
  AND column_name = 'subscription_id'`).Scan(&finalColumns); err != nil {
		t.Fatalf("inspect upgraded delivery schema: %v", err)
	}
	if transitionTables != 3 || finalColumns != 1 {
		t.Fatalf("upgraded tables/subscription column = %d/%d, want 3/1", transitionTables, finalColumns)
	}
	var forbiddenColumns, legacyTables int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*)
FROM information_schema.columns
WHERE table_schema = DATABASE()
  AND table_name = 'webhook_deliveries'
  AND column_name IN ('target_url', 'target_hash')`).Scan(&forbiddenColumns); err != nil {
		t.Fatalf("inspect removed unsigned delivery fields: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*)
FROM information_schema.tables
WHERE table_schema = DATABASE()
  AND table_name IN ('webhook_deliveries_v2', 'webhook_deliveries_legacy_unsigned')`).Scan(&legacyTables); err != nil {
		t.Fatalf("inspect transition table cleanup: %v", err)
	}
	if forbiddenColumns != 0 || legacyTables != 0 {
		t.Fatalf("legacy delivery columns/tables remain = %d/%d", forbiddenColumns, legacyTables)
	}

	result, err := db.ExecContext(ctx, `INSERT INTO webhook_subscriptions
  (workspace_id, target_url, target_hash, signing_secret)
VALUES (99002, 'https://signed.example/hooks/scribe', ?, ?)`, strings.Repeat("b", 64), strings.Repeat("s", 32))
	if err != nil {
		t.Fatalf("insert signed subscription after upgrade: %v", err)
	}
	subscriptionID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("read upgraded subscription ID: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO webhook_deliveries
  (event_id, subscription_id, status)
VALUES ('migration-upgrade-event', ?, 'delivered')`, subscriptionID); err != nil {
		t.Fatalf("insert signed delivery after upgrade: %v", err)
	}
}

func assertMigrationLedger(t *testing.T, db *sql.DB, migrations []migration, stage string) {
	t.Helper()

	rows, err := db.Query(`SELECT version, checksum, dirty, applied_at
FROM scribe_schema_migrations
ORDER BY version`)
	if err != nil {
		t.Fatalf("query migration ledger %s: %v", stage, err)
	}
	defer rows.Close()

	type ledgerRow struct {
		version   string
		checksum  string
		dirty     bool
		appliedAt sql.NullTime
	}
	actual := make([]ledgerRow, 0, len(migrations))
	for rows.Next() {
		var row ledgerRow
		if err := rows.Scan(&row.version, &row.checksum, &row.dirty, &row.appliedAt); err != nil {
			t.Fatalf("scan migration ledger %s: %v", stage, err)
		}
		actual = append(actual, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate migration ledger %s: %v", stage, err)
	}
	if len(actual) != len(migrations) {
		t.Fatalf("migration ledger rows %s = %d; want %d", stage, len(actual), len(migrations))
	}
	for index, expected := range migrations {
		row := actual[index]
		if row.version != expected.version || row.checksum != expected.checksum {
			t.Fatalf(
				"migration ledger row %d %s = version %q checksum %q; want %q %q",
				index,
				stage,
				row.version,
				row.checksum,
				expected.version,
				expected.checksum,
			)
		}
		if row.dirty || !row.appliedAt.Valid {
			t.Fatalf("migration ledger row %q %s is not clean and applied: dirty=%t applied_at=%v", row.version, stage, row.dirty, row.appliedAt)
		}
	}
}
