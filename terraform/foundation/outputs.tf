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
output "pubsub_service_agent_email" {
  value = google_project_service_identity.pubsub.email
}
