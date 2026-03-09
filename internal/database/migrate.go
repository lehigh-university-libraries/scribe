package database

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

func Migrate(db *sql.DB) error {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}

	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		files = append(files, "migrations/"+entry.Name())
	}
	sort.Strings(files)

	for _, file := range files {
		body, err := migrationsFS.ReadFile(file)
		if err != nil {
			return fmt.Errorf("read %s: %w", file, err)
		}
		for _, stmt := range splitStatements(string(body)) {
			if _, err := db.Exec(stmt); err != nil {
				return fmt.Errorf("apply %s: %w\nstatement: %s", file, err, stmt)
			}
		}
	}

	return nil
}

// splitStatements splits a SQL file into individual statements on semicolons,
// skipping blank entries and comment-only blocks.
func splitStatements(sql string) []string {
	var stmts []string
	for _, s := range strings.Split(sql, ";") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		// Skip lines that are entirely comments
		allComment := true
		for _, line := range strings.Split(s, "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "--") {
				allComment = false
				break
			}
		}
		if !allComment {
			stmts = append(stmts, s)
		}
	}
	return stmts
}
