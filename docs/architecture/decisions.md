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

**Status:** current interim topology; non-high-availability risk acceptance is
not recorded by this document.

The public frontend and Private Power Button run on Cloud Run, as do Vault and
the private OCR helpers. MariaDB, Triplet, the API, the worker, and Traefik run
together through Cloud Compose on one COS VM in one zone. Source uploads use
GCS in cloud deployments. The VM and its persistent disks can be recreated or
restored, but there is no live database replica, second backend instance, or
cross-zone failover path.

This topology is deliberately described by its demonstrated behavior rather
than as highly available:

- a VM, zone, rollout, or shared-disk failure can make every backend component
  unavailable at once;
- API and worker capacity scale vertically, and backend rollout is a single
  failure domain; and
- backup verification limits data-loss exposure, but it does not provide
  continuous replication or automatic service recovery.

The repository currently enforces these recovery-artifact bounds:

| Recovery evidence | Freshness check | What it proves |
| --- | --- | --- |
| Paired data/Compose-volume snapshots | each snapshot is at most 36 hours old and the pair is at most 30 minutes apart | A daily protected drill can materialize and inspect a source-matched, crash-consistent database/Triplet recovery point. |
| Independent uploads copy | its successful transfer is at most 36 hours old | A separately versioned bucket has a recent source-upload copy. |
| Portable MariaDB logical dump | the inspected dump is at most 48 hours old | A database-aware fallback exists inside the restored data-disk snapshot. |
| Protected artifact-verification run | the complete verification job has a 45-minute timeout | Recovery artifacts can be materialized and inspected inside that ceiling when the workflow passes. |

`ci/cloud-snapshot-restore-drill.sh`, `ci/verify-cloud-backups.sh`, and the
protected `.github/workflows/backup-verification.yaml` job are the executable
owners of those values.

These are per-layer artifact-freshness service levels, not an application RPO.
The checks do not prove that the newest database snapshot, Triplet state, and
uploads copy share a recoverable generation. Selecting a compatible set may
require an older artifact than each independent freshness ceiling. No bounded
coordinated application RPO is established until a drill selects compatible
database/blob generations, restores every layer, and verifies the resulting
application state.

These bounds do not claim that the application will serve traffic within 45
minutes. The current drill stops after read-only inspection; it does not rebuild
the production stack, restore every layer, run managed readiness, and accept
user traffic. A bounded service recovery-time objective (RTO) is therefore
**not established**. Service restoration is operator-driven and best effort
until a full isolated rehearsal records the elapsed time
required by all steps in the
[backup and restore runbook](../operations/backup-restore.md). A failed or stale
scheduled verification means even the artifact bounds above are not met and
must be treated as an availability incident.

### Gated migration sequence

Moving the backend to managed services remains the intended way to remove the
single failure domain, but it is not a container-placement-only change. Perform
the migration in this order and do not delete the VM recovery path until the
replacement has passed its own restore and rollback drills:

1. **Prove a database-engine migration.** Cloud SQL offers MySQL, PostgreSQL,
   and SQL Server, not MariaDB. Select and pin a supported Cloud SQL for MySQL
   version, then run every migration, store, lease/fencing, outbox, and
   backup/restore contract against it. Verify SQL modes, collations, time
   precision, locking, `SKIP LOCKED`, and cutover/rollback before moving
   production data. Define private connectivity, credential rotation, HA,
   point-in-time recovery, and a tested export path as part of the same gate.
2. **Externalize Triplet's durable state.** The current Triplet service persists
   `/var/lib/triplet/presentation` on `triplet-presentation-data`; its cache is
   separate. Cloud Run container filesystems do not persist when an instance
   stops. Either move the Presentation store to a supported shared durable
   backend or prove a complete, idempotent reconstruction and reconciliation
   process from Scribe's published snapshots. Exercise concurrent replicas and
   rollback before removing the persistent volume.
3. **Move the stateless processes deliberately.** Deploy the API only after the
   database and Triplet gates are complete. For the continuously polling worker,
   choose a Cloud Run execution/billing model that allocates CPU outside HTTP
   requests, preserves graceful lease draining, and has a nonzero capacity
   floor. Load-test queue contention, revision fencing, ingress, trusted-proxy
   handling, and canonical public URLs with more than one replica.
4. **Cut over and retire automation.** Promote immutable API and worker images
   to GAR, pass managed readiness and a real upload/edit/publish flow, rehearse
   database and Triplet rollback, and observe the agreed rollback window. Only
   then remove Traefik, Cloud Compose, VM backup scripts, snapshot drills, and
   their contract tests. Replace them with Cloud SQL, Cloud Run, and Triplet
   recovery evidence before changing the recovery objectives above.

The engine and filesystem constraints come from the provider contracts, not a
repository preference: see the official
[Cloud SQL engine list](https://docs.cloud.google.com/sql/docs/introduction)
and [Cloud Run container filesystem contract](https://docs.cloud.google.com/run/docs/container-contract#filesystem).
