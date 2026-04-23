locals {
  model_slug = replace(
    replace(lower(trimspace(var.model)), "/[^a-z0-9]+/", "-"),
    "/^-+|-+$/",
    ""
  )

  service_name = replace(
    substr(trimspace(var.name) != "" ? trimspace(var.name) : "ollama-${local.model_slug}", 0, 63),
    "/-+$/",
    ""
  )

  service_account_id = replace(
    substr("cr-${substr(local.model_slug, 0, 20)}-${substr(md5(var.model), 0, 6)}", 0, 30),
    "/-+$/",
    ""
  )

  image_name  = "${var.artifact_registry_location}-docker.pkg.dev/${var.project_id}/${var.artifact_registry_repository}/${local.service_name}:${var.image_tag}"
  primary_url = try(module.service.urls[var.regions[0]], "")
  image_dir_sha = sha1(join("", [
    for f in fileset(path.module, "image/**") :
    filesha1("${path.module}/${f}")
  ]))
}

resource "google_service_account" "service" {
  project      = var.project_id
  account_id   = local.service_account_id
  display_name = "Ollama ${var.model}"
}

resource "google_project_iam_member" "service_user" {
  project = var.project_id
  role    = "roles/iam.serviceAccountUser"
  member  = "serviceAccount:${google_service_account.service.email}"
}

resource "docker_image" "image" {
  name = local.image_name

  build {
    context    = "${path.module}/image"
    dockerfile = "Dockerfile"
    build_args = {
      OLLAMA_BASE_IMAGE = var.base_image
      OLLAMA_MODEL      = var.model
    }
  }

  triggers = {
    dir_sha = local.image_dir_sha
    base    = var.base_image
    model   = var.model
  }

  keep_locally = false
}

resource "docker_registry_image" "image" {
  name          = docker_image.image.name
  keep_remotely = true

  triggers = {
    dir_sha = local.image_dir_sha
    base    = var.base_image
    model   = var.model
  }
}

module "service" {
  source = "git::https://github.com/libops/terraform-cloudrun-v2?ref=0.5.2"

  name          = local.service_name
  project       = var.project_id
  gsa           = google_service_account.service.email
  min_instances = var.min_instances
  max_instances = var.max_instances
  skipNeg       = var.skip_neg
  regions       = var.regions
  invokers      = var.invokers

  containers = tolist([
    {
      name   = "ollama"
      image  = "${local.image_name}@${docker_registry_image.image.sha256_digest}"
      port   = 8080
      memory = var.memory
      cpu    = var.cpu
      gpus   = var.gpu_count
    }
  ])
}
