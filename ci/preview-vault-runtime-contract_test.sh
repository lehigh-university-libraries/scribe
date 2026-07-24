#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

fail() {
  echo "Preview Vault runtime contract failed: $*" >&2
  exit 1
}

# A preview Terraform graph must contain no Vault control-plane operation.
# Keep the complete Vault resource inventory explicit so adding a resource
# requires deciding whether it belongs only to an owner workspace.
actual_resources="$(
  rg -o '^resource "vault_[^"]+" "[^"]+"' terraform/vault.tf \
    | sed -E 's/^resource "([^"]+)" "([^"]+)"$/\1.\2/' \
    | sort
)"
expected_resources=$'vault_audit.stdout\nvault_gcp_auth_backend.gcp\nvault_gcp_auth_backend_role.app\nvault_gcp_auth_backend_role.ci\nvault_gcp_auth_backend_role.preview_app\nvault_jwt_auth_backend.google_jwt\nvault_jwt_auth_backend_role.admin\nvault_jwt_auth_backend_role.admin_break_glass\nvault_jwt_auth_backend_role.ci\nvault_mount.secret\nvault_policy.app\nvault_policy.preview_app\nvault_policy.vault\nvault_token_auth_backend_role.ci'
[[ "$actual_resources" == "$expected_resources" ]] || {
  printf 'Unexpected Vault resource inventory:\n%s\n' "$actual_resources" >&2
  fail "all Vault resources must be classified as owner-only"
}

for resource in \
  'vault_mount" "secret' \
  'vault_policy" "app' \
  'vault_token_auth_backend_role" "ci' \
  'vault_audit" "stdout' \
  'vault_gcp_auth_backend" "gcp' \
  'vault_jwt_auth_backend" "google_jwt' \
  'vault_gcp_auth_backend_role" "app'; do
  rg -Uq "resource \"${resource}\" \{[^\n]*\n[[:space:]]*count = local\\.vault_is_owner_workspace \? 1 : 0" terraform/vault.tf ||
    fail "${resource} is not limited to owner workspaces"
done

for resource in \
  'vault_policy" "vault' \
  'vault_jwt_auth_backend_role" "ci' \
  'vault_jwt_auth_backend_role" "admin' \
  'vault_jwt_auth_backend_role" "admin_break_glass' \
  'vault_gcp_auth_backend_role" "ci'; do
  rg -Uq "resource \"${resource}\" \{[^\n]*\n[[:space:]]*for_each = local\\.vault_is_owner_workspace \?" terraform/vault.tf ||
    fail "${resource} is not limited to owner workspaces"
done

for resource in 'vault_policy" "preview_app' 'vault_gcp_auth_backend_role" "preview_app'; do
  rg -Uq "resource \"${resource}\" \{[^\n]*\n[[:space:]]*count = terraform\\.workspace == \"dev\" \? 1 : 0" terraform/vault.tf ||
    fail "${resource} is not owned solely by the shared dev Vault"
done

if rg -q '^data "vault_' terraform/vault.tf; then
  fail "a preview plan can still perform a Vault data-source read"
fi

shared_vault_lookup="$(
  sed -n '/^data "google_cloud_run_v2_service" "shared_vault" {/,/^}/p' \
    terraform/main.tf
)"
rg -q 'count = local\.vault_is_owner_workspace \? 0 : 1' <<<"$shared_vault_lookup" ||
  fail "the shared Vault service lookup is not limited to consumer workspaces"
rg -q 'project[[:space:]]*= var\.project_id' <<<"$shared_vault_lookup" ||
  fail "the shared Vault service lookup is not project-bound"
rg -q 'location[[:space:]]*= var\.region' <<<"$shared_vault_lookup" ||
  fail "the shared Vault service lookup is not region-bound"
rg -q 'name[[:space:]]*= local\.vault_service_name' <<<"$shared_vault_lookup" ||
  fail "the shared Vault service lookup does not use the fixed owner name"
if rg -q '^data "terraform_remote_state" "shared_vault"' terraform/main.tf; then
  fail "preview Vault discovery still depends on stale owner-workspace root outputs"
fi
rg -q 'data\.google_cloud_run_v2_service\.shared_vault\[0\]\.uri' terraform/main.tf ||
  fail "preview Vault address does not come from the exact live service"
rg -q 'data\.google_cloud_run_v2_service\.shared_vault\[0\]\.template\[0\]\.service_account' terraform/main.tf ||
  fail "preview Vault runtime identity does not come from the exact live service"
rg -q 'local\.vault_gsa == local\.vault_expected_gsa' terraform/main.tf ||
  fail "preview Vault discovery does not reject runtime identity drift"

preview_policy="$(sed -n '/^resource "vault_policy" "preview_app" {/,/^resource "vault_token_auth_backend_role"/p' terraform/vault.tf)"
[[ "$(rg -c '^path ' <<<"$preview_policy")" -eq 1 ]] || fail "preview runtime policy must expose exactly one path"
rg -q 'secret/data/scribe/previews/\{\{identity\.entity\.aliases\.\$\{vault_gcp_auth_backend\.gcp\[0\]\.accessor\}\.metadata\.service_account_email\}\}/database/app' <<<"$preview_policy" ||
  fail "preview runtime policy is not scoped by verified GCP alias email"
rg -q 'capabilities = \["read"\]' <<<"$preview_policy" || fail "preview runtime cannot read its database bootstrap secret"
if rg -q 'create|update|delete|list|provider-secrets|google_oauth|openai|gemini|/\*' <<<"$preview_policy"; then
  fail "preview runtime policy grants more than one exact database read"
fi

rg -Uq '(?s)resource "vault_gcp_auth_backend" "gcp" \{.*?iam_alias[[:space:]]*=[[:space:]]*"unique_id".*?iam_metadata[[:space:]]*=[[:space:]]*\["service_account_email"\]' terraform/vault.tf ||
  fail "GCP auth does not provide stable identity alias email metadata"
rg -Uq '(?s)resource "vault_gcp_auth_backend_role" "preview_app" \{.*?bound_service_accounts[[:space:]]*=[[:space:]]*\["\*"\].*?bound_projects[[:space:]]*=[[:space:]]*\[var\.project_id\].*?allow_gce_inference[[:space:]]*=[[:space:]]*false.*?token_no_default_policy[[:space:]]*=[[:space:]]*true' terraform/vault.tf ||
  fail "shared preview login role is not project-bound and minimal"

rg -q 'vault_app_role_name[[:space:]]*= local\.is_preview_workspace \? "scribe-preview-app"' terraform/main.tf ||
  fail "preview runtime does not select the static login role"
rg -q 'vault_secret_prefix[[:space:]]*= local\.is_preview_workspace \? "scribe/previews/\$\{local\.preview_app_gsa_email\}"' terraform/main.tf ||
  fail "preview runtime does not select its deterministic service-account path"
rg -q 'secret/data/scribe/previews/scribe-pr-\*' terraform/policies/vault/ci.hcl ||
  fail "protected orchestration is not restricted to pr service-account namespaces"
if rg -q 'secret/data/scribe/\+' terraform/policies/vault/ci.hcl; then
  fail "protected orchestration still has an arbitrary workspace wildcard"
fi

echo "Preview plans are Vault-control-plane-free and runtime database reads are identity-scoped."
