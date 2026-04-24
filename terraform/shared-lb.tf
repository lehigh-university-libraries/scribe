locals {
  shared_lb_name = "scribe-edge"

  shared_lb_backends = merge(
    trimspace(var.app_domain) != "" ? { app = module.scribe.backend } : {},
  )

  shared_lb_hosts = merge(
    trimspace(var.app_domain) != "" ? { (var.app_domain) = "app" } : {},
  )

  shared_lb_default_backend = trimspace(var.app_domain) != "" ? "app" : ""

  shared_lb_enabled = terraform.workspace == "prod" && length(local.shared_lb_hosts) > 0
}

module "shared_lb" {
  count = local.shared_lb_enabled ? 1 : 0

  source = "./modules/shared-lb"

  project             = var.project_id
  name                = local.shared_lb_name
  backends            = local.shared_lb_backends
  host_backends       = local.shared_lb_hosts
  default_backend_key = local.shared_lb_default_backend
}
