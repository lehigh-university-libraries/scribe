output "model" {
  description = "Ollama model baked into the deployed Cloud Run service."
  value       = var.model
}

output "service_name" {
  description = "Cloud Run service name."
  value       = local.service_name
}

output "service_account_email" {
  description = "Runtime service account email for the Ollama Cloud Run service."
  value       = google_service_account.service.email
}

output "image" {
  description = "Fully-qualified container image reference deployed to Cloud Run."
  value       = "${local.image_name}@${docker_registry_image.image.sha256_digest}"
}

output "urls" {
  description = "Cloud Run URLs by region."
  value       = module.service.urls
}

output "primary_url" {
  description = "Cloud Run URL for the first configured region."
  value       = local.primary_url
}

output "audience" {
  description = "Default ID token audience for callers using the primary Cloud Run URL."
  value       = local.primary_url
}
