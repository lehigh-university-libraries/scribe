output "instance" {
  description = "VM instance details from the cloud-compose module."
  value       = module.scribe.instance
}

output "service_gsa" {
  description = "Internal services service account."
  value       = module.scribe.serviceGsa
}

output "app_gsa" {
  description = "Application service account."
  value       = module.scribe.appGsa
}

output "urls" {
  description = "Cloud Run ingress URLs by region."
  value       = module.scribe.urls
}

output "backend" {
  description = "Backend service ID for the main app Cloud Run ingress."
  value       = module.scribe.backend
}

output "shared_lb_ipv4" {
  description = "IPv4 address of the shared external HTTPS load balancer."
  value       = local.shared_lb_enabled ? module.shared_lb[0].ipv4_address : ""
}

output "shared_lb_ipv6" {
  description = "IPv6 address of the shared external HTTPS load balancer."
  value       = local.shared_lb_enabled ? module.shared_lb[0].ipv6_address : ""
}

output "vault_url" {
  description = "Cloud Run URL for the self-hosted Vault deployment."
  value       = local.vault_url
}

output "vault_gsa" {
  description = "Cloud Run service account email for the self-hosted Vault deployment."
  value       = local.vault_is_owner_workspace ? module.vault[0].gsa : ""
}

output "vault_workspace" {
  description = "Terraform workspace that owns the Vault server used by this deployment."
  value       = local.shared_vault_workspace
}

output "vault_gcp_auth_role" {
  description = "Workspace-specific Vault GCP auth role name used by the app."
  value       = local.vault_app_role_name
}

output "ollama_services" {
  description = "Shared Ollama model services keyed by model identifier."
  value = local.shared_ollama_services_enabled ? {
    for model, service in module.ollama_services : model => {
      service_name          = service.service_name
      service_account_email = service.service_account_email
      primary_url           = service.primary_url
      audience              = service.audience
      urls                  = service.urls
      image                 = service.image
    }
  } : {}
}

output "ocr_services" {
  description = "OCR Cloud Run services keyed by service role."
  value = {
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
}

output "kraken_segmentation_services" {
  description = "Kraken segmentation Cloud Run services keyed by the context segmentation_model value."
  value = {
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
}

output "kraken_transcription_services" {
  description = "Kraken transcription Cloud Run services keyed by the context transcription_model value."
  value = {
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
}

output "internal_artifact_registry_repository" {
  description = "Shared existing Artifact Registry repository used for Vault and Ollama images."
  value       = data.google_artifact_registry_repository.internal.id
}
