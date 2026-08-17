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
  project_number             = tostring(data.google_project.current.number)
  repo_root                  = abspath("${path.module}/..")
  terraform_state_bucket     = trimspace(var.terraform_state_bucket) != "" ? trimspace(var.terraform_state_bucket) : "${var.project_id}-terraform"
  is_prod_workspace          = terraform.workspace == "prod"
  is_preview_workspace       = startswith(terraform.workspace, "pr-")
  cloud_compose_machine_type = local.is_preview_workspace ? var.preview_machine_type : var.machine_type
  cloud_compose_disk_type    = local.is_preview_workspace ? "pd-standard" : "hyperdisk-balanced"
  foundation_state_prefix    = "scribe-foundation"
  shared_vault_workspace     = local.is_prod_workspace ? "prod" : "dev"
  shared_ollama_workspace    = "prod"
  vault_is_owner_workspace   = terraform.workspace == "prod" || terraform.workspace == "dev"
  workspace_slug             = replace(lower(terraform.workspace), "/[^a-z0-9-]+/", "-")
  preview_app_gsa_email      = format("%s@%s.iam.gserviceaccount.com", var.name, var.project_id)
  vault_app_role_name        = local.is_preview_workspace ? "scribe-preview-app" : "scribe-app-${local.workspace_slug}"
  vault_secret_prefix        = local.is_preview_workspace ? "scribe/previews/${local.preview_app_gsa_email}" : "scribe/${local.workspace_slug}"
  pubsub_service_agent       = "service-${local.project_number}@gcp-sa-pubsub.iam.gserviceaccount.com"
  uploads_bucket_name        = trimsuffix(substr(replace(lower("${var.project_id}-${var.name}-${local.workspace_slug}-uploads"), "/[^a-z0-9._-]/", "-"), 0, 63), "-")
  # canonical-v1 keeps its original singleton addresses so rollback restores a
  # state shape the previously deployed source understands. Append an approved
  # generation to this ordered list only during its explicit cutover. Slicing
  # through the selected generation retains every reviewed predecessor.
  reviewed_data_generations                         = ["canonical-v1", "canonical-v2"]
  data_generation_index                             = try(index(local.reviewed_data_generations, var.data_generation), 0)
  forward_transcription_data_generations            = toset(slice(local.reviewed_data_generations, 1, local.data_generation_index + 1))
  forward_production_transcription_data_generations = local.is_prod_workspace ? local.forward_transcription_data_generations : toset([])
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

data "google_cloud_run_v2_service" "shared_vault" {
  count = local.vault_is_owner_workspace ? 0 : 1

  project  = var.project_id
  location = var.region
  name     = local.vault_service_name
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
  # Consumer workspaces discover the exact, already-live shared Vault service
  # by project, region, and fixed name. This keeps a fresh preview plan
  # independent of stale or partially-upgraded owner-workspace root outputs.
  vault_expected_gsa = "${local.vault_service_name}@${var.project_id}.iam.gserviceaccount.com"
  vault_url = local.vault_is_owner_workspace ? module.vault[0].vault-url : join("", compact([
    try(data.google_cloud_run_v2_service.shared_vault[0].uri, null),
  ]))
  vault_gsa = local.vault_is_owner_workspace ? module.vault[0].gsa : join("", compact([
    try(data.google_cloud_run_v2_service.shared_vault[0].template[0].service_account, null),
  ]))
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
  # Runtime defaults are authored once in the same config baked into the Go
  # image. Terraform accepts explicit operator overrides but records and
  # deploys these extracted defaults when no override is supplied.
  application_config = yamldecode(file("${path.module}/../config.yaml"))
  runtime_limits = {
    transcription_max_active_jobs_per_workspace = coalesce(var.transcription_max_active_jobs_per_workspace, tonumber(regex(":-([^}]+)\\}$", tostring(local.application_config.transcription.max_active_jobs_per_workspace))[0]))
    storage_max_bytes_per_workspace             = coalesce(var.storage_max_bytes_per_workspace, tonumber(regex(":-([^}]+)\\}$", tostring(local.application_config.storage.max_bytes_per_workspace))[0]))
    storage_max_bytes_total                     = coalesce(var.storage_max_bytes_total, tonumber(regex(":-([^}]+)\\}$", tostring(local.application_config.storage.max_bytes_total))[0]))
    storage_max_items_per_workspace             = coalesce(var.storage_max_items_per_workspace, tonumber(regex(":-([^}]+)\\}$", tostring(local.application_config.storage.max_items_per_workspace))[0]))
    storage_max_items_total                     = coalesce(var.storage_max_items_total, tonumber(regex(":-([^}]+)\\}$", tostring(local.application_config.storage.max_items_total))[0]))
    storage_max_images_per_workspace            = coalesce(var.storage_max_images_per_workspace, tonumber(regex(":-([^}]+)\\}$", tostring(local.application_config.storage.max_images_per_workspace))[0]))
    storage_max_images_total                    = coalesce(var.storage_max_images_total, tonumber(regex(":-([^}]+)\\}$", tostring(local.application_config.storage.max_images_total))[0]))
    storage_reservation_ttl                     = coalesce(var.storage_reservation_ttl, regex(":-([^}]+)\\}$", tostring(local.application_config.storage.reservation_ttl))[0])
    storage_normalization_cache_max_bytes       = coalesce(var.storage_normalization_cache_max_bytes, tonumber(regex(":-([^}]+)\\}$", tostring(local.application_config.storage.normalization_cache_max_bytes))[0]))
    storage_normalization_cache_max_age         = coalesce(var.storage_normalization_cache_max_age, regex(":-([^}]+)\\}$", tostring(local.application_config.storage.normalization_cache_max_age))[0])
    iiif_max_manifest_canvases                  = coalesce(var.iiif_max_manifest_canvases, tonumber(regex(":-([^}]+)\\}$", tostring(local.application_config.iiif.max_manifest_canvases))[0]))
    iiif_max_manifest_import_bytes              = coalesce(var.iiif_max_manifest_import_bytes, tonumber(regex(":-([^}]+)\\}$", tostring(local.application_config.iiif.max_manifest_import_bytes))[0]))
  }
  storage_reservation_ttl_parts = try(
    regex("^([1-9][0-9]*)(s|m|h)$", local.runtime_limits.storage_reservation_ttl),
    ["0", "s"],
  )
  storage_normalization_cache_max_age_parts = try(
    regex("^([1-9][0-9]*)(s|m|h)$", local.runtime_limits.storage_normalization_cache_max_age),
    ["0", "s"],
  )
  storage_reservation_ttl_seconds = (
    tonumber(local.storage_reservation_ttl_parts[0]) *
    lookup({ s = 1, m = 60, h = 3600 }, local.storage_reservation_ttl_parts[1], 0)
  )
  storage_normalization_cache_max_age_seconds = (
    tonumber(local.storage_normalization_cache_max_age_parts[0]) *
    lookup({ s = 1, m = 60, h = 3600 }, local.storage_normalization_cache_max_age_parts[1], 0)
  )
  default_ollama_model = local.ollama_default_model
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
  # Allocate ordinary containers only from the upper half. Traefik's fixed
  # host-2 address remains in the parent subnet but outside Docker's dynamic
  # pool, so parallel Compose startup cannot assign it to another service.
  compose_dynamic_ip_range = cidrsubnet(var.compose_network_cidr, 1, 1)
  compose_gateway_ip       = cidrhost(var.compose_network_cidr, 1)
  compose_project_name     = "${var.name}-${local.workspace_slug}"

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
      name  = "SCRIBE_OTEL_EXPORTER"
      value = local.is_preview_workspace ? "none" : "google"
    },
    {
      name  = "GOOGLE_CLOUD_PROJECT"
      value = var.project_id
    },
    {
      name  = "SCRIBE_DEPLOYMENT_ENVIRONMENT"
      value = local.is_preview_workspace ? "preview" : (local.is_prod_workspace ? "prod" : "dev")
    },
    {
      name  = "SCRIBE_COMPOSE_SUBNET"
      value = var.compose_network_cidr
    },
    {
      name  = "SCRIBE_COMPOSE_IP_RANGE"
      value = local.compose_dynamic_ip_range
    },
    {
      name  = "SCRIBE_COMPOSE_GATEWAY"
      value = local.compose_gateway_ip
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
      # New application revisions trust Compose's service identity. Keep the
      # fixed /32 above only for rollback to the immediately previous source,
      # whose config does not yet understand trusted_proxy_hosts.
      name  = "SERVER_TRUSTED_PROXY_HOSTS"
      value = "traefik"
    },
    {
      name  = "TRAEFIK_FORWARDED_TRUSTED_IPS"
      value = var.network_ip_cidr_range
    },
    {
      name  = "TRANSCRIPTION_MAX_ACTIVE_JOBS_PER_WORKSPACE"
      value = tostring(local.runtime_limits.transcription_max_active_jobs_per_workspace)
    },
    {
      name  = "STORAGE_MAX_BYTES_PER_WORKSPACE"
      value = tostring(local.runtime_limits.storage_max_bytes_per_workspace)
    },
    {
      name  = "STORAGE_MAX_BYTES_TOTAL"
      value = tostring(local.runtime_limits.storage_max_bytes_total)
    },
    {
      name  = "STORAGE_MAX_ITEMS_PER_WORKSPACE"
      value = tostring(local.runtime_limits.storage_max_items_per_workspace)
    },
    {
      name  = "STORAGE_MAX_ITEMS_TOTAL"
      value = tostring(local.runtime_limits.storage_max_items_total)
    },
    {
      name  = "STORAGE_MAX_IMAGES_PER_WORKSPACE"
      value = tostring(local.runtime_limits.storage_max_images_per_workspace)
    },
    {
      name  = "STORAGE_MAX_IMAGES_TOTAL"
      value = tostring(local.runtime_limits.storage_max_images_total)
    },
    {
      name  = "STORAGE_RESERVATION_TTL"
      value = local.runtime_limits.storage_reservation_ttl
    },
    {
      name  = "STORAGE_NORMALIZATION_CACHE_MAX_BYTES"
      value = tostring(local.runtime_limits.storage_normalization_cache_max_bytes)
    },
    {
      name  = "STORAGE_NORMALIZATION_CACHE_MAX_AGE"
      value = local.runtime_limits.storage_normalization_cache_max_age
    },
    {
      name  = "IIIF_MAX_MANIFEST_CANVASES"
      value = tostring(local.runtime_limits.iiif_max_manifest_canvases)
    },
    {
      name  = "IIIF_MAX_MANIFEST_IMPORT_BYTES"
      value = tostring(local.runtime_limits.iiif_max_manifest_import_bytes)
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
      value = var.data_generation == "canonical-v1" ? google_pubsub_topic.transcription_jobs.name : google_pubsub_topic.transcription_jobs_forward[var.data_generation].name
    },
    {
      name  = "PUBSUB_TRANSCRIPTION_SUBSCRIPTION_ID"
      value = var.data_generation == "canonical-v1" ? google_pubsub_subscription.transcription_workers.name : google_pubsub_subscription.transcription_workers_forward[var.data_generation].name
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
  docker_compose_init = concat(
    local.compose_env_update_commands,
    [
      "SCRIBE_EXPECTED_DOCKER_ROOT=/mnt/disks/data/docker bash /home/cloud-compose/scribe-compose-runtime-preflight.sh \"$PWD\" \"$PWD/docker-compose.yaml\" /home/cloud-compose/scribe-runtime.compose.yaml",
    ]
  )
  docker_compose_up = concat(
    local.compose_env_update_commands,
    [
      "SCRIBE_EXPECTED_DOCKER_ROOT=/mnt/disks/data/docker bash /home/cloud-compose/scribe-compose-runtime-preflight.sh \"$PWD\" \"$PWD/docker-compose.yaml\" /home/cloud-compose/scribe-runtime.compose.yaml",
      format("source /home/cloud-compose/profile.sh && retry_until_success docker compose -f docker-compose.yaml -f /home/cloud-compose/scribe-runtime.compose.yaml pull %s", join(" ", local.docker_compose_services)),
      "SCRIBE_REPAIR_LOCAL_TOKENS=true bash generate-secrets.sh",
      "SCRIBE_EXPECTED_DOCKER_ROOT=/mnt/disks/data/docker bash /home/cloud-compose/scribe-compose-runtime-preflight.sh --converge \"$PWD\" \"$PWD/docker-compose.yaml\" /home/cloud-compose/scribe-runtime.compose.yaml",
      format("docker compose -f docker-compose.yaml -f /home/cloud-compose/scribe-runtime.compose.yaml up --no-build --wait --wait-timeout 180 %s", join(" ", local.docker_compose_services)),
      "curl --noproxy '*' --fail --silent --show-error --connect-timeout 2 --max-time 10 --output /dev/null http://127.0.0.1/readyz",
    ]
  )
  docker_compose_down = concat(local.compose_env_update_commands, [
    "docker compose -f docker-compose.yaml -f /home/cloud-compose/scribe-runtime.compose.yaml down",
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
  name = "${var.name}-${local.workspace_slug}-canonical-v1-transcription-jobs"
}

resource "google_pubsub_topic" "transcription_jobs_dead_letter" {
  name = "${var.name}-${local.workspace_slug}-canonical-v1-transcription-jobs-dlq"
}

resource "google_pubsub_subscription" "transcription_workers" {
  name  = "${var.name}-${local.workspace_slug}-canonical-v1-transcription-workers"
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
  name  = "${var.name}-${local.workspace_slug}-canonical-v1-transcription-jobs-dlq-monitor"
  topic = google_pubsub_topic.transcription_jobs_dead_letter.id

  ack_deadline_seconds       = 60
  message_retention_duration = "1209600s"

  expiration_policy {
    ttl = ""
  }
}

resource "google_pubsub_topic" "transcription_jobs_forward" {
  for_each = local.forward_transcription_data_generations

  name = "${var.name}-${local.workspace_slug}-${each.key}-transcription-jobs"
}

resource "google_pubsub_topic" "transcription_jobs_dead_letter_forward" {
  for_each = local.forward_transcription_data_generations

  name = "${var.name}-${local.workspace_slug}-${each.key}-transcription-jobs-dlq"
}

resource "google_pubsub_subscription" "transcription_workers_forward" {
  for_each = local.forward_transcription_data_generations

  name  = "${var.name}-${local.workspace_slug}-${each.key}-transcription-workers"
  topic = google_pubsub_topic.transcription_jobs_forward[each.key].id

  ack_deadline_seconds       = 60
  message_retention_duration = "604800s"

  dead_letter_policy {
    dead_letter_topic     = google_pubsub_topic.transcription_jobs_dead_letter_forward[each.key].id
    max_delivery_attempts = 5
  }

  retry_policy {
    minimum_backoff = "10s"
    maximum_backoff = "600s"
  }
}

resource "google_pubsub_subscription" "transcription_dead_letter_monitor_forward" {
  for_each = local.forward_transcription_data_generations

  name  = "${var.name}-${local.workspace_slug}-${each.key}-transcription-jobs-dlq-monitor"
  topic = google_pubsub_topic.transcription_jobs_dead_letter_forward[each.key].id

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

resource "google_monitoring_alert_policy" "transcription_dead_letter_depth_forward" {
  for_each = local.forward_production_transcription_data_generations

  display_name          = "${var.name} ${local.workspace_slug} ${each.key} transcription DLQ has messages"
  combiner              = "OR"
  notification_channels = var.monitoring_notification_channels

  documentation {
    content   = "The Scribe ${each.key} transcription Pub/Sub dead-letter subscription has unacked messages. Inspect ${google_pubsub_subscription.transcription_dead_letter_monitor_forward[each.key].name}; each message represents a job that exceeded Pub/Sub delivery attempts."
    mime_type = "text/markdown"
  }

  conditions {
    display_name = "${each.key} DLQ monitor subscription has undelivered messages"

    condition_threshold {
      filter          = "resource.type = \"pubsub_subscription\" AND resource.labels.subscription_id = \"${google_pubsub_subscription.transcription_dead_letter_monitor_forward[each.key].name}\" AND metric.type = \"pubsub.googleapis.com/subscription/num_undelivered_messages\""
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

resource "google_pubsub_topic_iam_member" "transcription_jobs_publisher_forward" {
  for_each = local.forward_transcription_data_generations

  topic  = google_pubsub_topic.transcription_jobs_forward[each.key].name
  role   = "roles/pubsub.publisher"
  member = "serviceAccount:${module.scribe.appGsa.email}"
}

resource "google_pubsub_subscription_iam_member" "transcription_workers_subscriber_forward" {
  for_each = local.forward_transcription_data_generations

  subscription = google_pubsub_subscription.transcription_workers_forward[each.key].name
  role         = "roles/pubsub.subscriber"
  member       = "serviceAccount:${module.scribe.appGsa.email}"
}

resource "google_pubsub_topic_iam_member" "transcription_dead_letter_publisher_forward" {
  for_each = local.forward_transcription_data_generations

  topic  = google_pubsub_topic.transcription_jobs_dead_letter_forward[each.key].name
  role   = "roles/pubsub.publisher"
  member = "serviceAccount:${local.pubsub_service_agent}"
}

resource "google_pubsub_subscription_iam_member" "transcription_dead_letter_source_subscriber_forward" {
  for_each = local.forward_transcription_data_generations

  subscription = google_pubsub_subscription.transcription_workers_forward[each.key].name
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

resource "google_project_iam_member" "app_telemetry" {
  for_each = local.is_preview_workspace ? toset([]) : toset([
    "roles/cloudtrace.agent",
    "roles/monitoring.metricWriter",
  ])

  project = var.project_id
  role    = each.value
  member  = "serviceAccount:${module.scribe.appGsa.email}"
}

check "shared_vault_ready" {
  assert {
    condition = local.vault_is_owner_workspace || (
      trimspace(local.vault_url) != "" && local.vault_gsa == local.vault_expected_gsa
    )
    error_message = "Shared Vault workspace '${local.shared_vault_workspace}' must expose a live '${local.vault_service_name}' service with a URL and the expected runtime service account '${local.vault_expected_gsa}' before consumer workspaces can be planned."
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
  source = "https://github.com/libops/cloud-compose/archive/889335d615fc3f32db2c66a604f14a3b1a3e8189.tar.gz//cloud-compose-889335d615fc3f32db2c66a604f14a3b1a3e8189?archive=tar.gz"
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
      # cloud-compose 1.8.1 mints and rotates the scribe app SA key into
      # secrets/GOOGLE_APPLICATION_CREDENTIALS. The app requires that file to
      # sign its Vault GCP-IAM login JWT (metadata signJwt is blocked on the VM),
      # so managed credentials must stay enabled.
      app_credentials_enabled = true
    }

    instance = {
      # Cloud Compose's GCP runtime is COS. Host lifecycle scripts are tested
      # against the jq and shell feature set shipped by that image.
      machine_type = local.cloud_compose_machine_type
      production   = local.is_prod_workspace
    }

    disks = {
      type                   = local.cloud_compose_disk_type
      data_size_gb           = local.cloud_compose_data_disk_size_gb
      docker_volumes_size_gb = var.disk_size_gb
    }

    network = {
      create                   = false
      project_id               = var.project_id
      name                     = google_compute_network.application.self_link
      subnetwork               = google_compute_subnetwork.application.self_link
      ip_cidr_range            = var.network_ip_cidr_range
      mtu                      = google_compute_network.application.mtu
      power_button_allowed_ips = distinct(concat(var.allowed_ips, local.browser_readiness_allowed_ips))
      power_button_ip_depth    = 0
      ssh_ipv4                 = local.effective_allowed_ssh_ipv4
      ssh_ipv6                 = var.allowed_ssh_ipv6
    }

    snapshots = {
      enabled = var.run_snapshots
    }

    cloud_init = {
      # The dedicated backup disk and its mount existed only in production.
      # Preserve that scope now that production dumps share the data disk:
      # cloud-compose enables its generic timer during bootstrap, then this
      # post-init command disables it everywhere except the protected prod
      # workspace.
      runcmd = local.is_prod_workspace ? [] : [
        "systemctl disable --now cloud-compose-mariadb-backup.timer cloud-compose-mariadb-backup.service",
      ]
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
    extra_env = local.is_prod_workspace ? {
      SCRIBE_MARIADB_BACKUP_MIN_FREE_BYTES = tostring(var.disk_size_gb * 1073741824)
    } : {}

    compose = {
      primary = "primary"
      projects = {
        primary = {
          docker_compose_repo   = local.docker_compose_repo
          docker_compose_branch = var.docker_compose_branch
          # This path is stable for the current persistence generation because
          # it owns ignored local secrets, including MariaDB's root bootstrap
          # password. Changing it requires an explicit credential migration.
          project_dir          = "/mnt/disks/data/scribe/${local.workspace_slug}"
          compose_project_name = local.compose_project_name
          docker_compose_init  = local.docker_compose_init
          docker_compose_up    = local.docker_compose_up
          docker_compose_down  = local.docker_compose_down
        }
      }
    }
  }
}
