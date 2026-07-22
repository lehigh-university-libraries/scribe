variable "project" {
  type        = string
  description = "The GCP project to create or deploy the GCP resources into"
}

variable "region" {
  type        = string
  description = "The region to deploy CloudRun"
  default     = "us-east5"
}

variable "name" {
  type        = string
  description = "Cloud Run service name for the Vault server."
  default     = "vault-server"
}

variable "gsa_account_id" {
  type        = string
  description = "Service account id for the Vault runtime. Defaults to a truncated form of name."
  default     = ""
}

variable "init_gsa_account_id" {
  type        = string
  description = "Service account id for the one-time Vault initializer. Defaults to the runtime account id plus -init."
  default     = ""
}

variable "bootstrap_service_account_emails" {
  type        = set(string)
  description = "Protected deployment identities allowed to read and decrypt the stored root token for first bootstrap or audited recovery."
  default     = []

  validation {
    condition = alltrue([
      for email in var.bootstrap_service_account_emails :
      can(regex("^[a-z0-9-]+@[a-z0-9-]+\\.iam\\.gserviceaccount\\.com$", email))
    ])
    error_message = "bootstrap_service_account_emails must contain Google service-account emails."
  }
}

variable "init_job_name" {
  type        = string
  description = "Cloud Run job name used to initialize Vault."
  default     = "vault-init"
}

variable "repository" {
  type        = string
  description = "The AR repo to create or push the vault image into"
  default     = "private"
}

variable "image_name" {
  type        = string
  description = "Docker image name to push into Artifact Registry."
  default     = "vault-server"
}

variable "init_image" {
  description = "Immutable Vault initialization job image."
  type        = string
  # renovate: datasource=docker depName=libops/vault-init
  default = "docker.io/libops/vault-init:1.0.1@sha256:973ebb2caf1379ee0986ad810911b6faddbeb087bebef7ed42c325d901f938d4"

  validation {
    condition     = can(regex("@sha256:[0-9a-f]{64}$", var.init_image))
    error_message = "init_image must be pinned by a lowercase sha256 digest."
  }
}

variable "create_repository" {
  type        = bool
  description = "Whether or not the AR repo needs to be created by this terraform"
  default     = true
}

variable "country" {
  type    = string
  default = "us"
}

variable "data_bucket_name" {
  type        = string
  description = "Bucket name for Vault data storage. Defaults to a name derived from project and service name."
  default     = ""
}

variable "key_bucket_name" {
  type        = string
  description = "Bucket name for stored Vault init material. Defaults to a name derived from project and service name."
  default     = ""
}

variable "soft_delete_retention_days" {
  description = "Soft-delete retention applied to Vault data and initialization-material buckets."
  type        = number
  default     = 30

  validation {
    condition     = var.soft_delete_retention_days >= 14 && var.soft_delete_retention_days <= 90
    error_message = "soft_delete_retention_days must be between 14 and 90."
  }
}

variable "noncurrent_version_retention_days" {
  description = "Days to retain noncurrent Vault object generations before soft deletion."
  type        = number
  default     = 90

  validation {
    condition     = var.noncurrent_version_retention_days >= 30
    error_message = "noncurrent_version_retention_days must be at least 30."
  }
}

variable "kms_key_ring_name" {
  type        = string
  description = "KMS key ring name used for auto-unseal."
  default     = "vault-server"
}

variable "kms_key_name" {
  type        = string
  description = "KMS crypto key name used for auto-unseal."
  default     = "vault"
}

variable "create_kms" {
  type        = bool
  description = "Whether to create the KMS key ring and crypto key."
  default     = true
}

variable "admin_emails" {
  description = "List of emails (users or service accounts) that are allowed to access non-public routes by passing X-Admin-Token header with a google access token."
  type        = list(string)
  default     = []
}

variable "public_routes" {
  description = "List of Vault API paths that should be accessible without X-Admin-Token header."
  type        = list(string)
  default = [
    "/.well-known/",
    "/v1/identity/oidc/",
    "/v1/auth/oidc/",
    "/v1/auth/userpass/",
  ]
}
