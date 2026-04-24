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
  repo_root                           = abspath("${path.module}/..")
  disk_type                           = "hyperdisk-balanced"
  terraform_state_bucket              = trimspace(var.terraform_state_bucket) != "" ? trimspace(var.terraform_state_bucket) : "${var.project_id}-terraform"
  shared_artifact_registry_location   = "us"
  shared_artifact_registry_repository = "internal"
  is_prod_workspace                   = terraform.workspace == "prod"
  shared_vault_workspace              = local.is_prod_workspace ? "prod" : "dev"
  shared_ollama_workspace             = "prod"
  vault_is_owner_workspace            = terraform.workspace == "prod" || terraform.workspace == "dev"
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
  count   = local.shared_ollama_services_enabled || length(local.ollama_models) == 0 ? 0 : 1
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
  default_ollama_url = !contains(local.ollama_models, local.default_ollama_model) ? "" : (
    local.shared_ollama_services_enabled ? module.ollama_services[local.default_ollama_model].primary_url :
    try(data.terraform_remote_state.shared_ollama[0].outputs.ollama_services[local.default_ollama_model].primary_url, "")
  )
  default_ollama_audience = !contains(local.ollama_models, local.default_ollama_model) ? "" : (
    local.shared_ollama_services_enabled ? module.ollama_services[local.default_ollama_model].audience :
    try(data.terraform_remote_state.shared_ollama[0].outputs.ollama_services[local.default_ollama_model].audience, "")
  )
  ollama_services_map = local.shared_ollama_services_enabled ? {
    for model, service in module.ollama_services :
    model => {
      primary_url = service.primary_url
      audience    = service.audience
    }
  } : try(data.terraform_remote_state.shared_ollama[0].outputs.ollama_services, {})
  ollama_endpoint_map = {
    for model, service in local.ollama_services_map :
    model => {
      url      = try(service.primary_url, "")
      audience = try(service.audience, "")
    }
    if trimspace(try(service.primary_url, "")) != ""
  }
  segmentor_url          = try(module.kraken["segmentor"].urls[var.region], try(module.kraken["segmentor"].urls[local.ocr_service_regions[0]], ""))
  segmentor_audience     = local.segmentor_url
  image_service_url      = try(module.kraken["image-service"].urls[var.region], try(module.kraken["image-service"].urls[local.ocr_service_regions[0]], ""))
  image_service_audience = local.image_service_url
  kraken_segmentation_services = {
    for name, service in module.kraken :
    service.route_key => {
      primary_url = service.primary_url
      audience    = service.audience
    }
    if service.route_type == "kraken-segmentation"
  }
  kraken_transcription_services = {
    for name, service in module.kraken :
    service.route_key => {
      primary_url = service.primary_url
      audience    = service.audience
    }
    if service.route_type == "kraken-transcription"
  }
  kraken_segmentation_endpoint_map = {
    for model, service in local.kraken_segmentation_services :
    model => {
      url      = service.primary_url
      audience = service.audience
    }
    if trimspace(try(service.primary_url, "")) != ""
  }
  kraken_transcription_endpoint_map = {
    for model, service in local.kraken_transcription_services :
    model => {
      url      = service.primary_url
      audience = service.audience
    }
    if trimspace(try(service.primary_url, "")) != ""
  }
  default_kraken_url      = try(local.kraken_transcription_services[local.kraken_default_transcription_key].primary_url, "")
  default_kraken_audience = try(local.kraken_transcription_services[local.kraken_default_transcription_key].audience, "")
  docker_compose_services = concat(["mariadb", "api", "worker"], local.shared_services_enabled ? [] : ["cantaloupe"])

  docker_compose_repo = "https://github.com/lehigh-university-libraries/scribe.git"
  compose_env_vars = [
    {
      name  = "CANTALOUPE_IIIF_INTERNAL_BASE"
      value = local.shared_cantaloupe_internal_base
    },
    {
      name  = "OLLAMA_AUDIENCE"
      value = local.default_ollama_audience
    },
    {
      name  = "OLLAMA_URL"
      value = local.default_ollama_url
    },
    {
      name  = "OLLAMA_MODEL_ENDPOINTS_JSON"
      value = jsonencode(local.ollama_endpoint_map)
    },
    {
      name  = "SEGMENTATION_SERVICE_URL"
      value = local.segmentor_url
    },
    {
      name  = "SEGMENTATION_SERVICE_AUDIENCE"
      value = local.segmentor_audience
    },
    {
      name  = "SEGMENTATION_MODEL_ENDPOINTS_JSON"
      value = jsonencode(local.kraken_segmentation_endpoint_map)
    },
    {
      name  = "IMAGE_SERVICE_URL"
      value = local.image_service_url
    },
    {
      name  = "IMAGE_SERVICE_AUDIENCE"
      value = local.image_service_audience
    },
    {
      name  = "KRAKEN_URL"
      value = local.default_kraken_url
    },
    {
      name  = "KRAKEN_AUDIENCE"
      value = local.default_kraken_audience
    },
    {
      name  = "KRAKEN_MODEL"
      value = local.kraken_default_transcription_key
    },
    {
      name  = "KRAKEN_MODEL_ENDPOINTS_JSON"
      value = jsonencode(local.kraken_transcription_endpoint_map)
    },
    {
      name  = "PUBLIC_BASE_URL"
      value = local.public_base_url
    },
    {
      name  = "SCRIBE_API_IMAGE"
      value = var.api_image
    },
    {
      name  = "VAULT_ADDRESS"
      value = local.vault_url
    },
    {
      name  = "VAULT_GCP_AUTH_ROLE"
      value = local.vault_app_role_name
    },
  ]
  compose_env_update_commands = [
    for env in local.compose_env_vars :
    format(
      "update_env %s '%s'",
      env.name,
      replace(
        replace(
          replace(
            replace(env.value, "\\", "\\\\"),
            "&",
            "\\&",
          ),
          "/",
          "\\/",
        ),
        "'",
        "'\"'\"'",
      ),
    )
  ]
  docker_compose_init = concat(local.compose_env_update_commands, [
    "bash /home/cloud-compose/rotate-keys-app.sh",
    "bash generate-secrets.sh",
  ])
  docker_compose_up = concat(local.compose_env_update_commands, [
    "git pull",
    "docker compose pull api worker",
    format("docker compose up --no-build %s", join(" ", local.docker_compose_services)),
  ])
  docker_compose_down = concat(local.compose_env_update_commands, [
    "docker compose down",
  ])
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
