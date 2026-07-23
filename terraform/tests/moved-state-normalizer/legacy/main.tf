resource "terraform_data" "root" {
  input = "root"
}

resource "terraform_data" "omitted_from_repository_moves" {
  input = "omitted"
}

resource "terraform_data" "repository_counted" {
  input = "repository-counted"
}

module "ppb" {
  source = "./ppb"
}
