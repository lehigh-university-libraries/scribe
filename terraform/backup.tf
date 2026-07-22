locals {
  uploads_backup_bucket_name = trimsuffix(substr(replace(lower("${var.project_id}-${var.name}-prod-uploads-backup"), "/[^a-z0-9._-]/", "-"), 0, 63), "-")
  # Keep one completed logical dump locally. Daily/weekly immutable snapshots
  # provide historical retention. The independent filesystem has space for the
  # retained dump, a full staging dump, and one full-database safety margin.
  mariadb_backup_retained_completed_copies = 1
  mariadb_backup_disk_size_gb = var.disk_size_gb * (
    local.mariadb_backup_retained_completed_copies + 2
  )
}

resource "google_compute_disk" "mariadb_backups" {
  count = local.is_prod_workspace ? 1 : 0

  project                   = var.project_id
  name                      = "${var.name}-mariadb-backups"
  type                      = local.disk_type
  zone                      = var.zone
  size                      = local.mariadb_backup_disk_size_gb
  physical_block_size_bytes = 4096

  labels = {
    managed_by = "terraform"
    instance   = var.name
    purpose    = "mariadb-logical-backups"
  }

  lifecycle {
    prevent_destroy = true
  }
}

resource "google_compute_attached_disk" "mariadb_backups" {
  count = local.is_prod_workspace ? 1 : 0

  project     = var.project_id
  zone        = var.zone
  disk        = google_compute_disk.mariadb_backups[0].id
  instance    = module.scribe.instance.name
  device_name = "scribe-mariadb-backups"
}

# The pinned cloud-compose policy names are part of its public runtime
# contract. Reusing them puts the logical dump and database snapshots in the
# same crash-consistency window without giving application code snapshot IAM.
resource "google_compute_disk_resource_policy_attachment" "mariadb_backups_daily" {
  count = local.is_prod_workspace && var.run_snapshots ? 1 : 0

  project = var.project_id
  zone    = var.zone
  name    = "${var.name}-daily-snapshot"
  disk    = google_compute_disk.mariadb_backups[0].name

  depends_on = [module.scribe]
}

resource "google_compute_disk_resource_policy_attachment" "mariadb_backups_weekly" {
  count = local.is_prod_workspace && var.run_snapshots ? 1 : 0

  project = var.project_id
  zone    = var.zone
  name    = "${var.name}-weekly-snapshot"
  disk    = google_compute_disk.mariadb_backups[0].name

  depends_on = [module.scribe]
}

check "production_logical_backup_capacity" {
  assert {
    condition = !local.is_prod_workspace || (
      local.mariadb_backup_retained_completed_copies >= 1 &&
      local.mariadb_backup_disk_size_gb >= var.disk_size_gb * (local.mariadb_backup_retained_completed_copies + 1)
    )
    error_message = "The production logical-backup disk must hold every retained full dump plus one full staging dump."
  }
}

resource "google_project_service" "storage_transfer" {
  count = local.is_prod_workspace ? 1 : 0

  project            = var.project_id
  service            = "storagetransfer.googleapis.com"
  disable_on_destroy = false
}

data "google_storage_transfer_project_service_account" "backup" {
  count = local.is_prod_workspace ? 1 : 0

  project    = var.project_id
  depends_on = [google_project_service.storage_transfer]
}

resource "google_storage_bucket" "uploads_backup" {
  count = local.is_prod_workspace ? 1 : 0

  project                     = var.project_id
  name                        = local.uploads_backup_bucket_name
  location                    = upper(var.region)
  force_destroy               = false
  uniform_bucket_level_access = true
  public_access_prevention    = "enforced"

  versioning {
    enabled = true
  }

  soft_delete_policy {
    retention_duration_seconds = var.backup_soft_delete_retention_days * 86400
  }

  lifecycle_rule {
    condition {
      days_since_noncurrent_time = var.backup_noncurrent_version_retention_days
    }
    action {
      type = "Delete"
    }
  }
}

resource "google_storage_bucket_iam_member" "uploads_transfer_source_reader" {
  count = local.is_prod_workspace ? 1 : 0

  bucket = google_storage_bucket.uploads.name
  role   = "roles/storage.objectViewer"
  member = "serviceAccount:${data.google_storage_transfer_project_service_account.backup[0].email}"
}

resource "google_storage_bucket_iam_member" "uploads_transfer_source_bucket_reader" {
  count = local.is_prod_workspace ? 1 : 0

  bucket = google_storage_bucket.uploads.name
  role   = "roles/storage.legacyBucketReader"
  member = "serviceAccount:${data.google_storage_transfer_project_service_account.backup[0].email}"
}

resource "google_storage_bucket_iam_member" "uploads_transfer_backup_writer" {
  count = local.is_prod_workspace ? 1 : 0

  bucket = google_storage_bucket.uploads_backup[0].name
  role   = "roles/storage.objectAdmin"
  member = "serviceAccount:${data.google_storage_transfer_project_service_account.backup[0].email}"
}

resource "google_storage_bucket_iam_member" "uploads_transfer_backup_bucket_reader" {
  count = local.is_prod_workspace ? 1 : 0

  bucket = google_storage_bucket.uploads_backup[0].name
  role   = "roles/storage.legacyBucketReader"
  member = "serviceAccount:${data.google_storage_transfer_project_service_account.backup[0].email}"
}

resource "google_storage_transfer_job" "uploads_backup" {
  count = local.is_prod_workspace ? 1 : 0

  project     = var.project_id
  description = "Daily immutable-copy backup of Scribe production uploads"
  status      = "ENABLED"

  transfer_spec {
    gcs_data_source {
      bucket_name = google_storage_bucket.uploads.name
    }
    gcs_data_sink {
      bucket_name = google_storage_bucket.uploads_backup[0].name
    }
    transfer_options {
      delete_objects_unique_in_sink              = false
      overwrite_objects_already_existing_in_sink = true
    }
  }

  schedule {
    schedule_start_date {
      year  = 2026
      month = 1
      day   = 1
    }
    start_time_of_day {
      hours   = 5
      minutes = 15
      seconds = 0
      nanos   = 0
    }
    repeat_interval = "86400s"
  }

  depends_on = [
    google_storage_bucket_iam_member.uploads_transfer_backup_bucket_reader,
    google_storage_bucket_iam_member.uploads_transfer_backup_writer,
    google_storage_bucket_iam_member.uploads_transfer_source_bucket_reader,
    google_storage_bucket_iam_member.uploads_transfer_source_reader,
  ]
}

# The scheduled verifier uses an external WIF-bound identity so compromise of a
# restore probe never grants application, Vault-token, or Terraform-apply
# privileges. Terraform grants only bucket observation/readback plus creation
# and deletion of tightly labelled disposable compute resources.
resource "google_project_iam_custom_role" "backup_restore_verifier" {
  count = local.is_prod_workspace ? 1 : 0

  project     = var.project_id
  role_id     = "scribeBackupRestoreVerifier"
  title       = "Scribe backup restore verifier"
  description = "Creates and inspects isolated read-only snapshot restore drills without production mutation permissions."
  stage       = "GA"
  permissions = [
    "compute.disks.create",
    "compute.disks.delete",
    "compute.disks.get",
    "compute.disks.list",
    "compute.disks.setLabels",
    "compute.disks.useReadOnly",
    "compute.firewalls.get",
    "compute.instances.create",
    "compute.instances.delete",
    "compute.instances.get",
    "compute.instances.getSerialPortOutput",
    "compute.instances.list",
    "compute.instances.setLabels",
    "compute.instances.setMetadata",
    "compute.instances.setTags",
    "compute.networks.use",
    "compute.projects.get",
    "compute.snapshots.list",
    "compute.snapshots.useReadOnly",
    "compute.subnetworks.use",
    "compute.zoneOperations.get",
  ]

  deletion_policy = "PREVENT"

  lifecycle {
    prevent_destroy = true
  }
}

resource "google_project_iam_member" "backup_restore_verifier" {
  count = local.is_prod_workspace ? 1 : 0

  project = var.project_id
  role    = google_project_iam_custom_role.backup_restore_verifier[0].name
  member  = "serviceAccount:${var.backup_restore_service_account_email}"
}

resource "google_project_iam_member" "backup_transfer_viewer" {
  count = local.is_prod_workspace ? 1 : 0

  project = var.project_id
  role    = "roles/storagetransfer.viewer"
  member  = "serviceAccount:${var.backup_restore_service_account_email}"
}

resource "google_storage_bucket_iam_member" "backup_verifier_bucket_metadata" {
  for_each = local.is_prod_workspace ? toset([
    local.terraform_state_bucket,
    google_storage_bucket.uploads.name,
    google_storage_bucket.uploads_backup[0].name,
    module.vault[0].data_bucket,
    module.vault[0].key_bucket,
  ]) : toset([])

  bucket = each.value
  role   = "roles/storage.legacyBucketReader"
  member = "serviceAccount:${var.backup_restore_service_account_email}"
}

resource "google_storage_bucket_iam_member" "backup_verifier_state_objects" {
  count = local.is_prod_workspace ? 1 : 0

  bucket = local.terraform_state_bucket
  role   = "roles/storage.objectViewer"
  member = "serviceAccount:${var.backup_restore_service_account_email}"
}

resource "google_storage_bucket_iam_member" "backup_verifier_upload_objects" {
  count = local.is_prod_workspace ? 1 : 0

  bucket = google_storage_bucket.uploads_backup[0].name
  role   = "roles/storage.objectViewer"
  member = "serviceAccount:${var.backup_restore_service_account_email}"
}

check "production_state_backup_policy_audited" {
  assert {
    condition     = !local.is_prod_workspace || var.terraform_state_backup_audited
    error_message = "Production requires an audited versioned/retained Terraform state bucket. Run ci/verify-cloud-backups.sh and set TF_VAR_terraform_state_backup_audited=true only for that invocation."
  }
}

check "production_vm_snapshots_enabled" {
  assert {
    condition     = !local.is_prod_workspace || var.run_snapshots
    error_message = "Production requires scheduled persistent-disk snapshots for MariaDB and Compose volumes."
  }
}

# The scheduled restore drill needs the production subnet only to attach the
# disposable VM. These priority-zero rules prevent that no-SA/no-address probe
# from reaching application, Vault, or internet services while it reads the
# restored disks locally. Google metadata routing is firewall-exempt, so the VM
# deliberately has no service account and therefore no workload token to mint.
resource "google_compute_firewall" "snapshot_restore_drill_deny_egress" {
  for_each = local.is_prod_workspace ? {
    ipv4 = "0.0.0.0/0"
    ipv6 = "::/0"
  } : {}

  project            = var.project_id
  name               = "${var.name}-restore-drill-deny-egress-${each.key}"
  network            = module.scribe.network.self_link
  direction          = "EGRESS"
  priority           = 0
  destination_ranges = [each.value]
  target_tags        = ["scribe-restore-drill"]

  deny {
    protocol = "all"
  }

  log_config {
    metadata = "EXCLUDE_ALL_METADATA"
  }
}

check "production_backup_policy" {
  assert {
    condition = !local.is_prod_workspace || (
      var.backup_soft_delete_retention_days >= 14 &&
      var.backup_noncurrent_version_retention_days >= 30
    )
    error_message = "Production upload backups require at least 14 days soft-delete retention and 30 days noncurrent-version retention."
  }
}

check "production_backup_restore_identity" {
  assert {
    condition = !local.is_prod_workspace || (
      trimspace(var.backup_restore_service_account_email) != "" &&
      var.backup_restore_service_account_email != module.scribe.appGsa.email &&
      var.backup_restore_service_account_email != module.scribe.instance.gsa.email &&
      var.backup_restore_service_account_email != local.vault_gsa &&
      var.backup_restore_service_account_email != module.vault[0].init_gsa
    )
    error_message = "Production requires a dedicated backup-restore verifier identity distinct from app, VM, and Vault identities."
  }
}
