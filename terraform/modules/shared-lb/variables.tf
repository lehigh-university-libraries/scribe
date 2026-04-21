variable "project" {
  type        = string
  description = "GCP project ID."
}

variable "name" {
  type        = string
  description = "Base name for shared load balancer resources."
}

variable "backends" {
  type        = map(string)
  description = "Named backend services keyed by logical backend name."
}

variable "host_backends" {
  type        = map(string)
  description = "Map of hostname to backend key."
}

variable "default_backend_key" {
  type        = string
  description = "Backend key used as the URL map default service."
}
