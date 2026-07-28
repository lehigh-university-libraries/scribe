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

vault_provider="$(
  sed -n '/^provider "vault" {/,/^}/p' terraform/main.tf
)"
rg -q 'address[[:space:]]*=[[:space:]]*local\.vault_url' <<<"$vault_provider" ||
  fail "the normal Vault provider no longer preserves owner bootstrap and destroy ordering"
rg -q '^resolve_shared_vault_address\(\)' terraform/deploy-local.sh ||
  fail "the shared deploy path does not resolve the exact live Vault service"
rg -Fq 'gcloud projects describe "$GCLOUD_PROJECT" --format=json' ci/resolve-shared-vault.sh ||
  fail "the preview reconciler project number is not resolved from the configured project"
rg -Fq 'gcloud run services describe "$service_name"' ci/resolve-shared-vault.sh ||
  fail "shared Vault discovery does not use one project-bound service resolver"
rg -Fq -- '--arg vault_addr "$expected_addr"' ci/resolve-shared-vault.sh ||
  fail "shared Vault clients do not use the independently derived deterministic address"
rg -Fq -- '--arg vault_audience "$reported_addr"' ci/resolve-shared-vault.sh ||
  fail "shared Vault JWT login does not preserve the Terraform-owned service URI audience"
rg -Fq '"$repo_root/ci/resolve-shared-vault.sh" "$shared_workspace"' terraform/deploy-local.sh ||
  fail "local preview Vault reconciliation bypasses the shared resolver"
rg -Fq 'export_preview_vault_reconciliation_inputs "$shared_vault_workspace_name"' terraform/deploy-local.sh ||
  fail "the preview reconciler does not receive the exact Vault address and project number"
rg -Fq 'export VAULT_ADDR GCLOUD_PROJECT_NUMBER' terraform/deploy-local.sh ||
  fail "the preview reconciler cannot bind its Vault origin to the configured project"

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

make_target="$(sed -n '/^tf-dev-vault-preview-runtime:/,/^[^[:space:]].*:/p' Makefile)"
rg -q 'TF_TARGET_SET="vault-preview-runtime".*deploy-local\.sh dev' <<<"$make_target" ||
  fail "the local preview Vault runtime entry point does not select the exact shared-dev target set"

target_case="$(sed -n '/^  vault-preview-runtime)/,/^  ocr)/p' terraform/deploy-local.sh)"
if rg -q -- '-target=|verify-vault-target-plan' <<<"$target_case"; then
  fail "preview Vault runtime reconciliation still refreshes the Terraform owner graph"
fi
rg -q 'preview_runtime_reconciler=true' <<<"$target_case" ||
  fail "preview Vault runtime reconciliation does not select the typed reconciler"
rg -q 'environment.*!= "dev"' <<<"$target_case" ||
  fail "preview Vault runtime reconciliation is not restricted to workspace dev"
rg -q 'needs_api_image=false' <<<"$target_case" ||
  fail "preview Vault runtime reconciliation still depends on an unrelated application image"
rg -q 'preview-runtime maintenance cannot bootstrap it outside its reviewed reconciliation boundary' terraform/deploy-local.sh ||
  fail "preview Vault runtime maintenance can bootstrap the Vault owner implicitly"
rg -Fq 'env -u VAULT_TOKEN -u VAULT_ADDR "$repo_root/ci/toolchain-check.sh" --go' terraform/deploy-local.sh ||
  fail "preview Vault runtime maintenance checks the compiler with the recovered token in its environment"
rg -Uq 'env -u VAULT_TOKEN -u VAULT_ADDR[[:space:]\\]+\n[[:space:]]*go build -trimpath -o "\$preview_reconciler_binary" \./cmd/vault-preview-runtime' terraform/deploy-local.sh ||
  fail "preview Vault runtime maintenance builds with the recovered token in the compiler environment"
rg -q '"\$preview_reconciler_binary" -mode="\$reconcile_mode"' terraform/deploy-local.sh ||
  fail "preview Vault runtime maintenance does not invoke the typed reconciler"

if rg -q '^  reconcile-preview-vault-runtime:' .github/workflows/terraform-preview.yaml; then
  fail "preview Vault runtime reconciliation duplicates the trusted deploy job"
fi

trusted_deploy_job="$(sed -n '/^  deploy:/,$p' .github/workflows/terraform-deploy.yaml)"
# shellcheck disable=SC2016 # Match literal workflow and shell variables.
for required in \
  'environment: \$\{\{ inputs\.environment_name \}\}' \
  'queue: max' \
  'cancel-in-progress: false' \
  'SCRIBE_REGION: \$\{\{ inputs\.region \}\}' \
  'ref: \$\{\{ inputs\.checkout_ref \}\}' \
  'WIF_EXPECTED_ENVIRONMENT: \$\{\{ inputs\.environment_name \}\}' \
  'WIF_IDENTITY_CLASS: deploy' \
  'run: \./ci/verify-gcp-wif\.sh' \
  "if: inputs.mode == 'apply' && inputs.pr_number != ''" \
  'actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e' \
  'go-version-file: \.go-version' \
  '\./ci/resolve-shared-vault\.sh "\$vault_workspace"' \
  'VAULT_BOOTSTRAP_MODE: root-token' \
  'make tf-dev-vault-preview-runtime BRANCH="[$]DEPLOY_BRANCH" ACTION=apply'; do
  rg -q "$required" <<<"$trusted_deploy_job" ||
    fail "trusted deploy job is missing preview Vault control: ${required}"
done
rg -Fq "group: \${{ inputs.environment_name == 'preview' && 'terraform-preview-dev-shared' || format('terraform-deploy-{0}', inputs.site_name) }}" <<<"$trusted_deploy_job" ||
  fail "the entire shared preview reconciliation and deployment path is not serialized"

[[ "$(rg -c '^[[:space:]]*- ' .github/actionlint.yaml)" -eq 2 ]] ||
  fail "the actionlint compatibility exception is not limited to the two queue uses"
for workflow in terraform-deploy.yaml terraform-preview.yaml; do
  rg -Fq ".github/workflows/${workflow}:" .github/actionlint.yaml ||
    fail "the actionlint queue exception is not scoped to ${workflow}"
done
rg -Fq 'unexpected key "queue" for "concurrency" section' .github/actionlint.yaml ||
  fail "the actionlint compatibility exception does not match only its unsupported queue syntax"

go_setup_line="$(rg -n 'name: Set up Go for preview Vault reconciliation' .github/workflows/terraform-deploy.yaml | cut -d: -f1)"
reconcile_line="$(rg -n 'name: Reconcile shared preview Vault runtime access' .github/workflows/terraform-deploy.yaml | cut -d: -f1)"
vault_login_line="$(rg -n 'name: Login to Vault' .github/workflows/terraform-deploy.yaml | cut -d: -f1)"
preview_apply_line="$(rg -n 'name: Run preview Terraform$' .github/workflows/terraform-deploy.yaml | cut -d: -f1)"
readiness_line="$(rg -n 'name: Verify frontend, backend, and OCR readiness' .github/workflows/terraform-deploy.yaml | cut -d: -f1)"
((go_setup_line < reconcile_line && reconcile_line < vault_login_line && vault_login_line < preview_apply_line && preview_apply_line < readiness_line)) ||
  fail "shared preview Vault reconciliation is not inside the locked deploy/readiness critical section"

preview_deploy_call="$(sed -n '/^  deploy:/,/^  destroy:/p' .github/workflows/terraform-preview.yaml)"
rg -q 'checkout_ref: \$\{\{ needs\.prepare\.outputs\.base_sha \}\}' <<<"$preview_deploy_call" ||
  fail "credentialed preview deploy does not execute the immutable trusted base"
rg -q 'region: \$\{\{ needs\.prepare\.outputs\.region \}\}' <<<"$preview_deploy_call" ||
  fail "shared preview Vault reconciliation does not inherit the validated deployment region"
if rg -q 'reconcile-preview-vault-runtime' <<<"$preview_deploy_call"; then
  fail "preview deploy still depends on a separately unlocked reconciliation job"
fi

echo "Preview plans are Vault-control-plane-free; protected shared-dev reconciliation and runtime database reads are identity-scoped."
