# Scribe root resources changed between singleton and counted addresses while
# the shared Vault boundary was introduced. Keep these transitions explicit so
# state-only normalization can commit them before a narrow Vault CI apply.
moved {
  from = google_service_account_iam_member.vault_gcp_auth_app_service_account_viewer[0]
  to   = google_service_account_iam_member.vault_gcp_auth_app_service_account_viewer
}

moved {
  from = google_service_account_iam_member.vault_gcp_auth_instance_service_account_viewer[0]
  to   = google_service_account_iam_member.vault_gcp_auth_instance_service_account_viewer
}

moved {
  from = vault_policy.app
  to   = vault_policy.app[0]
}

moved {
  from = vault_gcp_auth_backend_role.app
  to   = vault_gcp_auth_backend_role.app[0]
}
