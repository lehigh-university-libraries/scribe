# The public PPB ingress is intentionally source-IP restricted, so a hosted
# GitHub runner cannot reliably exercise the frontend's private backend origin.
# This job starts server.mjs from the exact deployed frontend image and probes
# its own /healthz proxy over the same direct-VPC network path as the sidecar.
# The deploy workflow executes it after every apply and fails if either the
# baked backend origin or the live backend route is wrong.
locals {
  # cloud-compose exposes Compute API HTTPS self-links, while Cloud Run Direct
  # VPC egress accepts only canonical relative resource names.
  readiness_network_resource_name = regex(
    "projects/[^/]+/global/networks/[^/]+$",
    module.scribe.network.self_link,
  )
  readiness_subnetwork_resource_name = regex(
    "projects/[^/]+/regions/[^/]+/subnetworks/[^/]+$",
    module.scribe.network.subnetwork,
  )
  normalized_browser_readiness_image = trimspace(var.browser_readiness_image)
  browser_readiness_enabled          = local.is_preview_workspace && local.normalized_browser_readiness_image != ""
  browser_readiness_network_resource_name = try(regex(
    "projects/[^/]+/global/networks/[^/]+$",
    google_compute_network.application.self_link,
  ), "")
  browser_readiness_subnetwork_resource_name = try(regex(
    "projects/[^/]+/regions/[^/]+/subnetworks/[^/]+$",
    google_compute_subnetwork.browser_readiness[0].self_link,
  ), "")
  browser_readiness_name_hash     = substr(sha256("${var.name}:${local.workspace_slug}"), 0, 8)
  browser_readiness_resource_name = "${trimsuffix(substr(var.name, 0, 46), "-")}-browser-${local.browser_readiness_name_hash}"
  browser_readiness_job_name      = "${trimsuffix(substr(var.name, 0, 32), "-")}-browser-${local.browser_readiness_name_hash}"
  browser_readiness_account_id    = "${trimsuffix(substr("probe-browser-${local.workspace_slug}", 0, 21), "-")}-${local.browser_readiness_name_hash}"
  browser_readiness_network_tag   = "${local.browser_readiness_job_name}-isolated"
  browser_readiness_allowed_ips = local.browser_readiness_enabled ? [
    google_compute_subnetwork.browser_readiness[0].external_ipv6_prefix,
  ] : []
}

# Direct VPC egress requires at least a /26. The browser receives a dedicated
# dual-stack subnet inside this environment's already-counted VPC, so protected
# previews do not consume one additional project-wide network quota each.
check "browser_readiness_subnet_isolated" {
  assert {
    condition = !local.browser_readiness_enabled || try(length(setintersection(
      toset([for offset in range(64) : cidrhost(var.browser_readiness_subnet_cidr, offset)]),
      toset([
        for offset in range(pow(2, 32 - tonumber(split("/", var.network_ip_cidr_range)[1]))) :
        cidrhost(var.network_ip_cidr_range, offset)
      ]),
    )) == 0, false)
    error_message = "browser_readiness_subnet_cidr must not overlap network_ip_cidr_range when protected preview browser readiness is enabled."
  }
}

resource "google_compute_subnetwork" "browser_readiness" {
  count = local.browser_readiness_enabled ? 1 : 0

  project                  = var.project_id
  region                   = var.region
  name                     = local.browser_readiness_resource_name
  network                  = google_compute_network.application.self_link
  ip_cidr_range            = var.browser_readiness_subnet_cidr
  stack_type               = "IPV4_IPV6"
  ipv6_access_type         = "EXTERNAL"
  private_ip_google_access = false

  lifecycle {
    postcondition {
      condition = (
        can(cidrhost(self.external_ipv6_prefix, 0)) &&
        strcontains(self.external_ipv6_prefix, ":") &&
        endswith(self.external_ipv6_prefix, "/64")
      )
      error_message = "The preview browser readiness subnet must receive one canonical external IPv6 /64."
    }
  }
}

# Public Cloud NAT does not translate traffic to run.app: it automatically
# enables Private Google Access for covered IPv4 ranges, and Cloud Run can then
# observe 0.0.0.0 instead of the reserved NAT address. The browser therefore
# forces canonical Scribe traffic over its environment-owned external IPv6
# /64. This IPv4 address remains only for fixed DNS and reviewed IPv4-only
# origins and is never included in a PPB policy.
resource "google_compute_address" "browser_readiness" {
  count = local.browser_readiness_enabled ? 1 : 0

  project      = var.project_id
  region       = var.region
  name         = local.browser_readiness_resource_name
  address_type = "EXTERNAL"
  network_tier = "PREMIUM"
}

resource "google_compute_router" "browser_readiness" {
  count = local.browser_readiness_enabled ? 1 : 0

  project = var.project_id
  region  = var.region
  name    = local.browser_readiness_resource_name
  network = google_compute_network.application.self_link
}

resource "google_compute_router_nat" "browser_readiness" {
  count = local.browser_readiness_enabled ? 1 : 0

  project                            = var.project_id
  region                             = var.region
  name                               = local.browser_readiness_resource_name
  router                             = google_compute_router.browser_readiness[0].name
  nat_ip_allocate_option             = "MANUAL_ONLY"
  nat_ips                            = [google_compute_address.browser_readiness[0].self_link]
  source_subnetwork_ip_ranges_to_nat = "LIST_OF_SUBNETWORKS"

  subnetwork {
    name                    = google_compute_subnetwork.browser_readiness[0].self_link
    source_ip_ranges_to_nat = ["ALL_IP_RANGES"]
  }
}

# The PR-head preview runner is untrusted. Its network tag and exact egress deny
# prevent direct access to the environment's private application subnet while
# preserving public DNS, canonical IPv6, and reviewed fixture traffic.
resource "google_compute_firewall" "browser_readiness_isolation" {
  count = local.browser_readiness_enabled ? 1 : 0

  project            = var.project_id
  name               = "${local.browser_readiness_job_name}-egress"
  network            = google_compute_network.application.self_link
  direction          = "EGRESS"
  priority           = 100
  destination_ranges = [var.network_ip_cidr_range]
  target_tags        = [local.browser_readiness_network_tag]

  deny {
    protocol = "all"
  }
}

resource "google_service_account" "backend_readiness" {
  project      = var.project_id
  account_id   = trimsuffix(substr("probe-backend-${local.workspace_slug}", 0, 30), "-")
  display_name = "Scribe ${local.workspace_slug} backend readiness"
  description  = "No-data, no-invoker runtime identity used only to verify the frontend-to-backend path."
}

resource "google_service_account" "ocr_readiness" {
  project      = var.project_id
  account_id   = trimsuffix(substr("probe-ocr-${local.workspace_slug}", 0, 30), "-")
  display_name = "Scribe ${local.workspace_slug} OCR readiness"
  description  = "No-data runtime identity allowed to invoke only the OCR services exercised by the deep probe."
}

resource "google_service_account" "browser_readiness" {
  count = local.browser_readiness_enabled ? 1 : 0

  project      = var.project_id
  account_id   = local.browser_readiness_account_id
  display_name = substr("Scribe ${local.workspace_slug} browser readiness", 0, 100)
  description  = substr("Preview-only, no-data identity for canonical browser ingress in ${local.workspace_slug}.", 0, 256)
}

check "readiness_identity_isolated" {
  assert {
    condition = length(toset(compact([
      google_service_account.backend_readiness.email,
      google_service_account.ocr_readiness.email,
      try(google_service_account.browser_readiness[0].email, ""),
      module.scribe.appGsa.email,
      module.scribe.instance.gsa.email,
      google_service_account.ocr_compute.email,
    ]))) == (local.browser_readiness_enabled ? 6 : 5)
    error_message = "Browser readiness, backend readiness, OCR readiness, app, VM, and OCR compute workloads must use distinct identities."
  }
}

resource "google_cloud_run_v2_job" "browser_readiness" {
  count = local.browser_readiness_enabled ? 1 : 0

  name                = local.browser_readiness_job_name
  location            = var.region
  deletion_protection = false

  template {
    parallelism = 1
    task_count  = 1

    template {
      service_account = google_service_account.browser_readiness[0].email
      max_retries     = 0
      # The deployed browser runner stops product work after 30 minutes and
      # reserves its final 10 minutes for bounded cleanup and reconciliation.
      timeout = "2400s"

      containers {
        image = local.normalized_browser_readiness_image

        resources {
          limits = {
            cpu    = "2"
            memory = "2Gi"
          }
        }

        env {
          name  = "SCRIBE_BROWSER_BASE_URL"
          value = local.public_base_url
        }

        # Playwright's APIRequestContext uses Node networking rather than the
        # Chromium resolver rule. Put AAAA first and disable IPv4 racing so all
        # canonical API requests use the same PPB-authorized IPv6 path.
        env {
          name  = "NODE_OPTIONS"
          value = "--dns-result-order=ipv6first --no-network-family-autoselection"
        }
      }

      vpc_access {
        egress = "ALL_TRAFFIC"
        network_interfaces {
          network    = local.browser_readiness_network_resource_name
          subnetwork = local.browser_readiness_subnetwork_resource_name
          tags       = [local.browser_readiness_network_tag]
        }
      }
    }
  }

  depends_on = [
    google_compute_firewall.browser_readiness_isolation,
    google_compute_router_nat.browser_readiness,
    google_service_account.browser_readiness,
    module.scribe,
  ]
}

resource "google_cloud_run_v2_job" "backend_readiness" {
  count = trimspace(var.frontend_gar_image) == "" ? 0 : 1

  name                = "${var.name}-${local.workspace_slug}-backend-readiness"
  location            = var.region
  deletion_protection = false

  template {
    parallelism = 1
    task_count  = 1

    template {
      # The probed image may be supplied by a pull request. This identity has no
      # data-plane, Vault, Pub/Sub, or project-level IAM grants.
      service_account = google_service_account.backend_readiness.email
      max_retries     = 0
      timeout         = "300s"

      containers {
        image   = var.frontend_gar_image
        command = ["node"]
        args    = ["readiness-job.mjs"]

        resources {
          limits = {
            cpu    = "1"
            memory = "512Mi"
          }
        }

        env {
          name  = "SCRIBE_EXPECTED_API_IMAGE"
          value = var.api_image
        }

        env {
          name  = "SCRIBE_EXPECTED_PUBLIC_ORIGIN"
          value = local.public_base_url
        }

        env {
          name  = "SCRIBE_EXPECTED_BACKEND_IP"
          value = module.scribe.internal_ip
        }
      }

      vpc_access {
        egress = "PRIVATE_RANGES_ONLY"
        network_interfaces {
          network    = local.readiness_network_resource_name
          subnetwork = local.readiness_subnetwork_resource_name
        }
      }
    }
  }

  depends_on = [module.scribe, google_service_account.backend_readiness]
}

locals {
  # Use the reviewed, digest-pinned API image as a tiny shell runtime. The probe
  # sends one repository-owned PNG through segmentation, Kraken transcription,
  # and (in production) the default Ollama generation endpoint. It never needs
  # uploads-bucket or Vault access. Bounded retries absorb identity propagation
  # and cold starts; a successful response that violates its contract fails
  # immediately instead of repeating expensive inference.
  ocr_readiness_script = file("${local.repo_root}/scripts/ocr-readiness.sh")
}

resource "google_cloud_run_v2_job" "ocr_readiness" {
  # The checked-in OCR catalog requires both services. Keep resource
  # cardinality plan-time stable when a fresh workspace creates their URLs.
  count = 1

  name                = "${var.name}-${local.workspace_slug}-ocr-readiness"
  location            = var.region
  deletion_protection = false

  template {
    parallelism = 1
    task_count  = 1

    template {
      # scripts/ocr-readiness.sh has a tested 1460-second retry/transfer budget.
      service_account = google_service_account.ocr_readiness.email
      max_retries     = 0
      timeout         = "1800s"

      containers {
        image   = var.api_image
        command = ["/bin/sh", "-c"]
        args    = [local.ocr_readiness_script]

        env {
          name  = "SEGMENTOR_URL"
          value = local.segmentor_url
        }
        env {
          name  = "TRANSCRIBER_URL"
          value = local.default_kraken_url
        }
        env {
          name  = "SEGMENTATION_MODEL"
          value = "tesseract"
        }
        env {
          name  = "TRANSCRIPTION_MODEL"
          value = local.kraken_default_transcription_key
        }
        env {
          name  = "SMOKE_IMAGE_BASE64"
          value = trimspace(file("${local.repo_root}/config/readiness-smoke.png.base64"))
        }
        env {
          name  = "OLLAMA_URL"
          value = local.is_prod_workspace ? local.default_ollama_url : ""
        }
        env {
          name  = "OLLAMA_MODEL"
          value = local.default_ollama_model
        }

        resources {
          limits = {
            cpu    = "1"
            memory = "512Mi"
          }
        }
      }

      vpc_access {
        egress = "PRIVATE_RANGES_ONLY"
        network_interfaces {
          network    = local.readiness_network_resource_name
          subnetwork = local.readiness_subnetwork_resource_name
        }
      }
    }
  }

  depends_on = [
    google_cloud_run_v2_service_iam_member.ocr_readiness_invoker,
    google_cloud_run_v2_service_iam_member.ollama_readiness_invoker,
    google_service_account.ocr_readiness,
    module.kraken,
  ]
}

check "production_deep_readiness_targets" {
  assert {
    condition = !local.is_prod_workspace || (
      trimspace(local.segmentor_url) != "" &&
      trimspace(local.default_kraken_url) != "" &&
      trimspace(local.default_ollama_url) != ""
    )
    error_message = "Production readiness requires segmentor, default Kraken, and default Ollama endpoints."
  }
}
