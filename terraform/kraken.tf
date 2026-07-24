locals {
  ocr_service_regions = [var.region]

  ocr_config                       = try(yamldecode(file("${local.repo_root}/config/ocr.yaml")), {})
  kraken_config                    = try(local.ocr_config.kraken, {})
  kraken_segmentation_models       = try(local.kraken_config.segmentation_models, {})
  kraken_transcription_models      = try(local.kraken_config.transcription_models, {})
  kraken_default_transcription_key = trimspace(try(local.kraken_config.default_transcription_model, ""))
  kraken_default_transcription_spec = try(
    local.kraken_transcription_models[local.kraken_default_transcription_key],
    null,
  )
  kraken_default_segmentation_key = trimspace(try(local.kraken_config.default_segmentation_model, try(sort(keys(local.kraken_segmentation_models))[0], "")))
  kraken_default_segmentation_spec = try(
    local.kraken_segmentation_models[local.kraken_default_segmentation_key],
    null,
  )

  ocr_invoker_gsas = [
    local.scribe_vm_gsa_email,
    local.scribe_app_gsa_email,
  ]

  ocr_readiness_services = toset(compact([
    try(local.ocr_services["segmentor"].service_name, ""),
    try(local.ocr_services["kraken-ocr/${local.kraken_default_transcription_key}"].service_name, ""),
  ]))

  ws_short = trimsuffix(substr(local.workspace_slug, 0, 15), "-")

  ocr_base_services = {
    "segmentor" = {
      route_type         = "generic-segmentation"
      route_key          = "segmentor"
      service_name       = "scribe-segmentor-${local.ws_short}"
      service_account_id = "ocr-seg-${local.ws_short}"
      container_name     = "segmentor"
      cpu                = "4000m"
      memory             = "8Gi"
      min_instances      = 0
      max_instances      = 3
      env = [
        { name = "KRAKEN_MODEL_DIR", value = "/models/kraken" },
        { name = "KRAKEN_TRANSCRIPTION_MODEL_ID", value = local.kraken_default_transcription_key },
        { name = "KRAKEN_TRANSCRIPTION_MODEL", value = try(local.kraken_default_transcription_spec.file, "") },
        { name = "KRAKEN_SEGMENTATION_MODEL_ID", value = local.kraken_default_segmentation_key },
        { name = "KRAKEN_SEGMENTATION_MODEL", value = try(local.kraken_default_segmentation_spec.file, "") },
        { name = "SEGMENTOR_MAX_CONCURRENCY", value = "1" },
      ]
    }
  }

  kraken_segmentation_service_defs = {
    for route_key, spec in local.kraken_segmentation_models :
    "kraken-seg/${route_key}" => {
      route_type         = "kraken-segmentation"
      route_key          = route_key
      service_name       = "scribe-ks-${substr(md5(route_key), 0, 8)}-${local.ws_short}"
      service_account_id = trimsuffix(substr("ocr-ks-${substr(md5(route_key), 0, 6)}-${local.ws_short}", 0, 30), "-")
      container_name     = "kraken-seg-${substr(md5(route_key), 0, 6)}"
      cpu                = "4000m"
      memory             = "8Gi"
      min_instances      = 0
      max_instances      = 3
      env = [
        { name = "KRAKEN_MODEL_DIR", value = "/models/kraken" },
        { name = "KRAKEN_TRANSCRIPTION_MODEL_ID", value = "" },
        { name = "KRAKEN_TRANSCRIPTION_MODEL", value = "" },
        { name = "KRAKEN_SEGMENTATION_MODEL_ID", value = route_key },
        { name = "KRAKEN_SEGMENTATION_MODEL", value = spec.file },
        { name = "SEGMENTOR_MAX_CONCURRENCY", value = "1" },
      ]
    }
  }

  kraken_transcription_service_defs = {
    for route_key, spec in local.kraken_transcription_models :
    "kraken-ocr/${route_key}" => {
      route_type         = "kraken-transcription"
      route_key          = route_key
      service_name       = "scribe-ko-${substr(md5(route_key), 0, 8)}-${local.ws_short}"
      service_account_id = trimsuffix(substr("ocr-ko-${substr(md5(route_key), 0, 6)}-${local.ws_short}", 0, 30), "-")
      container_name     = "kraken-ocr-${substr(md5(route_key), 0, 6)}"
      cpu                = "4000m"
      memory             = "8Gi"
      min_instances      = 0
      max_instances      = 3
      env = [
        { name = "KRAKEN_MODEL_DIR", value = "/models/kraken" },
        { name = "KRAKEN_TRANSCRIPTION_MODEL_ID", value = route_key },
        { name = "KRAKEN_TRANSCRIPTION_MODEL", value = spec.file },
        { name = "KRAKEN_SEGMENTATION_MODEL_ID", value = "" },
        { name = "KRAKEN_SEGMENTATION_MODEL", value = "" },
        { name = "SEGMENTOR_MAX_CONCURRENCY", value = "1" },
      ]
    }
  }

  ocr_services = merge(
    local.ocr_base_services,
    local.kraken_segmentation_service_defs,
    local.kraken_transcription_service_defs,
  )

  kraken_invoker_bindings = {
    for triple in setproduct(
      keys(local.ocr_services),
      local.ocr_service_regions,
      local.ocr_invoker_gsas,
    ) :
    "${triple[0]}|${triple[1]}|${triple[2]}" => {
      region  = triple[1]
      gsa     = triple[2]
      service = local.ocr_services[triple[0]].service_name
    }
  }
}


resource "google_service_account" "ocr_compute" {
  project     = var.project_id
  account_id  = trimsuffix(substr("ocr-compute-${local.workspace_slug}", 0, 30), "-")
  description = "Compute-only OCR identity with no source-document storage access."
}

module "kraken" {
  for_each = local.ocr_services

  source = "./modules/kraken-cloud-run"

  project_id         = var.project_id
  name               = each.value.service_name
  service_account_id = google_service_account.ocr_compute.account_id
  route_type         = each.value.route_type
  route_key          = each.value.route_key
  regions            = local.ocr_service_regions
  image = lookup(
    var.ocr_service_images,
    each.key,
    "MISSING_IMAGE_FOR_${each.key}@sha256:0000000000000000000000000000000000000000000000000000000000000000",
  )
  container_name = each.value.container_name
  env            = each.value.env
  cpu            = each.value.cpu
  memory         = each.value.memory
  min_instances  = each.value.min_instances
  max_instances  = each.value.max_instances
  invokers       = []

  depends_on_iam = [google_artifact_registry_repository_iam_member.cloud_run_reader]
  depends_on = [
    google_service_account.ocr_compute,
  ]
}

resource "google_cloud_run_v2_service_iam_member" "kraken_invoker" {
  for_each = local.kraken_invoker_bindings

  project  = var.project_id
  location = each.value.region
  name     = each.value.service
  role     = "roles/run.invoker"
  member   = "serviceAccount:${each.value.gsa}"

  depends_on = [
    module.kraken,
    module.scribe,
  ]
}

resource "google_cloud_run_v2_service_iam_member" "ocr_readiness_invoker" {
  for_each = local.ocr_readiness_services

  project  = var.project_id
  location = var.region
  name     = each.value
  role     = "roles/run.invoker"
  member   = "serviceAccount:${google_service_account.ocr_readiness.email}"

  depends_on = [module.kraken]
}

check "kraken_default_models_present" {
  assert {
    condition     = local.kraken_default_transcription_key != "" && contains(keys(local.kraken_transcription_models), local.kraken_default_transcription_key)
    error_message = "config/ocr.yaml kraken.default_transcription_model must reference a key present in kraken.transcription_models."
  }

  assert {
    condition     = local.kraken_default_segmentation_key != "" && contains(keys(local.kraken_segmentation_models), local.kraken_default_segmentation_key)
    error_message = "config/ocr.yaml kraken.default_segmentation_model must reference a key present in kraken.segmentation_models."
  }
}

check "kraken_service_image_route_alignment" {
  assert {
    condition = alltrue([
      for image_key, service in local.ocr_services :
      service.route_type == "kraken-segmentation" ? image_key == "kraken-seg/${service.route_key}" :
      service.route_type == "kraken-transcription" ? image_key == "kraken-ocr/${service.route_key}" :
      image_key == "segmentor" && service.route_key == "segmentor"
    ])
    error_message = "Each Kraken service route must select the image built for that exact public route key."
  }
}
