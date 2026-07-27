locals {
  recorded_root_outputs = {
    instance              = module.scribe.instance
    service_gsa           = module.scribe.serviceGsa
    app_gsa               = module.scribe.appGsa
    urls                  = module.scribe.urls
    backend               = module.scribe.backend
    backend_readiness_job = try(google_cloud_run_v2_job.backend_readiness[0].name, "")
    ocr_readiness_job     = try(google_cloud_run_v2_job.ocr_readiness[0].name, "")
    readiness_gsas = {
      backend = google_service_account.backend_readiness.email
      ocr     = google_service_account.ocr_readiness.email
    }
    deployment_inputs = {
      api_image = var.api_image
      configuration = {
        allowed_ips                                 = var.allowed_ips
        allowed_ssh_ipv4                            = var.allowed_ssh_ipv4
        allowed_ssh_ipv6                            = var.allowed_ssh_ipv6
        backup_restore_service_account_email        = var.backup_restore_service_account_email
        compose_network_cidr                        = var.compose_network_cidr
        dev_external_ocr_impersonators              = var.dev_external_ocr_impersonators
        iiif_max_manifest_canvases                  = var.iiif_max_manifest_canvases
        iiif_max_manifest_import_bytes              = var.iiif_max_manifest_import_bytes
        monitoring_notification_channels            = var.monitoring_notification_channels
        network_ip_cidr_range                       = var.network_ip_cidr_range
        project_id                                  = var.project_id
        region                                      = var.region
        storage_max_bytes_per_workspace             = var.storage_max_bytes_per_workspace
        storage_max_bytes_total                     = var.storage_max_bytes_total
        storage_max_images_per_workspace            = var.storage_max_images_per_workspace
        storage_max_images_total                    = var.storage_max_images_total
        storage_max_items_per_workspace             = var.storage_max_items_per_workspace
        storage_max_items_total                     = var.storage_max_items_total
        storage_normalization_cache_max_age         = var.storage_normalization_cache_max_age
        storage_normalization_cache_max_bytes       = var.storage_normalization_cache_max_bytes
        storage_reservation_ttl                     = var.storage_reservation_ttl
        transcription_max_active_jobs_per_workspace = var.transcription_max_active_jobs_per_workspace
        vault_admin_emails                          = var.vault_admin_emails
        vault_ci_service_account_emails             = var.vault_ci_service_account_emails
        zone                                        = var.zone
      }
      data_generation    = var.data_generation
      docker_compose_sha = var.docker_compose_branch
      frontend_gar_image = var.frontend_gar_image
      ocr_service_images = var.ocr_service_images
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
    backup_restore_verifier_role          = local.is_prod_workspace ? google_project_iam_custom_role.backup_restore_verifier[0].name : ""
  }
}

# Terraform's recovery-only -target applies intentionally leave unrelated
# resources untouched. Record public root outputs under the same managed
# lifecycle so those partial applies cannot replace truthful deployment data
# with values calculated from incomplete recovery inputs.
resource "terraform_data" "recorded_root_outputs" {
  input = local.recorded_root_outputs
}

output "instance" {
  description = "VM instance details from the cloud-compose module."
  value       = terraform_data.recorded_root_outputs.output.instance
}

output "service_gsa" {
  description = "Internal services service account."
  value       = terraform_data.recorded_root_outputs.output.service_gsa
}

output "app_gsa" {
  description = "Application service account."
  value       = terraform_data.recorded_root_outputs.output.app_gsa
}

output "urls" {
  description = "Cloud Run ingress URLs by region."
  value       = terraform_data.recorded_root_outputs.output.urls
}

output "backend" {
  description = "Backend service ID for the main app Cloud Run ingress."
  value       = terraform_data.recorded_root_outputs.output.backend
}

output "backend_readiness_job" {
  description = "Cloud Run job that verifies the frontend VPC path can reach backend readiness."
  value       = terraform_data.recorded_root_outputs.output.backend_readiness_job
}

output "ocr_readiness_job" {
  description = "Cloud Run job that sends a synthetic image through private OCR endpoints."
  value       = terraform_data.recorded_root_outputs.output.ocr_readiness_job
}

output "readiness_gsas" {
  description = "Separate no-data service accounts used by backend and OCR readiness jobs."
  value       = terraform_data.recorded_root_outputs.output.readiness_gsas
}

output "deployment_inputs" {
  description = "Immutable inputs needed to reproduce or roll back the current deployment."
  value       = terraform_data.recorded_root_outputs.output.deployment_inputs

  precondition {
    condition = !local.is_prod_workspace && !startswith(terraform.workspace, "pr-") || (
      can(regex("^[0-9a-f]{40}$", var.docker_compose_branch)) &&
      contains(["canonical-v1", "canonical-v2"], var.data_generation) &&
      var.docker_compose_branch != "0000000000000000000000000000000000000000" &&
      can(regex("^ghcr\\.io/lehigh-university-libraries/scribe@sha256:[0-9a-f]{64}$", var.api_image)) &&
      !endswith(var.api_image, "sha256:0000000000000000000000000000000000000000000000000000000000000000") &&
      can(regex("^us-docker\\.pkg\\.dev/${var.project_id}/internal/scribe-frontend@sha256:[0-9a-f]{64}$", var.frontend_gar_image)) &&
      !endswith(var.frontend_gar_image, "sha256:0000000000000000000000000000000000000000000000000000000000000000") &&
      length(setsubtract(
        toset(concat(
          keys(local.ocr_services),
          local.is_prod_workspace ? [for model in local.ollama_models : "ollama/${model}"] : [],
        )),
        toset(keys(var.ocr_service_images)),
      )) == 0 &&
      length(setsubtract(
        toset(keys(var.ocr_service_images)),
        toset(concat(
          keys(local.ocr_services),
          local.is_prod_workspace ? [for model in local.ollama_models : "ollama/${model}"] : [],
        )),
      )) == 0 &&
      alltrue([
        for image in values(var.ocr_service_images) :
        can(regex("^us-docker\\.pkg\\.dev/${var.project_id}/internal/[a-z0-9._/-]+@sha256:[0-9a-f]{64}$", image)) &&
        !endswith(image, "sha256:0000000000000000000000000000000000000000000000000000000000000000")
      ]) &&
      length(var.allowed_ips) > 0
    )
    error_message = "Production and preview plans require a non-placeholder compose SHA, exact project-owned digest-pinned images for every configured service, and a non-empty ingress CIDR allowlist."
  }

  precondition {
    condition = !local.is_prod_workspace || (
      var.terraform_state_backup_audited &&
      var.run_snapshots &&
      var.backup_soft_delete_retention_days >= 14 &&
      var.backup_noncurrent_version_retention_days >= 30
    )
    error_message = "Production plans require a live state-backup audit, VM snapshots, and the minimum upload-backup retention policy."
  }

  precondition {
    condition = !local.is_prod_workspace || (
      length(var.monitoring_notification_channels) > 0 &&
      alltrue([
        for channel in var.monitoring_notification_channels :
        can(regex("^projects/${var.project_id}/notificationChannels/[^/]+$", channel))
      ])
    )
    error_message = "Production plans require project-local Cloud Monitoring notification channels."
  }

  precondition {
    condition = !local.vault_is_owner_workspace || (
      length(var.vault_admin_emails) > 0 &&
      alltrue([for email in var.vault_admin_emails : can(regex("^[^@[:space:]]+@lehigh\\.edu$", email))]) &&
      length(var.vault_ci_service_account_emails) > 0 &&
      alltrue([
        for email in var.vault_ci_service_account_emails :
        can(regex("^[a-z0-9-]+@[a-z0-9-]+\\.iam\\.gserviceaccount\\.com$", email))
      ])
    )
    error_message = "Vault owner plans require lehigh.edu administrators and explicit Google service-account CI identities."
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

output "uploads_bucket" {
  description = "Workspace source-upload bucket."
  value       = terraform_data.recorded_root_outputs.output.uploads_bucket
}

output "uploads_backup_bucket" {
  description = "Independent production upload backup bucket, empty outside prod."
  value       = terraform_data.recorded_root_outputs.output.uploads_backup_bucket
}

output "uploads_backup_transfer_job" {
  description = "Daily production uploads Storage Transfer job name, empty outside prod."
  value       = terraform_data.recorded_root_outputs.output.uploads_backup_transfer_job
}

output "rollout" {
  description = "Optional cloud-compose rollout endpoint details."
  value       = terraform_data.recorded_root_outputs.output.rollout
}

output "cloud_compose_power_start_role" {
  description = "Project custom role used by cloud-compose power management to start or resume the VM."
  value       = terraform_data.recorded_root_outputs.output.cloud_compose_power_start_role
}

output "cloud_compose_power_suspend_role" {
  description = "Project custom role used by cloud-compose power management to suspend the VM."
  value       = terraform_data.recorded_root_outputs.output.cloud_compose_power_suspend_role
}

output "vault_gcp_auth_key_verifier_role" {
  description = "Singleton project custom role used by Vault to verify GCP IAM login signatures."
  value       = terraform_data.recorded_root_outputs.output.vault_gcp_auth_key_verifier_role
}

output "foundation_workspace" {
  description = "Standalone Terraform state prefix that exclusively owns project-scoped foundation resources."
  value       = terraform_data.recorded_root_outputs.output.foundation_workspace
}

output "vault_url" {
  description = "Cloud Run URL for the self-hosted Vault deployment."
  value       = terraform_data.recorded_root_outputs.output.vault_url
}

output "vault_gsa" {
  description = "Cloud Run service account email for the self-hosted Vault deployment."
  value       = terraform_data.recorded_root_outputs.output.vault_gsa
}

output "vault_init_gsa" {
  description = "Init-only Vault service account with initialization-material access."
  value       = terraform_data.recorded_root_outputs.output.vault_init_gsa
}

output "vault_data_bucket" {
  description = "Vault data bucket owned by this workspace, empty for shared-Vault consumers."
  value       = terraform_data.recorded_root_outputs.output.vault_data_bucket
}

output "vault_key_bucket" {
  description = "Vault initialization-material bucket owned by this workspace, empty for shared-Vault consumers."
  value       = terraform_data.recorded_root_outputs.output.vault_key_bucket
}

output "vault_workspace" {
  description = "Terraform workspace that owns the Vault server used by this deployment."
  value       = terraform_data.recorded_root_outputs.output.vault_workspace
}

output "vault_gcp_auth_role" {
  description = "Workspace-specific Vault GCP auth role name used by the app."
  value       = terraform_data.recorded_root_outputs.output.vault_gcp_auth_role
}

output "ollama_services" {
  description = "Shared Ollama model services keyed by model identifier."
  value       = terraform_data.recorded_root_outputs.output.ollama_services
}

output "ocr_services" {
  description = "OCR Cloud Run services keyed by service role."
  value       = terraform_data.recorded_root_outputs.output.ocr_services
}

output "kraken_segmentation_services" {
  description = "Kraken segmentation Cloud Run services keyed by the context segmentation_model value."
  value       = terraform_data.recorded_root_outputs.output.kraken_segmentation_services
}

output "kraken_transcription_services" {
  description = "Kraken transcription Cloud Run services keyed by the context transcription_model value."
  value       = terraform_data.recorded_root_outputs.output.kraken_transcription_services
}

output "internal_artifact_registry_repository" {
  description = "Shared Artifact Registry repository resource ID from the standalone foundation state."
  value       = terraform_data.recorded_root_outputs.output.internal_artifact_registry_repository
}

output "backup_restore_verifier_role" {
  description = "Least-privilege custom role granted to the protected backup verification identity."
  value       = terraform_data.recorded_root_outputs.output.backup_restore_verifier_role
}
