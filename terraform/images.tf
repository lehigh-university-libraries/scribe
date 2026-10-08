# Images are deployed by tag and resolved to digests at plan time, so pushing a
# new :main image and re-applying rolls it out. CI builds every image.
locals {
  ocr_image_names = merge(
    { "segmentor" = "scribe-segmentor" },
    { for key in keys(local.kraken_segmentation_models) : "kraken-seg/${key}" => "scribe-ks-${substr(md5(key), 0, 8)}" if key != local.kraken_default_segmentation_key },
    local.shared_ollama_services_enabled ? { for model in local.ollama_models : "ollama/${model}" => local.ollama_service_names[model] } : {},
  )

  api_image      = data.google_artifact_registry_docker_image.api.self_link
  frontend_image = data.google_artifact_registry_docker_image.frontend.self_link
  ocr_images     = { for key, image in data.google_artifact_registry_docker_image.ocr : key => image.self_link }
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

data "google_artifact_registry_docker_image" "api" {
  project       = var.project_id
  location      = "us"
  repository_id = "internal"
  image_name    = "scribe:${var.image_tag}"
}
data "google_artifact_registry_docker_image" "triplet" {
  project       = var.project_id
  location      = "us"
  repository_id = "internal"
  image_name    = "scribe-triplet:${var.ocr_image_tag}"
}
