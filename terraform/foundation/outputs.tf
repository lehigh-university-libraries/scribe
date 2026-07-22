output "artifact_registry_location" {
  value       = local.artifact_registry_location
  description = "Location of the shared runtime-image repository."
}

output "artifact_registry_repository" {
  value       = google_artifact_registry_repository.internal.repository_id
  description = "Repository ID used by reviewed image builds and application workspaces."
}

output "artifact_registry_repository_id" {
  value       = google_artifact_registry_repository.internal.id
  description = "Canonical repository resource ID."
}

output "cloud_compose_power_start_role" {
  value       = module.cloud_compose.cloud_compose_start_role_name
  description = "Project custom role used by proxy-power-button to start or resume a VM."
}

output "cloud_compose_power_suspend_role" {
  value       = module.cloud_compose.cloud_compose_suspend_role_name
  description = "Project custom role used by non-production lightsout runtimes."
}

output "cloud_compose_production_observe_role" {
  value       = google_project_iam_custom_role.cloud_compose_observe.name
  description = "Observation-only role used in production so lightsout cannot suspend the VM."
}

output "vault_gcp_auth_key_verifier_role" {
  value       = google_project_iam_custom_role.vault_gcp_auth_key_verifier.name
  description = "Least-privilege custom role used by Vault's GCP auth backend."
}
