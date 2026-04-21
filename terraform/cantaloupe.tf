locals {
  shared_services_enabled = terraform.workspace == "prod"
  shared_lb_enabled       = terraform.workspace == "prod" && length(local.shared_lb_hosts) > 0

  shared_lb_name                = "scribe-edge"
  cantaloupe_service_name       = "cantaloupe"
  cantaloupe_service_account_id = "scribe-cantaloupe"
  cantaloupe_bucket_location    = "US"
  cantaloupe_regions = [
    "us-east5",
    "us-central1",
    "us-south1",
  ]
  cantaloupe_image         = "islandora/cantaloupe:6.0.5@sha256:ef30df94fe51001682fb1a4704c8c87058df4ef96e936ab920c930819663ed58"
  cantaloupe_min_instances = "0"
  cantaloupe_max_instances = "25"
  cantaloupe_memory        = "16Gi"
  cantaloupe_cpu           = "4000m"

  cantaloupe_bucket_name = lower(
    replace(
      replace(
        replace("${var.project_id}-${var.name}-cantaloupe-data", "_", "-"),
        ".",
        "-"
      ),
      " ",
      "-"
    )
  )

  shared_lb_backends = merge(
    trimspace(var.app_domain) != "" ? { app = module.scribe.backend } : {},
    local.shared_services_enabled && trimspace(var.cantaloupe_domain) != "" ? { cantaloupe = module.cantaloupe[0].backend } : {}
  )

  shared_lb_hosts = merge(
    trimspace(var.app_domain) != "" ? { (var.app_domain) = "app" } : {},
    local.shared_services_enabled && trimspace(var.cantaloupe_domain) != "" ? { (var.cantaloupe_domain) = "cantaloupe" } : {}
  )

  shared_lb_default_backend = trimspace(var.app_domain) != "" ? "app" : "cantaloupe"
}

resource "google_service_account" "cantaloupe" {
  count = local.shared_services_enabled ? 1 : 0

  project      = var.project_id
  account_id   = local.cantaloupe_service_account_id
  display_name = "Scribe Cantaloupe"
}

resource "google_storage_bucket" "cantaloupe_data" {
  count = local.shared_services_enabled ? 1 : 0

  project                     = var.project_id
  name                        = local.cantaloupe_bucket_name
  location                    = local.cantaloupe_bucket_location
  uniform_bucket_level_access = true
}

resource "google_storage_bucket_iam_member" "cantaloupe_object_admin" {
  count = local.shared_services_enabled ? 1 : 0

  bucket = google_storage_bucket.cantaloupe_data[0].name
  role   = "roles/storage.objectAdmin"
  member = "serviceAccount:${google_service_account.cantaloupe[0].email}"
}

module "cantaloupe" {
  count = local.shared_services_enabled ? 1 : 0

  source = "git::https://github.com/libops/terraform-cloudrun-v2?ref=0.5.2"

  name          = local.cantaloupe_service_name
  project       = var.project_id
  gsa           = google_service_account.cantaloupe[0].account_id
  regions       = local.cantaloupe_regions
  min_instances = local.cantaloupe_min_instances
  max_instances = local.cantaloupe_max_instances
  containers = [
    {
      name   = "cantaloupe"
      image  = local.cantaloupe_image
      port   = 8182
      memory = local.cantaloupe_memory
      cpu    = local.cantaloupe_cpu
      volume_mounts = [
        {
          name       = "cantaloupe-data"
          mount_path = "/data"
        }
      ]
    }
  ]

  addl_env_vars = [
    {
      name  = "CANTALOUPE_PROCESSOR_STREAM_RETRIEVAL_STRATEGY"
      value = "CacheStrategy"
    },
    {
      name  = "CANTALOUPE_HTTPSOURCE_CHUNKING_ENABLED"
      value = "false"
    },
    {
      name  = "CANTALOUPE_CACHE_SERVER_DERIVATIVE_ENABLED"
      value = "true"
    },
    {
      name  = "CANTALOUPE_CACHE_SERVER_DERIVATIVE"
      value = "FilesystemCache"
    },
    {
      name  = "CANTALOUPE_LOG_APPLICATION_LEVEL"
      value = "info"
    },
  ]

  gcs_volumes = [
    {
      name      = "cantaloupe-data"
      bucket    = google_storage_bucket.cantaloupe_data[0].name
      read_only = false
    }
  ]

  depends_on = [
    google_service_account.cantaloupe,
    google_storage_bucket.cantaloupe_data,
    google_storage_bucket_iam_member.cantaloupe_object_admin,
  ]
}

module "shared_lb" {
  count = local.shared_lb_enabled ? 1 : 0

  source = "./modules/shared-lb"

  project             = var.project_id
  name                = local.shared_lb_name
  backends            = local.shared_lb_backends
  host_backends       = local.shared_lb_hosts
  default_backend_key = local.shared_lb_default_backend
}
