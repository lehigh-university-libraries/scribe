#!/bin/sh

set -eu

if ! jq -e '
  def harmless: . == ["no-op"] or . == ["read"];
  def reviewed_mutation: . == ["create"] or . == ["update"];
  def permitted_address:
    test("^module\\.vault\\[0\\]\\.google_storage_bucket_iam_member\\.bootstrap_key_reader\\[\"[^\"]+\"\\]$") or
    test("^module\\.vault\\[0\\]\\.google_kms_crypto_key_iam_member\\.bootstrap_root_token_decrypter\\[\"[^\"]+\"\\]$") or
    test("^vault_gcp_auth_backend\\.gcp\\[0\\]$") or
    test("^vault_gcp_auth_backend_role\\.ci\\[\"[^\"]+\"\\]$") or
    test("^vault_jwt_auth_backend\\.google_jwt\\[0\\]$") or
    test("^vault_jwt_auth_backend_role\\.ci\\[\"[^\"]+\"\\]$") or
    . == "vault_policy.vault[\"ci.hcl\"]";
  def permitted_changes:
    all(.[]?;
      (.change.actions | harmless) or
      ((.change.actions | reviewed_mutation) and (.address | permitted_address))
    );

  type == "object" and
  (.format_version | type == "string" and startswith("1.")) and
  ((.resource_changes // []) | permitted_changes) and
  ((.resource_drift // []) | permitted_changes) and
  ((.output_changes // {}) | all(.[]; .actions == ["no-op"]))
' >/dev/null; then
  echo "Refusing targeted Vault CI apply: the saved plan changes resources or recorded outputs outside the reviewed identity boundary." >&2
  exit 1
fi
