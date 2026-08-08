locals {
  production_iap_ssh_ipv4 = "35.235.240.0/20"
  effective_allowed_ssh_ipv4 = (
    local.is_prod_workspace ? distinct(concat(var.allowed_ssh_ipv4, [local.production_iap_ssh_ipv4])) : var.allowed_ssh_ipv4
  )
  # IAM member syntax is validated even when count is zero, so use a
  # syntactically valid non-production sentinel. The production precondition below
  # still fails closed unless the protected workflow supplies exactly one GSA.
  production_deploy_service_account_email = coalesce(
    try(one(var.vault_ci_service_account_emails), null),
    "scribe-browser-deploy-unused@${var.project_id}.iam.gserviceaccount.com",
  )
}

resource "google_project_service" "iap_tcp_forwarding" {
  count = local.is_prod_workspace ? 1 : 0

  project            = var.project_id
  service            = "iap.googleapis.com"
  disable_on_destroy = false
}

resource "terraform_data" "production_browser_instance" {
  count = local.is_prod_workspace ? 1 : 0

  triggers_replace = module.scribe.instance.id

  lifecycle {
    precondition {
      condition     = length(var.vault_ci_service_account_emails) == 1
      error_message = "Production IAP browser readiness requires exactly one protected deploy service account in vault_ci_service_account_emails."
    }
  }
}

resource "google_iap_tunnel_instance_iam_member" "production_browser" {
  count = local.is_prod_workspace ? 1 : 0

  project  = var.project_id
  zone     = var.zone
  instance = var.name
  role     = "roles/iap.tunnelResourceAccessor"
  member   = "serviceAccount:${local.production_deploy_service_account_email}"

  condition {
    title       = "production-browser-ssh-only"
    description = "Allow the protected deploy identity to tunnel only SSH for production browser readiness."
    expression  = "destination.port == 22"
  }

  lifecycle {
    replace_triggered_by = [terraform_data.production_browser_instance[0]]
  }

  depends_on = [google_project_service.iap_tcp_forwarding, module.scribe]
}
