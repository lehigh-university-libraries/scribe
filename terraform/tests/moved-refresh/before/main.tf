terraform {
  required_version = ">= 1.15.0, < 1.16.0"
}

resource "terraform_data" "legacy" {
  input = "state-address"
}

resource "terraform_data" "maintenance" {
  input = "vault-target"
}
