resource "terraform_data" "root" {
  input = "root"
}

resource "terraform_data" "omitted_from_repository_moves" {
  input = "omitted"
}

resource "terraform_data" "repository_counted" {
  input = "repository-counted"
}

resource "terraform_data" "application_network" {
  input = "application-network"
}

resource "terraform_data" "application_subnetwork" {
  input = "application-subnetwork"
}

module "ppb" {
  source = "./ppb"
}
