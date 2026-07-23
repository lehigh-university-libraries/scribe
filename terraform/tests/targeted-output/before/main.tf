variable "recorded_value" {
  type = string
}

variable "maintenance_revision" {
  type = string
}

resource "terraform_data" "maintenance" {
  input = var.maintenance_revision
}

output "deployment_inputs" {
  value = var.recorded_value
}
