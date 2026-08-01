# Operations

Production uses digest-pinned containers, versioned Terraform providers and
modules, a protected deployment environment, Vault-managed secrets, private OCR
helpers, and persistent database/blob storage.

The current backend is one Cloud Compose VM in one zone and is not highly
available. Its independent snapshot/upload artifacts have 36-hour freshness
checks and its logical MariaDB fallback has a 48-hour freshness check. Those
checks do not prove a mutually compatible recovery generation, so no bounded
coordinated application recovery-point objective (RPO) is established. The
protected workflow has 45 minutes to materialize and inspect the recovery
artifacts, but no bounded service recovery-time objective (RTO) is established
because that probe does not restore a serving application. This operator-driven
risk requires explicit owner acceptance while the migration gates in
[architecture decisions](../architecture/decisions.md#production-topology-and-availability)
remain open. Treat a stale or failed recovery verification as an availability
incident.

Before deployment:

```bash
make ci
```

Every push to `main` requests a production apply. The repository workflow binds
the credentialed job to the `production` GitHub environment; an operator must
configure that environment with required reviewers before release so the job
waits for approval.
Use manual dispatch with `mode=plan` for a non-mutating plan.
Same-repository pull requests automatically request a preview deployment,
which binds credentialed work to the `preview` environment. Required reviewers
for that environment are likewise an externally configured release
prerequisite.
Forks receive secret-free CI only.

Start with [configuration](configuration.md) and [deployment](deployment.md).
Use the bounded [production troubleshooting](troubleshooting.md) runbook for
boot, Compose, network, and managed-readiness failures. Then ensure the
[backup](backup-restore.md) and [job recovery](job-recovery.md) procedures have
been exercised. Use [observability](observability.md) for health, logs, metrics,
audit metadata, queue state, and alert response.
