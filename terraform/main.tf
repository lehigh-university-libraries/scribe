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
  project_number                      = tostring(data.google_project.current.number)
  repo_root                           = abspath("${path.module}/..")
  disk_type                           = "hyperdisk-balanced"
  terraform_state_bucket              = trimspace(var.terraform_state_bucket) != "" ? trimspace(var.terraform_state_bucket) : "${var.project_id}-terraform"
  shared_artifact_registry_location   = "us"
  shared_artifact_registry_repository = "internal"
  is_prod_workspace                   = terraform.workspace == "prod"
  shared_vault_workspace              = local.is_prod_workspace ? "prod" : "dev"
  shared_ollama_workspace             = "prod"
  vault_is_owner_workspace            = terraform.workspace == "prod" || terraform.workspace == "dev"
  workspace_slug                      = replace(lower(terraform.workspace), "/[^a-z0-9-]+/", "-")
  vault_app_role_name                 = "scribe-app-${local.workspace_slug}"
  pubsub_service_agent                = "service-${local.project_number}@gcp-sa-pubsub.iam.gserviceaccount.com"
  uploads_bucket_name                 = trimsuffix(substr(replace(lower("${var.project_id}-${var.name}-${local.workspace_slug}-uploads"), "/[^a-z0-9._-]/", "-"), 0, 63), "-")
}

data "google_artifact_registry_repository" "internal" {
  project       = var.project_id
  location      = local.shared_artifact_registry_location
  repository_id = local.shared_artifact_registry_repository
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
  vault_url            = local.vault_is_owner_workspace ? module.vault[0].vault-url : try(data.terraform_remote_state.shared_vault[0].outputs.vault_url, "")
  public_base_url      = trimspace(var.app_domain) != "" ? format("https://%s", trimspace(var.app_domain)) : ""
  default_ollama_model = "glm-ocr:bf16"
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
  image_service_url                  = try(module.kraken["image-service"].primary_url, "")
  image_service_audience             = local.image_service_url
  iiif_base                          = "/iiif/3"
  iiif_internal_base                 = trimspace(local.image_service_url) != "" ? "${local.image_service_url}/iiif/3" : ""
  triplet_presentation_base          = ""
  triplet_presentation_internal_base = ""
  triplet_presentation_write_token   = ""
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
  docker_compose_services = ["mariadb", "api", "worker", "traefik"]

  docker_compose_repo = "https://github.com/lehigh-university-libraries/scribe.git"
  compose_env_vars = [
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
      value = "uploads"
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
      name  = "IMAGE_SERVICE_URL"
      value = local.image_service_url
    },
    {
      name  = "IMAGE_SERVICE_AUDIENCE"
      value = local.image_service_audience
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
      name  = "TRIPLET_PRESENTATION_WRITE_TOKEN"
      value = local.triplet_presentation_write_token
    },
    {
      name  = "SCRIBE_FRONTEND_IIIF_ORIGIN"
      value = local.image_service_url
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
      value = "scribe/${local.workspace_slug}"
    },
    {
      name  = "VAULT_GCP_AUTH_ROLE"
      value = local.vault_app_role_name
    },
  ]
  compose_env_update_commands = [
    for env in local.compose_env_vars :
    format(
      "python3 scripts/update-env.py .env %s --base64 '%s'",
      env.name,
      base64encode(env.value),
    )
  ]
  docker_compose_init = concat(local.compose_env_update_commands, [
    "bash /home/cloud-compose/rotate-keys-app.sh",
    "bash generate-secrets.sh",
  ])
  docker_compose_up = concat(local.compose_env_update_commands, [
    "git pull",
    "docker compose pull api worker",
    format("docker compose up --no-build %s", join(" ", local.docker_compose_services)),
  ])
  docker_compose_down = concat(local.compose_env_update_commands, [
    "docker compose down",
  ])
  cloud_compose_power_start_role = try(
    module.cloud_compose_foundation[0].cloud_compose_start_role_name,
    data.terraform_remote_state.shared_vault[0].outputs.cloud_compose_power_start_role,
    "",
  )
  cloud_compose_power_suspend_role = try(
    module.cloud_compose_foundation[0].cloud_compose_suspend_role_name,
    data.terraform_remote_state.shared_vault[0].outputs.cloud_compose_power_suspend_role,
    "",
  )
}

resource "google_pubsub_topic" "transcription_jobs" {
  name = "${var.name}-${local.workspace_slug}-transcription-jobs"
}

resource "google_pubsub_topic" "transcription_jobs_dead_letter" {
  name = "${var.name}-${local.workspace_slug}-transcription-jobs-dlq"
}

resource "google_pubsub_subscription" "transcription_workers" {
  name  = "${var.name}-${local.workspace_slug}-transcription-workers"
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
  name  = "${var.name}-${local.workspace_slug}-transcription-jobs-dlq-monitor"
  topic = google_pubsub_topic.transcription_jobs_dead_letter.id

  ack_deadline_seconds       = 60
  message_retention_duration = "1209600s"

  expiration_policy {
    ttl = ""
  }
}

resource "google_monitoring_alert_policy" "transcription_dead_letter_depth" {
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

  lifecycle_rule {
    condition {
      age = 30
    }
    action {
      type = "AbortIncompleteMultipartUpload"
    }
  }
}

resource "google_storage_bucket_iam_member" "uploads_app_object_admin" {
  bucket = google_storage_bucket.uploads.name
  role   = "roles/storage.objectAdmin"
  member = "serviceAccount:${module.scribe.appGsa.email}"
}

resource "google_storage_bucket_iam_member" "uploads_image_service_reader" {
  bucket = google_storage_bucket.uploads.name
  role   = "roles/storage.objectViewer"
  member = "serviceAccount:${google_service_account.kraken.email}"
}

check "shared_vault_ready" {
  assert {
    condition     = local.vault_is_owner_workspace || trimspace(local.vault_url) != ""
    error_message = "Shared Vault workspace '${local.shared_vault_workspace}' must be applied first and expose the root output 'vault_url'. Apply the dev workspace before running preview environments."
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

check "shared_internal_repository_ready" {
  assert {
    condition     = trimspace(data.google_artifact_registry_repository.internal.id) != ""
    error_message = "Artifact Registry repository '${local.shared_artifact_registry_repository}' in location '${local.shared_artifact_registry_location}' must already exist in project '${var.project_id}'."
  }
}

module "cloud_compose_foundation" {
  count  = local.vault_is_owner_workspace ? 1 : 0
  source = "git::https://github.com/libops/cloud-compose//modules/gcp-foundation?ref=1.3.0"
  providers = {
    google      = google
    google-beta = google-beta
  }

  service_project_id = var.project_id
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
  source = "git::https://github.com/libops/cloud-compose?ref=1.3.0"
  providers = {
    google = google
  }

  name = var.name

  gcp = {
    project_id     = var.project_id
    project_number = local.project_number
    region         = var.region
    zone           = var.zone

    instance = {
      machine_type = var.machine_type
      os           = "cos-125-19216-395-4"
    }

    disks = {
      type                   = local.disk_type
      docker_volumes_size_gb = var.disk_size_gb
    }

    network = {
      power_button_allowed_ips = var.allowed_ips
      power_button_ip_depth    = 0
      ssh_ipv4                 = var.allowed_ssh_ipv4
      ssh_ipv6                 = var.allowed_ssh_ipv6
    }

    snapshots = {
      enabled = var.run_snapshots
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
      repo   = local.docker_compose_repo
      branch = var.docker_compose_branch
      init   = local.docker_compose_init
      up     = local.docker_compose_up
      down   = local.docker_compose_down
    }
  }
}
