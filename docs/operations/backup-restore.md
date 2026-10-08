# Backup and restore

Durable cloud state consists of the scribe and triplet Cloud SQL databases,
versioned GCS source uploads, Secret Manager credentials, and Terraform state.
Triplet's Presentation store is SQL-backed; its container cache is disposable.

Production Cloud SQL has regional HA, fourteen retained daily backups, and
seven days of binary logs for point-in-time recovery. Uploads retain object
versions and soft-deleted generations and have an independent daily Storage
Transfer copy. Terraform owns those settings. HA is an availability control,
not a replacement for backups.

## Local acceptance

```bash
make test-mysql
make backup-restore-smoke
```

The full database suite proves migrations, tenancy, canonical revisions,
concurrent job claims, lease/revision fencing, outboxes, and cleanup against
pinned MySQL 8.4. The restore smoke uses independent source/restore databases,
a logical dump containing the migration ledger, and an independent blob archive.
It verifies canonical and published pages, derived indexes, blob hashes, and
expired-job recovery. Temporary resources are removed on every exit.

## Cloud SQL restoration

Use an isolated instance and private Cloud Run jobs in the same VPC. Record the
Cloud SQL backup/PITR timestamp, upload object-generation boundary, Secret
Manager versions, and Terraform state generation together. Restore both scribe
and triplet databases; restoring only one can leave publication state stale.

Use Cloud SQL's managed export to an isolated GCS bucket as an independent
logical recovery artifact:

```bash
gcloud sql export sql INSTANCE gs://RECOVERY_BUCKET/database.sql --database=scribe,triplet --offload
gcloud sql import sql RESTORE_INSTANCE gs://RECOVERY_BUCKET/database.sql
```

The Cloud SQL instance service account needs scoped bucket permissions for this
operation. Restore source objects at compatible generations, run migration-ledger
validation, check canonical/publication references and upload hashes, then verify
that expired job/outbox leases recover. Run a real upload, correction, publication,
and export against the isolated application before considering it restored.

Secret Manager versions are separate from SQL backups. Retain the exact OAuth,
provider, database, and token versions needed by the recovery instance; ensure
its runtime identity has only its intended credential and bucket grants.

Neither a bounded coordinated application RPO nor serving-application RTO is
claimed until a timed isolated cloud restoration demonstrates them. Record
backup timestamps, object generations, integrity results, elapsed time, and
remaining errors. Never infer those objectives from regional HA alone.
