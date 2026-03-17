provider "google" {
  project = var.project_id
  region  = var.region
}

locals {
  docker_compose_init = concat(
    var.docker_compose_init,
    length(var.app_env) > 0 ? [
      format(
        "bash /home/cloud-compose/merge-env-json.sh .env '%s'",
        base64encode(jsonencode(var.app_env))
      )
    ] : []
  )
}

module "scribe" {
  source = "git::https://github.com/libops/cloud-compose?ref=0.5.1"

  project_id            = var.project_id
  project_number        = var.project_number
  name                  = var.name
  region                = var.region
  zone                  = var.zone
  machine_type          = var.machine_type
  disk_type             = var.disk_type
  disk_size_gb          = var.disk_size_gb
  docker_compose_repo   = var.docker_compose_repo
  docker_compose_branch = var.docker_compose_branch
  docker_compose_init   = local.docker_compose_init
  docker_compose_up     = var.docker_compose_up
  docker_compose_down   = var.docker_compose_down
  allowed_ips           = var.allowed_ips
  allowed_ssh_ipv4      = var.allowed_ssh_ipv4
  allowed_ssh_ipv6      = var.allowed_ssh_ipv6
  users                 = var.users
  run_snapshots         = var.run_snapshots
  rootfs                = "${path.module}/../rootfs"
}
