# Database migrations

The backend embeds ordered migrations from `internal/database/migrations` and
runs them in a finite Cloud Run migration job before creating service revisions.
Local API startup runs the same migrator. A MySQL/MariaDB advisory lock is held
on one pinned database session for the entire run, preventing concurrent schema
changes.

Create a new file named with the next four-digit prefix, for example:

```text
internal/database/migrations/0002_add_example.sql
```

Applied files are immutable. The migration ledger stores a SHA-256 checksum and
startup fails if an applied file changes, if history contains an unknown newer
version, or if applied history has a gap. Never edit, rename, or reorder an
applied migration; add a later migration instead. MariaDB DDL may commit
implicitly, so the migrator writes a durable dirty row before the first
statement and marks it complete only after every statement succeeds. A crash
or failed statement therefore makes subsequent startup fail closed.

After changing the schema, update SQL queries and run:

```bash
make generate
make generate-check
make test
```

The database tests run the migrator twice, verify lock wait/release behavior,
and compare the migration ledger with the embedded files. The backup/restore
gate creates its source schema through that migrator, dumps and restores the
ledger, and checks every restored checksum before rerunning migration
validation. CI also creates a fresh database for the end-to-end gates; a
migration that works only against an existing developer database is not
acceptable.

`0001_initial.sql` is immutable; use additive versioned files for schema work.
The initial migration is pinned by an executable checksum assertion.

`0002_islandora_editor_review.sql` replaces the old process-configured,
unsigned webhook delivery queue with workspace-owned signed subscriptions.
Existing unsigned queue rows have no subscription identity or receiver-known
signing secret, so the migration deliberately removes those attempts while
retaining their durable `event_outbox` parents. Administrators must create an
explicit subscription for future events after the upgrade. The backup/restore
source phase applies the released `0001` ledger first and verifies this exact
upgrade and data-retention boundary before producing its backup.

The initial migration is accepted only for an empty database. A non-empty
schema without completed migration history is rejected instead of being
stamped current through `CREATE TABLE IF NOT EXISTS`. The Cloud Run schema jobs
initialize fresh Cloud SQL databases. See [deployment](../operations/deployment.md#apply).

A local volume created by an unreleased checkout that rewrote
`0001_initial.sql` will not start once the released checksum is restored. Keep
the fail-closed ledger invariant and reset only that development database
instead:

```bash
SCRIBE_CONFIRM_RESET_DEV_DB=delete-local-mariadb-data make reset-dev-db
make up-db
```

The helper validates the exact Compose MariaDB container and project-owned
volume before deletion. It leaves uploads, cache, and Triplet volumes intact;
it is not permitted in CI and is not for shared or production databases. See
[local development](../getting-started/local-development.md#reset-an-obsolete-local-database)
for the full workflow.
