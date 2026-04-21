locals {
  host_rules = [
    for host, backend_key in var.host_backends : {
      host        = host
      backend_key = backend_key
      matcher     = substr(replace(replace(replace(host, ".", "-"), "*", "wildcard"), "_", "-"), 0, 62)
    }
  ]
}

resource "google_compute_global_address" "ipv4" {
  project = var.project
  name    = "${var.name}-ipv4"
}

resource "google_compute_global_address" "ipv6" {
  project    = var.project
  name       = "${var.name}-ipv6"
  ip_version = "IPV6"
}

resource "google_compute_managed_ssl_certificate" "default" {
  project = var.project
  name    = "${var.name}-tls"

  managed {
    domains = sort(keys(var.host_backends))
  }
}

resource "google_compute_url_map" "default" {
  project = var.project
  name    = "${var.name}-url-map"

  default_service = var.backends[var.default_backend_key]

  dynamic "host_rule" {
    for_each = local.host_rules
    content {
      hosts        = [host_rule.value.host]
      path_matcher = host_rule.value.matcher
    }
  }

  dynamic "path_matcher" {
    for_each = local.host_rules
    content {
      name            = path_matcher.value.matcher
      default_service = var.backends[path_matcher.value.backend_key]
    }
  }
}

resource "google_compute_target_https_proxy" "default" {
  project = var.project
  name    = "${var.name}-https-proxy"
  url_map = google_compute_url_map.default.id

  ssl_certificates = [
    google_compute_managed_ssl_certificate.default.id,
  ]
}

resource "google_compute_global_forwarding_rule" "https" {
  project               = var.project
  name                  = "${var.name}-https"
  target                = google_compute_target_https_proxy.default.id
  ip_address            = google_compute_global_address.ipv4.address
  port_range            = "443"
  load_balancing_scheme = "EXTERNAL_MANAGED"
}

resource "google_compute_global_forwarding_rule" "https_ipv6" {
  project               = var.project
  name                  = "${var.name}-https-v6"
  target                = google_compute_target_https_proxy.default.id
  ip_address            = google_compute_global_address.ipv6.address
  port_range            = "443"
  load_balancing_scheme = "EXTERNAL_MANAGED"
}
