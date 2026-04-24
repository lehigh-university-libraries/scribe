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

variable "disk_size_gb" {
  description = "Persistent docker volumes disk size in GB."
  type        = number
  default     = 50
}

variable "docker_compose_branch" {
  description = "Branch to deploy from the Scribe repository."
  type        = string
  default     = "main"
}

variable "api_image" {
  description = "Backend image deployed to the VM for the api and worker services."
  type        = string
  default     = "ghcr.io/lehigh-university-libraries/scribe:main"
}

variable "frontend_image" {
  description = "Reserved GHCR frontend image reference kept for local compose/build parity. The VM bootstrap no longer runs a local frontend container."
  type        = string
  default     = "ghcr.io/lehigh-university-libraries/scribe-frontend:main"
}

variable "frontend_gar_image" {
  description = "Frontend image deployed as the Cloud Run sidecar next to ppb. Must live in GAR or Docker Hub, since Cloud Run cannot pull from GHCR. Leave empty to disable the sidecar."
  type        = string
  default     = ""
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

variable "app_domain" {
  description = "Hostname routed to the main Scribe app backend on the shared load balancer."
  type        = string
  default     = ""
}

variable "vault_admin_emails" {
  description = "Email addresses treated as Vault administrators by the Vault Cloud Run deployment module."
  type        = list(string)
  default     = []
}

variable "vault_ci_service_account_emails" {
  description = "Service account emails allowed to read deployment secrets from Vault through GCP IAM auth."
  type        = list(string)
  default     = []
}

variable "ocr_service_images" {
  description = "Map of OCR service key (e.g. \"segmentor\", \"kraken-ocr/<model>\", \"ollama/<model>\") to a fully digest-pinned GAR image reference. Populated by the build-ocr GitHub Actions workflow (or generate-ocr-images-map.sh locally) from config.yaml's ocr section."
  type        = map(string)
  default     = {}
}

variable "vm_compose_images" {
  description = "Map of service key to a fully digest-pinned GHCR image reference for images pulled directly by the VM docker-compose stack (e.g. \"image-service\"). Populated by the build-ocr workflow (or generate-vm-images-map.sh locally)."
  type        = map(string)
  default     = {}
}
