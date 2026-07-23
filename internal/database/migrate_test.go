package database

import (
	"context"
	"database/sql"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

func TestSplitStatementsIgnoresCommentsAndQuotedSemicolons(t *testing.T) {
	t.Parallel()

	source := `
-- A comment containing a semicolon; none of this is executable.
CREATE TABLE example (value VARCHAR(64) DEFAULT 'semi;colon');
/* A block comment; also not executable. */
INSERT INTO example (value) VALUES ('it''s; safe'); # trailing; comment
`
	want := []string{
		"CREATE TABLE example (value VARCHAR(64) DEFAULT 'semi;colon')",
		"INSERT INTO example (value) VALUES ('it''s; safe')",
	}
	if got := splitStatements(source); !reflect.DeepEqual(got, want) {
		t.Fatalf("splitStatements() = %#v, want %#v", got, want)
	}
}

func TestEmbeddedMigrationsAreOrderedAndChecksummed(t *testing.T) {
	t.Parallel()

	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) == 0 || migrations[0].version != "0001_initial" {
		t.Fatalf("migrations = %#v", migrations)
	}
	for _, item := range migrations {
		if len(item.checksum) != 64 || len(splitStatements(item.body)) == 0 {
			t.Fatalf("invalid embedded migration %q", item.version)
		}
	}
}

func TestApplicationMigrationsDoNotMaskPartialSchemas(t *testing.T) {
	t.Parallel()

	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range migrations {
		if strings.Contains(strings.ToUpper(item.body), "CREATE TABLE IF NOT EXISTS") {
			t.Fatalf("migration %q uses conditional application DDL; partial schemas must fail instead of being repaired implicitly", item.version)
		}
	}
}

func TestApplicationMigrationsDelegateRelationshipLifecycleToRepositories(t *testing.T) {
	t.Parallel()

	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	forbidden := []string{"foreign key", "references", "on delete"}
	for _, item := range migrations {
		body := strings.ToLower(item.body)
		for _, token := range forbidden {
			if strings.Contains(body, token) {
				t.Fatalf("migration %q contains %q; repositories own relationship admission and cleanup", item.version, token)
			}
		}
	}
}

func TestValidateBootstrapCountsRejectsLegacyAndPartialSchemas(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                  string
		ledgerExists          bool
		migrationCount        int64
		applicationTableCount int64
		wantError             bool
	}{
		{name: "empty database", wantError: false},
		{name: "empty ledger", ledgerExists: true, wantError: false},
		{name: "migrated database", ledgerExists: true, migrationCount: 1, applicationTableCount: 30, wantError: false},
		{name: "legacy schema", applicationTableCount: 12, wantError: true},
		{name: "partial initial migration", ledgerExists: true, applicationTableCount: 1, wantError: true},
		{name: "impossible detached history", migrationCount: 1, wantError: true},
		{name: "invalid table count", ledgerExists: true, migrationCount: 1, applicationTableCount: -1, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := validateBootstrapCounts(test.ledgerExists, test.migrationCount, test.applicationTableCount)
			if (err != nil) != test.wantError {
				t.Fatalf("validateBootstrapCounts() error = %v, wantError %t", err, test.wantError)
			}
		})
	}
}

func TestValidateMigrationVersionsRejectsUnknownAndGappedHistory(t *testing.T) {
	t.Parallel()

	migrations := []migration{
		{version: "0001_initial"},
		{version: "0002_next"},
		{version: "0003_last"},
	}
	tests := []struct {
		name      string
		applied   []string
		wantError bool
	}{
		{name: "empty"},
		{name: "prefix", applied: []string{"0001_initial", "0002_next"}},
		{name: "all", applied: []string{"0001_initial", "0002_next", "0003_last"}},
		{name: "unknown newer version", applied: []string{"0001_initial", "0004_future"}, wantError: true},
		{name: "missing initial", applied: []string{"0002_next"}, wantError: true},
		{name: "middle gap", applied: []string{"0001_initial", "0003_last"}, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := validateMigrationVersions(migrations, test.applied)
			if (err != nil) != test.wantError {
				t.Fatalf("validateMigrationVersions() error = %v, wantError %t", err, test.wantError)
			}
		})
	}
}

func TestMigrateRecordsEachVersionOnce(t *testing.T) {
	dsn := os.Getenv("TEST_DSN")
	if dsn == "" {
		t.Skip("TEST_DSN is not set")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("second migration run was not idempotent: %v", err)
	}

	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM scribe_schema_migrations").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != len(migrations) {
		t.Fatalf("migration ledger count = %d, want %d", count, len(migrations))
	}
	var incomplete int
	if err := db.QueryRow("SELECT COUNT(*) FROM scribe_schema_migrations WHERE dirty OR applied_at IS NULL").Scan(&incomplete); err != nil {
		t.Fatal(err)
	}
	if incomplete != 0 {
		t.Fatalf("incomplete migration ledger rows = %d, want 0", incomplete)
	}
}

func TestMigrateUsesAndReleasesOneSessionLock(t *testing.T) {
	dsn := os.Getenv("TEST_DSN")
	if dsn == "" {
		t.Skip("TEST_DSN is not set")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	blocker, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()

	var acquired sql.NullInt64
	if err := blocker.QueryRowContext(ctx, "SELECT GET_LOCK(?, 1)", migrationLockName).Scan(&acquired); err != nil {
		t.Fatal(err)
	}
	if !acquired.Valid || acquired.Int64 != 1 {
		t.Fatal("test connection did not acquire migration lock")
	}

	done := make(chan error, 1)
	go func() { done <- Migrate(db) }()
	select {
	case err := <-done:
		t.Fatalf("second migrator did not wait for session lock: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	if _, err := blocker.ExecContext(ctx, "SELECT RELEASE_LOCK(?)", migrationLockName); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("waiting migrator failed after lock release: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("waiting migrator did not proceed after lock release")
	}

	verifier, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer verifier.Close()
	acquired = sql.NullInt64{}
	if err := verifier.QueryRowContext(ctx, "SELECT GET_LOCK(?, 1)", migrationLockName).Scan(&acquired); err != nil {
		t.Fatal(err)
	}
	if !acquired.Valid || acquired.Int64 != 1 {
		t.Fatal("migration lock remained held after Migrate returned")
	}
	if _, err := verifier.ExecContext(ctx, "SELECT RELEASE_LOCK(?)", migrationLockName); err != nil {
		t.Fatal(err)
	}
}

func TestSplitStatementsKeepsFinalStatementWithoutDelimiter(t *testing.T) {
	t.Parallel()

	want := []string{"SELECT `semi;colon` FROM example"}
	if got := splitStatements("SELECT `semi;colon` FROM example"); !reflect.DeepEqual(got, want) {
		t.Fatalf("splitStatements() = %#v, want %#v", got, want)
	}
}
