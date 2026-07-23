variable "project_id" {
  description = "GCP project ID."
  type        = string
}

variable "name" {
  description = "Cloud Run service name."
  type        = string
}

variable "service_account_id" {
  description = "GCP service account ID for the Cloud Run runtime identity."
  type        = string
}

variable "route_type" {
  description = "Logical route type exposed by this service (for example kraken-segmentation, kraken-transcription, or generic-segmentation)."
  type        = string
}

variable "route_key" {
  description = "Logical routing key within the route_type (e.g. model filename)."
  type        = string
}

variable "regions" {
  description = "Regions to deploy the Cloud Run service into."
  type        = list(string)
}

variable "image" {
  description = "Pre-built, digest-pinned container image reference (name@sha256:...) to deploy."
  type        = string

  validation {
    condition     = can(regex("@sha256:[0-9a-f]{64}$", var.image))
    error_message = "image must be digest-pinned (ends with @sha256:<64 hex>)."
  }
}

variable "container_name" {
  description = "Cloud Run container name."
  type        = string
}

variable "env" {
  description = "Additional environment variables for the Cloud Run container."
  type = list(object({
    name  = string
    value = string
  }))
  default = []
}

variable "cpu" {
  description = "CPU limit for the Cloud Run container."
  type        = string
}

variable "memory" {
  description = "Memory limit for the Cloud Run container."
  type        = string
}

variable "min_instances" {
  description = "Minimum number of Cloud Run instances."
  type        = number
  default     = 0
}

variable "max_instances" {
  description = "Maximum number of Cloud Run instances."
  type        = number
  default     = 3
}

variable "invokers" {
  description = "IAM members allowed to invoke the private Cloud Run service."
  type        = list(string)
  default     = []
}

variable "skip_neg" {
  description = "Whether to skip NEG creation in the upstream Cloud Run module."
  type        = bool
  default     = true
}

variable "depends_on_iam" {
  description = "Opaque list of inputs used to force a dependency on upstream IAM resources (not read by the module)."
  type        = any
  default     = []
}
