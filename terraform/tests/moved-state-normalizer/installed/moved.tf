moved {
  from = terraform_data.root
  to   = module.gcp.terraform_data.root
}

moved {
  from = terraform_data.omitted_from_repository_moves
  to   = module.gcp.terraform_data.omitted_from_repository_moves
}

moved {
  from = terraform_data.repository_counted
  to   = module.gcp.terraform_data.repository_counted
}

moved {
  from = terraform_data.application_network
  to   = module.gcp.terraform_data.application_network
}

moved {
  from = terraform_data.application_subnetwork
  to   = module.gcp.terraform_data.application_subnetwork
}

moved {
  from = module.ppb
  to   = module.gcp.module.ppb
}
