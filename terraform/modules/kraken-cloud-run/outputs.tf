output "service_name" {
  description = "Cloud Run service name."
  value       = var.name
}

output "service_account_email" {
  description = "Runtime service account email."
  value       = data.google_service_account.service.email
}

output "route_type" {
  description = "Logical route type for this service."
  value       = var.route_type
}

output "route_key" {
  description = "Logical route key for this service."
  value       = var.route_key
}

output "image" {
  description = "Fully-qualified container image reference (name@sha256)."
  value       = var.image
}

output "urls" {
  description = "Cloud Run URLs keyed by region."
  value       = module.service.urls
}

output "primary_url" {
  description = "Cloud Run URL for the first configured region."
  value       = local.primary_url
}

output "audience" {
  description = "Default ID token audience for callers (the primary Cloud Run URL)."
  value       = local.primary_url
}
