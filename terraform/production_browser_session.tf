locals {
  production_browser_session_enabled   = local.is_prod_workspace && local.browser_readiness_enabled
  production_browser_session_secret_id = "scribe-browser-session-${local.browser_readiness_name_hash}"
}

# The production browser receives one ordinary, short-lived application
# session through this exact secret. Terraform owns only the stable container
# and a harmless placeholder; the protected deploy workflow creates and
# destroys each credential-bearing version around one Cloud Run execution.
resource "google_secret_manager_secret" "browser_session" {
  count = local.production_browser_session_enabled ? 1 : 0

  project   = var.project_id
  secret_id = local.production_browser_session_secret_id

  replication {
    auto {}
  }

  labels = {
    component = "browser-readiness"
    workspace = local.workspace_slug
  }
}

# Terraform pins the idle Cloud Run job to this non-credential version 1. The
# protected transport temporarily updates only that job to an exact numeric
# credential version, then restores version 1 before destroying the credential.
# The write-only argument prevents even this inert JSON from entering state.
resource "google_secret_manager_secret_version" "browser_session_placeholder" {
  count = local.production_browser_session_enabled ? 1 : 0

  secret                 = google_secret_manager_secret.browser_session[0].id
  enabled                = true
  secret_data_wo         = jsonencode({ cookies = [], origins = [] })
  secret_data_wo_version = 1

  lifecycle {
    postcondition {
      condition     = self.version == "1"
      error_message = "The Terraform-owned browser session placeholder must be the secret's immutable version 1. Import or recreate the secret before enabling production browser readiness."
    }
  }
}

resource "google_secret_manager_secret_iam_member" "browser_session_accessor" {
  count = local.production_browser_session_enabled ? 1 : 0

  project   = var.project_id
  secret_id = google_secret_manager_secret.browser_session[0].secret_id
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${google_service_account.browser_readiness[0].email}"
}

resource "google_secret_manager_secret_iam_member" "browser_session_version_manager" {
  count = local.production_browser_session_enabled ? 1 : 0

  project   = var.project_id
  secret_id = google_secret_manager_secret.browser_session[0].secret_id
  role      = "roles/secretmanager.secretVersionManager"
  member    = "serviceAccount:${local.production_deploy_service_account_email}"
}
