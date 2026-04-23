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
  shared_ollama_workspace             = "prod"
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

data "terraform_remote_state" "shared_ollama" {
  count   = local.shared_ollama_services_enabled || length(var.ollama_models) == 0 ? 0 : 1
  backend = "gcs"
  config = {
    bucket = local.terraform_state_bucket
    prefix = "scribe"
  }
  workspace = local.shared_ollama_workspace
}

locals {
  vault_url                       = local.vault_is_owner_workspace ? module.vault[0].vault-url : try(data.terraform_remote_state.shared_vault[0].outputs.vault_url, "")
  shared_cantaloupe_url           = local.shared_services_enabled ? try(module.cantaloupe[0].urls[var.region], try(module.cantaloupe[0].urls[local.cantaloupe_regions[0]], "")) : ""
  shared_cantaloupe_internal_base = trimspace(local.shared_cantaloupe_url) == "" ? "" : format("%s/iiif/2", trimsuffix(local.shared_cantaloupe_url, "/"))
  public_base_url                 = trimspace(var.app_domain) != "" ? format("https://%s", trimspace(var.app_domain)) : try(module.scribe.urls[var.region], "")
  default_ollama_model            = "glm-ocr:bf16"
  default_ollama_url = !contains(var.ollama_models, local.default_ollama_model) ? "" : (
    local.shared_ollama_services_enabled ? module.ollama_services[local.default_ollama_model].primary_url :
    try(data.terraform_remote_state.shared_ollama[0].outputs.ollama_services[local.default_ollama_model].primary_url, "")
  )
  default_ollama_audience = !contains(var.ollama_models, local.default_ollama_model) ? "" : (
    local.shared_ollama_services_enabled ? module.ollama_services[local.default_ollama_model].audience :
    try(data.terraform_remote_state.shared_ollama[0].outputs.ollama_services[local.default_ollama_model].audience, "")
  )
  docker_compose_services = concat(["mariadb", "api", "worker"], local.shared_services_enabled ? [] : ["cantaloupe"])

  docker_compose_repo   = "https://github.com/lehigh-university-libraries/scribe.git"
  compose_env_file_name = ".scribe-runtime.env"
  compose_env_lines = [
    format("CANTALOUPE_IIIF_INTERNAL_BASE=%s", local.shared_cantaloupe_internal_base),
    format("OLLAMA_AUDIENCE=%s", local.default_ollama_audience),
    format("OLLAMA_URL=%s", local.default_ollama_url),
    format("PUBLIC_BASE_URL=%s", local.public_base_url),
    format("SCRIBE_API_IMAGE=%s", var.api_image),
    format("VAULT_ADDRESS=%s", local.vault_url),
    format("VAULT_GCP_AUTH_ROLE=%s", local.vault_app_role_name),
  ]
  compose_env_line_args = [
    for line in local.compose_env_lines :
    format("'%s'", replace(line, "'", "'\"'\"'"))
  ]
  compose_env_write_command = format(
    "printf '%%s\\n' %s > %s",
    join(" ", local.compose_env_line_args),
    local.compose_env_file_name,
  )
  docker_compose_init = [
    "bash generate-secrets.sh",
    local.compose_env_write_command,
  ]
  docker_compose_up = [
    local.compose_env_write_command,
    "git pull",
    format("docker compose --env-file %s pull api worker", local.compose_env_file_name),
    format("docker compose --env-file %s up --no-build %s", local.compose_env_file_name, join(" ", local.docker_compose_services)),
  ]
  docker_compose_down = [
    local.compose_env_write_command,
    format("docker compose --env-file %s down", local.compose_env_file_name),
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
  source = "git::https://github.com/libops/cloud-compose?ref=0.6.1"

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
    port  = 8888
  }
}
