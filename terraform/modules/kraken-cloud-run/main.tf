locals {
  primary_url = try(module.service.urls[var.regions[0]], "")
}

data "google_service_account" "service" {
  project    = var.project_id
  account_id = var.service_account_id
}

module "service" {
  source = "../cloud-run-service"

  name                             = var.name
  project                          = var.project_id
  gsa                              = data.google_service_account.service.account_id
  regions                          = var.regions
  min_instances                    = var.min_instances
  max_instances                    = var.max_instances
  max_instance_request_concurrency = 1
  skipNeg                          = var.skip_neg
  invokers                         = var.invokers

  containers = [
    {
      name   = var.container_name
      image  = var.image
      port   = 8080
      memory = var.memory
      cpu    = var.cpu
    }
  ]
  addl_env_vars = var.env
}
