terraform {
  required_version = ">= 1.15.0, < 1.16.0"
}

resource "terraform_data" "normalized" {
  input = "state-address"
}

moved {
  from = terraform_data.legacy
  to   = terraform_data.normalized
}

resource "terraform_data" "application_network" {
  input = "application-network"
}

resource "terraform_data" "application_subnetwork" {
  input = "application-subnetwork"
}

module "scribe" {
  source = "./modules/scribe"

  create_application_network = false
}

moved {
  from = module.scribe.module.gcp[0].terraform_data.application_network[0]
  to   = terraform_data.application_network
}

moved {
  from = module.scribe.module.gcp[0].terraform_data.application_subnetwork[0]
  to   = terraform_data.application_subnetwork
}

resource "terraform_data" "maintenance" {
  input = "vault-target"
}
