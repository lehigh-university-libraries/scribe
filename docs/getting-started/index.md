# Quick start

## Prerequisites

- Git
- jq
- Docker Engine with Docker Compose v2
- Enough disk space to build the frontend and OCR images

Check the workstation:

```bash
make doctor
```

Start the local stack:

```bash
make up
docker compose ps
```

The first run creates `.env`, a local Compose override, and missing local
secrets. Locally owned secrets are random; only the externally managed Google
credential mount starts as an empty `{}` placeholder. Compose builds any
missing API, worker, frontend, and local OCR images, so the first start can take
several minutes. Later starts reuse those images. After changing backend,
frontend, OCR, or `config.yaml` image inputs, rebuild explicitly:

```bash
make up REBUILD=true
```

Open <http://localhost/> after the services report healthy.

Stop the stack without deleting persistent volumes:

```bash
make down
```

## Reset an obsolete local database

Scribe maintains an ordered, checksummed migration ledger. The first production
ledger has been deployed, so `0001_initial.sql` is permanently immutable and
every schema change requires a new versioned file. If an older, unreleased
checkout left a local MariaDB volume with a different `0001` checksum, reset
only that local database and start it clean:

```bash
SCRIBE_CONFIRM_RESET_DEV_DB=delete-local-mariadb-data make reset-dev-db
make up
```

The target validates the exact project-owned MariaDB container and volume,
stops services that transitively depend on MariaDB, and removes only that
container and volume. Uploads, cache, and Triplet data remain intact. The
confirmation value is deliberately verbose because the database deletion is
permanent. The helper refuses to run in CI and must never be used against a
shared or production deployment.

New local stacks copy `sample.env` and default to the current `canonical-v2`
persistence namespace. Startup does not overwrite an existing `.env`; a
developer who intentionally cuts an older local stack over must stop it and
change `SCRIBE_DATA_GENERATION` to `canonical-v2` explicitly. The new namespace
starts empty. Its existing `canonical-v1` volumes are retained for explicit
recovery; changing the namespace does not migrate or delete them.

Provider credentials are not committed to the repository. See
[configuration](../operations/configuration.md) before testing hosted
transcription providers.

Continue with [local development](local-development.md) for the daily command
loops, learn when to [choose a processing context](using-contexts.md), then make
a tested [first change](first-change.md).
