# Local development

The pinned host toolchain is recorded in `.go-version`, `.nvmrc`, and
`.tool-versions`. Backend containers and CI use the patched Go release from
`.go-version`; frontend builds use the Node release from `.nvmrc`.

The minimal local prerequisites are Git, jq, and Docker with the Compose v2
plugin. `make doctor` reports any missing requirement before startup.

Install repository-local generators and scanners:

```bash
make install-tools
make install-doc-tools
```

`make install-tools` verifies and installs checksum-pinned ripgrep and yq, then
uses the pinned host Go toolchain for the remaining binaries. Everything is
written under ignored `.tools/`. `make install-doc-tools` needs only Docker and
builds the pinned Zensical image; it does not modify the host Python
environment.

For a narrower install, use `make install-shell-tools` (ripgrep and yq),
`make install-codegen-tools` (Buf/sqlc), or `make install-security-tools`
(gosec/govulncheck).

Common loops:

```bash
# Backend and contracts
make generate
make lint
make test-backend

# Frontend and Mirador plugin
make test-frontend
make test-browser

# Persistence recovery and dependency safety
make backup-restore-smoke
make security
make dependency-scan

# Documentation
make docs-build
make docs-serve
```

For Vite hot reload, start the Compose stack and run `npm --prefix web run dev`.
The dev proxy uses the Compose edge at `http://localhost`; set
`SCRIBE_DEV_BACKEND_ORIGIN` or `SCRIBE_DEV_PRESENTATION_ORIGIN` only when
running those services elsewhere. Image API requests go through the Compose
edge to Triplet; Triplet dereferences immutable originals through Scribe's
constrained source route, matching production.

`make docs-build` builds the Zensical site strictly into ignored `site/`;
`make docs` is an alias. `make docs-serve` starts the live-reload server at
<http://localhost:8000/>. Both targets build and use the pinned local Zensical
image without modifying the host Python environment.

Run the full pre-push contract with `make ci`. The same component scripts are
used in GitHub Actions.

If only a database is needed, use `make up-db`. Database-backed Go tests detect
the Compose MariaDB service and receive `TEST_DSN` automatically.

## Reset a greenfield database

An existing local database correctly refuses startup when its checksummed
migration history no longer matches a changed greenfield baseline. Reset only
the local MariaDB data with:

```bash
SCRIBE_CONFIRM_RESET_DEV_DB=delete-local-mariadb-data make reset-dev-db
make up-db
```

The reset resolves the repository's exact Compose project, verifies the
generated MariaDB data volume and container labels and mount, then stops only
services that transitively depend on MariaDB. It does not delete uploads,
caches, or Triplet data. The target is intentionally unavailable in CI and is
not a shared-environment or production recovery command.
