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

The first run creates `.env`, a local Compose override, and placeholder local
secret files when they do not exist. It builds the API, worker image, image
service, frontend, and local OCR segmentor from the current checkout, so it can
take several minutes. Open <http://localhost/> after the services report
healthy.

Stop the stack without deleting persistent volumes:

```bash
make down
```

## Greenfield database resets

Scribe maintains an ordered, checksummed migration ledger. Before the first
production deployment containing that ledger, this greenfield project permits
changes to `0001_initial.sql`; afterward, applied migrations are immutable and
schema changes require a new versioned file. When a branch changes the mutable
greenfield schema, reset only the local MariaDB data and start it clean:

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

Provider credentials are not committed to the repository. See
[configuration](../operations/configuration.md) before testing hosted
transcription providers.

Continue with [local development](local-development.md) for the daily command
loops, then make a tested [first change](first-change.md).
