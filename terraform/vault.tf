locals {
  vault_policy_files          = setsubtract(fileset("${path.module}/policies/vault", "*.hcl"), toset(["app.hcl"]))
  vault_gcp_auth_backend_path = "gcp"
  vault_jwt_auth_backend_path = "google-jwt"
  vault_operator_policy_name  = "operator"
  vault_break_glass_policy    = "break-glass"
  vault_app_policy_name       = "app-${local.workspace_slug}"
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

  source = "git::https://github.com/libops/terraform-vault-cloudrun?ref=bf62fe8cb4e8d391a357431894bf109d797d13a4"
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
  public_routes = [
    "/.well-known/",
    "/v1/auth/gcp/",
    "/v1/sys/health",
  ]
}

resource "google_service_account_iam_member" "vault_gcp_auth_app_service_account_viewer" {
  count = local.vault_is_owner_workspace ? 1 : 0

  service_account_id = module.scribe.appGsa.name
  role               = "roles/iam.serviceAccountViewer"
  member             = "serviceAccount:${module.vault[0].gsa}"
}

resource "google_service_account_iam_member" "vault_gcp_auth_instance_service_account_viewer" {
  count = local.vault_is_owner_workspace ? 1 : 0

  service_account_id = module.scribe.instance.gsa.name
  role               = "roles/iam.serviceAccountViewer"
  member             = "serviceAccount:${module.vault[0].gsa}"
}

resource "google_service_account_iam_member" "vault_gcp_auth_app_service_account_key_admin" {
  count = local.vault_is_owner_workspace ? 1 : 0

  service_account_id = module.scribe.appGsa.name
  role               = "roles/iam.serviceAccountKeyAdmin"
  member             = "serviceAccount:${module.vault[0].gsa}"
}

resource "google_service_account_iam_member" "vault_gcp_auth_instance_service_account_key_admin" {
  count = local.vault_is_owner_workspace ? 1 : 0

  service_account_id = module.scribe.instance.gsa.name
  role               = "roles/iam.serviceAccountKeyAdmin"
  member             = "serviceAccount:${module.vault[0].gsa}"
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

path "secret/data/scribe/${local.workspace_slug}/provider-secrets/workspaces/{{identity.entity.metadata.workspace_id}}/*" {
  capabilities = ["create", "read"]
}

path "secret/metadata/scribe/${local.workspace_slug}/provider-secrets/workspaces/{{identity.entity.metadata.workspace_id}}/*" {
  capabilities = ["delete"]
}
EOT
}

data "vault_auth_backend" "gcp" {
  path = local.vault_gcp_auth_backend_path

  depends_on = [
    vault_auth_backend.gcp,
  ]
}

resource "vault_token_auth_backend_role" "ci" {
  count = local.vault_is_owner_workspace ? 1 : 0

  role_name        = local.vault_ci_policy_name
  allowed_policies = [local.vault_ci_policy_name]
  orphan           = false
  renewable        = false
  token_ttl        = 300
  token_max_ttl    = 900

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

resource "vault_auth_backend" "gcp" {
  count = local.vault_is_owner_workspace ? 1 : 0

  path = local.vault_gcp_auth_backend_path
  type = "gcp"
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
  token_ttl     = 300
  token_max_ttl = 900

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
  token_ttl     = 120
  token_max_ttl = 300

  depends_on = [
    vault_policy.vault,
  ]
}

resource "vault_gcp_auth_backend_role" "app" {
  backend = local.vault_gcp_auth_backend_path
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
    vault_auth_backend.gcp,
    vault_policy.app,
  ]
}

data "vault_gcp_auth_backend_role" "app" {
  backend   = local.vault_gcp_auth_backend_path
  role_name = local.vault_app_role_name

  depends_on = [
    vault_gcp_auth_backend_role.app,
  ]
}

resource "vault_identity_entity" "app" {
  name = "${local.vault_app_role_name}-workspace-${var.vault_app_workspace_id}"
  metadata = {
    workspace_id   = var.vault_app_workspace_id
    workspace_slug = local.workspace_slug
  }
}

resource "vault_identity_entity_alias" "app_gcp_role" {
  name           = data.vault_gcp_auth_backend_role.app.role_id
  mount_accessor = data.vault_auth_backend.gcp.accessor
  canonical_id   = vault_identity_entity.app.id
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
  token_ttl              = 300
  token_max_ttl          = 900
  token_policies = [
    local.vault_ci_policy_name,
  ]

  depends_on = [
    vault_auth_backend.gcp,
    vault_policy.vault,
  ]
}
