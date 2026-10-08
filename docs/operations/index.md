# Operations

Cloud Run hosts Scribe's application and worker. Cloud SQL for MySQL, GCS,
and Secret Manager own durable state. Production Cloud SQL uses regional HA;
there is no VM, Cloud Compose, Traefik, or cloud Vault service. Availability
settings alone do not establish a coordinated recovery objective; isolated
restore evidence remains required.

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
Cloud Run, Cloud SQL, credential, and readiness failures. Then ensure the
[backup](backup-restore.md) and [job recovery](job-recovery.md) procedures have
been exercised. Use [observability](observability.md) for health, logs, metrics,
audit metadata, queue state, and alert response.
