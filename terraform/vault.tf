locals {
  vault_policy_files          = fileset("${path.module}/policies/vault", "*.hcl")
  vault_gcp_auth_backend_path = "gcp"
  vault_jwt_auth_backend_path = "google-jwt"
  vault_app_policy_name       = "app"
  vault_ci_policy_name        = "ci"
  vault_proxy_admin_emails    = distinct(concat(var.vault_admin_emails, var.vault_ci_service_account_emails))
  vault_service_name          = local.is_prod_workspace ? "vault-server-prod" : "vault-server-dev"
  vault_init_job_name         = local.is_prod_workspace ? "vault-init-prod" : "vault-init-dev"
  vault_repository            = local.shared_artifact_registry_repository
  vault_kms_key_ring_name     = local.is_prod_workspace ? "vault-server-prod" : "vault-server-dev"
  vault_kms_key_name          = "vault"
}

module "vault" {
  count = local.vault_is_owner_workspace ? 1 : 0

  source = "git::https://github.com/libops/terraform-vault-cloudrun?ref=0.5.1"
  providers = {
    docker      = docker
    google      = google
    google-beta = google-beta
  }

  project           = var.project_id
  region            = var.region
  name              = local.vault_service_name
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

resource "vault_mount" "secret" {
  count = local.vault_is_owner_workspace ? 1 : 0

  path = "secret"
  type = "kv"
  options = {
    version = 1
  }
}

resource "vault_mount" "keys" {
  count = local.vault_is_owner_workspace ? 1 : 0

  path = "keys"
  type = "kv"
  options = {
    version = 1
  }
}

resource "vault_policy" "vault" {
  for_each = local.vault_is_owner_workspace ? toset(local.vault_policy_files) : toset([])

  name   = trimsuffix(each.value, ".hcl")
  policy = file("${path.module}/policies/vault/${each.value}")
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
  add_group_aliases = true

  depends_on = [
    vault_auth_backend.gcp,
    vault_policy.vault,
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
  token_ttl              = 300
  token_max_ttl          = 900
  token_policies = [
    local.vault_ci_policy_name,
  ]
  add_group_aliases = true

  depends_on = [
    vault_auth_backend.gcp,
    vault_policy.vault,
  ]
}
