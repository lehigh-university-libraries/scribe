variable "project_id" {
  description = "GCP project that owns Scribe's singleton foundation resources."
  type        = string

  validation {
    condition     = can(regex("^([a-z0-9][a-z0-9.-]*:)?[a-z][a-z0-9-]{4,28}[a-z0-9]$", trimspace(var.project_id)))
    error_message = "project_id must be a valid GCP project ID."
  }
}
