package database

import (
	"database/sql"
	"embed"
	"fmt"
	"strings"
)

//go:embed schema.sql
var schemaFS embed.FS

func Migrate(db *sql.DB) error {
	body, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		return fmt.Errorf("read schema: %w", err)
	}
	for _, stmt := range splitStatements(string(body)) {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("apply schema: %w\nstatement: %s", err, stmt)
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
