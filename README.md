# Scribe

Scribe processes handwritten documents with reusable segmentation and
transcription contexts, then lets users correct the result in a text-first
Mirador editor. Correction state is stored as IIIF Presentation 3
AnnotationPages using the IIIF Text Granularity Extension.

Scribe accepts a single image URL, a single upload, a multi-file ingest, or a
IIIF manifest. Its Connect API is also intended for external IIIF editors and
plugins.

> Scribe is greenfield software under active hardening. Treat `main` as a
> development branch until every published
> [release criterion](docs/reference/release-criteria.md) is backed by a passing
> automated check.

## Run locally

Prerequisites are Git, jq, and Docker with the Compose v2 plugin.

```bash
make doctor
make up
docker compose ps
```

The first start builds the frontend and local OCR helper and may take several
minutes. Open <http://localhost/> once services are healthy. Stop without
deleting persistent volumes with `make down`.

## Develop

Pinned runtime versions are in `.go-version`, `.nvmrc`, and `.tool-versions`.
Install repository-local generators, scanners, and documentation tooling:

```bash
make install-tools
make install-doc-tools
```

Useful commands:

```bash
make generate          # protobuf, Connect, TypeScript, OpenAPI, and sqlc
make lint              # Go, shell, formatting, and protobuf lint
make test              # backend, web, and Mirador plugin
make test-browser      # real Chromium editor acceptance in a pinned container
make e2e-smoke         # DB-backed ingest/revision checks
make backup-restore-smoke # isolated database/blob recovery verification
make security          # Go and npm vulnerability/security checks
make dependency-scan   # Trivy scan of locked production dependencies
make docs-build        # strict Zensical build into ./site
make docs-serve        # local Zensical documentation with live reload
make ci                # local equivalent of required CI gates
```

Generated files are committed. CI runs `make generate-check` and fails if they
do not match their protobuf or SQL sources.

## Documentation

The [published Scribe documentation](https://lehigh-university-libraries.github.io/scribe/)
is built from [docs/index.md](docs/index.md) and includes:

- [local development](docs/getting-started/local-development.md)
- [canonical IIIF model](docs/concepts/canonical-iiif.md)
- [architecture and data flow](docs/architecture/index.md)
- [adding providers, segmentors, and RPCs](docs/development/index.md)
- [external API integration](docs/api/index.md)
- [deployment and operations](docs/operations/index.md)

Build the site with pinned Zensical using `make docs-build` (`make docs` is an
alias).

## Repository layout

```text
cmd/                 Go API, worker, and segmentor entrypoints
internal/            application, domain, adapters, persistence, and auth
proto/               Connect/protobuf source contracts
sqlc/                SQL query source
web/                  TypeScript application shell
mirador-scribe/       reusable Mirador 4 OCR editor plugin
terraform/            GCP infrastructure
ci/                   local/CI shared quality commands
docs/                 Zensical documentation source
```

Secrets belong in Vault or local Compose secret files. Do not commit credentials
or put them in `.env`, configuration YAML, Terraform variables, build arguments,
or provider error messages.
