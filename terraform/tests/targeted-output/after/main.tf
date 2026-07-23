variable "recorded_value" {
  type = string
}

variable "maintenance_revision" {
  type = string
}

resource "terraform_data" "maintenance" {
  input = var.maintenance_revision
}

resource "terraform_data" "recorded_root_outputs" {
  input = {
    deployment_inputs = var.recorded_value
  }
}

output "deployment_inputs" {
  value = terraform_data.recorded_root_outputs.output.deployment_inputs
}
