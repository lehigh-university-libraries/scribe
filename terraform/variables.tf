variable "project_id" {
  description = "GCP project ID."
  type        = string
}

variable "terraform_state_bucket" {
  description = "Optional GCS bucket name used for remote Terraform state lookups. Defaults to <project_id>-terraform."
  type        = string
  default     = ""
}

variable "name" {
  description = "Deployment name used for the VM and related resources."
  type        = string
  default     = "scribe"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{0,62}$", var.name))
    error_message = "name must start with a lowercase letter and contain at most 63 lowercase letters, digits, or hyphens."
  }
}

variable "region" {
  description = "GCP region."
  type        = string
  default     = "us-east5"
}

variable "zone" {
  description = "GCP zone."
  type        = string
  default     = "us-east5-b"
}

variable "machine_type" {
  description = "Compute Engine machine type."
  type        = string
  default     = "n4-standard-2"
}

variable "preview_machine_type" {
  description = "Reviewed Compute Engine machine profile used only by pull-request preview workspaces."
  type        = string
  default     = "n2d-standard-2"

  validation {
    condition     = contains(["e2-medium", "n2d-standard-2"], var.preview_machine_type)
    error_message = "preview_machine_type must be an explicitly reviewed preview profile: e2-medium or n2d-standard-2."
  }
}

variable "disk_size_gb" {
  description = "Persistent docker volumes disk size in GB."
  type        = number
  default     = 50
}

variable "docker_compose_branch" {
  description = "Git ref to deploy from the Scribe repository. CI supplies an immutable commit SHA."
  type        = string
  default     = "0000000000000000000000000000000000000000"
}

variable "data_generation" {
  description = "Reviewed persistence generation shared by MariaDB, blobs, Triplet, caches, and transcription queues. Change only for an intentional persistence-generation cutover."
  type        = string
  default     = "canonical-v2"

  validation {
    condition     = contains(["canonical-v1", "canonical-v2"], var.data_generation)
    error_message = "data_generation must be an explicitly reviewed canonical generation: canonical-v1 or canonical-v2."
  }
}

variable "api_image" {
  description = "Backend image deployed to the VM for the api and worker services."
  type        = string
  default     = "ghcr.io/lehigh-university-libraries/scribe@sha256:0000000000000000000000000000000000000000000000000000000000000000"

  validation {
    condition     = can(regex("^ghcr\\.io/[a-z0-9._/-]+@sha256:[0-9a-f]{64}$", var.api_image))
    error_message = "api_image must be a digest-pinned GHCR reference."
  }
}

variable "frontend_gar_image" {
  description = "Frontend image deployed as the Cloud Run sidecar next to ppb. Must live in GAR or Docker Hub, since Cloud Run cannot pull from GHCR. Leave empty to disable the sidecar."
  type        = string
  default     = ""

  validation {
    condition     = trimspace(var.frontend_gar_image) == "" || can(regex("^[^[:space:]@]+@sha256:[0-9a-f]{64}$", var.frontend_gar_image))
    error_message = "frontend_gar_image must be empty or a digest-pinned image reference."
  }
}

variable "browser_readiness_image" {
  description = "Protected, digest-pinned Playwright image used only by preview readiness jobs. Leave empty outside protected preview apply workflows."
  type        = string
  default     = ""

  validation {
    condition     = trimspace(var.browser_readiness_image) == "" || can(regex("^us-docker\\.pkg\\.dev/${var.project_id}/internal/scribe-browser-readiness@sha256:[0-9a-f]{64}$", var.browser_readiness_image))
    error_message = "browser_readiness_image must be empty or the project-owned digest-pinned readiness image."
  }
}

variable "allowed_ips" {
  description = "CIDR ranges allowed to reach the Cloud Run ingress that powers on the VM."
  type        = list(string)
  default     = []

  validation {
    condition     = alltrue([for cidr in var.allowed_ips : can(cidrhost(cidr, 0))])
    error_message = "allowed_ips entries must be valid IPv4 or IPv6 CIDR ranges."
  }
}

variable "allowed_ssh_ipv4" {
  description = "CIDR IPv4 ranges allowed to SSH to the VM."
  type        = list(string)
  default     = []

  validation {
    condition     = alltrue([for cidr in var.allowed_ssh_ipv4 : can(cidrnetmask(cidr))])
    error_message = "allowed_ssh_ipv4 entries must be valid IPv4 CIDR ranges."
  }
}

variable "allowed_ssh_ipv6" {
  description = "CIDR IPv6 ranges allowed to SSH to the VM."
  type        = list(string)
  default     = []

  validation {
    condition = alltrue([
      for cidr in var.allowed_ssh_ipv6 : can(cidrhost(cidr, 0)) && strcontains(cidr, ":")
    ])
    error_message = "allowed_ssh_ipv6 entries must be valid IPv6 CIDR ranges."
  }
}

variable "network_ip_cidr_range" {
  description = "Exact GCP subnet used by the VM and Cloud Run direct VPC egress; Traefik trusts forwarding headers only from this range."
  type        = string
  default     = "10.42.0.0/24"

  validation {
    condition = (
      can(cidrhost(var.network_ip_cidr_range, 1)) &&
      length(regexall(":", var.network_ip_cidr_range)) == 0 &&
      length(regexall("/(2[4-9]|3[0-2])$", var.network_ip_cidr_range)) == 1 &&
      !startswith(var.network_ip_cidr_range, "169.254.")
    )
    error_message = "network_ip_cidr_range must be a non-link-local IPv4 CIDR no broader than /24."
  }
}

variable "browser_readiness_subnet_cidr" {
  description = "Dedicated, non-overlapping /26 inside the environment application VPC, used only by the protected preview browser-readiness job and its subnet-scoped Cloud NAT."
  type        = string
  default     = "10.43.0.0/26"

  validation {
    condition = (
      can(cidrhost(var.browser_readiness_subnet_cidr, 63)) &&
      try(cidrhost(var.browser_readiness_subnet_cidr, 0), "") == try(split("/", var.browser_readiness_subnet_cidr)[0], "") &&
      length(regexall(":", var.browser_readiness_subnet_cidr)) == 0 &&
      endswith(var.browser_readiness_subnet_cidr, "/26") &&
      !startswith(var.browser_readiness_subnet_cidr, "169.254.")
    )
    error_message = "browser_readiness_subnet_cidr must be a canonical, non-link-local IPv4 /26."
  }
}

variable "compose_network_cidr" {
  description = "Dedicated Docker bridge CIDR; the API trusts only the derived Traefik container /32."
  type        = string
  default     = "172.30.0.0/24"

  validation {
    condition = (
      can(cidrhost(var.compose_network_cidr, 2)) &&
      can(cidrsubnet(var.compose_network_cidr, 1, 1)) &&
      try(cidrhost(var.compose_network_cidr, 0), "") == try(split("/", var.compose_network_cidr)[0], "") &&
      length(regexall(":", var.compose_network_cidr)) == 0 &&
      length(regexall("/(2[4-8])$", var.compose_network_cidr)) == 1 &&
      !startswith(var.compose_network_cidr, "169.254.")
    )
    error_message = "compose_network_cidr must be a canonical non-link-local IPv4 CIDR between /24 and /28 with room for Traefik at host 2 and at least six dynamically allocated container addresses."
  }
}

variable "users" {
  description = "Map of SSH users to authorized public keys."
  type        = map(list(string))
  default     = {}
}

variable "run_snapshots" {
  description = "Whether to enable scheduled snapshots for the persistent disks."
  type        = bool
  default     = true
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

variable "backup_restore_service_account_email" {
  description = "Protected GitHub Actions service account used only for recovery-policy verification and isolated snapshot drills."
  type        = string
  default     = ""

  validation {
    condition = trimspace(var.backup_restore_service_account_email) == "" || can(regex(
      "^[a-z0-9-]+@[a-z0-9-]+\\.iam\\.gserviceaccount\\.com$",
      trimspace(var.backup_restore_service_account_email),
    ))
    error_message = "backup_restore_service_account_email must be empty or a Google service-account email."
  }
}

variable "terraform_state_backup_audited" {
  description = "Ephemeral assertion set only after ci/verify-cloud-backups.sh verifies versioning and retention on the externally managed Terraform state bucket."
  type        = bool
  default     = false
}

variable "monitoring_notification_channels" {
  description = "Optional Cloud Monitoring notification channel IDs used by alert policies managed by this root module."
  type        = list(string)
  default     = []
}

variable "vault_admin_emails" {
  description = "Email addresses treated as Vault administrators by the Vault Cloud Run deployment module."
  type        = list(string)
  default     = []
}

variable "vault_ci_service_account_emails" {
  description = "Service account emails that must keep Vault CI login roles and secret-read access. Terraform creates both google-jwt ci-* roles and GCP auth roles from this list."
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

variable "ocr_service_images" {
  description = "Map of OCR service key (e.g. \"segmentor\", \"kraken-ocr/<model>\", \"ollama/<model>\") to a fully digest-pinned GAR image reference. Populated by the build-ocr workflow from config/ocr.yaml."
  type        = map(string)
  default     = {}

  validation {
    condition     = alltrue([for image in values(var.ocr_service_images) : can(regex("^[^[:space:]@]+@sha256:[0-9a-f]{64}$", image))])
    error_message = "Every ocr_service_images value must be a digest-pinned image reference."
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
        lookup({ s = 1, m = 60, h = 3600 }, regex("^([1-9][0-9]*)(s|m|h)$", var.storage_reservation_ttl)[1], 0) >= 300 &&
        tonumber(regex("^([1-9][0-9]*)(s|m|h)$", var.storage_reservation_ttl)[0]) *
        lookup({ s = 1, m = 60, h = 3600 }, regex("^([1-9][0-9]*)(s|m|h)$", var.storage_reservation_ttl)[1], 0) <= 86400
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
        lookup({ s = 1, m = 60, h = 3600 }, regex("^([1-9][0-9]*)(s|m|h)$", var.storage_normalization_cache_max_age)[1], 0) >= 3600 &&
        tonumber(regex("^([1-9][0-9]*)(s|m|h)$", var.storage_normalization_cache_max_age)[0]) *
        lookup({ s = 1, m = 60, h = 3600 }, regex("^([1-9][0-9]*)(s|m|h)$", var.storage_normalization_cache_max_age)[1], 0) <= 31536000
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
