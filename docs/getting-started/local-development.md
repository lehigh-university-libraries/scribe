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
# Fast pre-push loop: lint, generated drift, cached Go unit tests
make check

# Complete component checks
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

`make check` is an iteration aid, not the release contract. Its backend step
uses the exact host Go version and race detector when they are available, runs
packages in parallel, and lets Go's build/test cache rerun affected packages.
It deliberately does not attach to MariaDB. Use `make test-backend` for the
uncached full backend gate; when Compose MariaDB is active, that target includes
the DB-backed tests. Both targets fall back to the prepared, pinned test-runner
image when the host toolchain is unavailable.

`make up` reuses existing service images and builds only missing ones. Set
`REBUILD=true` after changing an image input:

```bash
make up REBUILD=true
```

When local development points at Vault, startup prepares the shared
`scribe-api:local` image before the one-shot `vault-init` container runs. This
also honors `REBUILD=true`; digest-pinned or operator-selected images remain
pull-only.

The application runtime reads the `config.yaml` baked into its backend image;
Compose does not replace that file with a host bind mount. A `config.yaml`
change therefore requires the explicit rebuild above.

For Vite hot reload, start the Compose stack and run `npm --prefix web run dev`.
The dev proxy uses the Compose edge at `http://localhost`; set
`SCRIBE_DEV_BACKEND_ORIGIN` or `SCRIBE_DEV_PRESENTATION_ORIGIN` only when
running those services elsewhere. Image API requests go through the Compose
edge to Triplet; Triplet dereferences immutable originals through Scribe's
constrained source route, matching production.

## Cloud OCR from local development

`make up-cloud-ocr` runs the frontend, API, worker, database, and Triplet
locally while the API and worker call private dev Cloud Run segmentation and
Kraken services. It uses
`docker-compose.override.cloud-example.yaml`; that override deliberately has no
local `segmentor` service. Host-side Vite hot reload continues to work with
`npm --prefix web run dev` after the stack is ready.

This path has two operator-supplied inputs:

1. The exact dev-only Cloud Run origins and the server-owned model endpoint
   maps. Each authenticated audience must equal the canonical HTTPS origin,
   without a path or trailing slash.
2. A keyless Application Default Credentials (ADC) file that impersonates the
   dev-only `scribe-dev-external` service account. The initiating user or group
   receives `roles/iam.serviceAccountTokenCreator` on that one account, and the
   account receives `roles/run.invoker` on dev Kraken/segmentor services only.
   No downloadable service-account private key is used.

Copy `sample.env` to `.env` if needed, then set these non-secret values using
the real `https://...run.app` origins supplied by the operator:

```dotenv
GCLOUD_PROJECT=your-dev-project
OLLAMA_URL=https://OLLAMA-SERVICE-PROJECT.REGION.run.app
OLLAMA_AUDIENCE=https://OLLAMA-SERVICE-PROJECT.REGION.run.app
OLLAMA_MODEL_ENDPOINTS_JSON={"glm-ocr:bf16":{"url":"https://OLLAMA-SERVICE-PROJECT.REGION.run.app","audience":"https://OLLAMA-SERVICE-PROJECT.REGION.run.app"}}
SEGMENTATION_SERVICE_URL=https://SEGMENTOR-SERVICE-PROJECT.REGION.run.app
SEGMENTATION_SERVICE_AUDIENCE=https://SEGMENTOR-SERVICE-PROJECT.REGION.run.app
SEGMENTATION_MODEL_ENDPOINTS_JSON={"kraken":{"url":"https://SEGMENTOR-SERVICE-PROJECT.REGION.run.app","audience":"https://SEGMENTOR-SERVICE-PROJECT.REGION.run.app"}}
KRAKEN_MODEL_ENDPOINTS_JSON={"catmus-print-fondue-large.mlmodel":{"url":"https://KRAKEN-SERVICE-PROJECT.REGION.run.app","audience":"https://KRAKEN-SERVICE-PROJECT.REGION.run.app"}}
```

`GCLOUD_PROJECT` must be the project that owns the dev-only
`scribe-dev-external` identity. Compose supplies that same value to the API and
worker, and `make up-cloud-ocr` resolves it from the active Compose
configuration before accepting the ADC. A missing, malformed, mismatched, or
different-project credential fails before any container starts.

After an operator has reviewed and applied the dev Terraform IAM change, create
the repository-local ADC file through the browser-backed `gcloud` login:

```bash
GCLOUD_PROJECT=your-dev-project \
  scripts/configure-dev-cloud-ocr.sh configure
make up-cloud-ocr
```

The helper invokes `gcloud auth application-default login
--impersonate-service-account=...` in an isolated CLI configuration and installs
the resulting `impersonated_service_account` ADC at
`secrets/GOOGLE_APPLICATION_CREDENTIALS` with mode `0600`. It refuses to
overwrite an existing file and never creates or downloads a service-account
key. The ADC still contains a user refresh token and is secret material. The
entire `secrets/` directory is ignored by Git, but that is only a last line of
defense: never stage, paste, or copy the credential into logs or `.env`.

Both the startup target and Scribe validate the credential before use. They
accept only the exact `scribe-dev-external` account in the project resolved
from the active Compose configuration, Google's fixed OAuth/IAM endpoints, an
`authorized_user` source, no delegation chain, and no private key. Success
means `docker compose ps` reports the local services healthy and
`docker compose config --services` does not list `segmentor`.

Neither `make up` nor `make up-cloud-ocr` overwrites an existing
`docker-compose.override.yaml`. If the local-segmentor override already exists,
preserve it outside the active Compose filename (the ignored `secrets/`
directory is suitable) before running the cloud target. The cloud target
refuses to start while the active override still defines `segmentor`; it never
silently turns a cloud invocation into the local OCR workflow. It also verifies
that the API and worker resolve identical `https://...run.app` endpoint maps
and exact matching audiences before creating or starting containers. Restore
the desired override explicitly when switching workflows.

The repository's Ollama services are intentionally owned by the production
workspace and are not included in this dev identity's invoker grant. Ollama-
backed contexts are therefore unavailable through this dev-only boundary. Do
not broaden the shared production IAM policy for local development; a future
dev-owned Ollama service would need an explicit architecture and IAM change.
The override still requires a syntactically valid Ollama endpoint configuration
because API and worker configuration stays identical, but this identity cannot
invoke the production-owned service. Segmentation and Kraken transcription do
not depend on Ollama.

Rotate or revoke the local ADC through the same helper:

```bash
GCLOUD_PROJECT=your-dev-project \
  scripts/configure-dev-cloud-ocr.sh rotate
GCLOUD_PROJECT=your-dev-project \
  scripts/configure-dev-cloud-ocr.sh revoke
```

Rotation obtains and installs a new ADC before revoking the prior one. Until
`gcloud` reports success, the helper retains the prior ADC as a mode-`0600`
recovery file beside the current credential. If rotation reports a revocation
failure, do not delete that file: rerun `rotate` to retry the pending revocation,
or run `revoke` to revoke both credentials. Revocation removes each local file
only after `gcloud` reports success. When finished, run `make down` and revoke
the ADC. A lost or exposed ADC is an incident: stop the stack and revoke it
immediately rather than merely deleting or replacing the file.

`make docs-build` builds the Zensical site strictly into ignored `site/`;
`make docs` is an alias. `make docs-serve` starts the live-reload server at
<http://localhost:8000/>. Both targets build and use the pinned local Zensical
image without modifying the host Python environment.

Run the full release contract with `make ci`. The same component scripts are
used in GitHub Actions.

If only a database is needed, use `make up-db`. Database-backed Go tests detect
the Compose MariaDB service and receive `TEST_DSN` automatically.

## Reset an obsolete local database

An existing local database correctly refuses startup when its checksummed
migration history does not match the permanently frozen migration files. This
can happen when an unreleased checkout created the volume before `0001` was
frozen. Reset only the local MariaDB data with:

```bash
SCRIBE_CONFIRM_RESET_DEV_DB=delete-local-mariadb-data make reset-dev-db
make up-db
```

The reset resolves the repository's exact Compose project, verifies the
generated MariaDB data volume and container labels and mount, then stops only
services that transitively depend on MariaDB. It does not delete uploads,
caches, or Triplet data. The target is intentionally unavailable in CI and is
not a shared-environment or production recovery command.
