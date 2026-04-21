provider "google" {
  project = var.project_id
  region  = var.region
}

provider "google-beta" {
  project = var.project_id
  region  = var.region
}

provider "docker" {
  registry_auth {
    address     = "us-docker.pkg.dev"
    config_file = pathexpand("~/.docker/config.json")
  }
}

data "google_client_config" "current" {}

data "google_project" "current" {
  project_id = var.project_id
}

locals {
  project_number                      = tostring(data.google_project.current.number)
  disk_type                           = "hyperdisk-balanced"
  terraform_state_bucket              = trimspace(var.terraform_state_bucket) != "" ? trimspace(var.terraform_state_bucket) : "${var.project_id}-terraform"
  shared_artifact_registry_location   = "us"
  shared_artifact_registry_repository = "internal"
  is_prod_workspace                   = terraform.workspace == "prod"
  is_dev_workspace                    = terraform.workspace == "dev"
  shared_vault_workspace              = local.is_prod_workspace ? "prod" : "dev"
  vault_is_owner_workspace            = local.is_prod_workspace || local.is_dev_workspace
  workspace_slug                      = replace(lower(terraform.workspace), "/[^a-z0-9-]+/", "-")
  vault_app_role_name                 = "scribe-app-${local.workspace_slug}"
}

data "google_artifact_registry_repository" "internal" {
  project       = var.project_id
  location      = local.shared_artifact_registry_location
  repository_id = local.shared_artifact_registry_repository
}

data "terraform_remote_state" "shared_vault" {
  count   = local.vault_is_owner_workspace ? 0 : 1
  backend = "gcs"
  config = {
    bucket = local.terraform_state_bucket
    prefix = "scribe"
  }
  workspace = local.shared_vault_workspace
}

locals {
  shared_vault_outputs = local.vault_is_owner_workspace ? {} : data.terraform_remote_state.shared_vault[0].outputs
  vault_url            = local.vault_is_owner_workspace ? module.vault[0].vault-url : try(local.shared_vault_outputs.vault_url, "")

  docker_compose_repo = "https://github.com/lehigh-university-libraries/scribe.git"
  compose_env_prefix = (
    trimspace(var.frontend_image) == ""
    ? format("SCRIBE_API_IMAGE=%s", var.api_image)
    : format("SCRIBE_API_IMAGE=%s SCRIBE_FRONTEND_IMAGE=%s", var.api_image, var.frontend_image)
  )
  docker_compose_init = [
    "bash generate-secrets.sh",
    format("bash /home/cloud-compose/configure-scribe-config.sh config.yaml %q %q", local.vault_url, local.vault_app_role_name),
  ]
  docker_compose_up = [
    for cmd in [
      "git pull",
      "docker compose pull api worker frontend",
      "docker compose up --no-build",
    ] :
    length(regexall("docker compose", cmd)) > 0 ? format("%s %s", local.compose_env_prefix, cmd) : cmd
  ]
  docker_compose_down = [
    for cmd in ["docker compose down"] :
    length(regexall("docker compose", cmd)) > 0 ? format("%s %s", local.compose_env_prefix, cmd) : cmd
  ]
}

check "shared_vault_ready" {
  assert {
    condition     = local.vault_is_owner_workspace || trimspace(local.vault_url) != ""
    error_message = "Shared Vault workspace '${local.shared_vault_workspace}' must be applied first and expose the root output 'vault_url'. Apply the dev workspace before running preview environments."
  }
}

check "shared_internal_repository_ready" {
  assert {
    condition     = trimspace(data.google_artifact_registry_repository.internal.id) != ""
    error_message = "Artifact Registry repository '${local.shared_artifact_registry_repository}' in location '${local.shared_artifact_registry_location}' must already exist in project '${var.project_id}'."
  }
}

provider "vault" {
  address = local.vault_url
  headers {
    name  = "X-Admin-Token"
    value = data.google_client_config.current.access_token
  }
}

module "scribe" {
  source = "git::https://github.com/libops/cloud-compose?ref=f74adaebad82193c613df23434d3c4c9d444a837"

  project_id            = var.project_id
  project_number        = local.project_number
  name                  = var.name
  region                = var.region
  zone                  = var.zone
  machine_type          = var.machine_type
  disk_type             = local.disk_type
  disk_size_gb          = var.disk_size_gb
  docker_compose_repo   = local.docker_compose_repo
  docker_compose_branch = var.docker_compose_branch
  docker_compose_init   = local.docker_compose_init
  docker_compose_up     = local.docker_compose_up
  docker_compose_down   = local.docker_compose_down
  allowed_ips           = var.allowed_ips
  allowed_ssh_ipv4      = var.allowed_ssh_ipv4
  allowed_ssh_ipv6      = var.allowed_ssh_ipv6
  users                 = var.users
  run_snapshots         = var.run_snapshots
  rootfs                = "${path.module}/rootfs"
  frontend = trimspace(var.frontend_gar_image) == "" ? null : {
    image = var.frontend_gar_image
  }
}
