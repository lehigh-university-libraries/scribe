provider "google" {
  project = var.project_id
  region  = var.region
}

provider "google-beta" {
  project = var.project_id
  region  = var.region
}

data "google_client_config" "current" {}

data "google_project" "current" {
  project_id = var.project_id
}

locals {
  project_number         = tostring(data.google_project.current.number)
  repo_root              = abspath("${path.module}/..")
  terraform_state_bucket = trimspace(var.terraform_state_bucket) != "" ? trimspace(var.terraform_state_bucket) : "${var.project_id}-terraform"
  is_prod_workspace      = terraform.workspace == "prod"
  is_preview_workspace   = startswith(terraform.workspace, "pr-")
  # prod is "scribe"; every other workspace is "scribe-<workspace>" (scribe-dev, scribe-pr-12).
  name                    = local.is_prod_workspace ? "scribe" : "scribe-${terraform.workspace}"
  foundation_state_prefix = "scribe-foundation"
  shared_ollama_workspace = "prod"
  workspace_slug          = replace(lower(terraform.workspace), "/[^a-z0-9-]+/", "-")
  pubsub_service_agent    = data.terraform_remote_state.shared_foundation.outputs.pubsub_service_agent_email
  uploads_bucket_name     = trimsuffix(substr(replace(lower("${var.project_id}-${local.name}-${local.workspace_slug}-uploads"), "/[^a-z0-9._-]/", "-"), 0, 63), "-")

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
  # Project APIs, the shared registry, and custom roles have a standalone state
  # owner applied before image builds or any application workspace.
  shared_artifact_registry_location   = try(data.terraform_remote_state.shared_foundation.outputs.artifact_registry_location, "")
  shared_artifact_registry_repository = try(data.terraform_remote_state.shared_foundation.outputs.artifact_registry_repository, "")
  # Cloud Run assigns this deterministic URL before the service exists. Direct
  # run.app ingress is the sole supported edge topology, keeping the frontend.s trusted
  # forwarding depth and canonical resource identity unambiguous.
  cloud_run_public_base_url = format("https://%s-%s.%s.run.app", local.name, local.project_number, var.region)
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
  iiif_internal_base                 = "http://localhost:8082/iiif/3"
  iiif_source_base                   = "http://localhost:8080/static/uploads"
  triplet_presentation_base          = "${local.public_base_url}/presentation/v3"
  triplet_presentation_internal_base = "http://localhost:8082/presentation/v3"
  kraken_segmentation_services = {
    for name, service in module.kraken :
    service.route_key => {
      primary_url = service.primary_url
      audience    = service.audience
    }
    if service.route_type == "kraken-segmentation"
  }
  kraken_segmentation_endpoint_map = merge({
    (local.kraken_default_segmentation_key) = { url = local.segmentor_url, audience = local.segmentor_audience }
    newspapers                              = { url = local.segmentor_url, audience = local.segmentor_audience }
    }, {
    for model, service in local.kraken_segmentation_services :
    model => {
      url      = service.primary_url
      audience = service.audience
    }
    if trimspace(try(service.primary_url, "")) != ""
  })
  runtime_env_vars = [
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
      value = google_pubsub_topic.transcription_jobs.name
    },
    {
      name  = "PUBSUB_TRANSCRIPTION_SUBSCRIPTION_ID"
      value = "${local.name}-transcription-workers"
    },
    {
      name  = "PUBSUB_MAINTENANCE_TOPIC_ID"
      value = google_pubsub_topic.worker_maintenance.name
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
      value = jsonencode(sort(concat(keys(local.kraken_segmentation_models), ["newspapers"])))
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
      name  = "PUBLIC_BASE_URL"
      value = local.public_base_url
    },
    {
      name  = "TRIPLET_PUBLIC_BASE_URL"
      value = local.public_base_url
    },
    {
      name  = "SCRIBE_API_IMAGE"
      value = local.api_image
    },
  ]
}

resource "google_pubsub_topic" "transcription_jobs" {
  name = "${local.name}-transcription-jobs"
}

resource "google_pubsub_topic" "transcription_jobs_dead_letter" {
  name = "${local.name}-transcription-jobs-dlq"
}

resource "google_pubsub_subscription" "transcription_workers" {
  name  = "${local.name}-transcription-workers"
  topic = google_pubsub_topic.transcription_jobs.id

  ack_deadline_seconds       = 600
  message_retention_duration = "604800s"

  push_config {
    push_endpoint = "${google_cloud_run_v2_service.worker.uri}/internal/transcription"
    oidc_token {
      service_account_email = google_service_account.worker_invoker.email
      audience              = google_cloud_run_v2_service.worker.uri
    }
  }

  dead_letter_policy {
    dead_letter_topic     = google_pubsub_topic.transcription_jobs_dead_letter.id
    max_delivery_attempts = 5
  }

  retry_policy {
    minimum_backoff = "10s"
    maximum_backoff = "600s"
  }

  depends_on = [google_cloud_run_v2_service_iam_member.worker_delivery, google_service_account_iam_member.pubsub_worker_token]
}

resource "google_pubsub_subscription" "transcription_dead_letter_monitor" {
  name  = "${local.name}-transcription-jobs-dlq-monitor"
  topic = google_pubsub_topic.transcription_jobs_dead_letter.id

  ack_deadline_seconds       = 60
  message_retention_duration = "1209600s"

  expiration_policy {
    ttl = ""
  }
}





resource "google_monitoring_alert_policy" "transcription_dead_letter_depth" {
  count = local.is_prod_workspace ? 1 : 0

  display_name          = "${local.name} ${local.workspace_slug} transcription DLQ has messages"
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
  member = "serviceAccount:${google_service_account.app.email}"
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
  member = "serviceAccount:${google_service_account.app.email}"
}

resource "google_project_iam_member" "app_telemetry" {
  for_each = local.is_preview_workspace ? toset([]) : toset([
    "roles/cloudtrace.agent",
    "roles/monitoring.metricWriter",
  ])

  project = var.project_id
  role    = each.value
  member  = "serviceAccount:${google_service_account.app.email}"
}

resource "google_project_iam_member" "worker_telemetry" {
  for_each = local.is_preview_workspace ? toset([]) : toset([
    "roles/cloudtrace.agent",
    "roles/monitoring.metricWriter",
  ])

  project = var.project_id
  role    = each.value
  member  = "serviceAccount:${google_service_account.worker.email}"
}
