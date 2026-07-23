package database

import (
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
	if phase == "restore" {
		assertMigrationLedger(t, db, migrations, "before restored migration validation")
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate %s database: %v", phase, err)
	}
	assertMigrationLedger(t, db, migrations, "after migration validation")
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
