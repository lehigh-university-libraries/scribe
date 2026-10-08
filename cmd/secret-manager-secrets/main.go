// secret-manager-secrets copies bootstrap credential maps from Vault to GCP.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/secretmanager"
	"github.com/lehigh-university-libraries/scribe/internal/vaultkv"
)

type secretStore interface {
	Read(context.Context, string) (map[string]string, error)
	Write(context.Context, string, map[string]string) error
}

func main() {
	workspace := flag.String("workspace", "dev", "dev or prod")
	project := flag.String("project", "", "destination GCP project")
	flag.Parse()
	if *workspace != "dev" && *workspace != "prod" {
		fmt.Fprintln(os.Stderr, "workspace must be dev or prod")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	prefix := "scribe-" + *workspace + "-secret"
	if *workspace == "prod" {
		prefix = "scribe-secret"
	}
	destination, err := secretmanager.New(ctx, *project, prefix)
	if err != nil {
		fmt.Fprintln(os.Stderr, "initialize Secret Manager failed")
		os.Exit(1)
	}
	if os.Getenv("VAULT_ADDR") == "" || os.Getenv("VAULT_TOKEN") == "" {
		fmt.Fprintln(os.Stderr, "VAULT_ADDR and VAULT_TOKEN are required; use make secret-manager-secrets")
		os.Exit(2)
	}
	source := vaultkv.New(os.Getenv("VAULT_ADDR"), os.Getenv("VAULT_TOKEN"), "secret", "")
	if token := os.Getenv("VAULT_ADMIN_TOKEN"); token != "" {
		source.UseAdminToken(token)
	}
	if err := copySecrets(ctx, source, destination, "scribe/"+*workspace, os.Stdout); err != nil {
		// Backend error strings may contain credential payloads. Report the
		// reviewed operation only, without interpolating the source error.
		fmt.Fprintln(os.Stderr, "copy application secrets failed")
		os.Exit(1)
	}
}

func copySecrets(ctx context.Context, source, destination secretStore, prefix string, out io.Writer) error {
	for _, suffix := range []string{"google_oauth", "openai", "gemini", "database/app"} {
		path := prefix + "/" + suffix
		values, err := source.Read(ctx, path)
		if err != nil {
			if (suffix == "openai" || suffix == "gemini") && vaultkv.IsNotFound(err) {
				values = map[string]string{}
			} else {
				return errors.New("read source secret")
			}
		}
		if suffix == "database/app" && values["password"] == "" {
			return errors.New("missing database password")
		}
		if suffix == "google_oauth" && (values["client_id"] == "" || values["client_secret"] == "") {
			return errors.New("missing OAuth credentials")
		}
		current, err := destination.Read(ctx, path)
		if err != nil && !secretmanager.IsNotFound(err) {
			return errors.New("read destination secret")
		}
		if err != nil || !maps.Equal(current, values) {
			if err := destination.Write(ctx, path, values); err != nil {
				return errors.New("write destination secret")
			}
		}
		verified, err := destination.Read(ctx, path)
		if err != nil || !maps.Equal(verified, values) {
			return errors.New("verify destination secret")
		}
		if _, err := fmt.Fprintln(out, "Copied and verified", suffix); err != nil {
			return err
		}
	}
	return nil
}
