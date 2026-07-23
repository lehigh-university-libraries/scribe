terraform {
  required_version = ">= 1.4.0"
}

resource "terraform_data" "root_app_viewer" {
  count = 1
  input = "root-app-viewer"
}

resource "terraform_data" "root_instance_viewer" {
  count = 1
  input = "root-instance-viewer"
}

resource "terraform_data" "root_app_policy" {
  input = "root-app-policy"
}

resource "terraform_data" "root_app_role" {
  input = "root-app-role"
}

resource "terraform_data" "root_unindexed_conflict_seed" {
  input = "root-unindexed-conflict-seed"
}

resource "terraform_data" "root_indexed_conflict_seed" {
  count = 1
  input = "root-indexed-conflict-seed"
}

module "scribe" {
  source = "./legacy"
}
