provider "google" {
  project = var.project_id
}

provider "google-beta" {
  project = var.project_id
}

locals {
  artifact_registry_location           = "us"
  artifact_registry_repository         = "internal"
  preview_deploy_service_account_email = "scribe-preview-deploy@${var.project_id}.iam.gserviceaccount.com"
  control_plane_services = toset([
    "servicemanagement.googleapis.com",
    "serviceusage.googleapis.com",
  ])
}

resource "google_project_service" "control_plane" {
  for_each = local.control_plane_services

  project            = var.project_id
  service            = each.value
  disable_on_destroy = false
  deletion_policy    = "ABANDON"
}

# This state is deliberately separate from every application workspace. It is
# applied before any GAR build or app plan, so a clean project has an acyclic
# bootstrap path: external WIF/state -> foundation -> reviewed images -> app.
module "cloud_compose" {
  source = "https://github.com/libops/cloud-compose/archive/refs/tags/1.11.1.tar.gz//cloud-compose-1.11.1/modules/gcp-foundation?archive=tar.gz"
  providers = {
    google      = google
    google-beta = google-beta
  }

  service_project_id = var.project_id

  depends_on = [google_project_service.control_plane]
}

# Cloud Run's ordinary service-agent role covers Direct VPC egress, but Google
# requires this additional role when the selected subnet uses external IPv6.
# Own the project-wide grant once in foundation state; preview workspaces must
# never race each other by managing the same IAM member independently.
resource "google_project_iam_member" "cloud_run_external_ipv6" {
  project = var.project_id
  role    = "roles/compute.publicIpAdmin"
  member  = module.cloud_compose.cloud_run_service_agent_member

  lifecycle {
    prevent_destroy = true
  }
}

resource "google_project_service" "artifact_registry" {
  project            = var.project_id
  service            = "artifactregistry.googleapis.com"
  disable_on_destroy = false
  deletion_policy    = "ABANDON"

  depends_on = [google_project_service.control_plane]
}

resource "google_project_service" "cloud_trace" {
  project            = var.project_id
  service            = "cloudtrace.googleapis.com"
  disable_on_destroy = false
  deletion_policy    = "ABANDON"

  depends_on = [google_project_service.control_plane]
}

resource "google_project_service" "secret_manager" {
  project            = var.project_id
  service            = "secretmanager.googleapis.com"
  disable_on_destroy = false
  deletion_policy    = "ABANDON"

  depends_on = [google_project_service.control_plane]
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

resource "google_project_iam_custom_role" "preview_artifact_registry_policy_manager" {
  project     = var.project_id
  role_id     = "scribePreviewArtifactPolicy"
  title       = "Scribe Preview Artifact Policy Manager"
  description = "Allows protected preview Terraform to reconcile VM reader access on the single reviewed Artifact Registry repository."
  permissions = [
    "artifactregistry.repositories.getIamPolicy",
    "artifactregistry.repositories.setIamPolicy",
  ]
  stage = "GA"

  deletion_policy = "PREVENT"

  lifecycle {
    prevent_destroy = true
  }
}

resource "google_artifact_registry_repository_iam_member" "preview_deploy_policy_manager" {
  project    = var.project_id
  location   = google_artifact_registry_repository.internal.location
  repository = google_artifact_registry_repository.internal.repository_id
  role       = google_project_iam_custom_role.preview_artifact_registry_policy_manager.name
  member     = "serviceAccount:${local.preview_deploy_service_account_email}"
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
