# Architecture decisions

This page summarizes the decisions most often needed while navigating the
code. The complete authoritative set, including persistence, editor, security,
operations, and developer-experience invariants, is the
[engineering contract](../reference/engineering-contract.md).

## Canonical correction state

IIIF Presentation 3 AnnotationPage JSON is canonical. hOCR and other exports are
derived representations.

## Page identity and tenancy

The primary identity is workspace plus item image. Imported Canvas IDs remain
targets/provenance and can repeat across workspaces.

## Concurrency

Clients save complete pages with an expected revision. Conflicts are explicit
and require rebase or user resolution; last-write-wins is not accepted.

## Extensibility

Providers and segmentors implement registered interfaces and publish capability
descriptors. UI choices and defaults come from the registry rather than copied
switch statements.

## Deployment trust

Pull-request validation and image builds receive no cloud credentials. A
same-repository PR automatically requests a preview, but deployment waits for
a required reviewer only when repository operators have configured that rule
on the protected `preview` environment. The workflow binds the job to the
environment but cannot create its protection settings. Terraform and
credentialed helpers execute from the trusted base SHA, images are promoted by
digest, and the approved PR image runs only with preview-scoped identities.

## Production topology and availability

The deployment target is entirely managed: Cloud Run hosts the frontend, API,
worker, Triplet, PDF converter, and private OCR helpers. Cloud SQL for MySQL 8.4
stores Scribe and Triplet in separate databases on one instance. GCS stores
source uploads. Secret Manager stores OAuth, provider, database, and application
token credentials. There is no Scribe-managed VM, Cloud Compose, Traefik, or
cloud Vault server.

The frontend and API share a Cloud Run instance with the Triplet image helper,
PDF converter, and Private Service Connect Cloud SQL Auth Proxy. The worker is a separate
private Cloud Run service with request-based billing, a zero instance floor,
and authenticated Pub/Sub push delivery. Cloud Scheduler wakes it every thirty
minutes for bounded recovery, outbox delivery, and retention passes; committed
application events push immediate maintenance wake-ups. Its
source-serving API and Triplet sidecars use
the same SQL and GCS state, so every replica can resolve the exact localhost
source identifiers without exposing private uploads through another public
endpoint. Cloud workers process work inside requests; local workers retain
their polling loops. No cloud work depends on CPU between requests.

Triplet's pinned SQL store provides byte-preserving Presentation resources and
transactional ETag preconditions across replicas. Container filesystems contain
only disposable derivative caches. The migration jobs apply Scribe's versioned
schema and Triplet's schema before services start; neither runtime replays DDL.

Production Cloud SQL uses regional HA, daily backups retained for fourteen days,
and seven days of transaction logs for point-in-time recovery. Production keeps
a separate daily uploads copy, versioning, and soft deletion. These settings do
not establish a coordinated application RPO or serving-application RTO; an
isolated restoration must verify database state and compatible blob versions.

Existing Vault bootstrap credentials are copied with
`make secret-manager-secrets` before removing the cloud Vault deployment.
Database-engine acceptance runs against pinned MySQL 8.4 as well as local
MariaDB; publication tests exercise two Triplet replicas sharing MySQL.

See [deployment](../operations/deployment.md),
[backup and restore](../operations/backup-restore.md), and the provider contracts:
[Cloud SQL versions](https://docs.cloud.google.com/sql/docs/mysql/db-versions),
[Cloud Run billing](https://docs.cloud.google.com/run/docs/configuring/billing-settings),
and [Private Service Connect](https://docs.cloud.google.com/sql/docs/mysql/configure-private-service-connect).
