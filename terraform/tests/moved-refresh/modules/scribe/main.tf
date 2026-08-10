variable "create_application_network" {
  type = bool
}

module "gcp" {
  count  = 1
  source = "./modules/gcp"

  create_application_network = var.create_application_network
}
