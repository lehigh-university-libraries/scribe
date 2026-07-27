locals {
  # This identity is intentionally absent from every production and preview
  # state graph. Contributors impersonate it with short-lived ADC credentials;
  # no service-account key resource belongs in this repository.
  dev_external_ocr_enabled = terraform.workspace == "dev"
}

resource "google_service_account" "dev_external_ocr" {
  count = local.dev_external_ocr_enabled ? 1 : 0

  project      = var.project_id
  account_id   = "scribe-dev-external"
  display_name = "Scribe dev external OCR"
  description  = "Keyless, developer-impersonated identity limited to invoking dev-owned OCR services."
}

resource "google_service_account_iam_member" "dev_external_ocr_token_creator" {
  for_each = local.dev_external_ocr_enabled ? var.dev_external_ocr_impersonators : toset([])

  service_account_id = google_service_account.dev_external_ocr[0].name
  role               = "roles/iam.serviceAccountTokenCreator"
  member             = each.value
}

resource "terraform_data" "dev_external_ocr_workspace_guard" {
  input = sort(tolist(var.dev_external_ocr_impersonators))

  lifecycle {
    precondition {
      condition     = local.dev_external_ocr_enabled || length(var.dev_external_ocr_impersonators) == 0
      error_message = "dev_external_ocr_impersonators must be empty outside the dev Terraform workspace."
    }
  }
}
