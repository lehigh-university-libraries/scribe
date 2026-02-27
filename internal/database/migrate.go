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
    if _, err := db.Exec(string(body)); err != nil {
      return fmt.Errorf("apply %s: %w", file, err)
    }
  }

  return nil
}
