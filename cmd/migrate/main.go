package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/lehigh-university-libraries/scribe/internal/app"
)

func main() {
	deps, err := app.NewDependencies(context.Background(), app.BootstrapOptions{RunMigrations: true, SeedSystemContexts: true, TelemetryServiceName: "scribe-migrate"})
	if err != nil {
		slog.Error("database migration failed")
		os.Exit(1)
	}
	if err := deps.Close(); err != nil {
		os.Exit(1)
	}
}
