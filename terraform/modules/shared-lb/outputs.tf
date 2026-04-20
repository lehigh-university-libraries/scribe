output "ipv4_address" {
  description = "IPv4 address of the shared HTTPS load balancer."
  value       = google_compute_global_address.ipv4.address
}

output "ipv6_address" {
  description = "IPv6 address of the shared HTTPS load balancer."
  value       = google_compute_global_address.ipv6.address
}
