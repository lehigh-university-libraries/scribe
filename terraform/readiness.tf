# The public PPB ingress is intentionally source-IP restricted, so a hosted
# GitHub runner cannot reliably exercise the frontend's private backend origin.
# This job starts server.mjs from the exact deployed frontend image and probes
# its own /healthz proxy over the same direct-VPC network path as the sidecar.
# The deploy workflow executes it after every apply and fails if either the
# baked backend origin or the live backend route is wrong.
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
      }

      vpc_access {
        egress = "PRIVATE_RANGES_ONLY"
        network_interfaces {
          network    = module.scribe.network.self_link
          subnetwork = module.scribe.network.subnetwork
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
  # uploads-bucket or Vault access.
  ocr_readiness_script = <<-EOT
    set -eu
    boundary="scribe-readiness-boundary"
    printf '%s' "$SMOKE_IMAGE_BASE64" | base64 -d > /tmp/readiness.png
    test "$(wc -c < /tmp/readiness.png)" -gt 1000
    make_body() {
      model="$1"
      {
        printf -- '--%s\r\nContent-Disposition: form-data; name="model"\r\n\r\n%s\r\n' "$boundary" "$model"
        printf -- '--%s\r\nContent-Disposition: form-data; name="image"; filename="readiness.png"\r\nContent-Type: image/png\r\n\r\n' "$boundary"
        cat /tmp/readiness.png
        printf '\r\n--%s--\r\n' "$boundary"
      } > /tmp/readiness.multipart
    }
    invoke() {
      audience="$1"
      path="$2"
      model="$3"
      kind="$4"
      token="$(wget -qO- --header='Metadata-Flavor: Google' "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/identity?audience=$${audience}&format=full")"
      test -n "$token"
      make_body "$model"
      wget -qO /tmp/readiness.response \
        --header="Authorization: Bearer $${token}" \
        --header="Content-Type: multipart/form-data; boundary=$${boundary}" \
        --post-file=/tmp/readiness.multipart \
        "$${audience}$${path}"
      case "$kind" in
        segment)
          grep -Eq '"words":\[[^]]' /tmp/readiness.response
          ;;
        transcribe)
          grep -Fq "\"model\":\"$${model}\"" /tmp/readiness.response
          grep -Eq '"text":"[^\"]+"' /tmp/readiness.response
          ;;
        *) exit 2 ;;
      esac
    }
    identity_token() {
      audience="$1"
      wget -qO- --header='Metadata-Flavor: Google' "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/identity?audience=$${audience}&format=full"
    }
    invoke "$SEGMENTOR_URL" /v1/segment kraken segment
    invoke "$TRANSCRIBER_URL" /v1/transcribe "$TRANSCRIPTION_MODEL" transcribe
    if [ -n "$OLLAMA_URL" ]; then
      ollama_token="$(identity_token "$OLLAMA_URL")"
      test -n "$ollama_token"
      printf '{"model":"%s","prompt":"Transcribe the visible text. Return only the text.","images":["%s"],"stream":false}' \
        "$OLLAMA_MODEL" "$SMOKE_IMAGE_BASE64" > /tmp/ollama.json
      wget -qO /tmp/ollama.response \
        --header="Authorization: Bearer $${ollama_token}" \
        --header='Content-Type: application/json' \
        --post-file=/tmp/ollama.json \
        "$${OLLAMA_URL}/api/generate"
      grep -Eq '"response":"[^\"]+' /tmp/ollama.response
      grep -Eq '"done":true' /tmp/ollama.response
    fi
  EOT
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
      service_account = google_service_account.ocr_readiness.email
      max_retries     = 0
      timeout         = "600s"

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
          network    = module.scribe.network.self_link
          subnetwork = module.scribe.network.subnetwork
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
