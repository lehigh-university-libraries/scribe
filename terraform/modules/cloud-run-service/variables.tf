variable "name" {
  type        = string
  description = "Name of the Cloud Run service"
}

variable "gsa" {
  type        = string
  description = "Service account name the Cloud Run service will run as. If empty, creates a new one."
}

variable "min_instances" {
  type        = string
  default     = "0"
  description = "Minimum number of instances to keep running"
}

variable "max_instances" {
  type        = string
  default     = "100"
  description = "Maximum number of instances to scale to"
}

variable "regions" {
  type        = list(string)
  description = "The GCP region(s) to deploy to"
  default = [
    "us-east4",
    "us-east5",
    "us-central1",
    "us-west3",
    "us-west1",
    "us-west4",
    "us-south1",
    "northamerica-northeast1",
    "northamerica-northeast2",
    "northamerica-south1",
    "australia-southeast1",
    "australia-southeast2"
  ]
}

variable "project" {
  type        = string
  description = "The GCP project to use"
}

variable "launch_stage" {
  type        = string
  default     = "GA"
  description = "Cloud Run launch stage for the service."

  validation {
    condition     = contains(["ALPHA", "BETA", "GA"], var.launch_stage)
    error_message = "launch_stage must be one of ALPHA, BETA, or GA."
  }
}

variable "ingress" {
  type        = string
  default     = "INGRESS_TRAFFIC_ALL"
  description = "Cloud Run ingress setting."

  validation {
    condition = contains([
      "INGRESS_TRAFFIC_ALL",
      "INGRESS_TRAFFIC_INTERNAL_ONLY",
      "INGRESS_TRAFFIC_INTERNAL_LOAD_BALANCER",
    ], var.ingress)
    error_message = "ingress must be one of INGRESS_TRAFFIC_ALL, INGRESS_TRAFFIC_INTERNAL_ONLY, or INGRESS_TRAFFIC_INTERNAL_LOAD_BALANCER."
  }
}

variable "deletion_protection" {
  type        = bool
  default     = false
  description = "Whether to enable deletion protection on the Cloud Run service."
}

variable "labels" {
  type        = map(string)
  default     = {}
  description = "Labels to apply to each Cloud Run service."
}

variable "custom_audiences" {
  type        = list(string)
  default     = []
  description = "Custom audiences accepted by each Cloud Run service."
}

variable "timeout_seconds" {
  type        = number
  default     = null
  description = "Optional request timeout for each Cloud Run service, in seconds."

  validation {
    condition     = var.timeout_seconds == null || (var.timeout_seconds >= 1 && var.timeout_seconds <= 3600)
    error_message = "timeout_seconds must be between 1 and 3600 when set."
  }
}

variable "max_instance_request_concurrency" {
  type        = number
  default     = null
  description = "Optional maximum concurrent requests per Cloud Run instance."

  validation {
    condition     = var.max_instance_request_concurrency == null || (var.max_instance_request_concurrency >= 1 && var.max_instance_request_concurrency <= 1000)
    error_message = "max_instance_request_concurrency must be between 1 and 1000 when set."
  }
}

variable "skipNeg" {
  type        = bool
  default     = false
  description = "Skip creating Network Endpoint Group and Backend Service"
}

variable "invokers" {
  type        = list(string)
  default     = ["allUsers"]
  description = "List of members to grant Cloud Run invoker role"
}

variable "secrets" {
  type = list(object({
    name        = string
    secret_id   = string
    secret_name = string
  }))
  default     = []
  description = "List of Secret Manager secrets to mount as environment variables"
}

variable "containers" {
  type = list(object({
    image          = string
    name           = string
    command        = optional(list(string), null)
    args           = optional(list(string), null)
    port           = optional(number, 0)
    memory         = optional(string, "512Mi")
    cpu            = optional(string, "1000m")
    liveness_probe = optional(string, "")
    startup_probe  = optional(string, "")
    gpus           = optional(string, "")
    volume_mounts = optional(list(object({
      name       = string
      mount_path = string
    })), [])
  }))
  description = "List of container configurations to run in the service. At least one container needs a port. This allows easily configuring multi-container deployments."
}

variable "addl_env_vars" {
  type = list(object({
    name  = string
    value = string
  }))
  default     = []
  description = "Additional environment variables to set in containers"
}

variable "empty_dir_volumes" {
  type = list(object({
    name       = string
    size_limit = optional(string, "2Mi")
  }))
  default     = []
  description = "List of empty directory volumes to create and mount"
}


variable "gcs_volumes" {
  type = list(object({
    name      = string
    bucket    = string
    read_only = optional(bool, true)
  }))
  default     = []
  description = "List of Google Cloud Storage buckets to mount as volumes. Must ensure the Cloud Run GSA has proper IAM set on the bucket"
}


variable "vpc_direct_egress" {
  type        = string
  description = "Traffic VPC egress settings. Possible values are: `ALL_TRAFFIC`, `PRIVATE_RANGES_ONLY`."
  default     = "OFF"
  validation {
    condition     = contains(["OFF", "ALL_TRAFFIC", "PRIVATE_RANGES_ONLY"], var.vpc_direct_egress)
    error_message = "The 'vpc_direct_egress' variable must be one of 'ALL_TRAFFIC' or 'PRIVATE_RANGES_ONLY'"
  }
}

variable "vpc_direct_egress_network" {
  type        = string
  description = "The VPC network that the Cloud Run resource will be able to send traffic to"
  default     = "default"
}

variable "vpc_direct_egress_subnetwork" {
  type        = string
  default     = "default"
  description = "The VPC subnetwork that the Cloud Run resource will get IPs from"
}

variable "vpc_direct_egress_tags" {
  type        = list(string)
  default     = null
  description = "Network tags applied to this Cloud Run service"
}
