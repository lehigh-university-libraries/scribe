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

check "readiness_identity_isolated" {
  assert {
    condition = length(toset([
      google_service_account.backend_readiness.email,
      google_service_account.ocr_readiness.email,
      module.scribe.appGsa.email,
      module.scribe.instance.gsa.email,
      google_service_account.ocr_compute.email,
    ])) == 5
    error_message = "Backend readiness, OCR readiness, app, VM, and OCR compute workloads must use distinct identities."
  }
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
  count = trimspace(local.segmentor_url) != "" && trimspace(local.default_kraken_url) != "" ? 1 : 0

  name                = "${var.name}-${local.workspace_slug}-ocr-readiness"
  location            = var.region
  deletion_protection = false

  template {
    parallelism = 1
    task_count  = 1

    template {
      # scripts/ocr-readiness.sh has a tested 980-second retry/transfer budget.
      service_account = google_service_account.ocr_readiness.email
      max_retries     = 0
      timeout         = "1500s"

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
          value = local.kraken_default_segmentation_key
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
