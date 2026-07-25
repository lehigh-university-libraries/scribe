#!/bin/sh

set -eu

scope="${1:-}"
case "$scope" in
  vault-ci-identities) ;;
  *)
    echo "Usage: $0 vault-ci-identities" >&2
    exit 2
    ;;
esac

if ! jq -e '
  def harmless: . == ["no-op"] or . == ["read"];
  def reviewed_mutation: . == ["create"] or . == ["update"];
  def no_state_rebinding:
    (.previous_address? == null) and
    (.change.importing? == null) and
    (.generated_config? == null) and
    (.change.generated_config? == null);
  def permitted_mutation:
    test("^module\\.vault\\[0\\]\\.google_storage_bucket_iam_member\\.bootstrap_key_reader\\[\"[^\"]+\"\\]$") or
    test("^module\\.vault\\[0\\]\\.google_kms_crypto_key_iam_member\\.bootstrap_root_token_decrypter\\[\"[^\"]+\"\\]$") or
    test("^vault_gcp_auth_backend\\.gcp\\[0\\]$") or
    test("^vault_gcp_auth_backend_role\\.ci\\[\"[^\"]+\"\\]$") or
    test("^vault_jwt_auth_backend\\.google_jwt\\[0\\]$") or
    test("^vault_jwt_auth_backend_role\\.ci\\[\"[^\"]+\"\\]$") or
    . == "vault_policy.vault[\"ci.hcl\"]";
  def permitted_changes:
    type == "array" and
    all(.[];
      type == "object" and
      (.address | type == "string") and
      (.change | type == "object") and
      (.change.actions | type == "array") and
      no_state_rebinding and
      (
        (.change.actions | harmless) or
        ((.change.actions | reviewed_mutation) and (.address | permitted_mutation))
      )
    );
  type == "object" and
  .format_version == "1.2" and
  (.resource_changes | permitted_changes) and
  ((.resource_drift? // []) | permitted_changes) and
  (.output_changes | type == "object") and
  (.output_changes | all(.[];
    type == "object" and
    (.actions | type == "array") and
    .actions == ["no-op"]
  ))
' --arg scope "$scope" >/dev/null; then
  echo "Refusing targeted ${scope} apply: the saved plan is incomplete or changes resources or recorded outputs outside the reviewed boundary." >&2
  exit 1
fi
