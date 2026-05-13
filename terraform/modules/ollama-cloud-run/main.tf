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

  primary_url = try(module.service.urls[var.regions[0]], "")
}

resource "google_service_account" "service" {
  project      = var.project_id
  account_id   = local.service_account_id
  display_name = "Ollama ${var.model}"
}

module "service" {
  source = "git::https://github.com/libops/terraform-cloudrun-v2?ref=903c0758f5b19740a233558d097efdccabece7c5"

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
      image  = var.image
      port   = 8080
      memory = var.memory
      cpu    = var.cpu
      gpus   = var.gpu_count
    }
  ])
}
