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

# Foundation owns project APIs independently of every runtime workspace.
resource "google_project_service_identity" "pubsub" {
  provider   = google-beta
  project    = var.project_id
  service    = "pubsub.googleapis.com"
  depends_on = [google_project_service.runtime["pubsub.googleapis.com"]]
}

resource "google_project_service" "runtime" {
  for_each           = toset(["run.googleapis.com", "compute.googleapis.com", "sqladmin.googleapis.com", "dns.googleapis.com", "servicedirectory.googleapis.com", "pubsub.googleapis.com", "cloudscheduler.googleapis.com", "iam.googleapis.com", "iamcredentials.googleapis.com", "monitoring.googleapis.com", "logging.googleapis.com"])
  project            = var.project_id
  service            = each.value
  disable_on_destroy = false
  deletion_policy    = "ABANDON"
  depends_on         = [google_project_service.control_plane]
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

  # KEEP wins over DELETE. Production runs the :main tag of each package, and
  # the most recent versions cover dev and open previews; everything else ages
  # out so the registry does not grow without bound.
  cleanup_policies {
    id     = "keep-main"
    action = "KEEP"

    condition {
      tag_state    = "TAGGED"
      tag_prefixes = ["main"]
    }
  }

  cleanup_policies {
    id     = "keep-recent-versions"
    action = "KEEP"

    most_recent_versions {
      keep_count = 10
    }
  }

  cleanup_policies {
    id     = "delete-untagged"
    action = "DELETE"

    condition {
      tag_state  = "UNTAGGED"
      older_than = "604800s"
    }
  }

  cleanup_policies {
    id     = "delete-stale"
    action = "DELETE"

    condition {
      tag_state  = "ANY"
      older_than = "2592000s"
    }
  }

  depends_on = [google_project_service.artifact_registry]
}

resource "google_project_iam_custom_role" "preview_artifact_registry_policy_manager" {
  project     = var.project_id
  role_id     = "scribePreviewArtifactPolicy"
  title       = "Scribe Preview Artifact Policy Manager"
  description = "Allows protected preview Terraform to reconcile Cloud Run image reader access on the single reviewed Artifact Registry repository."
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
