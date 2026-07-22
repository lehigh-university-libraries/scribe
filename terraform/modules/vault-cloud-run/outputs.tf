output "vault-url" {
  value       = module.vault.urls[var.region]
  description = "The URL to the Vault instance."
  depends_on  = [google_cloud_run_v2_job.vault-init]
}

output "gsa" {
  value       = google_service_account.gsa.email
  description = "The GSA the Vault instance runs as."
}

output "init_gsa" {
  value       = google_service_account.init.email
  description = "Init-only GSA that can write the encrypted Vault root-token object."
}

output "key_bucket" {
  value = local.key_bucket_name
}

output "data_bucket" {
  value = local.data_bucket_name
}


output "repo" {
  value = var.repository
}
