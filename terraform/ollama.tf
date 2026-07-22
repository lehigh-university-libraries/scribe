locals {
  ollama_model_specs             = try(local.ocr_config.ollama.models, {})
  ollama_models                  = sort(keys(local.ollama_model_specs))
  shared_ollama_services_enabled = terraform.workspace == "prod" && length(local.ollama_models) > 0
  ollama_preview_iam_enabled     = terraform.workspace != "prod" && length(local.ollama_models) > 0
  ollama_cloud_run               = try(local.ocr_config.ollama.cloud_run, {})
  ollama_regions                 = try(local.ollama_cloud_run.regions, ["us-east4"])
  ollama_memory                  = try(local.ollama_cloud_run.memory, "16Gi")
  ollama_cpu                     = try(local.ollama_cloud_run.cpu, "4000m")
  ollama_gpu_count               = try(local.ollama_cloud_run.gpu_count, 1)
  ollama_min_instances           = try(local.ollama_cloud_run.min_instances, 0)
  ollama_max_instances           = try(local.ollama_cloud_run.max_instances, 1)
  ollama_skip_neg                = try(local.ollama_cloud_run.skip_neg, true)
  scribe_vm_gsa_email            = format("vm-%s@%s.iam.gserviceaccount.com", var.name, var.project_id)
  scribe_app_gsa_email           = format("%s@%s.iam.gserviceaccount.com", var.name, var.project_id)

  ollama_service_names = {
    for model in local.ollama_models :
    model => replace(
      substr(
        "ollama-${replace(replace(lower(trimspace(model)), "/[^a-z0-9]+/", "-"), "/^-+|-+$/", "")}",
        0,
        63,
      ),
      "/-+$/",
      "",
    )
  }

  ollama_invoker_gsas = [
    local.scribe_vm_gsa_email,
    local.scribe_app_gsa_email,
  ]

  ollama_service_iam_bindings = local.shared_ollama_services_enabled ? {
    for triple in setproduct(
      local.ollama_models,
      local.ollama_regions,
      local.ollama_invoker_gsas,
    ) :
    "${triple[0]}|${triple[1]}|${triple[2]}" => {
      region  = triple[1]
      gsa     = triple[2]
      service = local.ollama_service_names[triple[0]]
    }
  } : {}

  ollama_preview_invoker_gsas = local.ollama_preview_iam_enabled ? [
    local.scribe_vm_gsa_email,
    local.scribe_app_gsa_email,
  ] : []

  ollama_preview_iam_bindings = {
    for triple in setproduct(
      local.ollama_models,
      local.ollama_regions,
      local.ollama_preview_invoker_gsas,
    ) :
    "${triple[0]}|${triple[1]}|${triple[2]}" => {
      region  = triple[1]
      gsa     = triple[2]
      service = local.ollama_service_names[triple[0]]
    }
  }
}

resource "google_artifact_registry_repository_iam_member" "cloud_run_reader" {
  count = local.is_prod_workspace ? 1 : 0

  project    = var.project_id
  location   = local.shared_artifact_registry_location
  repository = local.shared_artifact_registry_repository
  role       = "roles/artifactregistry.reader"
  member     = "serviceAccount:service-${local.project_number}@serverless-robot-prod.iam.gserviceaccount.com"
}

module "ollama_services" {
  for_each = local.shared_ollama_services_enabled ? { for model in local.ollama_models : model => model } : {}

  source = "./modules/ollama-cloud-run"

  project_id = var.project_id
  model      = each.value
  name       = local.ollama_service_names[each.value]
  regions    = local.ollama_regions
  image = lookup(
    var.ocr_service_images,
    "ollama/${each.value}",
    "MISSING_IMAGE_FOR_ollama/${each.value}@sha256:0000000000000000000000000000000000000000000000000000000000000000",
  )
  memory        = local.ollama_memory
  cpu           = local.ollama_cpu
  gpu_count     = local.ollama_gpu_count
  min_instances = local.ollama_min_instances
  max_instances = local.ollama_max_instances
  skip_neg      = local.ollama_skip_neg
  invokers      = []

  depends_on = [
    google_artifact_registry_repository_iam_member.cloud_run_reader,
  ]
}

resource "google_cloud_run_v2_service_iam_member" "ollama_invoker" {
  for_each = local.ollama_service_iam_bindings

  project  = var.project_id
  location = each.value.region
  name     = each.value.service
  role     = "roles/run.invoker"
  member   = "serviceAccount:${each.value.gsa}"

  depends_on = [
    module.ollama_services,
    module.scribe,
  ]
}

resource "google_cloud_run_v2_service_iam_member" "ollama_preview_invoker" {
  for_each = local.ollama_preview_iam_bindings

  project  = var.project_id
  location = each.value.region
  name     = each.value.service
  role     = "roles/run.invoker"
  member   = "serviceAccount:${each.value.gsa}"

  depends_on = [
    module.scribe,
  ]
}

resource "google_cloud_run_v2_service_iam_member" "ollama_readiness_invoker" {
  for_each = local.shared_ollama_services_enabled && contains(local.ollama_models, local.default_ollama_model) ? toset([local.ollama_regions[0]]) : toset([])

  project  = var.project_id
  location = each.value
  name     = local.ollama_service_names[local.default_ollama_model]
  role     = "roles/run.invoker"
  member   = "serviceAccount:${google_service_account.ocr_readiness.email}"

  depends_on = [
    google_service_account.ocr_readiness,
    module.ollama_services,
  ]
}
