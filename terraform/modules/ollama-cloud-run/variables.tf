variable "project_id" {
  description = "GCP project ID."
  type        = string
}

variable "model" {
  description = "Ollama model identifier baked into the deployed image, for example glm-ocr:bf16."
  type        = string
}

variable "name" {
  description = "Optional explicit Cloud Run service name. When empty, Terraform derives one from the model."
  type        = string
  default     = ""
}

variable "regions" {
  description = "Regions to deploy the Cloud Run service into."
  type        = list(string)
  default     = ["us-east4"]
}

variable "image" {
  description = "Pre-built, digest-pinned container image reference (name@sha256:...) to deploy."
  type        = string

  validation {
    condition     = can(regex("@sha256:[0-9a-f]{64}$", var.image))
    error_message = "image must be digest-pinned (ends with @sha256:<64 hex>)."
  }
}

variable "memory" {
  description = "Memory limit for the Cloud Run Ollama container."
  type        = string
  default     = "16Gi"
}

variable "cpu" {
  description = "CPU limit for the Cloud Run Ollama container."
  type        = string
  default     = "4000m"
}

variable "gpu_count" {
  description = "Number of GPUs attached to the Cloud Run Ollama container."
  type        = number
  default     = 1
}

variable "min_instances" {
  description = "Minimum number of Cloud Run instances."
  type        = number
  default     = 0
}

variable "max_instances" {
  description = "Maximum number of Cloud Run instances."
  type        = number
  default     = 1
}

variable "invokers" {
  description = "IAM members allowed to invoke the private Cloud Run Ollama service."
  type        = list(string)
  default     = []
}

variable "skip_neg" {
  description = "Whether to skip NEG creation in the Cloud Run module."
  type        = bool
  default     = true
}
