locals {
  shared_ocr_services_enabled = local.ocr_is_owner_workspace
  ocr_service_regions         = [var.region]

  default_kraken_segmentation_spec  = try(var.kraken_segmentation_models["kraken"], null)
  default_kraken_transcription_spec = try(var.kraken_transcription_models[var.kraken_default_transcription_model], null)

  ocr_invokers = [
    "serviceAccount:${local.scribe_vm_gsa_email}",
    "serviceAccount:${local.scribe_app_gsa_email}",
  ]

  ocr_base_services = {
    segmentor = {
      route_type         = "generic-segmentation"
      route_key          = "segmentor"
      service_name       = "scribe-segmentor-${local.shared_ocr_workspace}"
      service_account_id = "ocr-seg-${local.shared_ocr_workspace}"
      image_tag          = local.shared_ocr_workspace
      dockerfile         = "Dockerfile.segmentor"
      cpu                = "4000m"
      memory             = "8Gi"
      min_instances      = 0
      max_instances      = 3
      build_args = {
        KRAKEN_PIP_SPEC                = var.kraken_pip_spec
        KRAKEN_RECOGNITION_MODEL_DOI   = try(local.default_kraken_transcription_spec.doi, "")
        KRAKEN_RECOGNITION_MODEL_FILE  = try(local.default_kraken_transcription_spec.file, "")
        KRAKEN_SEGMENTATION_MODEL_DOI  = try(local.default_kraken_segmentation_spec.doi, "")
        KRAKEN_SEGMENTATION_MODEL_FILE = try(local.default_kraken_segmentation_spec.file, "")
      }
      env = [
        {
          name  = "KRAKEN_MODEL_DIR"
          value = "/models/kraken"
        },
        {
          name  = "KRAKEN_TRANSCRIPTION_MODEL"
          value = try(local.default_kraken_transcription_spec.file, "")
        },
        {
          name  = "KRAKEN_SEGMENTATION_MODEL"
          value = try(local.default_kraken_segmentation_spec.file, "")
        },
      ]
      hash_files = concat(
        [
          "Dockerfile.segmentor",
          "go.mod",
          "go.sum",
          "scripts/install-kraken-models.sh",
        ],
        [for f in fileset(local.repo_root, "cmd/segmentor/**") : f],
        [for f in fileset(local.repo_root, "internal/config/**") : f],
        [for f in fileset(local.repo_root, "internal/segmentor/**") : f],
        [for f in fileset(local.repo_root, "internal/serviceauth/**") : f],
        [for f in fileset(local.repo_root, "internal/worddetection/**") : f],
      )
    }
    image-service = {
      route_type         = "image-service"
      route_key          = "image-service"
      service_name       = "scribe-image-service-${local.shared_ocr_workspace}"
      service_account_id = "ocr-img-${local.shared_ocr_workspace}"
      image_tag          = local.shared_ocr_workspace
      dockerfile         = "Dockerfile.image-service"
      cpu                = "2000m"
      memory             = "4Gi"
      min_instances      = 0
      max_instances      = 5
      build_args         = {}
      env                = []
      hash_files = concat(
        [
          "Dockerfile.image-service",
          "go.mod",
          "go.sum",
        ],
        [for f in fileset(local.repo_root, "cmd/image-service/**") : f],
        [for f in fileset(local.repo_root, "internal/config/**") : f],
        [for f in fileset(local.repo_root, "internal/imageservice/**") : f],
        [for f in fileset(local.repo_root, "internal/serviceauth/**") : f],
      )
    }
  }

  kraken_segmentation_service_defs = {
    for route_key, spec in var.kraken_segmentation_models :
    "kraken-seg|${route_key}" => {
      route_type = "kraken-segmentation"
      route_key  = route_key
      service_name = replace(
        substr(
          "scribe-kraken-seg-${replace(replace(lower(trimspace(route_key)), "/[^a-z0-9]+/", "-"), "/^-+|-+$/", "")}-${substr(md5(route_key), 0, 6)}-${local.shared_ocr_workspace}",
          0,
          63,
        ),
        "/-+$/",
        "",
      )
      service_account_id = replace(
        substr("ocr-ks-${substr(md5(route_key), 0, 20)}", 0, 30),
        "/-+$/",
        "",
      )
      image_tag     = local.shared_ocr_workspace
      dockerfile    = "Dockerfile.segmentor"
      cpu           = "4000m"
      memory        = "8Gi"
      min_instances = 0
      max_instances = 3
      build_args = {
        KRAKEN_PIP_SPEC                = var.kraken_pip_spec
        KRAKEN_RECOGNITION_MODEL_DOI   = ""
        KRAKEN_RECOGNITION_MODEL_FILE  = ""
        KRAKEN_SEGMENTATION_MODEL_DOI  = spec.doi
        KRAKEN_SEGMENTATION_MODEL_FILE = spec.file
      }
      env = [
        {
          name  = "KRAKEN_MODEL_DIR"
          value = "/models/kraken"
        },
        {
          name  = "KRAKEN_TRANSCRIPTION_MODEL"
          value = ""
        },
        {
          name  = "KRAKEN_SEGMENTATION_MODEL"
          value = spec.file
        },
      ]
      hash_files = concat(
        [
          "Dockerfile.segmentor",
          "go.mod",
          "go.sum",
          "scripts/install-kraken-models.sh",
        ],
        [for f in fileset(local.repo_root, "cmd/segmentor/**") : f],
        [for f in fileset(local.repo_root, "internal/config/**") : f],
        [for f in fileset(local.repo_root, "internal/segmentor/**") : f],
        [for f in fileset(local.repo_root, "internal/serviceauth/**") : f],
        [for f in fileset(local.repo_root, "internal/worddetection/**") : f],
      )
    }
  }

  kraken_transcription_service_defs = {
    for route_key, spec in var.kraken_transcription_models :
    "kraken-ocr|${route_key}" => {
      route_type = "kraken-transcription"
      route_key  = route_key
      service_name = replace(
        substr(
          "scribe-kraken-ocr-${replace(replace(lower(trimspace(route_key)), "/[^a-z0-9]+/", "-"), "/^-+|-+$/", "")}-${substr(md5(route_key), 0, 6)}-${local.shared_ocr_workspace}",
          0,
          63,
        ),
        "/-+$/",
        "",
      )
      service_account_id = replace(
        substr("ocr-ko-${substr(md5(route_key), 0, 20)}", 0, 30),
        "/-+$/",
        "",
      )
      image_tag     = local.shared_ocr_workspace
      dockerfile    = "Dockerfile.segmentor"
      cpu           = "4000m"
      memory        = "8Gi"
      min_instances = 0
      max_instances = 3
      build_args = {
        KRAKEN_PIP_SPEC                = var.kraken_pip_spec
        KRAKEN_RECOGNITION_MODEL_DOI   = spec.doi
        KRAKEN_RECOGNITION_MODEL_FILE  = spec.file
        KRAKEN_SEGMENTATION_MODEL_DOI  = try(local.default_kraken_segmentation_spec.doi, "")
        KRAKEN_SEGMENTATION_MODEL_FILE = try(local.default_kraken_segmentation_spec.file, "")
      }
      env = [
        {
          name  = "KRAKEN_MODEL_DIR"
          value = "/models/kraken"
        },
        {
          name  = "KRAKEN_TRANSCRIPTION_MODEL"
          value = spec.file
        },
        {
          name  = "KRAKEN_SEGMENTATION_MODEL"
          value = try(local.default_kraken_segmentation_spec.file, "")
        },
      ]
      hash_files = concat(
        [
          "Dockerfile.segmentor",
          "go.mod",
          "go.sum",
          "scripts/install-kraken-models.sh",
        ],
        [for f in fileset(local.repo_root, "cmd/segmentor/**") : f],
        [for f in fileset(local.repo_root, "internal/config/**") : f],
        [for f in fileset(local.repo_root, "internal/segmentor/**") : f],
        [for f in fileset(local.repo_root, "internal/serviceauth/**") : f],
        [for f in fileset(local.repo_root, "internal/worddetection/**") : f],
      )
    }
  }

  ocr_services = merge(
    local.ocr_base_services,
    local.kraken_segmentation_service_defs,
    local.kraken_transcription_service_defs,
  )

  ocr_service_hashes = {
    for name, svc in local.ocr_services :
    name => sha1(join("", concat(
      [for f in sort(distinct(svc.hash_files)) : filesha1("${local.repo_root}/${f}")],
      [for key in sort(keys(svc.build_args)) : "${key}=${svc.build_args[key]}"],
      [for env in svc.env : "${env.name}=${env.value}"],
    )))
  }

  ocr_preview_invoker_gsas = local.shared_ocr_services_enabled ? [] : [
    local.scribe_vm_gsa_email,
    local.scribe_app_gsa_email,
  ]

  ocr_preview_iam_bindings = {
    for triple in setproduct(keys(local.ocr_services), local.ocr_service_regions, local.ocr_preview_invoker_gsas) :
    "${triple[0]}|${triple[1]}|${triple[2]}" => {
      service_key = triple[0]
      region      = triple[1]
      gsa         = triple[2]
      service     = local.ocr_services[triple[0]].service_name
    }
  }
}

resource "google_service_account" "ocr_service" {
  for_each = local.shared_ocr_services_enabled ? local.ocr_services : {}

  project      = var.project_id
  account_id   = each.value.service_account_id
  display_name = replace(each.value.service_name, "-", " ")
}

resource "google_project_iam_member" "ocr_service_user" {
  for_each = local.shared_ocr_services_enabled ? local.ocr_services : {}

  project = var.project_id
  role    = "roles/iam.serviceAccountUser"
  member  = "serviceAccount:${google_service_account.ocr_service[each.key].email}"
}

resource "docker_image" "ocr_service" {
  for_each = local.shared_ocr_services_enabled ? local.ocr_services : {}

  name = "${local.shared_artifact_registry_location}-docker.pkg.dev/${var.project_id}/${local.shared_artifact_registry_repository}/${each.value.service_name}:${each.value.image_tag}"

  build {
    context    = local.repo_root
    dockerfile = each.value.dockerfile
    build_args = each.value.build_args
  }

  triggers = {
    source = local.ocr_service_hashes[each.key]
  }

  keep_locally = false
}

resource "docker_registry_image" "ocr_service" {
  for_each = local.shared_ocr_services_enabled ? local.ocr_services : {}

  name          = docker_image.ocr_service[each.key].name
  keep_remotely = true

  triggers = {
    source = local.ocr_service_hashes[each.key]
  }
}

module "ocr_services" {
  for_each = local.shared_ocr_services_enabled ? local.ocr_services : {}

  source = "git::https://github.com/libops/terraform-cloudrun-v2?ref=0.5.2"

  name          = each.value.service_name
  project       = var.project_id
  gsa           = google_service_account.ocr_service[each.key].email
  regions       = local.ocr_service_regions
  min_instances = each.value.min_instances
  max_instances = each.value.max_instances
  skipNeg       = true
  invokers      = local.ocr_invokers
  containers = [
    {
      name   = replace(each.key, "|", "-")
      image  = "${docker_image.ocr_service[each.key].name}@${docker_registry_image.ocr_service[each.key].sha256_digest}"
      port   = 8080
      memory = each.value.memory
      cpu    = each.value.cpu
    }
  ]
  addl_env_vars = each.value.env

  depends_on = [
    google_artifact_registry_repository_iam_member.cloud_run_reader,
  ]
}

resource "google_cloud_run_v2_service_iam_member" "ocr_preview_invoker" {
  for_each = local.ocr_preview_iam_bindings

  project  = var.project_id
  location = each.value.region
  name     = each.value.service
  role     = "roles/run.invoker"
  member   = "serviceAccount:${each.value.gsa}"
}
