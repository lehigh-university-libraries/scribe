locals {
  vault_policy_files          = setsubtract(fileset("${path.module}/policies/vault", "*.hcl"), toset(["app.hcl"]))
  vault_gcp_auth_backend_path = "gcp"
  vault_jwt_auth_backend_path = "google-jwt"
  vault_operator_policy_name  = "operator"
  vault_break_glass_policy    = "break-glass"
  vault_app_policy_name       = "app-${local.workspace_slug}"
  vault_preview_policy_name   = "scribe-preview-app"
  vault_ci_policy_name        = "ci"
  vault_gcloud_client_id      = "32555940559.apps.googleusercontent.com"
  vault_proxy_admin_emails    = distinct(concat(var.vault_admin_emails, var.vault_ci_service_account_emails))
  vault_service_name          = local.is_prod_workspace ? "vault-server-prod" : "vault-server-dev"
  vault_init_job_name         = local.is_prod_workspace ? "vault-init-prod" : "vault-init-dev"
  vault_repository            = local.shared_artifact_registry_repository
  vault_kms_key_ring_name     = local.is_prod_workspace ? "vault-server-prod" : "vault-server-dev"
  vault_kms_key_name          = "vault"
}

module "vault" {
  count = local.vault_is_owner_workspace ? 1 : 0

  source = "./modules/vault-cloud-run"
  providers = {
    docker      = docker
    google      = google
    google-beta = google-beta
  }

  project           = var.project_id
  region            = var.region
  name              = local.vault_service_name
  image_name        = local.vault_service_name
  init_job_name     = local.vault_init_job_name
  admin_emails      = local.vault_proxy_admin_emails
  repository        = local.vault_repository
  create_repository = false
  kms_key_ring_name = local.vault_kms_key_ring_name
  kms_key_name      = local.vault_kms_key_name
  bootstrap_service_account_emails = toset(
    var.vault_ci_service_account_emails,
  )
  public_routes = [
    "/.well-known/",
    "/v1/auth/gcp/",
    # "Public" bypasses only the proxy's Google admin header. Vault still
    # requires X-Vault-Token and enforces the workspace-scoped ACL below.
    "/v1/secret/",
    "/v1/sys/health",
  ]
}

resource "google_service_account_iam_member" "vault_gcp_auth_app_service_account_viewer" {
  service_account_id = module.scribe.appGsa.name
  role               = "roles/iam.serviceAccountViewer"
  member             = "serviceAccount:${local.vault_gsa}"
}

resource "google_service_account_iam_member" "vault_gcp_auth_instance_service_account_viewer" {
  service_account_id = module.scribe.instance.gsa.name
  role               = "roles/iam.serviceAccountViewer"
  member             = "serviceAccount:${local.vault_gsa}"
}

resource "google_service_account_iam_member" "vault_gcp_auth_app_service_account_key_verifier" {
  service_account_id = module.scribe.appGsa.name
  role               = local.vault_gcp_auth_key_verifier_role
  member             = "serviceAccount:${local.vault_gsa}"
}

resource "google_service_account_iam_member" "vault_gcp_auth_instance_service_account_key_verifier" {
  service_account_id = module.scribe.instance.gsa.name
  role               = local.vault_gcp_auth_key_verifier_role
  member             = "serviceAccount:${local.vault_gsa}"
}

resource "vault_mount" "secret" {
  count = local.vault_is_owner_workspace ? 1 : 0

  path = "secret"
  type = "kv"
  options = {
    version = 2
  }
}

resource "vault_policy" "vault" {
  for_each = local.vault_is_owner_workspace ? toset(local.vault_policy_files) : toset([])

  name   = trimsuffix(each.value, ".hcl")
  policy = file("${path.module}/policies/vault/${each.value}")
}

resource "vault_policy" "app" {
  count = local.vault_is_owner_workspace ? 1 : 0

  name = local.vault_app_policy_name

  policy = <<-EOT
path "secret/data/scribe/${local.workspace_slug}/google_oauth" {
  capabilities = ["read"]
}

path "secret/data/scribe/${local.workspace_slug}/openai" {
  capabilities = ["read"]
}

path "secret/data/scribe/${local.workspace_slug}/gemini" {
  capabilities = ["read"]
}

path "secret/data/scribe/${local.workspace_slug}/database/app" {
  capabilities = ["read"]
}

path "secret/data/scribe/${local.workspace_slug}/provider-secrets/workspaces/*" {
  capabilities = ["create", "read"]
}

path "secret/metadata/scribe/${local.workspace_slug}/provider-secrets/workspaces/*" {
  capabilities = ["delete"]
}
EOT
}

resource "vault_policy" "preview_app" {
  count = terraform.workspace == "dev" ? 1 : 0

  name = local.vault_preview_policy_name
  # GCP auth uses a stable service-account unique ID as the entity alias and
  # copies the verified email into alias metadata. The policy therefore renders
  # one exact database path for the caller; it cannot read another preview's
  # bootstrap value or any provider/application secret.
  policy = <<-EOT
path "secret/data/scribe/previews/{{identity.entity.aliases.${vault_gcp_auth_backend.gcp[0].accessor}.metadata.service_account_email}}/database/app" {
  capabilities = ["read"]
}
EOT
}

resource "vault_token_auth_backend_role" "ci" {
  count = local.vault_is_owner_workspace ? 1 : 0

  role_name        = local.vault_ci_policy_name
  allowed_policies = [local.vault_ci_policy_name]
  orphan           = false
  renewable        = false
  token_ttl        = 1800
  token_max_ttl    = 3600

  depends_on = [
    vault_policy.vault,
  ]
}

resource "vault_audit" "stdout" {
  count = local.vault_is_owner_workspace ? 1 : 0

  type = "file"
  path = "stdout"
  options = {
    file_path = "stdout"
  }

  depends_on = [
    vault_policy.vault,
    vault_policy.app,
  ]
}

resource "vault_gcp_auth_backend" "gcp" {
  count = local.vault_is_owner_workspace ? 1 : 0

  path         = local.vault_gcp_auth_backend_path
  iam_alias    = "unique_id"
  iam_metadata = ["service_account_email"]
}

resource "vault_jwt_auth_backend" "google_jwt" {
  count = local.vault_is_owner_workspace ? 1 : 0

  path               = local.vault_jwt_auth_backend_path
  type               = "jwt"
  oidc_discovery_url = "https://accounts.google.com"
  bound_issuer       = "https://accounts.google.com"
}

resource "vault_jwt_auth_backend_role" "ci" {
  for_each = local.vault_is_owner_workspace ? {
    for email in var.vault_ci_service_account_emails : replace(replace(email, "@", "-at-"), ".", "-") => email
  } : {}

  backend         = vault_jwt_auth_backend.google_jwt[0].path
  role_name       = "ci-${each.key}"
  role_type       = "jwt"
  user_claim      = "email"
  bound_audiences = [module.vault[0].vault-url]
  bound_claims = {
    email = each.value
  }
  token_policies = [
    local.vault_ci_policy_name,
  ]
  token_ttl     = 1800
  token_max_ttl = 3600

  depends_on = [
    vault_policy.vault,
  ]
}

resource "vault_jwt_auth_backend_role" "admin" {
  for_each = local.vault_is_owner_workspace ? {
    for email in var.vault_admin_emails : replace(replace(email, "@", "-at-"), ".", "-") => email
  } : {}

  backend    = vault_jwt_auth_backend.google_jwt[0].path
  role_name  = "admin-${each.key}"
  role_type  = "jwt"
  user_claim = "email"
  bound_audiences = [
    module.vault[0].vault-url,
    local.vault_gcloud_client_id,
  ]
  bound_claims = {
    email          = each.value
    email_verified = "true"
    hd             = "lehigh.edu"
  }
  token_policies = [
    local.vault_operator_policy_name,
  ]
  token_ttl     = 300
  token_max_ttl = 900

  depends_on = [
    vault_policy.vault,
  ]
}

resource "vault_jwt_auth_backend_role" "admin_break_glass" {
  for_each = local.vault_is_owner_workspace ? {
    for email in var.vault_admin_emails : replace(replace(email, "@", "-at-"), ".", "-") => email
  } : {}

  backend    = vault_jwt_auth_backend.google_jwt[0].path
  role_name  = "break-glass-admin-${each.key}"
  role_type  = "jwt"
  user_claim = "email"
  bound_audiences = [
    module.vault[0].vault-url,
    local.vault_gcloud_client_id,
  ]
  bound_claims = {
    email          = each.value
    email_verified = "true"
    hd             = "lehigh.edu"
  }
  token_policies = [
    local.vault_operator_policy_name,
    local.vault_break_glass_policy,
  ]
  token_ttl     = 600
  token_max_ttl = 900

  depends_on = [
    vault_policy.vault,
  ]
}

resource "vault_gcp_auth_backend_role" "app" {
  count = local.vault_is_owner_workspace ? 1 : 0

  backend = vault_gcp_auth_backend.gcp[0].path
  role    = local.vault_app_role_name
  type    = "iam"
  bound_service_accounts = [
    module.scribe.appGsa.email,
    module.scribe.instance.gsa.email,
  ]
  bound_projects = [var.project_id]
  token_ttl      = 300
  token_max_ttl  = 900
  token_policies = [
    local.vault_app_policy_name,
  ]

  depends_on = [
    vault_gcp_auth_backend.gcp,
    vault_policy.app,
  ]
}

resource "vault_gcp_auth_backend_role" "preview_app" {
  count = terraform.workspace == "dev" ? 1 : 0

  backend                 = vault_gcp_auth_backend.gcp[0].path
  role                    = local.vault_preview_policy_name
  type                    = "iam"
  bound_service_accounts  = ["*"]
  bound_projects          = [var.project_id]
  allow_gce_inference     = false
  token_no_default_policy = true
  token_ttl               = 300
  token_max_ttl           = 900
  token_policies = [
    local.vault_preview_policy_name,
  ]

  depends_on = [
    vault_gcp_auth_backend.gcp,
    vault_policy.preview_app,
  ]
}

resource "vault_gcp_auth_backend_role" "ci" {
  for_each = local.vault_is_owner_workspace ? {
    for email in var.vault_ci_service_account_emails : replace(replace(email, "@", "-at-"), ".", "-") => email
  } : {}

  backend                = local.vault_gcp_auth_backend_path
  role                   = "ci-${each.key}"
  type                   = "iam"
  bound_service_accounts = [each.value]
  bound_projects         = [var.project_id]
  token_ttl              = 1800
  token_max_ttl          = 3600
  token_policies = [
    local.vault_ci_policy_name,
  ]

  depends_on = [
    vault_gcp_auth_backend.gcp,
    vault_policy.vault,
  ]
}
