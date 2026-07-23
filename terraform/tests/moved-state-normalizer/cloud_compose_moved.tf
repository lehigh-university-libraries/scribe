moved {
  from = module.scribe.module.gcp.terraform_data.root
  to   = module.scribe.module.gcp[0].terraform_data.root
}

moved {
  from = module.scribe.module.gcp.terraform_data.repository_counted
  to   = module.scribe.module.gcp[0].terraform_data.repository_counted[0]
}

moved {
  from = module.scribe.module.gcp.module.ppb
  to   = module.scribe.module.gcp[0].module.ppb[0]
}
