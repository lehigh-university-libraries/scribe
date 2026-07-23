package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

const (
	migrationLockName    = "scribe-schema-migrations-v1"
	migrationLedgerTable = "scribe_schema_migrations"
)

type migration struct {
	version  string
	checksum string
	body     string
}

func Migrate(db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("database is required")
	}
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("reserve database migration connection: %w", err)
	}
	defer conn.Close()

	acquired, err := acquireMigrationLock(ctx, conn)
	if err != nil {
		return err
	}
	if !acquired {
		return fmt.Errorf("timed out waiting for database migration lock")
	}
	defer func() {
		// Advisory locks are scoped to this exact MariaDB session. A background
		// context gives release its best chance even if a future caller adds a
		// cancelable migration context.
		_, _ = conn.ExecContext(context.Background(), "SELECT RELEASE_LOCK(?)", migrationLockName)
	}()

	if err := validateMigrationBootstrapState(ctx, conn); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS scribe_schema_migrations (
  version VARCHAR(255) NOT NULL PRIMARY KEY,
  checksum CHAR(64) NOT NULL,
  dirty BOOLEAN NOT NULL DEFAULT TRUE,
  applied_at DATETIME(6) NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`); err != nil {
		return fmt.Errorf("create migration ledger: %w", err)
	}

	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	if err := validateMigrationHistory(ctx, conn, migrations); err != nil {
		return err
	}
	for _, item := range migrations {
		var appliedChecksum string
		var dirty bool
		err := conn.QueryRowContext(ctx, "SELECT checksum, dirty FROM scribe_schema_migrations WHERE version = ?", item.version).Scan(&appliedChecksum, &dirty)
		switch {
		case err == nil:
			if dirty {
				return fmt.Errorf("migration %s did not complete; reset this greenfield database before retrying", item.version)
			}
			if appliedChecksum != item.checksum {
				return fmt.Errorf("migration %s checksum changed after it was applied", item.version)
			}
			continue
		case !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("read migration %s ledger row: %w", item.version, err)
		}

		// MariaDB DDL commits implicitly, so the migration itself cannot be made
		// atomic. Record a durable dirty marker first: a crash or failed statement
		// then fails closed on the next startup instead of treating a partially
		// created schema as current.
		if _, err := conn.ExecContext(ctx,
			"INSERT INTO scribe_schema_migrations (version, checksum, dirty, applied_at) VALUES (?, ?, TRUE, NULL)",
			item.version,
			item.checksum,
		); err != nil {
			return fmt.Errorf("mark migration %s in progress: %w", item.version, err)
		}
		for _, stmt := range splitStatements(item.body) {
			if _, err := conn.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("apply migration %s: %w\nstatement: %s", item.version, err, stmt)
			}
		}
		result, err := conn.ExecContext(ctx,
			`UPDATE scribe_schema_migrations
SET dirty = FALSE, applied_at = CURRENT_TIMESTAMP(6)
WHERE version = ? AND checksum = ? AND dirty = TRUE`,
			item.version,
			item.checksum,
		)
		if err != nil {
			return fmt.Errorf("complete migration %s: %w", item.version, err)
		}
		updated, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("verify migration %s completion: %w", item.version, err)
		}
		if updated != 1 {
			return fmt.Errorf("complete migration %s: updated %d ledger rows, want 1", item.version, updated)
		}
	}
	return nil
}

// validateMigrationHistory refuses a database created by a newer or different
// build and refuses gaps in the applied sequence. Silently accepting either
// case can run an older binary against a schema whose invariants it does not
// understand, or apply an earlier migration after a later one already ran.
func validateMigrationHistory(ctx context.Context, conn *sql.Conn, migrations []migration) error {
	rows, err := conn.QueryContext(ctx, "SELECT version FROM scribe_schema_migrations ORDER BY version")
	if err != nil {
		return fmt.Errorf("read migration history: %w", err)
	}
	defer rows.Close()

	applied := make([]string, 0, len(migrations))
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			return fmt.Errorf("scan migration history: %w", err)
		}
		applied = append(applied, version)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate migration history: %w", err)
	}
	if err := validateMigrationVersions(migrations, applied); err != nil {
		return fmt.Errorf("invalid migration history: %w", err)
	}
	return nil
}

func validateMigrationVersions(migrations []migration, applied []string) error {
	known := make(map[string]int, len(migrations))
	for index, item := range migrations {
		known[item.version] = index
	}

	present := make(map[string]struct{}, len(applied))
	highest := -1
	for _, version := range applied {
		index, ok := known[version]
		if !ok {
			return fmt.Errorf("database contains unknown migration %q; use the matching application version", version)
		}
		present[version] = struct{}{}
		if index > highest {
			highest = index
		}
	}
	for index := 0; index <= highest; index++ {
		if _, ok := present[migrations[index].version]; !ok {
			return fmt.Errorf("migration %q is missing before an applied later version", migrations[index].version)
		}
	}
	return nil
}

// validateMigrationBootstrapState prevents the greenfield initial migration
// from being stamped onto a legacy or partially created schema. CREATE TABLE IF
// NOT EXISTS would otherwise leave old table shapes untouched while recording
// the new migration as successfully applied.
func validateMigrationBootstrapState(ctx context.Context, conn *sql.Conn) error {
	var schema sql.NullString
	if err := conn.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&schema); err != nil {
		return fmt.Errorf("resolve migration database: %w", err)
	}
	if !schema.Valid || strings.TrimSpace(schema.String) == "" {
		return fmt.Errorf("a database must be selected before migrations run")
	}

	var ledgerCount int64
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*)
FROM information_schema.tables
WHERE table_schema = ? AND table_name = ?`, schema.String, migrationLedgerTable).Scan(&ledgerCount); err != nil {
		return fmt.Errorf("inspect migration ledger: %w", err)
	}
	if ledgerCount > 1 {
		return fmt.Errorf("database %q has an ambiguous migration ledger", schema.String)
	}

	var applicationTableCount int64
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*)
FROM information_schema.tables
WHERE table_schema = ? AND table_name <> ?`, schema.String, migrationLedgerTable).Scan(&applicationTableCount); err != nil {
		return fmt.Errorf("inspect existing database objects: %w", err)
	}

	var appliedMigrationCount int64
	if ledgerCount == 1 {
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM scribe_schema_migrations").Scan(&appliedMigrationCount); err != nil {
			return fmt.Errorf("inspect migration history: %w", err)
		}
	}
	if err := validateBootstrapCounts(ledgerCount == 1, appliedMigrationCount, applicationTableCount); err != nil {
		return fmt.Errorf("database %q is not a clean migration target: %w", schema.String, err)
	}
	return nil
}

func validateBootstrapCounts(ledgerExists bool, migrationCount, applicationTableCount int64) error {
	if migrationCount < 0 || applicationTableCount < 0 {
		return fmt.Errorf("database object counts cannot be negative")
	}
	if !ledgerExists && migrationCount != 0 {
		return fmt.Errorf("migration history exists without its ledger")
	}
	if migrationCount == 0 && applicationTableCount != 0 {
		return fmt.Errorf("found %d application tables without completed migration history; reset this greenfield database", applicationTableCount)
	}
	return nil
}

func acquireMigrationLock(ctx context.Context, conn *sql.Conn) (bool, error) {
	var acquired sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, 30)", migrationLockName).Scan(&acquired); err != nil {
		return false, fmt.Errorf("acquire database migration lock: %w", err)
	}
	return acquired.Valid && acquired.Int64 == 1, nil
}

func loadMigrations() ([]migration, error) {
	paths, err := fs.Glob(migrationFS, "migrations/[0-9][0-9][0-9][0-9]_*.sql")
	if err != nil {
		return nil, fmt.Errorf("list migrations: %w", err)
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return nil, fmt.Errorf("no embedded database migrations found")
	}
	items := make([]migration, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		base := filepath.Base(path)
		version := strings.TrimSuffix(base, filepath.Ext(base))
		if _, duplicate := seen[version]; duplicate {
			return nil, fmt.Errorf("duplicate database migration %s", version)
		}
		seen[version] = struct{}{}
		body, err := migrationFS.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", version, err)
		}
		sum := sha256.Sum256(body)
		items = append(items, migration{
			version:  version,
			checksum: fmt.Sprintf("%x", sum[:]),
			body:     string(body),
		})
	}
	return items, nil
}

// splitStatements splits a SQL file on statement delimiters while ignoring
// semicolons in quoted values and comments. It intentionally supports the SQL
// constructs used by the embedded schema instead of depending on a naive
// strings.Split, which can turn prose in a comment into executable SQL.
func splitStatements(source string) []string {
	var (
		statements     []string
		statement      strings.Builder
		quote          byte
		inLineComment  bool
		inBlockComment bool
	)

	flush := func() {
		if value := strings.TrimSpace(statement.String()); value != "" {
			statements = append(statements, value)
		}
		statement.Reset()
	}

	for i := 0; i < len(source); i++ {
		current := source[i]

		if inLineComment {
			if current == '\n' {
				inLineComment = false
				statement.WriteByte(current)
			}
			continue
		}
		if inBlockComment {
			if current == '*' && i+1 < len(source) && source[i+1] == '/' {
				inBlockComment = false
				i++
				statement.WriteByte(' ')
			}
			continue
		}
		if quote != 0 {
			statement.WriteByte(current)
			if current == '\\' && i+1 < len(source) {
				i++
				statement.WriteByte(source[i])
				continue
			}
			if current == quote {
				if i+1 < len(source) && source[i+1] == quote {
					i++
					statement.WriteByte(source[i])
					continue
				}
				quote = 0
			}
			continue
		}

		switch {
		case (current == '-' && i+1 < len(source) && source[i+1] == '-') || current == '#':
			inLineComment = true
			if current == '-' {
				i++
			}
		case current == '/' && i+1 < len(source) && source[i+1] == '*':
			inBlockComment = true
			i++
		case current == '\'' || current == '"' || current == '`':
			quote = current
			statement.WriteByte(current)
		case current == ';':
			flush()
		default:
			statement.WriteByte(current)
		}
	}
	flush()
	return statements
}
