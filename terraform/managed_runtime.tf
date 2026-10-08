locals {
  runtime_accounts = {
    api     = google_service_account.app.email
    worker  = google_service_account.worker.email
    migrate = google_service_account.migrate.email
  }
  secret_prefix = "${local.name}-secret"
  bootstrap_paths = merge(
    {
      database = "scribe/${terraform.workspace}/database/app"
    },
    local.is_preview_workspace ? {} : {
      google_oauth = "scribe/${terraform.workspace}/google_oauth"
      openai       = "scribe/${terraform.workspace}/openai"
      gemini       = "scribe/${terraform.workspace}/gemini"
    },
  )
  application_env = merge(
    {
      for env in local.runtime_env_vars : env.name => env.value
    },
    {
      AUTH_PREVIEW_ANONYMOUS         = tostring(local.is_preview_workspace)
      SCRIBE_DEPLOYMENT_WORKSPACE    = terraform.workspace
      SECRET_MANAGER_PROJECT_ID      = var.project_id
      SECRET_MANAGER_PREFIX          = local.secret_prefix
      SCRIBE_DATABASE_SECRET_VERSION = local.is_preview_workspace ? google_secret_manager_secret_version.database[0].version : data.google_secret_manager_secret_version.database[0].version
      DATABASE_HOST                  = "127.0.0.1"
      DATABASE_NAME                  = "scribe"
      DATABASE_USER                  = "scribe"
      SERVER_TRUSTED_PROXY_CIDRS     = "127.0.0.1/32,::1/128"
      SCRIBE_DEPLOYED_API_IMAGE      = local.api_image
      SCRIBE_RUN_MIGRATIONS          = "false"
      PDF_EXPORT_URL                 = "http://localhost:8083"
      SCRIBE_OTEL_EXPORTER           = local.is_preview_workspace ? "none" : "google"
      GOOGLE_CLOUD_PROJECT           = var.project_id
      SCRIBE_DEPLOYMENT_ENVIRONMENT  = local.is_preview_workspace ? "preview" : terraform.workspace
      WORKER_HEALTH_LISTEN_ADDR      = ":8081"
    },
  )
  proxy_image       = "gcr.io/cloud-sql-connectors/cloud-sql-proxy:2.18.3@sha256:4f9071e7fb8bc0acc0c66dbbaa292d7c0e6337003ccd29f75e1431f8f8c3fce6"
  database_password = local.is_preview_workspace ? random_password.database[0].result : jsondecode(data.google_secret_manager_secret_version.database[0].secret_data).password
  runtime_secret_bindings = {
    for pair in setproduct(keys(local.runtime_accounts), keys(local.bootstrap_paths)) : "${pair[0]}-${pair[1]}" => {
      account = pair[0]
      secret  = pair[1]
    }
  }
}
resource "google_service_account" "app" {
  project     = var.project_id
  account_id  = local.name
  description = "Scribe Cloud Run API identity; no service account keys."
}
resource "google_service_account" "worker" {
  project     = var.project_id
  account_id  = "worker-${local.name}"
  description = "Scribe request-driven Cloud Run worker."
}
resource "google_service_account" "migrate" {
  project     = var.project_id
  account_id  = "migrate-${local.name}"
  description = "Finite schema migration jobs."
}
# PSC keeps private SQL connectivity independent of producer VPC peering.
resource "google_compute_address" "sql" {
  project      = var.project_id
  name         = "${local.name}-sql"
  region       = var.region
  address_type = "INTERNAL"
  subnetwork   = google_compute_subnetwork.application.id
}
resource "google_compute_forwarding_rule" "sql" {
  project                 = var.project_id
  name                    = "${local.name}-sql"
  region                  = var.region
  network                 = google_compute_network.application.id
  ip_address              = google_compute_address.sql.self_link
  load_balancing_scheme   = ""
  target                  = google_sql_database_instance.application.psc_service_attachment_link
  allow_psc_global_access = true
}
resource "google_dns_managed_zone" "sql" {
  project     = var.project_id
  name        = "${local.name}-sql"
  dns_name    = "${trimsuffix(google_sql_database_instance.application.dns_name, ".")}."
  description = "Cloud SQL Private Service Connect endpoint."
  visibility  = "private"
  private_visibility_config {
    networks {
      network_url = google_compute_network.application.id
    }
  }
}
resource "google_dns_record_set" "sql" {
  project      = var.project_id
  managed_zone = google_dns_managed_zone.sql.name
  name         = google_dns_managed_zone.sql.dns_name
  type         = "A"
  ttl          = 60
  rrdatas      = [google_compute_address.sql.address]
}
resource "google_sql_database_instance" "application" {
  project             = var.project_id
  name                = "${local.name}-mysql"
  region              = var.region
  database_version    = "MYSQL_8_4"
  deletion_protection = local.is_prod_workspace
  settings {
    tier                        = var.cloud_sql_tier
    edition                     = "ENTERPRISE"
    availability_type           = local.is_prod_workspace ? "REGIONAL" : "ZONAL"
    disk_type                   = "PD_SSD"
    disk_size                   = 50
    disk_autoresize             = true
    deletion_protection_enabled = local.is_prod_workspace
    ip_configuration {
      ipv4_enabled = false
      ssl_mode     = "ENCRYPTED_ONLY"
      psc_config {
        psc_enabled               = true
        allowed_consumer_projects = [var.project_id]
      }
    }
    database_flags {
      name  = "character_set_server"
      value = "utf8mb4"
    }
    database_flags {
      name  = "collation_server"
      value = "utf8mb4_unicode_ci"
    }
    backup_configuration {
      enabled                        = true
      binary_log_enabled             = true
      start_time                     = "03:00"
      transaction_log_retention_days = 7
      backup_retention_settings {
        retained_backups = 14
        retention_unit   = "COUNT"
      }
    }
    maintenance_window {
      day          = 7
      hour         = 5
      update_track = "stable"
    }
  }
}
resource "google_sql_database" "application" {
  project   = var.project_id
  instance  = google_sql_database_instance.application.name
  name      = "scribe"
  charset   = "utf8mb4"
  collation = "utf8mb4_unicode_ci"
}
resource "google_sql_database" "triplet" {
  project   = var.project_id
  instance  = google_sql_database_instance.application.name
  name      = "triplet"
  charset   = "utf8mb4"
  collation = "utf8mb4_unicode_ci"
}
resource "random_password" "database" {
  count   = local.is_preview_workspace ? 1 : 0
  length  = 64
  special = false
}
resource "google_sql_user" "application" {
  project  = var.project_id
  instance = google_sql_database_instance.application.name
  name     = "scribe"
  password = local.database_password
}
resource "google_secret_manager_secret" "bootstrap" {
  for_each  = local.bootstrap_paths
  project   = var.project_id
  secret_id = "${local.secret_prefix}-${sha256(each.value)}"
  replication {
    auto {
    }
  }
}
resource "google_secret_manager_secret_version" "database" {
  count  = local.is_preview_workspace ? 1 : 0
  secret = google_secret_manager_secret.bootstrap["database"].id
  secret_data = jsonencode({
    password = local.database_password
    }
  )
}
# make secret-manager-secrets creates and verifies these before the first apply.
# Adopt the exact named resources without putting OAuth/provider payloads in state.
import {
  for_each = local.is_preview_workspace ? {} : local.bootstrap_paths
  to       = google_secret_manager_secret.bootstrap[each.key]
  id       = "projects/${var.project_id}/secrets/${local.secret_prefix}-${sha256(each.value)}"
}
data "google_secret_manager_secret_version" "database" {
  count   = local.is_preview_workspace ? 0 : 1
  project = var.project_id
  secret  = "${local.secret_prefix}-${sha256(local.bootstrap_paths.database)}"
  version = "latest"
}
resource "google_secret_manager_secret_iam_member" "bootstrap" {
  for_each  = local.runtime_secret_bindings
  project   = var.project_id
  secret_id = google_secret_manager_secret.bootstrap[each.value.secret].secret_id
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${local.runtime_accounts[each.value.account]}"
}
resource "random_password" "triplet_write" {
  length  = 64
  special = false
}
resource "random_password" "triplet_source" {
  length  = 64
  special = false
}
resource "random_password" "pagination" {
  length  = 64
  special = false
}
resource "google_secret_manager_secret" "runtime" {
  for_each  = toset(["triplet-dsn", "triplet-write", "triplet-source", "pagination"])
  project   = var.project_id
  secret_id = "${local.name}-${each.key}"
  replication {
    auto {
    }
  }
}
resource "google_secret_manager_secret_version" "runtime" {
  for_each = google_secret_manager_secret.runtime
  secret_data = {
    triplet-dsn    = "scribe:${local.database_password}@tcp(127.0.0.1:3306)/triplet?parseTime=true"
    triplet-write  = random_password.triplet_write.result
    triplet-source = random_password.triplet_source.result
    pagination     = random_password.pagination.result
  }[each.key]
  secret = google_secret_manager_secret.runtime[each.key].id
}
resource "google_secret_manager_secret_iam_member" "runtime" {
  for_each = {
    for pair in setproduct(keys(local.runtime_accounts), ["triplet-dsn", "triplet-write", "triplet-source", "pagination"]) : "${pair[0]}-${pair[1]}" => pair
  }
  project   = var.project_id
  secret_id = google_secret_manager_secret.runtime[each.value[1]].secret_id
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${local.runtime_accounts[each.value[0]]}"
}
resource "google_project_iam_member" "sql_client" {
  for_each = local.runtime_accounts
  project  = var.project_id
  role     = "roles/cloudsql.client"
  member   = "serviceAccount:${each.value}"
  condition {
    title      = "${local.name}-sql-only"
    expression = "resource.name == 'projects/${var.project_id}/instances/${google_sql_database_instance.application.name}'"
  }
}
resource "google_project_iam_custom_role" "provider_secret_create" {
  count       = local.is_preview_workspace ? 0 : 1
  project     = var.project_id
  role_id     = "${replace(local.name, "-", "_")}_secret_create"
  title       = "Scribe credential creation"
  permissions = ["secretmanager.secrets.create"]
}
resource "google_project_iam_member" "provider_secret_create" {
  count   = local.is_preview_workspace ? 0 : 1
  project = var.project_id
  role    = google_project_iam_custom_role.provider_secret_create[0].name
  member  = "serviceAccount:${google_service_account.app.email}"
}
resource "google_project_iam_custom_role" "provider_secrets" {
  for_each = local.is_preview_workspace ? {} : {
    api    = ["secretmanager.secrets.delete", "secretmanager.versions.access", "secretmanager.versions.add"]
    worker = ["secretmanager.secrets.delete", "secretmanager.versions.access"]
  }
  project     = var.project_id
  role_id     = "${replace(local.name, "-", "_")}_${each.key}_secrets"
  title       = "Scribe ${each.key} provider credentials"
  permissions = each.value
}
resource "google_project_iam_member" "provider_secrets" {
  for_each = local.is_preview_workspace ? {} : {
    api    = google_service_account.app.email
    worker = google_service_account.worker.email
  }
  project = var.project_id
  role    = google_project_iam_custom_role.provider_secrets[each.key].name
  member  = "serviceAccount:${each.value}"
  condition {
    title      = "${local.name}-provider-secrets-only"
    expression = "resource.name.startsWith('projects/${local.project_number}/secrets/${local.secret_prefix}-provider-')"
  }
}
resource "google_cloud_run_v2_job" "migrate" {
  for_each            = toset(["scribe", "triplet"])
  name                = "${local.name}-migrate-${each.key}"
  location            = var.region
  deletion_protection = false
  template {
    template {
      service_account = google_service_account.migrate.email
      timeout         = "600s"
      max_retries     = 0
      containers {
        image   = each.key == "scribe" ? local.api_image : data.google_artifact_registry_docker_image.triplet.self_link
        command = ["/app/scribe-cloudsql"]
        args    = [each.key]
        dynamic "env" {
          for_each = merge(local.application_env, {
            CLOUD_SQL_CONNECTION_NAME = google_sql_database_instance.application.connection_name
            }
          )
          content {
            name  = env.key
            value = env.value
          }
        }
        dynamic "env" {
          for_each = {
            TRIPLET_DATABASE_DSN             = "triplet-dsn"
            TRIPLET_PRESENTATION_WRITE_TOKEN = "triplet-write"
            TRIPLET_SOURCE_READ_TOKEN        = "triplet-source"
            SCRIBE_PAGE_TOKEN_SIGNING_KEY    = "pagination"
          }
          content {
            name = env.key
            value_source {
              secret_key_ref {
                secret  = google_secret_manager_secret.runtime[env.value].secret_id
                version = google_secret_manager_secret_version.runtime[env.value].version
              }
            }
          }
        }
        resources {
          limits = {
            cpu    = "1"
            memory = "1Gi"
          }
        }
      }
      vpc_access {
        egress = "PRIVATE_RANGES_ONLY"
        network_interfaces {
          network    = google_compute_network.application.name
          subnetwork = google_compute_subnetwork.application.name
        }
      }
    }
  }
  depends_on = [google_secret_manager_secret_iam_member.runtime, google_secret_manager_secret_iam_member.bootstrap, google_project_iam_member.sql_client, google_sql_user.application, google_sql_database.application, google_sql_database.triplet, google_compute_forwarding_rule.sql, google_dns_record_set.sql]
}
resource "terraform_data" "migrate" {
  triggers_replace = [local.api_image, data.google_artifact_registry_docker_image.triplet.self_link, google_sql_database_instance.application.connection_name, google_secret_manager_secret_version.runtime["triplet-dsn"].version]
  provisioner "local-exec" {
    command = "gcloud run jobs execute '${google_cloud_run_v2_job.migrate["scribe"].name}' --project '${var.project_id}' --region '${var.region}' --wait && gcloud run jobs execute '${google_cloud_run_v2_job.migrate["triplet"].name}' --project '${var.project_id}' --region '${var.region}' --wait"
  }
}
resource "google_cloud_run_v2_service" "application" {
  name                = local.name
  project             = var.project_id
  location            = var.region
  deletion_protection = local.is_prod_workspace
  lifecycle {
    precondition {
      condition     = !local.is_prod_workspace || length(var.allowed_ips) > 0
      error_message = "Production requires a nonempty frontend IP allowlist."
    }
  }
  template {
    execution_environment            = "EXECUTION_ENVIRONMENT_GEN2"
    service_account                  = google_service_account.app.email
    timeout                          = "300s"
    max_instance_request_concurrency = 20
    scaling {
      min_instance_count = local.is_prod_workspace ? 2 : 0
      max_instance_count = var.api_max_instances
    }
    containers {
      name       = "frontend"
      image      = local.frontend_image
      depends_on = ["api"]
      ports {
        container_port = 8888
      }
      env {
        name  = "SCRIBE_FRONTEND_EDGE_MODE"
        value = "cloudrun"
      }
      env {
        name  = "SCRIBE_FRONTEND_ALLOWED_IPS"
        value = jsonencode(var.allowed_ips)
      }
      env {
        name  = "SCRIBE_FRONTEND_BACKEND_ORIGIN"
        value = "http://localhost:8080"
      }
      env {
        name  = "SCRIBE_FRONTEND_PRESENTATION_ORIGIN"
        value = "http://localhost:8082"
      }
      resources {
        limits = {
          cpu    = "1"
          memory = "512Mi"
        }
      }
      startup_probe {
        http_get {
          path = "/healthz"
          port = 8888
        }
      }
    }
    containers {
      name       = "api"
      image      = local.api_image
      command    = ["/app/scribe-api"]
      depends_on = ["cloudsql", "triplet", "pdf"]
      dynamic "env" {
        for_each = local.application_env
        content {
          name  = env.key
          value = env.value
        }
      }
      dynamic "env" {
        for_each = {
          TRIPLET_PRESENTATION_WRITE_TOKEN = "triplet-write"
          TRIPLET_SOURCE_READ_TOKEN        = "triplet-source"
          SCRIBE_PAGE_TOKEN_SIGNING_KEY    = "pagination"
        }
        content {
          name = env.key
          value_source {
            secret_key_ref {
              secret  = google_secret_manager_secret.runtime[env.value].secret_id
              version = google_secret_manager_secret_version.runtime[env.value].version
            }
          }
        }
      }
      resources {
        limits = {
          cpu    = "2"
          memory = "2Gi"
        }
      }
      startup_probe {
        http_get {
          path = "/readyz"
          port = 8080
        }
        failure_threshold = 30
        period_seconds    = 5
      }
      liveness_probe {
        http_get {
          path = "/livez"
          port = 8080
        }
      }
    }
    containers {
      name       = "triplet"
      image      = data.google_artifact_registry_docker_image.triplet.self_link
      depends_on = ["cloudsql"]
      env {
        name  = "PUBLIC_BASE_URL"
        value = local.public_base_url
      }
      dynamic "env" {
        for_each = {
          TRIPLET_DATABASE_DSN             = "triplet-dsn"
          TRIPLET_PRESENTATION_WRITE_TOKEN = "triplet-write"
        }
        content {
          name = env.key
          value_source {
            secret_key_ref {
              secret  = google_secret_manager_secret.runtime[env.value].secret_id
              version = google_secret_manager_secret_version.runtime[env.value].version
            }
          }
        }
      }
      resources {
        limits = {
          cpu    = "2"
          memory = "2Gi"
        }
      }
      startup_probe {
        http_get {
          path = "/healthz"
          port = 8082
        }
      }
    }
    containers {
      name    = "pdf"
      image   = local.api_image
      command = ["/app/scyllaridae"]
      env {
        name  = "SCYLLARIDAE_YML_PATH"
        value = "/app/scyllaridae.yml"
      }
      env {
        name  = "SCYLLARIDAE_PORT"
        value = "8083"
      }
      resources {
        limits = {
          cpu    = "1"
          memory = "1Gi"
        }
      }
      startup_probe {
        tcp_socket {
          port = 8083
        }
      }
    }
    containers {
      name  = "cloudsql"
      image = local.proxy_image
      args  = ["--psc", "--structured-logs", "--health-check", "--http-address=0.0.0.0", "--http-port=9099", "--port=3306", google_sql_database_instance.application.connection_name]
      resources {
        limits = {
          cpu    = "1"
          memory = "256Mi"
        }
      }
      startup_probe {
        http_get {
          path = "/readiness"
          port = 9099
        }
      }
    }
    vpc_access {
      egress = "PRIVATE_RANGES_ONLY"
      network_interfaces {
        network    = google_compute_network.application.name
        subnetwork = google_compute_subnetwork.application.name
      }
    }
  }
  depends_on = [terraform_data.migrate, google_secret_manager_secret_iam_member.runtime, google_project_iam_member.provider_secrets]
}
resource "google_cloud_run_v2_service" "worker" {
  name                = "${local.name}-worker"
  project             = var.project_id
  location            = var.region
  deletion_protection = local.is_prod_workspace
  template {
    execution_environment            = "EXECUTION_ENVIRONMENT_GEN2"
    service_account                  = google_service_account.worker.email
    timeout                          = "600s"
    max_instance_request_concurrency = 1
    scaling {
      min_instance_count = var.worker_min_instances
      max_instance_count = var.worker_max_instances
    }
    containers {
      name       = "worker"
      image      = local.api_image
      command    = ["/app/scribe-worker"]
      depends_on = ["api"]
      env {
        name  = "SCRIBE_WORKER_PUSH"
        value = "true"
      }
      ports {
        container_port = 8081
      }
      dynamic "env" {
        for_each = local.application_env
        content {
          name  = env.key
          value = env.value
        }
      }
      dynamic "env" {
        for_each = {
          TRIPLET_PRESENTATION_WRITE_TOKEN = "triplet-write"
          TRIPLET_SOURCE_READ_TOKEN        = "triplet-source"
          SCRIBE_PAGE_TOKEN_SIGNING_KEY    = "pagination"
        }
        content {
          name = env.key
          value_source {
            secret_key_ref {
              secret  = google_secret_manager_secret.runtime[env.value].secret_id
              version = google_secret_manager_secret_version.runtime[env.value].version
            }
          }
        }
      }
      resources {
        cpu_idle = true
        limits = {
          cpu    = "2"
          memory = "2Gi"
        }
      }
      startup_probe {
        http_get {
          path = "/readyz"
          port = 8081
        }
        failure_threshold = 30
        period_seconds    = 5
      }
      liveness_probe {
        http_get {
          path = "/livez"
          port = 8081
        }
      }
    }
    containers {
      name       = "api"
      image      = local.api_image
      command    = ["/app/scribe-api"]
      depends_on = ["cloudsql", "triplet"]
      dynamic "env" {
        for_each = local.application_env
        content {
          name  = env.key
          value = env.value
        }
      }
      dynamic "env" {
        for_each = {
          TRIPLET_PRESENTATION_WRITE_TOKEN = "triplet-write"
          TRIPLET_SOURCE_READ_TOKEN        = "triplet-source"
          SCRIBE_PAGE_TOKEN_SIGNING_KEY    = "pagination"
        }
        content {
          name = env.key
          value_source {
            secret_key_ref {
              secret  = google_secret_manager_secret.runtime[env.value].secret_id
              version = google_secret_manager_secret_version.runtime[env.value].version
            }
          }
        }
      }
      resources {
        cpu_idle = true
        limits = {
          cpu    = "2"
          memory = "2Gi"
        }
      }
      startup_probe {
        http_get {
          path = "/readyz"
          port = 8080
        }
        failure_threshold = 30
        period_seconds    = 5
      }
      liveness_probe {
        http_get {
          path = "/livez"
          port = 8080
        }
      }
    }
    containers {
      name       = "triplet"
      image      = data.google_artifact_registry_docker_image.triplet.self_link
      depends_on = ["cloudsql"]
      env {
        name  = "PUBLIC_BASE_URL"
        value = local.public_base_url
      }
      dynamic "env" {
        for_each = {
          TRIPLET_DATABASE_DSN             = "triplet-dsn"
          TRIPLET_PRESENTATION_WRITE_TOKEN = "triplet-write"
        }
        content {
          name = env.key
          value_source {
            secret_key_ref {
              secret  = google_secret_manager_secret.runtime[env.value].secret_id
              version = google_secret_manager_secret_version.runtime[env.value].version
            }
          }
        }
      }
      resources {
        cpu_idle = true
        limits = {
          cpu    = "2"
          memory = "2Gi"
        }
      }
      startup_probe {
        http_get {
          path = "/healthz"
          port = 8082
        }
      }
    }
    containers {
      name  = "cloudsql"
      image = local.proxy_image
      args  = ["--psc", "--structured-logs", "--health-check", "--http-address=0.0.0.0", "--http-port=9099", "--port=3306", google_sql_database_instance.application.connection_name]
      resources {
        cpu_idle = true
        limits = {
          cpu    = "1"
          memory = "256Mi"
        }
      }
      startup_probe {
        http_get {
          path = "/readiness"
          port = 9099
        }
      }
    }
    vpc_access {
      egress = "PRIVATE_RANGES_ONLY"
      network_interfaces {
        network    = google_compute_network.application.name
        subnetwork = google_compute_subnetwork.application.name
      }
    }
  }
  depends_on = [terraform_data.migrate, google_secret_manager_secret_iam_member.runtime, google_project_iam_member.provider_secrets]
}
resource "google_cloud_run_v2_service_iam_member" "public" {
  project  = var.project_id
  location = var.region
  name     = google_cloud_run_v2_service.application.name
  role     = "roles/run.invoker"
  member   = "allUsers"
}
output "cloud_sql_instance" {
  value = google_sql_database_instance.application.name
}
output "bootstrap_secrets" {
  value = {
    for key, secret in google_secret_manager_secret.bootstrap : key => secret.id
  }
}
resource "google_storage_bucket_iam_member" "uploads_worker" {
  bucket = google_storage_bucket.uploads.name
  role   = "roles/storage.objectAdmin"
  member = "serviceAccount:${google_service_account.worker.email}"
}
resource "google_pubsub_topic_iam_member" "worker_publisher" {
  topic  = google_pubsub_topic.transcription_jobs.name
  role   = "roles/pubsub.publisher"
  member = "serviceAccount:${google_service_account.worker.email}"
}
resource "google_service_account" "worker_invoker" {
  project     = var.project_id
  account_id  = "invoke-${local.name}"
  description = "Pub/Sub and Scheduler worker invocation only; no data access."
}

resource "google_cloud_run_v2_service_iam_member" "worker_delivery" {
  project  = var.project_id
  location = var.region
  name     = google_cloud_run_v2_service.worker.name
  role     = "roles/run.invoker"
  member   = "serviceAccount:${google_service_account.worker_invoker.email}"
}

resource "google_service_account_iam_member" "pubsub_worker_token" {
  service_account_id = google_service_account.worker_invoker.name
  role               = "roles/iam.serviceAccountTokenCreator"
  member             = "serviceAccount:${local.pubsub_service_agent}"
}

resource "google_cloud_scheduler_job" "worker_maintenance" {
  name    = "${local.name}-worker-maintenance"
  project = var.project_id
  # Scheduler is unavailable in the runtime's default us-east5 region.
  # This control-plane job sends an authenticated HTTPS request only.
  region           = "us-east4"
  schedule         = "*/30 * * * *"
  time_zone        = "Etc/UTC"
  attempt_deadline = "300s"
  http_target {
    uri         = "${google_cloud_run_v2_service.worker.uri}/internal/maintenance"
    http_method = "POST"
    oidc_token {
      service_account_email = google_service_account.worker_invoker.email
      audience              = google_cloud_run_v2_service.worker.uri
    }
  }
  depends_on = [google_cloud_run_v2_service_iam_member.worker_delivery]
}

resource "google_pubsub_topic" "worker_maintenance" {
  name = "${local.name}-worker-maintenance"
}

resource "google_pubsub_topic_iam_member" "maintenance_publisher" {
  for_each = { api = google_service_account.app.email, worker = google_service_account.worker.email }
  topic    = google_pubsub_topic.worker_maintenance.name
  role     = "roles/pubsub.publisher"
  member   = "serviceAccount:${each.value}"
}

resource "google_pubsub_subscription" "worker_maintenance" {
  name                       = "${local.name}-worker-maintenance"
  topic                      = google_pubsub_topic.worker_maintenance.id
  ack_deadline_seconds       = 300
  message_retention_duration = "604800s"
  push_config {
    push_endpoint = "${google_cloud_run_v2_service.worker.uri}/internal/maintenance"
    oidc_token {
      service_account_email = google_service_account.worker_invoker.email
      audience              = google_cloud_run_v2_service.worker.uri
    }
  }
  retry_policy {
    minimum_backoff = "10s"
    maximum_backoff = "600s"
  }
  depends_on = [google_cloud_run_v2_service_iam_member.worker_delivery, google_service_account_iam_member.pubsub_worker_token]
}
