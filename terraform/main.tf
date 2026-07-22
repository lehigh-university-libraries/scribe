provider "google" {
  project = var.project_id
  region  = var.region
}

provider "google-beta" {
  project = var.project_id
  region  = var.region
}

provider "docker" {
  registry_auth {
    address     = "us-docker.pkg.dev"
    config_file = pathexpand("~/.docker/config.json")
  }
}

data "google_client_config" "current" {}

data "google_project" "current" {
  project_id = var.project_id
}

locals {
  project_number           = tostring(data.google_project.current.number)
  repo_root                = abspath("${path.module}/..")
  disk_type                = "hyperdisk-balanced"
  terraform_state_bucket   = trimspace(var.terraform_state_bucket) != "" ? trimspace(var.terraform_state_bucket) : "${var.project_id}-terraform"
  is_prod_workspace        = terraform.workspace == "prod"
  is_preview_workspace     = startswith(terraform.workspace, "pr-")
  foundation_state_prefix  = "scribe-foundation"
  shared_vault_workspace   = local.is_prod_workspace ? "prod" : "dev"
  shared_ollama_workspace  = "prod"
  vault_is_owner_workspace = terraform.workspace == "prod" || terraform.workspace == "dev"
  workspace_slug           = replace(lower(terraform.workspace), "/[^a-z0-9-]+/", "-")
  preview_app_gsa_email    = format("%s@%s.iam.gserviceaccount.com", var.name, var.project_id)
  vault_app_role_name      = local.is_preview_workspace ? "scribe-preview-app" : "scribe-app-${local.workspace_slug}"
  vault_secret_prefix      = local.is_preview_workspace ? "scribe/previews/${local.preview_app_gsa_email}" : "scribe/${local.workspace_slug}"
  pubsub_service_agent     = "service-${local.project_number}@gcp-sa-pubsub.iam.gserviceaccount.com"
  uploads_bucket_name      = trimsuffix(substr(replace(lower("${var.project_id}-${var.name}-${local.workspace_slug}-uploads"), "/[^a-z0-9._-]/", "-"), 0, 63), "-")
}

check "immutable_reviewed_deployment_inputs" {
  assert {
    condition = !local.is_prod_workspace && !startswith(terraform.workspace, "pr-") || (
      can(regex("^[0-9a-f]{40}$", var.docker_compose_branch)) &&
      can(regex("^ghcr\\.io/[a-z0-9._/-]+@sha256:[0-9a-f]{64}$", var.api_image)) &&
      can(regex("^[^[:space:]@]+@sha256:[0-9a-f]{64}$", var.frontend_gar_image)) &&
      length(var.ocr_service_images) > 0 &&
      alltrue([for image in values(var.ocr_service_images) : can(regex("^[^[:space:]@]+@sha256:[0-9a-f]{64}$", image))])
    )
    error_message = "Production and preview deployments require an immutable compose commit plus digest-pinned backend, frontend, and OCR images."
  }
}

data "terraform_remote_state" "shared_vault" {
  count   = local.vault_is_owner_workspace ? 0 : 1
  backend = "gcs"
  config = {
    bucket = local.terraform_state_bucket
    prefix = "scribe"
  }
  workspace = local.shared_vault_workspace
}

data "terraform_remote_state" "shared_foundation" {
  backend = "gcs"
  config = {
    bucket = local.terraform_state_bucket
    prefix = local.foundation_state_prefix
  }
}

data "terraform_remote_state" "shared_ollama" {
  count   = local.shared_ollama_services_enabled || length(local.ollama_models) == 0 ? 0 : 1
  backend = "gcs"
  config = {
    bucket = local.terraform_state_bucket
    prefix = "scribe"
  }
  workspace = local.shared_ollama_workspace
}

locals {
  vault_url = local.vault_is_owner_workspace ? module.vault[0].vault-url : try(data.terraform_remote_state.shared_vault[0].outputs.vault_url, "")
  vault_gsa = local.vault_is_owner_workspace ? module.vault[0].gsa : try(data.terraform_remote_state.shared_vault[0].outputs.vault_gsa, "")
  # Project APIs, the shared registry, and custom roles have a standalone state
  # owner applied before image builds or any application workspace.
  shared_artifact_registry_location   = try(data.terraform_remote_state.shared_foundation.outputs.artifact_registry_location, "")
  shared_artifact_registry_repository = try(data.terraform_remote_state.shared_foundation.outputs.artifact_registry_repository, "")
  vault_gcp_auth_key_verifier_role    = try(data.terraform_remote_state.shared_foundation.outputs.vault_gcp_auth_key_verifier_role, "")
  # Cloud Run assigns this deterministic URL before the service exists. Direct
  # run.app ingress is the sole supported edge topology, keeping PPB's trusted
  # forwarding depth and canonical resource identity unambiguous.
  cloud_run_public_base_url = format("https://%s-%s.%s.run.app", var.name, local.project_number, var.region)
  public_base_url           = local.cloud_run_public_base_url
  default_ollama_model      = "glm-ocr:bf16"
  default_ollama_url = !contains(local.ollama_models, local.default_ollama_model) ? "" : (
    local.shared_ollama_services_enabled ? module.ollama_services[local.default_ollama_model].primary_url :
    try(data.terraform_remote_state.shared_ollama[0].outputs.ollama_services[local.default_ollama_model].primary_url, "")
  )
  default_ollama_audience = !contains(local.ollama_models, local.default_ollama_model) ? "" : (
    local.shared_ollama_services_enabled ? module.ollama_services[local.default_ollama_model].audience :
    try(data.terraform_remote_state.shared_ollama[0].outputs.ollama_services[local.default_ollama_model].audience, "")
  )
  ollama_services_map = local.shared_ollama_services_enabled ? {
    for model, service in module.ollama_services :
    model => {
      primary_url = service.primary_url
      audience    = service.audience
    }
  } : try(data.terraform_remote_state.shared_ollama[0].outputs.ollama_services, {})
  ollama_endpoint_map = {
    for model, service in local.ollama_services_map :
    model => {
      url      = try(service.primary_url, "")
      audience = try(service.audience, "")
    }
    if trimspace(try(service.primary_url, "")) != ""
  }
  segmentor_url                      = try(module.kraken["segmentor"].urls[var.region], try(module.kraken["segmentor"].urls[local.ocr_service_regions[0]], ""))
  segmentor_audience                 = local.segmentor_url
  iiif_base                          = "${local.public_base_url}/iiif/3"
  iiif_internal_base                 = "http://triplet:8080/iiif/3"
  iiif_source_base                   = "http://api:8080/static/uploads"
  triplet_presentation_base          = "${local.public_base_url}/presentation/v3"
  triplet_presentation_internal_base = "http://triplet:8080/presentation/v3"
  kraken_segmentation_services = {
    for name, service in module.kraken :
    service.route_key => {
      primary_url = service.primary_url
      audience    = service.audience
    }
    if service.route_type == "kraken-segmentation"
  }
  kraken_transcription_services = {
    for name, service in module.kraken :
    service.route_key => {
      primary_url = service.primary_url
      audience    = service.audience
    }
    if service.route_type == "kraken-transcription"
  }
  kraken_segmentation_endpoint_map = {
    for model, service in local.kraken_segmentation_services :
    model => {
      url      = service.primary_url
      audience = service.audience
    }
    if trimspace(try(service.primary_url, "")) != ""
  }
  kraken_transcription_endpoint_map = {
    for model, service in local.kraken_transcription_services :
    model => {
      url      = service.primary_url
      audience = service.audience
    }
    if trimspace(try(service.primary_url, "")) != ""
  }
  default_kraken_url      = try(local.kraken_transcription_services[local.kraken_default_transcription_key].primary_url, "")
  default_kraken_audience = try(local.kraken_transcription_services[local.kraken_default_transcription_key].audience, "")
  docker_compose_services = ["mariadb", "triplet", "api", "worker", "traefik"]
  compose_traefik_ip      = cidrhost(var.compose_network_cidr, 2)

  docker_compose_repo = "https://github.com/lehigh-university-libraries/scribe.git"
  compose_env_vars = [
    {
      name  = "AUTH_PREVIEW_ANONYMOUS"
      value = tostring(local.is_preview_workspace)
    },
    {
      name  = "SCRIBE_DATA_GENERATION"
      value = var.data_generation
    },
    {
      name  = "SCRIBE_COMPOSE_SUBNET"
      value = var.compose_network_cidr
    },
    {
      name  = "SCRIBE_TRAEFIK_IP"
      value = local.compose_traefik_ip
    },
    {
      name  = "SERVER_TRUSTED_PROXY_CIDRS"
      value = "${local.compose_traefik_ip}/32"
    },
    {
      name  = "TRAEFIK_FORWARDED_TRUSTED_IPS"
      value = var.network_ip_cidr_range
    },
    {
      name  = "TRANSCRIPTION_MAX_ACTIVE_JOBS_PER_WORKSPACE"
      value = tostring(var.transcription_max_active_jobs_per_workspace)
    },
    {
      name  = "STORAGE_MAX_BYTES_PER_WORKSPACE"
      value = tostring(var.storage_max_bytes_per_workspace)
    },
    {
      name  = "STORAGE_MAX_BYTES_TOTAL"
      value = tostring(var.storage_max_bytes_total)
    },
    {
      name  = "STORAGE_MAX_ITEMS_PER_WORKSPACE"
      value = tostring(var.storage_max_items_per_workspace)
    },
    {
      name  = "STORAGE_MAX_ITEMS_TOTAL"
      value = tostring(var.storage_max_items_total)
    },
    {
      name  = "STORAGE_MAX_IMAGES_PER_WORKSPACE"
      value = tostring(var.storage_max_images_per_workspace)
    },
    {
      name  = "STORAGE_MAX_IMAGES_TOTAL"
      value = tostring(var.storage_max_images_total)
    },
    {
      name  = "STORAGE_RESERVATION_TTL"
      value = var.storage_reservation_ttl
    },
    {
      name  = "STORAGE_NORMALIZATION_CACHE_MAX_BYTES"
      value = tostring(var.storage_normalization_cache_max_bytes)
    },
    {
      name  = "STORAGE_NORMALIZATION_CACHE_MAX_AGE"
      value = var.storage_normalization_cache_max_age
    },
    {
      name  = "IIIF_MAX_MANIFEST_CANVASES"
      value = tostring(var.iiif_max_manifest_canvases)
    },
    {
      name  = "IIIF_MAX_MANIFEST_IMPORT_BYTES"
      value = tostring(var.iiif_max_manifest_import_bytes)
    },
    {
      name  = "IIIF_SOURCE_BASE"
      value = local.iiif_source_base
    },
    {
      name  = "TRANSCRIPTION_QUEUE_BACKEND"
      value = "pubsub"
    },
    {
      name  = "PUBSUB_PROJECT_ID"
      value = var.project_id
    },
    {
      name  = "PUBSUB_TRANSCRIPTION_TOPIC_ID"
      value = google_pubsub_topic.transcription_jobs.name
    },
    {
      name  = "PUBSUB_TRANSCRIPTION_SUBSCRIPTION_ID"
      value = google_pubsub_subscription.transcription_workers.name
    },
    {
      name  = "SCRIBE_UPLOADS_BUCKET"
      value = google_storage_bucket.uploads.name
    },
    {
      name  = "SCRIBE_UPLOADS_PREFIX"
      value = "uploads/${var.data_generation}"
    },
    {
      name  = "OLLAMA_AUDIENCE"
      value = local.default_ollama_audience
    },
    {
      name  = "OLLAMA_URL"
      value = local.default_ollama_url
    },
    {
      name  = "OLLAMA_MODEL_ENDPOINTS_JSON"
      value = jsonencode(local.ollama_endpoint_map)
    },
    {
      name  = "OLLAMA_MODELS_JSON"
      value = jsonencode(local.ollama_models)
    },
    {
      name  = "SEGMENTATION_SERVICE_URL"
      value = local.segmentor_url
    },
    {
      name  = "SEGMENTATION_SERVICE_AUDIENCE"
      value = local.segmentor_audience
    },
    {
      name  = "SEGMENTATION_MODEL_ENDPOINTS_JSON"
      value = jsonencode(local.kraken_segmentation_endpoint_map)
    },
    {
      name  = "SEGMENTATION_MODELS_JSON"
      value = jsonencode(sort(keys(local.kraken_segmentation_models)))
    },
    {
      name  = "IIIF_BASE"
      value = local.iiif_base
    },
    {
      name  = "IIIF_INTERNAL_BASE"
      value = local.iiif_internal_base
    },
    {
      name  = "TRIPLET_PRESENTATION_BASE"
      value = local.triplet_presentation_base
    },
    {
      name  = "TRIPLET_PRESENTATION_INTERNAL_BASE"
      value = local.triplet_presentation_internal_base
    },
    {
      name  = "KRAKEN_URL"
      value = local.default_kraken_url
    },
    {
      name  = "KRAKEN_AUDIENCE"
      value = local.default_kraken_audience
    },
    {
      name  = "KRAKEN_MODEL"
      value = local.kraken_default_transcription_key
    },
    {
      name  = "KRAKEN_MODEL_ENDPOINTS_JSON"
      value = jsonencode(local.kraken_transcription_endpoint_map)
    },
    {
      name  = "KRAKEN_MODELS_JSON"
      value = jsonencode(sort(keys(local.kraken_transcription_models)))
    },
    {
      name  = "PUBLIC_BASE_URL"
      value = local.public_base_url
    },
    {
      name  = "TRIPLET_PUBLIC_BASE_URL"
      value = local.public_base_url
    },
    {
      name  = "SCRIBE_API_IMAGE"
      value = var.api_image
    },
    {
      name  = "VAULT_ADDRESS"
      value = local.vault_url
    },
    {
      name  = "VAULT_WORKSPACE"
      value = local.workspace_slug
    },
    {
      name  = "VAULT_SECRET_PREFIX"
      value = local.vault_secret_prefix
    },
    {
      name  = "VAULT_DATABASE_APP_PATH"
      value = "${local.vault_secret_prefix}/database/app"
    },
    {
      name  = "VAULT_GCP_AUTH_ROLE"
      value = local.vault_app_role_name
    },
  ]
  compose_env_update_commands = [
    for env in local.compose_env_vars :
    format(
      "bash scripts/update-env.sh .env %s --base64 '%s'",
      env.name,
      base64encode(env.value),
    )
  ]
  docker_compose_init = concat(local.compose_env_update_commands, [
    "bash /home/cloud-compose/rotate-keys-app.sh",
    "bash generate-secrets.sh",
  ])
  docker_compose_up = concat(local.compose_env_update_commands, [
    "bash generate-secrets.sh",
    "docker compose pull triplet api worker",
    format("docker compose up --no-build --wait --wait-timeout 180 %s", join(" ", local.docker_compose_services)),
  ])
  docker_compose_down = concat(local.compose_env_update_commands, [
    "docker compose down",
  ])
  cloud_compose_power_start_role = try(
    data.terraform_remote_state.shared_foundation.outputs.cloud_compose_power_start_role,
    "",
  )
  cloud_compose_power_suspend_role = local.is_prod_workspace ? try(
    data.terraform_remote_state.shared_foundation.outputs.cloud_compose_production_observe_role,
    "",
    ) : try(
    data.terraform_remote_state.shared_foundation.outputs.cloud_compose_power_suspend_role,
    "",
  )
}

resource "google_pubsub_topic" "transcription_jobs" {
  name = "${var.name}-${local.workspace_slug}-${var.data_generation}-transcription-jobs"
}

resource "google_pubsub_topic" "transcription_jobs_dead_letter" {
  name = "${var.name}-${local.workspace_slug}-${var.data_generation}-transcription-jobs-dlq"
}

resource "google_pubsub_subscription" "transcription_workers" {
  name  = "${var.name}-${local.workspace_slug}-${var.data_generation}-transcription-workers"
  topic = google_pubsub_topic.transcription_jobs.id

  ack_deadline_seconds       = 60
  message_retention_duration = "604800s"

  dead_letter_policy {
    dead_letter_topic     = google_pubsub_topic.transcription_jobs_dead_letter.id
    max_delivery_attempts = 5
  }

  retry_policy {
    minimum_backoff = "10s"
    maximum_backoff = "600s"
  }
}

resource "google_pubsub_subscription" "transcription_dead_letter_monitor" {
  name  = "${var.name}-${local.workspace_slug}-${var.data_generation}-transcription-jobs-dlq-monitor"
  topic = google_pubsub_topic.transcription_jobs_dead_letter.id

  ack_deadline_seconds       = 60
  message_retention_duration = "1209600s"

  expiration_policy {
    ttl = ""
  }
}

resource "google_monitoring_alert_policy" "transcription_dead_letter_depth" {
  count = local.is_prod_workspace ? 1 : 0

  display_name          = "${var.name} ${local.workspace_slug} transcription DLQ has messages"
  combiner              = "OR"
  notification_channels = var.monitoring_notification_channels

  documentation {
    content   = "The Scribe transcription Pub/Sub dead-letter subscription has unacked messages. Inspect ${google_pubsub_subscription.transcription_dead_letter_monitor.name}; each message represents a job that exceeded Pub/Sub delivery attempts."
    mime_type = "text/markdown"
  }

  conditions {
    display_name = "DLQ monitor subscription has undelivered messages"

    condition_threshold {
      filter          = "resource.type = \"pubsub_subscription\" AND resource.labels.subscription_id = \"${google_pubsub_subscription.transcription_dead_letter_monitor.name}\" AND metric.type = \"pubsub.googleapis.com/subscription/num_undelivered_messages\""
      comparison      = "COMPARISON_GT"
      threshold_value = 0
      duration        = "300s"

      aggregations {
        alignment_period   = "60s"
        per_series_aligner = "ALIGN_MAX"
      }
    }
  }
}

resource "google_pubsub_topic_iam_member" "transcription_jobs_publisher" {
  topic  = google_pubsub_topic.transcription_jobs.name
  role   = "roles/pubsub.publisher"
  member = "serviceAccount:${module.scribe.appGsa.email}"
}

resource "google_pubsub_subscription_iam_member" "transcription_workers_subscriber" {
  subscription = google_pubsub_subscription.transcription_workers.name
  role         = "roles/pubsub.subscriber"
  member       = "serviceAccount:${module.scribe.appGsa.email}"
}

resource "google_pubsub_topic_iam_member" "transcription_dead_letter_publisher" {
  topic  = google_pubsub_topic.transcription_jobs_dead_letter.name
  role   = "roles/pubsub.publisher"
  member = "serviceAccount:${local.pubsub_service_agent}"
}

resource "google_pubsub_subscription_iam_member" "transcription_dead_letter_source_subscriber" {
  subscription = google_pubsub_subscription.transcription_workers.name
  role         = "roles/pubsub.subscriber"
  member       = "serviceAccount:${local.pubsub_service_agent}"
}

resource "google_storage_bucket" "uploads" {
  name                        = local.uploads_bucket_name
  location                    = upper(var.region)
  uniform_bucket_level_access = true
  public_access_prevention    = "enforced"
  # Preview/dev workspaces must remain destroyable after ingest smoke tests.
  # Production objects require explicit lifecycle handling and are protected.
  force_destroy = !local.is_prod_workspace

  versioning {
    enabled = true
  }

  soft_delete_policy {
    retention_duration_seconds = var.uploads_soft_delete_retention_days * 86400
  }

  lifecycle_rule {
    condition {
      age = 30
    }
    action {
      type = "AbortIncompleteMultipartUpload"
    }
  }

  lifecycle_rule {
    condition {
      days_since_noncurrent_time = var.uploads_noncurrent_version_retention_days
    }
    action {
      type = "Delete"
    }
  }
}

check "uploads_bucket_destroy_policy" {
  assert {
    condition     = google_storage_bucket.uploads.force_destroy == !local.is_prod_workspace
    error_message = "The uploads bucket must be force-destroyable outside prod and protected in prod."
  }
}

resource "google_storage_bucket_iam_member" "uploads_app_object_admin" {
  bucket = google_storage_bucket.uploads.name
  role   = "roles/storage.objectAdmin"
  member = "serviceAccount:${module.scribe.appGsa.email}"
}

check "shared_vault_ready" {
  assert {
    condition = local.vault_is_owner_workspace || (
      trimspace(local.vault_url) != "" && trimspace(local.vault_gsa) != ""
    )
    error_message = "Shared Vault workspace '${local.shared_vault_workspace}' must be applied first and expose the root outputs 'vault_url' and 'vault_gsa'. Apply the dev workspace before running preview environments."
  }
}

check "shared_foundation_ready" {
  assert {
    condition = (
      trimspace(local.cloud_compose_power_start_role) != "" &&
      trimspace(local.cloud_compose_power_suspend_role) != "" &&
      trimspace(local.vault_gcp_auth_key_verifier_role) != "" &&
      trimspace(local.shared_artifact_registry_location) != "" &&
      trimspace(local.shared_artifact_registry_repository) != ""
    )
    error_message = "The standalone project foundation state '${local.foundation_state_prefix}' must be applied first and expose the registry, power-management, and Vault verifier outputs."
  }
}

check "vault_admin_emails_configured" {
  assert {
    condition     = !local.vault_is_owner_workspace || length(var.vault_admin_emails) > 0
    error_message = "Owner Vault workspaces ('dev' and 'prod') require vault_admin_emails to be set before apply. Use terraform.tfvars locally or VAULT_ADMIN_EMAILS in deploy-local.sh/GitHub Actions."
  }
}

check "vault_ci_service_account_emails_configured" {
  assert {
    condition     = !local.vault_is_owner_workspace || length(var.vault_ci_service_account_emails) > 0
    error_message = "Owner Vault workspaces ('dev' and 'prod') require vault_ci_service_account_emails to be set before apply. Use terraform.tfvars locally or VAULT_CI_SERVICE_ACCOUNT_EMAILS in deploy-local.sh/GitHub Actions, and include the GitHub Actions deploy service account from secrets.GSA."
  }
}

provider "vault" {
  address          = local.vault_url
  skip_child_token = true

  headers {
    name  = "X-Admin-Token"
    value = data.google_client_config.current.access_token
  }
}

module "scribe" {
  source = "https://github.com/libops/cloud-compose/archive/521d99a481ae8560c0b130e15d103d8e079001f2.tar.gz//cloud-compose-521d99a481ae8560c0b130e15d103d8e079001f2?archive=tar.gz"
  providers = {
    google = google
  }

  name = var.name

  gcp = {
    project_id     = var.project_id
    project_number = local.project_number
    region         = var.region
    zone           = var.zone

    identity = {
      # cloud-compose 1.3.0 mints and rotates the scribe app SA key into
      # secrets/GOOGLE_APPLICATION_CREDENTIALS. The app requires that file to
      # sign its Vault GCP-IAM login JWT (metadata signJwt is blocked on the VM),
      # so managed credentials must stay enabled.
      app_credentials_enabled = true
    }

    instance = {
      machine_type = var.machine_type
      os           = "cos-125-19216-395-4"
      production   = local.is_prod_workspace
    }

    disks = {
      type                   = local.disk_type
      docker_volumes_size_gb = var.disk_size_gb
    }

    network = {
      ip_cidr_range            = var.network_ip_cidr_range
      power_button_allowed_ips = var.allowed_ips
      power_button_ip_depth    = 0
      ssh_ipv4                 = var.allowed_ssh_ipv4
      ssh_ipv6                 = var.allowed_ssh_ipv6
    }

    snapshots = {
      enabled = var.run_snapshots
    }

    cloud_init = {
      # The independently sized logical-backup disk is attached immediately
      # after the VM resource is created. This bounded init step waits for it,
      # formats/mounts it safely, and makes the mount persistent before the
      # application and nightly timer are enabled.
      initcmd = local.is_prod_workspace ? [
        "bash /home/cloud-compose/configure-scribe-backup-disk.sh",
      ] : []
    }

    artifact_registry = {
      repository = local.shared_artifact_registry_repository
      location   = local.shared_artifact_registry_location
    }

    power_management = {
      enabled      = true
      start_role   = local.cloud_compose_power_start_role
      suspend_role = local.cloud_compose_power_suspend_role
      frontend = trimspace(var.frontend_gar_image) == "" ? null : {
        image = var.frontend_gar_image
        port  = 8888
      }
    }
  }

  runtime = {
    rootfs = "${path.module}/rootfs"
    users  = var.users

    compose = {
      primary = "primary"
      projects = {
        primary = {
          docker_compose_repo   = local.docker_compose_repo
          docker_compose_branch = var.docker_compose_branch
          # Source revisions are immutable, but persistence and ignored local
          # secret files belong to the deployment workspace, not a commit.
          # cloud-compose safely updates this checkout to the exact requested
          # SHA while retaining the workspace-owned ignored files.
          project_dir          = "/mnt/disks/data/scribe/${local.workspace_slug}"
          compose_project_name = "${var.name}-${local.workspace_slug}"
          docker_compose_init  = local.docker_compose_init
          docker_compose_up    = local.docker_compose_up
          docker_compose_down  = local.docker_compose_down
        }
      }
    }
  }
}
