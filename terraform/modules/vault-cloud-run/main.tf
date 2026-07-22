terraform {
  required_providers {
    docker = {
      source  = "kreuzwerker/docker"
      version = ">= 3.0.1"
    }
    google = {
      source  = "hashicorp/google"
      version = ">= 7.22.0"
    }
    google-beta = {
      source  = "hashicorp/google-beta"
      version = ">= 7.22.0"
    }
  }
}

data "google_client_openid_userinfo" "current" {}

locals {
  service_name = trimspace(var.name)
  image_name   = format("%s-docker.pkg.dev/%s/%s/%s:%s", var.country, var.project, var.repository, var.image_name, local.vault_image_context_sha)
  # renovate: datasource=docker depName=libops/vault-proxy
  vault_proxy = "docker.io/libops/vault-proxy:1.0.0@sha256:310285fe6a70600693445c0f3049e38ec8c39f6617fc9a81bf5d5afca2936cee"
  account_id  = trimspace(var.gsa_account_id) != "" ? trimspace(var.gsa_account_id) : substr(local.service_name, 0, 30)
  init_account_id = trimspace(var.init_gsa_account_id) != "" ? trimspace(var.init_gsa_account_id) : trimsuffix(
    substr("${local.account_id}-init", 0, 30),
    "-",
  )
  gsa      = "${local.account_id}@${var.project}.iam.gserviceaccount.com"
  init_gsa = "${local.init_account_id}@${var.project}.iam.gserviceaccount.com"
  vault_image_context_sha = sha256(join("", [
    filesha256("${path.module}/Dockerfile"),
    filesha256("${path.module}/docker-entrypoint.sh"),
    filesha256("${path.module}/vault-server.hcl.tmpl"),
  ]))
  data_bucket_name = trimspace(var.data_bucket_name) != "" ? trimspace(var.data_bucket_name) : lower(
    replace(replace(replace("${var.project}-${local.service_name}-data", "_", "-"), ".", "-"), " ", "-")
  )
  key_bucket_name = trimspace(var.key_bucket_name) != "" ? trimspace(var.key_bucket_name) : lower(
    replace(replace(replace("${var.project}-${local.service_name}-key", "_", "-"), ".", "-"), " ", "-")
  )

  # see https://github.com/libops/vault-proxy/blob/main/config.example.yaml
  vault_proxy_config = {
    vault_addr = "http://127.0.0.1:8200"
    port       = 8080
    admin_emails = concat(
      var.admin_emails,
      [
        data.google_client_openid_userinfo.current.email,
        local.gsa,
        local.init_gsa,
      ]
    )
    public_routes = concat(
      var.public_routes,
      ["/v1/sys/health"] # Essential for health checks
    )
  }
  vault_proxy_yaml = yamlencode(local.vault_proxy_config)
}

## Create the GSA the Vault CloudRun deployment will run as
resource "google_service_account" "gsa" {
  project    = var.project
  account_id = local.account_id
}

resource "google_service_account" "init" {
  project      = var.project
  account_id   = local.init_account_id
  display_name = "${local.service_name} initializer"
  description  = "Init-only Vault identity with root-token bucket access; never assigned to the Vault runtime."
}

check "vault_runtime_and_initializer_are_distinct" {
  assert {
    condition     = google_service_account.gsa.email != google_service_account.init.email
    error_message = "Vault runtime and initialization jobs must use separate service accounts."
  }
}

## Create buckets to store the Vault backend (data) and root token (key)
resource "google_storage_bucket" "vault" {
  for_each = {
    data = local.data_bucket_name
    key  = local.key_bucket_name
  }
  project                     = var.project
  name                        = each.value
  location                    = var.country
  force_destroy               = false
  uniform_bucket_level_access = true
  public_access_prevention    = "enforced"

  versioning {
    enabled = true
  }

  soft_delete_policy {
    retention_duration_seconds = var.soft_delete_retention_days * 86400
  }

  lifecycle_rule {
    condition {
      days_since_noncurrent_time = var.noncurrent_version_retention_days
    }
    action {
      type = "Delete"
    }
  }
}

resource "google_storage_bucket_iam_member" "runtime_data" {
  bucket = google_storage_bucket.vault["data"].name
  role   = "roles/storage.objectAdmin"
  member = format("serviceAccount:%s", google_service_account.gsa.email)
}

resource "google_storage_bucket_iam_member" "initializer_key" {
  bucket = google_storage_bucket.vault["key"].name
  role   = "roles/storage.objectAdmin"
  member = format("serviceAccount:%s", google_service_account.init.email)
}

resource "google_storage_bucket_iam_member" "bootstrap_key_reader" {
  for_each = var.bootstrap_service_account_emails

  bucket = google_storage_bucket.vault["key"].name
  role   = "roles/storage.objectViewer"
  member = "serviceAccount:${each.value}"
}

## Create AR repo and push the Vault image to there, to be deployed to CloudRun
resource "google_artifact_registry_repository" "private" {
  count         = var.create_repository ? 1 : 0
  project       = var.project
  location      = var.country
  repository_id = var.repository
  format        = "DOCKER"
}

# docker build vault server image
resource "docker_image" "vault" {
  name     = local.image_name
  platform = "linux/amd64"

  build {
    context    = path.module
    dockerfile = "Dockerfile"
  }

  keep_locally = false

  triggers = {
    dir_sha = local.vault_image_context_sha
  }
}

# Terraform builds the Vault server rather than consuming a separately
# published application artifact. Scan that exact local image ID before the
# registry push so OS/library vulnerabilities in the produced layers are a
# hard apply failure, not merely a source-tree advisory.
resource "terraform_data" "vault_image_scan" {
  triggers_replace = [
    docker_image.vault.image_id,
    local.vault_image_context_sha,
  ]

  provisioner "local-exec" {
    command = "\"$SCAN_SCRIPT\" \"$VAULT_IMAGE_ID\""
    environment = {
      SCAN_SCRIPT    = abspath("${path.module}/../../../ci/image-vulnerability-scan.sh")
      VAULT_IMAGE_ID = docker_image.vault.image_id
    }
  }
}

# docker push to Artifact Registry
resource "docker_registry_image" "vault" {
  name          = docker_image.vault.name
  keep_remotely = true
  depends_on    = [terraform_data.vault_image_scan, google_artifact_registry_repository.private]

  triggers = {
    dir_sha = local.vault_image_context_sha
  }
}

## Create KMS keys
resource "google_kms_key_ring" "vault-server" {
  count    = var.create_kms ? 1 : 0
  project  = var.project
  name     = var.kms_key_ring_name
  location = "global"
}

resource "google_kms_crypto_key" "key" {
  count    = var.create_kms ? 1 : 0
  name     = var.kms_key_name
  key_ring = google_kms_key_ring.vault-server[0].id

  lifecycle {
    prevent_destroy = true
  }
}

locals {
  kms_key_id = var.create_kms ? google_kms_crypto_key.key[0].id : format(
    "projects/%s/locations/global/keyRings/%s/cryptoKeys/%s",
    var.project,
    var.kms_key_ring_name,
    var.kms_key_name,
  )
}

resource "google_kms_crypto_key_iam_member" "vault_runtime" {
  for_each = toset([
    "roles/cloudkms.viewer",
    "roles/cloudkms.cryptoKeyEncrypterDecrypter"
  ])

  crypto_key_id = local.kms_key_id
  role          = each.value
  member        = format("serviceAccount:%s", google_service_account.gsa.email)
}

resource "google_kms_crypto_key_iam_member" "vault_initializer" {
  for_each = toset([
    "roles/cloudkms.viewer",
    "roles/cloudkms.cryptoKeyEncrypterDecrypter"
  ])

  crypto_key_id = local.kms_key_id
  role          = each.value
  member        = format("serviceAccount:%s", google_service_account.init.email)
}

resource "google_kms_crypto_key_iam_member" "bootstrap_root_token_decrypter" {
  for_each = var.bootstrap_service_account_emails

  crypto_key_id = local.kms_key_id
  role          = "roles/cloudkms.cryptoKeyDecrypter"
  member        = "serviceAccount:${each.value}"
}

module "vault" {
  source = "https://github.com/libops/terraform-cloudrun-v2/archive/903c0758f5b19740a233558d097efdccabece7c5.tar.gz//terraform-cloudrun-v2-903c0758f5b19740a233558d097efdccabece7c5?archive=tar.gz"

  name          = local.service_name
  project       = var.project
  regions       = [var.region]
  skipNeg       = true
  gsa           = google_service_account.gsa.email
  min_instances = 0
  max_instances = 1
  containers = tolist([
    {
      name   = "proxy",
      image  = local.vault_proxy
      port   = 8080
      memory = "512Mi"
      cpu    = "500m"
    },
    {
      name   = "vault",
      image  = format("%s@%s", local.image_name, docker_registry_image.vault.sha256_digest)
      memory = "2Gi"
      cpu    = "2000m"
    }
  ])

  addl_env_vars = tolist([
    {
      name  = "GOOGLE_PROJECT"
      value = var.project
    },
    {
      name  = "KMS_KEY_RING"
      value = var.kms_key_ring_name
    },
    {
      name  = "KMS_CRYPTO_KEY"
      value = var.kms_key_name
    },
    {
      name  = "GOOGLE_STORAGE_BUCKET"
      value = google_storage_bucket.vault["data"].name
    },
    {
      name  = "VAULT_PROXY_YAML"
      value = local.vault_proxy_yaml
    }
  ])

  depends_on = [google_kms_crypto_key_iam_member.vault_runtime, docker_registry_image.vault]
}

resource "google_cloud_run_v2_job" "vault-init" {
  provider = google-beta

  name                = var.init_job_name
  location            = var.region
  deletion_protection = false
  # Terraform must not expose the Vault URL to the root Vault provider until
  # the one-time initialization job has completed successfully. Waiting only
  # for the execution to start leaves first apply racing an uninitialized
  # Vault service.
  run_execution_token = "run-once-created"
  template {
    template {
      service_account = google_service_account.init.email
      containers {
        name  = "vault-init"
        image = var.init_image

        env {
          name  = "GOOGLE_PROJECT"
          value = var.project
        }
        env {
          name  = "GCS_BUCKET_NAME"
          value = google_storage_bucket.vault["key"].name
        }
        env {
          name  = "CHECK_INTERVAL"
          value = "-1"
        }
        env {
          name  = "KMS_KEY_ID"
          value = local.kms_key_id
        }
        env {
          name  = "VAULT_ADDR"
          value = module.vault.urls[var.region]
        }
        env {
          name  = "VAULT_SECRET_SHARES"
          value = 0
        }
        env {
          name  = "VAULT_SECRET_THRESHOLD"
          value = 0
        }
      }
    }
  }
  depends_on = [
    module.vault,
    google_kms_crypto_key_iam_member.vault_initializer,
    google_storage_bucket_iam_member.initializer_key,
  ]
}
