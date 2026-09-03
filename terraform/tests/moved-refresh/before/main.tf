terraform {
  required_version = ">= 1.16.0, < 1.17.0"
}

resource "terraform_data" "legacy" {
  input = "state-address"
}

module "scribe" {
  source = "./modules/scribe"

  create_application_network = true
}

resource "terraform_data" "maintenance" {
  input = "vault-target"
}
