# Backup and restore

A recoverable Scribe deployment needs coordinated backups of:

1. MariaDB, including canonical pages, revisions, jobs, audits, and outbox;
2. uploaded source blobs;
3. any Triplet material that cannot be deterministically regenerated;
4. Vault storage according to the Vault operator procedure;
5. Terraform state.

Record the database snapshot time and blob-version boundary together. Encrypt
backups, restrict restore identities, and test restore into an isolated
workspace on a schedule.

The protected production deployment path enforces the deployed recovery layers:

- Cloud Compose writes nightly logical MariaDB dumps under
  `/mnt/disks/data/backups/mariadb`. Terraform explicitly sizes that data disk
  for runtime overhead, the retained complete dump, staging space, and a
  full-dump safety margin. The writer stages atomically, verifies gzip content
  and a completion marker, then retains the newest complete copy.
- Daily and weekly immutable snapshot policies cover the data and
  Compose-volume disks. These disk snapshots are crash-consistent recovery
  points; the completed logical dump captured on the data-disk snapshot is the
  portable, database-aware restore artifact. Together they survive loss of the
  VM or either live disk without adding a third attachment.
- Source uploads have GCS versioning and 30-day soft delete.
- A daily Storage Transfer job copies production uploads to an independent,
  versioned backup bucket with retained noncurrent generations.
- Vault data and initialization-material buckets use versioning and soft delete.
- Production plan/apply refuses to proceed until
  `ci/verify-cloud-backups.sh` confirms that the externally managed Terraform
  state bucket has versioning and at least 14 days of retention or soft delete.
- Production apply inspects one saved Terraform plan and rejects deletion or
  replacement of either Cloud Compose persistent disk before applying that
  exact plan. Capacity growth remains an in-place update.

The rollout from the former dedicated MariaDB backup disk deliberately removes
that disk from Terraform state without destroying it. Keep the orphaned
`scribe-mariadb-backups` disk as a recovery source until a fresh logical dump
has been captured on the data disk and the protected two-disk restore drill has
passed. Removing the retired disk is a separate, explicitly approved operation;
normal deployment must never delete it.

The protected `Production Backup Verification` workflow runs daily with the
dedicated `BACKUP_GCLOUD_OIDC_POOL` and `BACKUP_RESTORE_GSA`. It verifies every
bucket policy and requires a successful upload transfer no older than 36
hours. It then selects fresh,
source-matched snapshots for both production disks, creates two distinct
disposable restore disks, and attaches them read-only (`ro,noload`) to an
isolated no-service-account, no-external-address VM behind priority-zero IPv4
and IPv6 deny-egress rules. The probe verifies the MariaDB dump freshness,
gzip stream, completion marker, required canonical tables, and persistent
MariaDB volume before cleanup. A scheduled failure opens an issue. A manual
dispatch can additionally download one exact backup object into the ephemeral
runner, verify it, and discard it.

`BACKUP_RESTORE_GSA` is not an application or Terraform deployment identity.
Terraform grants only its custom disposable compute drill role, Storage
Transfer viewer, bucket metadata reads, and object reads from Terraform state
and the independent upload backup. It has no source-upload write, Vault token,
Vault root-object decrypt, KMS decrypt, runtime, or broad project role. The WIF
provider, binding, and initially deployable service account are external
bootstrap prerequisites. Use a pool containing one active GitHub provider,
restrict it to this repository, `backup-verification.yaml`,
`refs/heads/main`, and the protected `production` environment, and bind only
the repository-scoped principal set. The workflow verifies that live boundary
before reading state or creating restore resources. The protected deploy
identity must be able to grant the listed resource bindings during the first
production apply; the verifier must not be given that grant authority.

After restore, run persistence integrity checks before accepting traffic:

- every item image resolves to one tenant-scoped canonical page;
- every public AnnotationPage snapshot resolves to its canonical image and a
  real committed revision;
- referenced source blobs exist;
- page/index revisions agree;
- outbox and leased jobs are reset according to the recovery policy;
- a sample manifest, edit load, and export validates successfully.

The repository exercises this procedure without touching development data:

```bash
make backup-restore-smoke
make verify-cloud-backups-test
make cloud-snapshot-restore-drill-test
```

The smoke test creates isolated source and restore MariaDB containers and blob
volumes, creates the source schema through the embedded migrator, restores a
logical database dump and blob archive, and verifies the clean migration
ledger and checksums with a second migration pass. It then validates the
canonical and published IIIF pages plus the derived index through the
production stores and confirms that an expired job at its attempt limit is
fenced into `failed`.
Every resource is uniquely named and removed when the test exits.

## Isolated production restore drill

1. Record the database snapshot timestamp, uploads backup generation boundary,
   Vault generations, and Terraform state generation. Never restore one layer
   without recording the others.
2. Run the protected backup workflow. Its required two-disk probe proves that
   the physical MariaDB volume and the data-disk copy of the completed logical
   dump can be inspected together without production credentials or egress.
   Passing this probe confirms both a crash-consistent disk recovery point and
   a portable logical restore artifact; it does not treat one as a substitute
   for the other.
   Optionally supply a non-sensitive source-object name to prove the independent
   upload copy is readable.
3. For a broader rehearsal, copy selected upload generations into a new
   isolated bucket. Do not overwrite production during a drill.
4. Restore Vault data/key generations into isolated buckets and point an
   isolated Vault service at them. Keep restored root material access logged and
   short-lived.
5. Restore the selected Terraform state generation under a new backend prefix,
   select a non-production workspace, and run `terraform plan` before apply.
6. Run the integrity checks above, both managed readiness jobs, and a real
   annotation save/reload. Record the achieved recovery point and elapsed time.

For an incident restore, obtain explicit incident-commander approval before
copying backup generations over production names. Preserve the failed state and
current generations first so the operation remains reversible.
