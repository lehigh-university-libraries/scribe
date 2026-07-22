provider "google" {
  project = var.project_id
}

provider "google-beta" {
  project = var.project_id
}

locals {
  artifact_registry_location   = "us"
  artifact_registry_repository = "internal"
}

# This state is deliberately separate from every application workspace. It is
# applied before any GAR build or app plan, so a clean project has an acyclic
# bootstrap path: external WIF/state -> foundation -> reviewed images -> app.
module "cloud_compose" {
  source = "https://github.com/libops/cloud-compose/archive/521d99a481ae8560c0b130e15d103d8e079001f2.tar.gz//cloud-compose-521d99a481ae8560c0b130e15d103d8e079001f2/modules/gcp-foundation?archive=tar.gz"
  providers = {
    google      = google
    google-beta = google-beta
  }

  service_project_id = var.project_id
}

resource "google_project_service" "artifact_registry" {
  project            = var.project_id
  service            = "artifactregistry.googleapis.com"
  disable_on_destroy = false
  deletion_policy    = "ABANDON"
}

resource "google_artifact_registry_repository" "internal" {
  project       = var.project_id
  location      = local.artifact_registry_location
  repository_id = local.artifact_registry_repository
  description   = "Reviewed Scribe runtime images"
  format        = "DOCKER"

  cleanup_policy_dry_run = false

  cleanup_policies {
    id     = "retain-recent-versions"
    action = "KEEP"

    most_recent_versions {
      keep_count = 20
    }
  }

  depends_on = [google_project_service.artifact_registry]
}

resource "google_project_iam_custom_role" "vault_gcp_auth_key_verifier" {
  project     = var.project_id
  role_id     = "vaultGcpAuthKeyVerifier"
  title       = "Vault GCP Auth Key Verifier"
  description = "Allows Vault to verify service-account JWT signing keys without key mutation."
  permissions = ["iam.serviceAccountKeys.get"]
  stage       = "GA"

  deletion_policy = "PREVENT"

  lifecycle {
    prevent_destroy = true
  }
}

# Production retains proxy-power-button startup/proxying but may not suspend
# itself. This observation-only role is passed in the module's suspend slot;
# lightsout therefore fails closed instead of racing the nightly backup timer.
resource "google_project_iam_custom_role" "cloud_compose_observe" {
  project     = var.project_id
  role_id     = "cloudComposeObserve"
  title       = "Cloud Compose Observe"
  description = "Allows production lightsout to inspect, but never suspend, the Scribe VM."
  permissions = ["compute.instances.get"]
  stage       = "GA"

  deletion_policy = "PREVENT"

  lifecycle {
    prevent_destroy = true
  }
}
