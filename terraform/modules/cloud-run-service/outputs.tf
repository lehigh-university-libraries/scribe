output "name" {
  value = {
    for region, service in google_cloud_run_v2_service.cloudrun :
    region => service.name
  }
  description = "Map of region to Cloud Run service names"
}

output "backend" {
  value       = var.skipNeg ? "" : google_compute_backend_service.backend[0].id
  description = "Backend service ID for load balancer (empty if skipNeg is true)"
}

output "urls" {
  value = {
    for region, service in google_cloud_run_v2_service.cloudrun :
    region => service.uri
  }
  description = "Map of region to Cloud Run service URLs"
}

output "url" {
  value       = values(google_cloud_run_v2_service.cloudrun)[0].uri
  description = "Primary Cloud Run service URL (first region)"
}
