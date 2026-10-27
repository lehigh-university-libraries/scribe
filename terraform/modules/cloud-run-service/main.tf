# Vendored from libops/terraform-cloudrun-v2 at
# 8718cd663a74fd33f45306321b4f2b11c83814d5; provider constraint updated for v8.
# Keep resource names stable so existing module state addresses are retained.
terraform {
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 8.0"
    }
  }
}

data "google_service_account" "service_account" {
  account_id = var.gsa
  project    = var.project
}

resource "google_cloud_run_v2_service" "cloudrun" {
  for_each     = toset(var.regions)
  name         = var.name
  location     = each.value
  launch_stage = var.launch_stage
  project      = var.project
  ingress      = var.ingress
  labels       = var.labels

  deletion_protection = var.deletion_protection
  custom_audiences    = var.custom_audiences

  scaling {
    min_instance_count = var.min_instances
    max_instance_count = var.max_instances
  }

  template {
    execution_environment = "EXECUTION_ENVIRONMENT_GEN2"

    service_account                  = data.google_service_account.service_account.email
    gpu_zonal_redundancy_disabled    = anytrue([for c in var.containers : c.gpus != ""])
    max_instance_request_concurrency = var.max_instance_request_concurrency
    timeout                          = var.timeout_seconds == null ? null : "${var.timeout_seconds}s"

    dynamic "containers" {
      for_each = var.containers
      content {
        image   = containers.value.image
        name    = containers.value.name
        command = containers.value.command
        args    = containers.value.args
        dynamic "ports" {
          for_each = containers.value.port != 0 ? [containers.value.port] : []
          content {
            container_port = ports.value
          }
        }

        dynamic "liveness_probe" {
          for_each = containers.value.liveness_probe != "" ? [containers.value.liveness_probe] : []
          content {
            http_get {
              path = liveness_probe.value
            }
          }
        }

        dynamic "startup_probe" {
          for_each = containers.value.startup_probe != "" ? [containers.value.startup_probe] : []
          content {
            http_get {
              path = startup_probe.value
            }
          }
        }

        dynamic "env" {
          for_each = concat(
            [
              for secret in var.secrets : {
                name = secret.name
                value_source = {
                  secret_key_ref = {
                    secret  = secret.secret_name
                    version = "latest"
                  }
                }
              }
            ],
            [
              for env_var in var.addl_env_vars : {
                name  = env_var.name
                value = env_var.value
              }
            ]
          )
          content {
            name  = env.value.name
            value = try(env.value.value, null)

            dynamic "value_source" {
              for_each = try([env.value.value_source], [])
              content {
                secret_key_ref {
                  secret  = value_source.value.secret
                  version = value_source.value.version
                }
              }
            }
          }
        }

        dynamic "volume_mounts" {
          for_each = containers.value.volume_mounts
          content {
            name       = volume_mounts.value.name
            mount_path = volume_mounts.value.mount_path
          }
        }

        resources {
          cpu_idle = containers.value.gpus == ""
          limits = merge(
            {
              memory = containers.value.memory,
              cpu    = containers.value.cpu
            },
            containers.value.gpus != "" ? {
              "nvidia.com/gpu" = containers.value.gpus
            } : {}
          )
        }
      }
    }

    dynamic "vpc_access" {
      for_each = var.vpc_direct_egress == "OFF" ? [] : [true]
      content {
        egress = var.vpc_direct_egress
        network_interfaces {
          network    = var.vpc_direct_egress_network
          subnetwork = var.vpc_direct_egress_subnetwork
          tags       = var.vpc_direct_egress_tags
        }
      }
    }

    dynamic "volumes" {
      for_each = var.empty_dir_volumes
      content {
        name = volumes.value.name
        empty_dir {
          medium     = "MEMORY"
          size_limit = volumes.value.size_limit
        }
      }
    }

    dynamic "volumes" {
      for_each = var.gcs_volumes
      content {
        name = volumes.value.name
        gcs {
          bucket    = volumes.value.bucket
          read_only = volumes.value.read_only
        }
      }
    }
  }
}

resource "google_cloud_run_v2_service_iam_member" "invoker" {
  for_each = {
    for pair in setproduct(var.regions, var.invokers) :
    "${pair[0]}-${pair[1]}" => {
      region = pair[0]
      member = pair[1]
    }
  }

  location = each.value.region
  name     = google_cloud_run_v2_service.cloudrun[each.value.region].name
  project  = var.project
  role     = "roles/run.invoker"
  member   = each.value.member
}

# create a serverless NEG for this set of regional services
resource "google_compute_region_network_endpoint_group" "neg" {
  for_each              = var.skipNeg ? toset([]) : toset(var.regions)
  name                  = "libops-neg-${var.name}-${each.value}"
  network_endpoint_type = "SERVERLESS"
  region                = each.value
  project               = var.project

  cloud_run {
    service = google_cloud_run_v2_service.cloudrun[each.value].name
  }

  lifecycle {
    create_before_destroy = true
  }
}

resource "google_compute_backend_service" "backend" {
  count = var.skipNeg ? 0 : 1

  project = var.project
  name    = "libops-backend-${var.name}"

  load_balancing_scheme = "EXTERNAL_MANAGED"
  protocol              = "HTTPS"

  dynamic "backend" {
    for_each = google_compute_region_network_endpoint_group.neg

    content {
      group = backend.value.id
    }
  }

  log_config {
    enable      = true
    sample_rate = 1.0
  }
}
