moved {
  from = module.scribe.module.gcp[0].terraform_data.application_network[0]
  to   = terraform_data.application_network
}

moved {
  from = module.scribe.module.gcp[0].terraform_data.application_subnetwork[0]
  to   = terraform_data.application_subnetwork
}
