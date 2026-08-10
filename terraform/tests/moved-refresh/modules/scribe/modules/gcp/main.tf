variable "create_application_network" {
  type = bool
}

resource "terraform_data" "application_network" {
  count = var.create_application_network ? 1 : 0
  input = "application-network"
}

resource "terraform_data" "application_subnetwork" {
  count = var.create_application_network ? 1 : 0
  input = "application-subnetwork"
}
