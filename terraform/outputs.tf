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
