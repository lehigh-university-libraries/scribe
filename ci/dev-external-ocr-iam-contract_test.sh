#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

fail() {
  echo "dev external OCR IAM contract failed: $*" >&2
  exit 1
}

require_pattern() {
  local pattern="$1"
  local file="$2"
  rg -q -- "$pattern" "$file" || fail "$file is missing required pattern: $pattern"
}

forbid_pattern() {
  local pattern="$1"
  local file="$2"
  if rg -q -- "$pattern" "$file"; then
    fail "$file contains forbidden pattern: $pattern"
  fi
}

identity_block="$(sed -n '/^resource "google_service_account" "dev_external_ocr" {$/,/^}$/p' terraform/dev_external_ocr.tf)"
impersonation_block="$(sed -n '/^resource "google_service_account_iam_member" "dev_external_ocr_token_creator" {$/,/^}$/p' terraform/dev_external_ocr.tf)"
guard_block="$(sed -n '/^resource "terraform_data" "dev_external_ocr_workspace_guard" {$/,/^}$/p' terraform/dev_external_ocr.tf)"
variable_block="$(sed -n '/^variable "dev_external_ocr_impersonators" {$/,/^}$/p' terraform/variables.tf)"

rg -q 'count = local\.dev_external_ocr_enabled \? 1 : 0' <<<"$identity_block" ||
  fail "the service account is not gated by the dev workspace local"
rg -q 'account_id[[:space:]]+= "scribe-dev-external"' <<<"$identity_block" ||
  fail "the service account ID changed"
forbid_pattern '^resource "google_service_account_key"' terraform/dev_external_ocr.tf

rg -q 'for_each = local\.dev_external_ocr_enabled \? var\.dev_external_ocr_impersonators : toset\(\[\]\)' <<<"$impersonation_block" ||
  fail "impersonator bindings are not absent outside dev"
rg -q 'service_account_id = google_service_account\.dev_external_ocr\[0\]\.name' <<<"$impersonation_block" ||
  fail "impersonators are not bound directly to the dev-only service account"
rg -q 'role[[:space:]]+= "roles/iam\.serviceAccountTokenCreator"' <<<"$impersonation_block" ||
  fail "impersonators lack the one reviewed account-level role"
if rg -q 'roles/(owner|editor|iam\.serviceAccountUser|iam\.serviceAccountAdmin|run\.admin)' <<<"$impersonation_block"; then
  fail "the impersonation block contains a broader role"
fi

rg -q 'condition[[:space:]]+= local\.dev_external_ocr_enabled \|\| length\(var\.dev_external_ocr_impersonators\) == 0' <<<"$guard_block" ||
  fail "direct Terraform entry points do not reject non-dev impersonators"
rg -q 'type[[:space:]]+= set\(string\)' <<<"$variable_block" ||
  fail "impersonators are not a deduplicated set"
rg -q '\^\(user\|group\):' <<<"$variable_block" ||
  fail "the variable does not restrict principals to explicit users and groups"

require_pattern 'google_service_account\.dev_external_ocr\[\*\]\.email' terraform/kraken.tf
require_pattern 'role[[:space:]]+= "roles/run\.invoker"' terraform/kraken.tf
forbid_pattern 'google_service_account\.dev_external_ocr' terraform/ollama.tf
require_pattern 'production-service invoker' terraform/ollama.tf

mapfile -t identity_references < <(rg -l 'google_service_account\.dev_external_ocr' terraform --glob '*.tf' | sort)
expected_references=$'terraform/dev_external_ocr.tf\nterraform/kraken.tf'
[ "${identity_references[*]}" = "${expected_references//$'\n'/ }" ] ||
  fail "the dev identity is referenced outside its account policy and dev Kraken invokers"

for replay_file in \
  terraform/outputs.tf \
  terraform/deploy-local.sh \
  ci/resolve-destroy-inputs.sh \
  ci/resolve-refresh-inputs.sh \
  ci/resolve-rollback-inputs.sh \
  ci/fixtures/deployment-inputs.json; do
  require_pattern 'dev_external_ocr_impersonators' "$replay_file"
done
require_pattern 'DEV_EXTERNAL_OCR_IMPERSONATORS must be empty outside the dev Terraform workspace' terraform/deploy-local.sh
require_pattern "DEV_EXTERNAL_OCR_IMPERSONATORS: '\[\]'" .github/workflows/terraform-deploy.yaml
require_pattern 'DEV_EXTERNAL_OCR_IMPERSONATORS=.*dev_external_ocr_impersonators' .github/workflows/terraform-drift.yaml

dev_replay="$(
  jq '.configuration.dev_external_ocr_impersonators = ["user:developer@example.edu", "group:scribe@example.edu"]' \
    ci/fixtures/deployment-inputs.json |
    GCLOUD_PROJECT=scribe-test1 ci/resolve-refresh-inputs.sh
)"
[ "$(jq -c '.configuration.dev_external_ocr_impersonators' <<<"$dev_replay")" = \
  '["user:developer@example.edu","group:scribe@example.edu"]' ] ||
  fail "refresh replay did not preserve explicit dev impersonators"
if jq '.configuration.dev_external_ocr_impersonators = ["serviceAccount:other@scribe-test1.iam.gserviceaccount.com"]' \
  ci/fixtures/deployment-inputs.json |
  GCLOUD_PROJECT=scribe-test1 ci/resolve-refresh-inputs.sh >/dev/null 2>&1; then
  fail "refresh replay accepted a non-human impersonator"
fi

echo "Dev external OCR IAM static contract passed."
