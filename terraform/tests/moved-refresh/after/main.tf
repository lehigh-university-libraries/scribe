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

resource "terraform_data" "maintenance" {
  input = "vault-target"
}
