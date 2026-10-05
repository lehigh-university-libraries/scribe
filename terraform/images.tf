# Images are deployed by tag and resolved to digests at plan time, so pushing a
# new :main image and re-applying rolls it out. CI builds every image.
locals {
  ocr_image_names = merge(
    { "segmentor" = "scribe-segmentor" },
    { for key in keys(local.kraken_segmentation_models) : "kraken-seg/${key}" => "scribe-ks-${substr(md5(key), 0, 8)}" },
    { for key in keys(local.kraken_transcription_models) : "kraken-ocr/${key}" => "scribe-ko-${substr(md5(key), 0, 8)}" },
    local.shared_ollama_services_enabled ? { for model in local.ollama_models : "ollama/${model}" => local.ollama_service_names[model] } : {},
  )

  api_image      = "ghcr.io/lehigh-university-libraries/scribe@${data.docker_registry_image.api.sha256_digest}"
  frontend_image = data.google_artifact_registry_docker_image.frontend.self_link
  ocr_images     = { for key, image in data.google_artifact_registry_docker_image.ocr : key => image.self_link }
}

data "docker_registry_image" "api" {
  name = "ghcr.io/lehigh-university-libraries/scribe:${var.image_tag}"
}

data "google_artifact_registry_docker_image" "frontend" {
  project       = var.project_id
  location      = "us"
  repository_id = "internal"
  image_name    = "scribe-frontend:${var.image_tag}"
}

data "google_artifact_registry_docker_image" "ocr" {
  for_each = local.ocr_image_names

  project       = var.project_id
  location      = "us"
  repository_id = "internal"
  image_name    = "${each.value}:${var.ocr_image_tag}"
}
