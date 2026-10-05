locals {
  uploads_backup_bucket_name = trimsuffix(substr(replace(lower("${var.project_id}-${local.name}-prod-uploads-backup"), "/[^a-z0-9._-]/", "-"), 0, 63), "-")
  # Keep one completed logical dump on cloud-compose's snapshotted data disk.
  # Reserve space for that dump, a full staging dump, and one full-database
  # safety margin in addition to cloud-compose's 20 GiB application baseline.
  # Daily and weekly snapshots provide historical retention.
  mariadb_backup_retained_completed_copies = 1
  cloud_compose_data_baseline_size_gb      = 20
  cloud_compose_data_disk_size_gb = local.is_prod_workspace ? (
    local.cloud_compose_data_baseline_size_gb + var.disk_size_gb * (
      local.mariadb_backup_retained_completed_copies + 2
    )
  ) : local.cloud_compose_data_baseline_size_gb
}

# Logical dumps now live on cloud-compose's existing data disk. Forget the old
# independently managed disk so Terraform cannot destroy its historical data.
# The former google_compute_attached_disk is intentionally removed normally:
# destroying that non-data-bearing resource only detaches this preserved disk.
removed {
  from = google_compute_disk.mariadb_backups

  lifecycle {
    destroy = false
  }
}

check "production_logical_backup_capacity" {
  assert {
    condition = !local.is_prod_workspace || (
      local.mariadb_backup_retained_completed_copies >= 1 &&
      local.cloud_compose_data_disk_size_gb >= (
        local.cloud_compose_data_baseline_size_gb +
        var.disk_size_gb * (local.mariadb_backup_retained_completed_copies + 2)
      )
    )
    error_message = "The production data disk must preserve cloud-compose's baseline capacity plus every retained full dump, one staging dump, and one safety margin."
  }
}

resource "google_project_service" "storage_transfer" {
  count = local.is_prod_workspace ? 1 : 0

  project            = var.project_id
  service            = "storagetransfer.googleapis.com"
  disable_on_destroy = false
}

# Generate the Google-managed Storage Transfer service agent explicitly before
# bucket IAM refers to it. The Storage Transfer lookup below also triggers
# creation, but that GET can return the agent email before IAM recognizes a
# newly materialized principal.
resource "google_project_service_identity" "storage_transfer" {
  provider = google-beta
  count    = local.is_prod_workspace ? 1 : 0

  project = var.project_id
  service = "storagetransfer.googleapis.com"

  depends_on = [google_project_service.storage_transfer]
}

data "google_storage_transfer_project_service_account" "backup" {
  count = local.is_prod_workspace ? 1 : 0

  project    = var.project_id
  depends_on = [google_project_service_identity.storage_transfer]
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

check "production_backup_policy" {
  assert {
    condition = !local.is_prod_workspace || (
      var.backup_soft_delete_retention_days >= 14 &&
      var.backup_noncurrent_version_retention_days >= 30
    )
    error_message = "Production upload backups require at least 14 days soft-delete retention and 30 days noncurrent-version retention."
  }
}


# The backup verifier role is no longer used. Custom roles are kept on delete
# (deletion_policy = PREVENT), so drop it from state and leave it in the project.
removed {
  from = google_project_iam_custom_role.backup_restore_verifier

  lifecycle {
    destroy = false
  }
}
