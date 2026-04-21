variable "project_id" {
  description = "GCP project ID."
  type        = string
}

variable "model" {
  description = "Ollama model identifier to bake into the image and deploy, for example glm-ocr:bf16."
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

variable "artifact_registry_location" {
  description = "Artifact Registry location for the Ollama image repository."
  type        = string
  default     = "us"
}

variable "artifact_registry_repository" {
  description = "Artifact Registry repository used for built Ollama model images."
  type        = string
  default     = "internal"
}

variable "image_tag" {
  description = "Tag to apply to the built Ollama image."
  type        = string
  default     = "main"
}

variable "base_image" {
  description = "Base Ollama image used to build the model-specific container."
  type        = string
  default     = "ollama/ollama:0.19.0@sha256:bf240c2847a8bc7b2c630b85dab5d1dedcba257b551d5fc9b290ce544d59272a"
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
