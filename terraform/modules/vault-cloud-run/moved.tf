# Preserve the existing Vault runtime KMS grants when the runtime and
# initializer identities are split. Without explicit moves, Terraform can
# create the replacement IAM members and then destroy the legacy members with
# identical bindings, removing the live runtime's ability to unseal.
moved {
  from = google_kms_crypto_key_iam_member.vault["roles/cloudkms.viewer"]
  to   = google_kms_crypto_key_iam_member.vault_runtime["roles/cloudkms.viewer"]
}

moved {
  from = google_kms_crypto_key_iam_member.vault["roles/cloudkms.cryptoKeyEncrypterDecrypter"]
  to   = google_kms_crypto_key_iam_member.vault_runtime["roles/cloudkms.cryptoKeyEncrypterDecrypter"]
}
