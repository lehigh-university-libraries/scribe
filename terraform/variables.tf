variable "project_id" {
  description = "GCP project ID."
  type        = string
  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{4,28}[a-z0-9]$", var.project_id))
    error_message = "project_id must be a canonical Google Cloud project ID."
  }
}
variable "terraform_state_bucket" {
  description = "Optional GCS bucket name used for remote Terraform state lookups. Defaults to <project_id>-terraform."
  type        = string
  default     = ""
}
variable "region" {
  description = "GCP region."
  type        = string
  default     = "us-east5"
  validation {
    condition     = can(regex("^[a-z]+(-[a-z]+)+[0-9]+$", var.region))
    error_message = "region must be a canonical Google Cloud region."
  }
}
variable "allowed_ips" {
  description = "CIDR ranges allowed by the Cloud Run frontend ingress."
  type        = list(string)
  default     = []
  validation {
    condition     = alltrue([for cidr in var.allowed_ips : can(cidrhost(cidr, 0))])
    error_message = "allowed_ips entries must be valid IPv4 or IPv6 CIDR ranges."
  }
}
variable "network_ip_cidr_range" {
  description = "GCP subnet for Cloud Run Direct VPC egress to private Cloud SQL."
  type        = string
  default     = "10.42.0.0/24"
  validation {
    condition = (
      can(cidrhost(var.network_ip_cidr_range, 1)) &&
      length(regexall(":", var.network_ip_cidr_range)) == 0 &&
      length(regexall("/(2[4-6])$", var.network_ip_cidr_range)) == 1 &&
      !startswith(var.network_ip_cidr_range, "169.254.")
    )
    error_message = "network_ip_cidr_range must be a non-link-local IPv4 CIDR between /24 and /26 for Cloud Run Direct VPC egress."
  }
}
variable "uploads_soft_delete_retention_days" {
  description = "Soft-delete retention for source uploads. Production must retain recoverable deletions for at least 14 days."
  type        = number
  default     = 30
  validation {
    condition     = var.uploads_soft_delete_retention_days >= 7 && var.uploads_soft_delete_retention_days <= 90
    error_message = "uploads_soft_delete_retention_days must be between 7 and 90."
  }
}
variable "uploads_noncurrent_version_retention_days" {
  description = "Days to keep noncurrent source-upload object versions before soft deletion."
  type        = number
  default     = 30
  validation {
    condition     = var.uploads_noncurrent_version_retention_days >= 7
    error_message = "uploads_noncurrent_version_retention_days must be at least 7."
  }
}
variable "backup_soft_delete_retention_days" {
  description = "Soft-delete retention for the independent production uploads backup bucket."
  type        = number
  default     = 30
  validation {
    condition     = var.backup_soft_delete_retention_days >= 14 && var.backup_soft_delete_retention_days <= 90
    error_message = "backup_soft_delete_retention_days must be between 14 and 90."
  }
}
variable "backup_noncurrent_version_retention_days" {
  description = "Days to keep noncurrent versions in the production uploads backup bucket."
  type        = number
  default     = 90
  validation {
    condition     = var.backup_noncurrent_version_retention_days >= 30
    error_message = "backup_noncurrent_version_retention_days must be at least 30."
  }
}
variable "monitoring_notification_channels" {
  description = "Optional Cloud Monitoring notification channel IDs used by alert policies managed by this root module."
  type        = list(string)
  default     = []
}
variable "dev_external_ocr_impersonators" {
  description = "Explicit user: or group: IAM members allowed to mint short-lived credentials for the dev-only external OCR service account. Must be empty outside workspace dev."
  type        = set(string)
  default     = []
  validation {
    condition = alltrue([
      for member in var.dev_external_ocr_impersonators :
      can(regex("^(user|group):[A-Za-z0-9.!#$%&'*+/=?^_`{|}~-]+@[A-Za-z0-9.-]+\\.[A-Za-z]{2,63}$", member))
    ])
    error_message = "dev_external_ocr_impersonators entries must be explicit user: or group: email IAM members."
  }
}
variable "transcription_max_active_jobs_per_workspace" {
  description = "Maximum active transcription jobs admitted per workspace."
  type        = number
  default     = null
  validation {
    condition = var.transcription_max_active_jobs_per_workspace == null ? true : (
      floor(var.transcription_max_active_jobs_per_workspace) == var.transcription_max_active_jobs_per_workspace &&
      var.transcription_max_active_jobs_per_workspace >= 1 && var.transcription_max_active_jobs_per_workspace <= 100000
    )
    error_message = "transcription_max_active_jobs_per_workspace must be an integer from 1 through 100000."
  }
}
variable "storage_max_bytes_per_workspace" {
  type        = number
  description = "Maximum reserved and committed source bytes per workspace."
  default     = null
  validation {
    condition = var.storage_max_bytes_per_workspace == null ? true : (
      floor(var.storage_max_bytes_per_workspace) == var.storage_max_bytes_per_workspace &&
      var.storage_max_bytes_per_workspace >= 104857600 && var.storage_max_bytes_per_workspace <= 10995116277760
    )
    error_message = "storage_max_bytes_per_workspace must be an integer from 100 MiB through 10 TiB."
  }
}
variable "storage_max_bytes_total" {
  type        = number
  description = "Maximum reserved and committed source bytes for the deployment."
  default     = null
  validation {
    condition = var.storage_max_bytes_total == null ? true : (
      floor(var.storage_max_bytes_total) == var.storage_max_bytes_total &&
      var.storage_max_bytes_total >= 104857600 && var.storage_max_bytes_total <= 10995116277760
    )
    error_message = "storage_max_bytes_total must be an integer from 100 MiB through 10 TiB."
  }
}
variable "storage_max_items_per_workspace" {
  type        = number
  description = "Maximum items per workspace."
  default     = null
  validation {
    condition = var.storage_max_items_per_workspace == null ? true : (
      floor(var.storage_max_items_per_workspace) == var.storage_max_items_per_workspace &&
      var.storage_max_items_per_workspace >= 1 && var.storage_max_items_per_workspace <= 10000000
    )
    error_message = "storage_max_items_per_workspace must be an integer from 1 through 10000000."
  }
}
variable "storage_max_items_total" {
  type        = number
  description = "Maximum items for the deployment."
  default     = null
  validation {
    condition = var.storage_max_items_total == null ? true : (
      floor(var.storage_max_items_total) == var.storage_max_items_total &&
      var.storage_max_items_total >= 1 && var.storage_max_items_total <= 10000000
    )
    error_message = "storage_max_items_total must be an integer from 1 through 10000000."
  }
}
variable "storage_max_images_per_workspace" {
  type        = number
  description = "Maximum item images per workspace."
  default     = null
  validation {
    condition = var.storage_max_images_per_workspace == null ? true : (
      floor(var.storage_max_images_per_workspace) == var.storage_max_images_per_workspace &&
      var.storage_max_images_per_workspace >= 1 && var.storage_max_images_per_workspace <= 10000000
    )
    error_message = "storage_max_images_per_workspace must be an integer from 1 through 10000000."
  }
}
variable "storage_max_images_total" {
  type        = number
  description = "Maximum item images for the deployment."
  default     = null
  validation {
    condition = var.storage_max_images_total == null ? true : (
      floor(var.storage_max_images_total) == var.storage_max_images_total &&
      var.storage_max_images_total >= 1 && var.storage_max_images_total <= 10000000
    )
    error_message = "storage_max_images_total must be an integer from 1 through 10000000."
  }
}
variable "storage_reservation_ttl" {
  type        = string
  description = "TTL for abandoned storage reservations."
  default     = null
  validation {
    condition = var.storage_reservation_ttl == null ? true : (
      can(regex("^([1-9][0-9]*)(s|m|h)$", var.storage_reservation_ttl)) ? (
        tonumber(regex("^([1-9][0-9]*)(s|m|h)$", var.storage_reservation_ttl)[0]) *
        lookup({
          s = 1, m = 60, h = 3600
          }
        , regex("^([1-9][0-9]*)(s|m|h)$", var.storage_reservation_ttl)[1], 0) >= 300 &&
        tonumber(regex("^([1-9][0-9]*)(s|m|h)$", var.storage_reservation_ttl)[0]) *
        lookup({
          s = 1, m = 60, h = 3600
          }
        , regex("^([1-9][0-9]*)(s|m|h)$", var.storage_reservation_ttl)[1], 0) <= 86400
      ) : false
    )
    error_message = "storage_reservation_ttl must be a Go duration from 5m through 24h using s, m, or h."
  }
}
variable "storage_normalization_cache_max_bytes" {
  type        = number
  description = "Maximum normalized-image cache bytes."
  default     = null
  validation {
    condition = var.storage_normalization_cache_max_bytes == null ? true : (
      floor(var.storage_normalization_cache_max_bytes) == var.storage_normalization_cache_max_bytes &&
      var.storage_normalization_cache_max_bytes >= 104857600 && var.storage_normalization_cache_max_bytes <= 10995116277760
    )
    error_message = "storage_normalization_cache_max_bytes must be an integer from 100 MiB through 10 TiB."
  }
}
variable "storage_normalization_cache_max_age" {
  type        = string
  description = "Maximum normalized-image cache age."
  default     = null
  validation {
    condition = var.storage_normalization_cache_max_age == null ? true : (
      can(regex("^([1-9][0-9]*)(s|m|h)$", var.storage_normalization_cache_max_age)) ? (
        tonumber(regex("^([1-9][0-9]*)(s|m|h)$", var.storage_normalization_cache_max_age)[0]) *
        lookup({
          s = 1, m = 60, h = 3600
          }
        , regex("^([1-9][0-9]*)(s|m|h)$", var.storage_normalization_cache_max_age)[1], 0) >= 3600 &&
        tonumber(regex("^([1-9][0-9]*)(s|m|h)$", var.storage_normalization_cache_max_age)[0]) *
        lookup({
          s = 1, m = 60, h = 3600
          }
        , regex("^([1-9][0-9]*)(s|m|h)$", var.storage_normalization_cache_max_age)[1], 0) <= 31536000
      ) : false
    )
    error_message = "storage_normalization_cache_max_age must be a Go duration from 1h through 8760h using s, m, or h."
  }
}
variable "iiif_max_manifest_canvases" {
  type        = number
  description = "Maximum canvases accepted from one imported IIIF manifest."
  default     = null
  validation {
    condition = var.iiif_max_manifest_canvases == null ? true : (
      floor(var.iiif_max_manifest_canvases) == var.iiif_max_manifest_canvases &&
      var.iiif_max_manifest_canvases >= 1 && var.iiif_max_manifest_canvases <= 5000
    )
    error_message = "iiif_max_manifest_canvases must be an integer from 1 through 5000."
  }
}
variable "iiif_max_manifest_import_bytes" {
  type        = number
  description = "Maximum bytes downloaded for one imported IIIF manifest."
  default     = null
  validation {
    condition = var.iiif_max_manifest_import_bytes == null ? true : (
      floor(var.iiif_max_manifest_import_bytes) == var.iiif_max_manifest_import_bytes &&
      var.iiif_max_manifest_import_bytes >= 1 && var.iiif_max_manifest_import_bytes <= 67108864
    )
    error_message = "iiif_max_manifest_import_bytes must be an integer from 1 through 67108864."
  }
}
variable "image_tag" {
  description = "Tag of the backend (GHCR) and frontend (GAR) images to deploy. Terraform resolves it to a digest, so re-applying after the tag moves rolls out the new image."
  type        = string
  default     = "main"
}
variable "ocr_image_tag" {
  description = "Tag of the OCR images in GAR. Every workspace reuses the images CI builds for main unless overridden."
  type        = string
  default     = "main"
}
variable "cloud_sql_tier" {
  type        = string
  default     = "db-custom-2-7680"
  description = "Cloud SQL Enterprise machine tier."
}
variable "api_max_instances" {
  type    = number
  default = 5
  validation {
    condition     = var.api_max_instances >= 2 && var.api_max_instances <= 20 && floor(var.api_max_instances) == var.api_max_instances
    error_message = "API capacity must be an integer from 2 to 20."
  }
}
variable "worker_min_instances" {
  type    = number
  default = 0
  validation {
    condition     = var.worker_min_instances >= 0 && var.worker_min_instances <= var.worker_max_instances && floor(var.worker_min_instances) == var.worker_min_instances
    error_message = "Worker minimum must be an integer from zero to worker_max_instances."
  }
}
variable "worker_max_instances" {
  type    = number
  default = 3
  validation {
    condition     = var.worker_max_instances >= 1 && var.worker_max_instances <= 10 && floor(var.worker_max_instances) == var.worker_max_instances
    error_message = "Worker maximum must be an integer from 1 to 10."
  }
}
