variable "project_id" {
  description = "GCP project ID."
  type        = string
}

variable "project_number" {
  description = "GCP project number."
  type        = string
}

variable "name" {
  description = "Deployment name used for the VM and related resources."
  type        = string
  default     = "scribe"
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

variable "disk_type" {
  description = "Disk type for attached disks."
  type        = string
  default     = "hyperdisk-balanced"
}

variable "disk_size_gb" {
  description = "Persistent docker volumes disk size in GB."
  type        = number
  default     = 50
}

variable "docker_compose_repo" {
  description = "HTTPS git URL for the Scribe repository the VM will clone."
  type        = string
  default     = "https://github.com/lehigh-university-libraries/scribe.git"
}

variable "docker_compose_branch" {
  description = "Branch to deploy from the docker compose repository."
  type        = string
  default     = "main"
}

variable "docker_compose_init" {
  description = "Shell command run after cloning and before docker compose up."
  type        = string
  default     = "test -f .env || cp sample.env .env; bash generate-secrets.sh"
}

variable "app_env" {
  description = "Sensitive environment variables to merge into the application's .env file."
  type        = map(string)
  default     = {}
  sensitive   = true
}

variable "docker_compose_up" {
  description = "Shell command used to start the compose stack."
  type        = string
  default     = "docker compose up -d --remove-orphans"
}

variable "docker_compose_down" {
  description = "Shell command used to stop the compose stack."
  type        = string
  default     = "docker compose down"
}

variable "allowed_ips" {
  description = "CIDR ranges allowed to reach the Cloud Run ingress that powers on the VM."
  type        = list(string)
  default     = ["128.180.0.0/16"]
}

variable "allowed_ssh_ipv4" {
  description = "CIDR IPv4 ranges allowed to SSH to the VM."
  type        = list(string)
  default     = []
}

variable "allowed_ssh_ipv6" {
  description = "CIDR IPv6 ranges allowed to SSH to the VM."
  type        = list(string)
  default     = []
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
