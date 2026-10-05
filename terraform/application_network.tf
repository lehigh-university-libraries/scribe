# The application VPC must be a root-owned dependency rather than an output of
# module.scribe. The protected browser subnet contributes its external IPv6
# prefix to the module's PPB policy, so leaving network ownership inside that
# module would create a dependency cycle on every fresh workspace.
resource "google_compute_network" "application" {
  project                 = var.project_id
  name                    = local.name
  auto_create_subnetworks = false
  mtu                     = 1460
}

resource "google_compute_subnetwork" "application" {
  project                  = var.project_id
  region                   = var.region
  name                     = local.name
  network                  = google_compute_network.application.self_link
  ip_cidr_range            = var.network_ip_cidr_range
  private_ip_google_access = true
}
