locals {
  root_outputs = {
    instance    = module.scribe.instance
    service_gsa = module.scribe.serviceGsa
    app_gsa     = module.scribe.appGsa
    # Cloud Run exposes deterministic and legacy non-deterministic run.app
    # hostnames. Persist and publish only the authority used by PUBLIC_BASE_URL
    # so browser navigation, OAuth, IIIF IDs, and CORS cannot silently diverge.
    urls                  = { (var.region) = local.public_base_url }
    backend               = module.scribe.backend
    backend_readiness_job = try(google_cloud_run_v2_job.backend_readiness[0].name, "")
    ocr_readiness_job     = try(google_cloud_run_v2_job.ocr_readiness[0].name, "")
    readiness_gsas = {
      backend = google_service_account.backend_readiness.email
      ocr     = google_service_account.ocr_readiness.email
    }
    uploads_bucket                   = google_storage_bucket.uploads.name
    uploads_backup_bucket            = try(google_storage_bucket.uploads_backup[0].name, "")
    uploads_backup_transfer_job      = try(google_storage_transfer_job.uploads_backup[0].name, "")
    rollout                          = module.scribe.rollout
    cloud_compose_power_start_role   = local.cloud_compose_power_start_role
    cloud_compose_power_suspend_role = local.cloud_compose_power_suspend_role
    vault_gcp_auth_key_verifier_role = local.vault_gcp_auth_key_verifier_role
    foundation_workspace             = local.foundation_state_prefix
    vault_url                        = local.vault_url
    vault_gsa                        = local.vault_gsa
    vault_init_gsa                   = local.vault_is_owner_workspace ? module.vault[0].init_gsa : ""
    vault_data_bucket                = local.vault_is_owner_workspace ? module.vault[0].data_bucket : ""
    vault_key_bucket                 = local.vault_is_owner_workspace ? module.vault[0].key_bucket : ""
    vault_workspace                  = local.shared_vault_workspace
    vault_gcp_auth_role              = local.vault_app_role_name
    ollama_services = local.shared_ollama_services_enabled ? {
      for model, service in module.ollama_services : model => {
        service_name          = service.service_name
        service_account_email = service.service_account_email
        primary_url           = service.primary_url
        audience              = service.audience
        urls                  = service.urls
        image                 = service.image
      }
    } : {}
    ocr_services = {
      for name, service in module.kraken : name => {
        route_type            = service.route_type
        route_key             = service.route_key
        service_name          = service.service_name
        service_account_email = service.service_account_email
        primary_url           = service.primary_url
        audience              = service.audience
        urls                  = service.urls
        image                 = service.image
      }
    }
    kraken_segmentation_services = {
      for name, service in module.kraken :
      service.route_key => {
        service_name          = service.service_name
        service_account_email = service.service_account_email
        primary_url           = service.primary_url
        audience              = service.audience
        urls                  = service.urls
        image                 = service.image
      }
      if service.route_type == "kraken-segmentation"
    }
    kraken_transcription_services = {
      for name, service in module.kraken :
      service.route_key => {
        service_name          = service.service_name
        service_account_email = service.service_account_email
        primary_url           = service.primary_url
        audience              = service.audience
        urls                  = service.urls
        image                 = service.image
      }
      if service.route_type == "kraken-transcription"
    }
    internal_artifact_registry_repository = try(data.terraform_remote_state.shared_foundation.outputs.artifact_registry_repository_id, "")
  }
}

output "instance" {
  description = "VM instance details from the cloud-compose module."
  value       = local.root_outputs.instance
}

output "service_gsa" {
  description = "Internal services service account."
  value       = local.root_outputs.service_gsa
}

output "app_gsa" {
  description = "Application service account."
  value       = local.root_outputs.app_gsa
}

output "urls" {
  description = "Canonical deterministic Cloud Run ingress URLs by region."
  value       = local.root_outputs.urls

  precondition {
    condition     = length("${local.name}-${local.project_number}") <= 63
    error_message = "name plus project number must fit Cloud Run's 63-character deterministic URL segment; Scribe persists and enforces that canonical origin."
  }

  precondition {
    condition     = !local.is_prod_workspace || length(var.allowed_ips) > 0
    error_message = "Production requires a non-empty ingress CIDR allowlist."
  }

  precondition {
    condition = (
      floor(local.runtime_limits.transcription_max_active_jobs_per_workspace) == local.runtime_limits.transcription_max_active_jobs_per_workspace &&
      local.runtime_limits.transcription_max_active_jobs_per_workspace >= 1 &&
      local.runtime_limits.transcription_max_active_jobs_per_workspace <= 100000 &&
      floor(local.runtime_limits.storage_max_bytes_per_workspace) == local.runtime_limits.storage_max_bytes_per_workspace &&
      local.runtime_limits.storage_max_bytes_per_workspace >= 104857600 &&
      local.runtime_limits.storage_max_bytes_per_workspace <= 10995116277760 &&
      floor(local.runtime_limits.storage_max_bytes_total) == local.runtime_limits.storage_max_bytes_total &&
      local.runtime_limits.storage_max_bytes_total >= local.runtime_limits.storage_max_bytes_per_workspace &&
      local.runtime_limits.storage_max_bytes_total <= 10995116277760 &&
      floor(local.runtime_limits.storage_max_items_per_workspace) == local.runtime_limits.storage_max_items_per_workspace &&
      local.runtime_limits.storage_max_items_per_workspace >= 1 &&
      local.runtime_limits.storage_max_items_per_workspace <= 10000000 &&
      floor(local.runtime_limits.storage_max_items_total) == local.runtime_limits.storage_max_items_total &&
      local.runtime_limits.storage_max_items_total >= local.runtime_limits.storage_max_items_per_workspace &&
      local.runtime_limits.storage_max_items_total <= 10000000 &&
      floor(local.runtime_limits.storage_max_images_per_workspace) == local.runtime_limits.storage_max_images_per_workspace &&
      local.runtime_limits.storage_max_images_per_workspace >= 1 &&
      local.runtime_limits.storage_max_images_per_workspace <= 10000000 &&
      floor(local.runtime_limits.storage_max_images_total) == local.runtime_limits.storage_max_images_total &&
      local.runtime_limits.storage_max_images_total >= local.runtime_limits.storage_max_images_per_workspace &&
      local.runtime_limits.storage_max_images_total <= 10000000 &&
      floor(local.runtime_limits.storage_normalization_cache_max_bytes) == local.runtime_limits.storage_normalization_cache_max_bytes &&
      local.runtime_limits.storage_normalization_cache_max_bytes >= 104857600 &&
      local.runtime_limits.storage_normalization_cache_max_bytes <= 10995116277760 &&
      local.storage_reservation_ttl_seconds >= 300 &&
      local.storage_reservation_ttl_seconds <= 86400 &&
      local.storage_normalization_cache_max_age_seconds >= 3600 &&
      local.storage_normalization_cache_max_age_seconds <= 31536000 &&
      floor(local.runtime_limits.iiif_max_manifest_canvases) == local.runtime_limits.iiif_max_manifest_canvases &&
      local.runtime_limits.iiif_max_manifest_canvases >= 1 &&
      local.runtime_limits.iiif_max_manifest_canvases <= 5000 &&
      floor(local.runtime_limits.iiif_max_manifest_import_bytes) == local.runtime_limits.iiif_max_manifest_import_bytes &&
      local.runtime_limits.iiif_max_manifest_import_bytes >= 1 &&
      local.runtime_limits.iiif_max_manifest_import_bytes <= 67108864
    )
    error_message = "Effective runtime limits must satisfy the application's integer, duration, storage, transcription, and IIIF bounds."
  }
  precondition {
    condition = local.vault_is_owner_workspace || (
      trimspace(local.vault_url) != "" && local.vault_gsa == local.vault_expected_gsa
    )
    error_message = "Consumer workspaces require a live shared Vault service URL and its expected fixed runtime service account."
  }

  precondition {
    condition = (
      local.kraken_default_transcription_key != "" &&
      contains(keys(local.kraken_transcription_models), local.kraken_default_transcription_key) &&
      local.kraken_default_segmentation_key != "" &&
      contains(keys(local.kraken_segmentation_models), local.kraken_default_segmentation_key)
    )
    error_message = "The configured Kraken defaults must reference declared segmentation and transcription models."
  }
}

output "backend" {
  description = "Backend service ID for the main app Cloud Run ingress."
  value       = local.root_outputs.backend
}

output "backend_readiness_job" {
  description = "Cloud Run job that verifies the frontend VPC path can reach backend readiness."
  value       = local.root_outputs.backend_readiness_job
}

output "ocr_readiness_job" {
  description = "Cloud Run job that sends a synthetic image through private OCR endpoints."
  value       = local.root_outputs.ocr_readiness_job
}

output "readiness_gsas" {
  description = "Separate no-data service accounts used by backend and OCR readiness jobs."
  value       = local.root_outputs.readiness_gsas
}
output "uploads_bucket" {
  description = "Workspace source-upload bucket."
  value       = local.root_outputs.uploads_bucket
}

output "uploads_backup_bucket" {
  description = "Independent production upload backup bucket, empty outside prod."
  value       = local.root_outputs.uploads_backup_bucket
}

output "uploads_backup_transfer_job" {
  description = "Daily production uploads Storage Transfer job name, empty outside prod."
  value       = local.root_outputs.uploads_backup_transfer_job
}

output "rollout" {
  description = "Optional cloud-compose rollout endpoint details."
  value       = local.root_outputs.rollout
}

output "cloud_compose_power_start_role" {
  description = "Project custom role used by cloud-compose power management to start or resume the VM."
  value       = local.root_outputs.cloud_compose_power_start_role
}

output "cloud_compose_power_suspend_role" {
  description = "Project custom role used by cloud-compose power management to suspend the VM."
  value       = local.root_outputs.cloud_compose_power_suspend_role
}

output "vault_gcp_auth_key_verifier_role" {
  description = "Singleton project custom role used by Vault to verify GCP IAM login signatures."
  value       = local.root_outputs.vault_gcp_auth_key_verifier_role
}

output "foundation_workspace" {
  description = "Standalone Terraform state prefix that exclusively owns project-scoped foundation resources."
  value       = local.root_outputs.foundation_workspace
}

output "vault_url" {
  description = "Cloud Run URL for the self-hosted Vault deployment."
  value       = local.root_outputs.vault_url
}

output "vault_gsa" {
  description = "Cloud Run service account email for the self-hosted Vault deployment."
  value       = local.root_outputs.vault_gsa
}

output "vault_init_gsa" {
  description = "Init-only Vault service account with initialization-material access."
  value       = local.root_outputs.vault_init_gsa
}

output "vault_data_bucket" {
  description = "Vault data bucket owned by this workspace, empty for shared-Vault consumers."
  value       = local.root_outputs.vault_data_bucket
}

output "vault_key_bucket" {
  description = "Vault initialization-material bucket owned by this workspace, empty for shared-Vault consumers."
  value       = local.root_outputs.vault_key_bucket
}

output "vault_workspace" {
  description = "Terraform workspace that owns the Vault server used by this deployment."
  value       = local.root_outputs.vault_workspace
}

output "vault_gcp_auth_role" {
  description = "Workspace-specific Vault GCP auth role name used by the app."
  value       = local.root_outputs.vault_gcp_auth_role
}

output "ollama_services" {
  description = "Shared Ollama model services keyed by model identifier."
  value       = local.root_outputs.ollama_services
}

output "ocr_services" {
  description = "OCR Cloud Run services keyed by service role."
  value       = local.root_outputs.ocr_services
}

output "kraken_segmentation_services" {
  description = "Kraken segmentation Cloud Run services keyed by the context segmentation_model value."
  value       = local.root_outputs.kraken_segmentation_services
}

output "kraken_transcription_services" {
  description = "Kraken transcription Cloud Run services keyed by the context transcription_model value."
  value       = local.root_outputs.kraken_transcription_services
}

output "internal_artifact_registry_repository" {
  description = "Shared Artifact Registry repository resource ID from the standalone foundation state."
  value       = local.root_outputs.internal_artifact_registry_repository
}

