locals {
  readiness_network_resource_name    = google_compute_network.application.id
  readiness_subnetwork_resource_name = google_compute_subnetwork.application.id
}

resource "google_service_account" "backend_readiness" {
  project      = var.project_id
  account_id   = trimsuffix(substr("probe-backend-${local.workspace_slug}", 0, 30), "-")
  display_name = "Scribe ${local.workspace_slug} backend readiness"
  description  = "No-data identity allowed to probe the API and invoke only the worker readiness endpoint."
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
      google_service_account.app.email,
      google_service_account.worker.email,
      google_service_account.ocr_compute.email,
    ])) == 5
    error_message = "Backend readiness, OCR readiness, API, worker, and OCR compute workloads must use distinct identities."
  }
}

resource "google_cloud_run_v2_job" "backend_readiness" {
  count = trimspace(local.frontend_image) == "" ? 0 : 1

  name                = "${local.name}-${local.workspace_slug}-backend-readiness"
  location            = var.region
  deletion_protection = false

  template {
    parallelism = 1
    task_count  = 1

    template {
      # The probed image may be supplied by a pull request. This identity has no
      # data-plane, Secret Manager, Pub/Sub, or project-level IAM grants.
      service_account = google_service_account.backend_readiness.email
      max_retries     = 0
      timeout         = "300s"

      containers {
        image   = local.api_image
        command = ["/app/scribe-readiness"]

        resources {
          limits = {
            cpu    = "1"
            memory = "512Mi"
          }
        }

        env {
          name  = "SCRIBE_EXPECTED_API_IMAGE"
          value = local.api_image
        }

        env {
          name  = "SCRIBE_EXPECTED_PUBLIC_ORIGIN"
          value = local.public_base_url
        }

        env {
          name  = "SCRIBE_API_ORIGIN"
          value = local.public_base_url
        }
        env {
          name  = "SCRIBE_WORKER_ORIGIN"
          value = google_cloud_run_v2_service.worker.uri
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

  depends_on = [google_cloud_run_v2_service.application, google_cloud_run_v2_service_iam_member.worker_readiness]
}

locals {
  # Use the reviewed, digest-pinned API image as a tiny shell runtime. The probe
  # sends one repository-owned PNG through segmentation, newspaper layout,
  # and (in production) the default Ollama generation endpoint. It never needs
  # uploads-bucket or Secret Manager access. Bounded retries absorb identity propagation
  # and cold starts; a successful response that violates its contract fails
  # immediately instead of repeating expensive inference.
  ocr_readiness_script = file("${local.repo_root}/scripts/ocr-readiness.sh")
}

resource "google_cloud_run_v2_job" "ocr_readiness" {
  # The checked-in OCR catalog requires both services. Keep resource
  # cardinality plan-time stable when a fresh workspace creates their URLs.
  count = 1

  name                = "${local.name}-${local.workspace_slug}-ocr-readiness"
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
        image   = local.api_image
        command = ["/bin/sh", "-c"]
        args    = [local.ocr_readiness_script]

        env {
          name  = "SEGMENTOR_URL"
          value = local.segmentor_url
        }
        env {
          name  = "LAYOUT_URL"
          value = local.segmentor_url
        }
        env {
          name  = "SEGMENTATION_MODEL"
          value = local.kraken_default_segmentation_key
        }
        env {
          name  = "LAYOUT_MODEL"
          value = "newspapers"
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
      trimspace(local.default_ollama_url) != ""
    )
    error_message = "Production readiness requires segmentor, newspaper layout, and default Ollama endpoints."
  }
}

resource "google_cloud_run_v2_service_iam_member" "worker_readiness" {
  project  = var.project_id
  location = var.region
  name     = google_cloud_run_v2_service.worker.name
  role     = "roles/run.invoker"
  member   = "serviceAccount:${google_service_account.backend_readiness.email}"
}

# Readiness belongs to Terraform apply, including operator-driven Make targets.
resource "terraform_data" "readiness" {
  triggers_replace = [
    local.frontend_image,
    jsonencode(local.ocr_images),
  ]
  lifecycle {
    replace_triggered_by = [google_cloud_run_v2_service.application, google_cloud_run_v2_service.worker]
  }
  provisioner "local-exec" {
    command = "gcloud run jobs execute '${google_cloud_run_v2_job.backend_readiness[0].name}' --project '${var.project_id}' --region '${var.region}' --wait && gcloud run jobs execute '${google_cloud_run_v2_job.ocr_readiness[0].name}' --project '${var.project_id}' --region '${var.region}' --wait"
  }
  depends_on = [google_cloud_run_v2_service_iam_member.public, google_cloud_run_v2_service_iam_member.worker_readiness, google_cloud_run_v2_job.backend_readiness, google_cloud_run_v2_job.ocr_readiness]
}
