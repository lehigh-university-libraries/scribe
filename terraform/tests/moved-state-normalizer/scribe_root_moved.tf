moved {
  from = terraform_data.root_app_viewer[0]
  to   = terraform_data.root_app_viewer
}

moved {
  from = terraform_data.root_instance_viewer[0]
  to   = terraform_data.root_instance_viewer
}

moved {
  from = terraform_data.root_app_policy
  to   = terraform_data.root_app_policy[0]
}

moved {
  from = terraform_data.root_app_role
  to   = terraform_data.root_app_role[0]
}
