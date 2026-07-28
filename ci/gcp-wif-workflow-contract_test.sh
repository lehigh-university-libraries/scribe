#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

fail() {
  echo "GCP WIF workflow contract failed: $*" >&2
  exit 1
}

assert_count() {
  local expected="$1" pattern="$2" file="$3" actual
  actual="$(rg -c -- "$pattern" "$file" || true)"
  [ "$actual" = "$expected" ] || fail "$file expected $expected occurrences of $pattern, found $actual"
}

assert_before() {
  local file="$1" before_pattern="$2" after_pattern="$3" before_line after_line
  before_line="$(rg -n -m 1 -- "$before_pattern" "$file" | cut -d: -f1)"
  after_line="$(rg -n -m 1 -- "$after_pattern" "$file" | cut -d: -f1)"
  [ -n "$before_line" ] && [ -n "$after_line" ] && [ "$before_line" -lt "$after_line" ] ||
    fail "$file must run $before_pattern before $after_pattern"
}

assert_block_before() {
  local block="$1" before_pattern="$2" after_pattern="$3" before_line after_line
  before_line="$(rg -n -m 1 -- "$before_pattern" <<<"$block" | cut -d: -f1)"
  after_line="$(rg -n -m 1 -- "$after_pattern" <<<"$block" | cut -d: -f1)"
  [ -n "$before_line" ] && [ -n "$after_line" ] && [ "$before_line" -lt "$after_line" ] ||
    fail "workflow job must run $before_pattern before $after_pattern"
}

assert_gcloud_auth_order() {
  local file="$1" setup_line auth_line checkout_line
  while IFS= read -r setup_line; do
    auth_line="$({ rg -n -- 'uses: google-github-actions/auth@' "$file" || true; } |
      cut -d: -f1 | awk -v limit="$setup_line" '$1 < limit { found = $1 } END { print found }')"
    checkout_line="$({ rg -n -- 'uses: actions/checkout@' "$file" || true; } |
      cut -d: -f1 | awk -v limit="$setup_line" '$1 < limit { found = $1 } END { print found }')"
    [ -n "$checkout_line" ] && [ -n "$auth_line" ] &&
      [ "$checkout_line" -lt "$auth_line" ] && [ "$auth_line" -lt "$setup_line" ] ||
      fail "$file must run checkout and Google auth before setup-gcloud at line $setup_line"
  done < <(rg -n -- 'uses: google-github-actions/setup-gcloud@' "$file" | cut -d: -f1)
}

assert_count 3 'run: ./ci/verify-gcp-wif\.sh' .github/workflows/terraform-apply.yaml
assert_count 1 'run: ./ci/verify-gcp-wif\.sh' .github/workflows/terraform-preview.yaml
assert_count 1 'run: ./ci/verify-gcp-wif\.sh' .github/workflows/terraform-deploy.yaml
assert_count 1 'run: ./ci/verify-gcp-wif\.sh' .github/workflows/terraform-drift.yaml
assert_count 2 'run: ./ci/verify-gcp-wif\.sh' .github/workflows/build-ocr.yaml
assert_count 1 'run: ./ci/verify-gcp-wif\.sh' .github/workflows/backup-verification.yaml

for file in terraform-apply.yaml terraform-preview.yaml terraform-deploy.yaml terraform-drift.yaml build-ocr.yaml backup-verification.yaml; do
  assert_gcloud_auth_order ".github/workflows/$file"
done

if rg -q '(secrets|vars)\.GCLOUD_OIDC_POOL' .github/workflows/build-ocr.yaml; then
  fail "OCR publishing must not share the deploy identity pool"
fi
if rg -q 'secrets\.GCLOUD_OIDC_POOL' .github/workflows/backup-verification.yaml; then
  fail "backup verification must not share the deploy identity pool"
fi
assert_count 4 'vars\.OCR_GCLOUD_OIDC_POOL' .github/workflows/build-ocr.yaml
assert_count 4 'vars\.OCR_GSA' .github/workflows/build-ocr.yaml
assert_count 2 'secrets\.BACKUP_GCLOUD_OIDC_POOL' .github/workflows/backup-verification.yaml

if rg -q 'secrets\.OCR_(GCLOUD_OIDC_POOL|GSA)' .github/workflows/build-ocr.yaml; then
  fail "OCR WIF identifiers are environment configuration, not inherited secrets"
fi
caller_block="$(sed -n '/^  build-ocr:/,/^  [a-zA-Z0-9_-]*:/p' .github/workflows/terraform-apply.yaml)"
if printf '%s\n' "$caller_block" | grep -Eq '^[[:space:]]+secrets:'; then
  fail "terraform-apply.yaml passes caller secrets instead of using protected OCR environment configuration"
fi
for job in build aggregate; do
  job_block="$(sed -n "/^  ${job}:/,/^  [a-zA-Z0-9_-]*:/p" .github/workflows/build-ocr.yaml)"
  # shellcheck disable=SC2016 # Match the literal GitHub expression.
  printf '%s\n' "$job_block" | grep -Fq 'environment: ${{ inputs.environment_name }}' ||
    fail "build-ocr.yaml ${job} job is not bound to the selected protected environment"
done

assert_before .github/workflows/terraform-apply.yaml 'run: ./ci/verify-gcp-wif\.sh' 'Plan or apply singleton project foundation'
ocr_change_detection_job="$(sed -n '/^  ocr-change-detection:/,/^  [a-zA-Z0-9_-]*:/p' .github/workflows/terraform-apply.yaml)"
assert_block_before "$ocr_change_detection_job" 'run: ./ci/verify-gcp-wif\.sh' 'terraform -chdir=terraform output -json deployment_inputs'
preview_ocr_resolver_job="$(sed -n '/^  resolve-production-ocr-images:/,/^  [a-zA-Z0-9_-]*:/p' .github/workflows/terraform-preview.yaml)"
assert_block_before "$preview_ocr_resolver_job" 'run: ./ci/verify-gcp-wif\.sh' 'terraform -chdir=terraform output -json deployment_inputs'
assert_before .github/workflows/terraform-deploy.yaml 'run: ./ci/verify-gcp-wif\.sh' 'Promote the reviewed frontend digest to GAR'
assert_before .github/workflows/terraform-deploy.yaml 'run: ./ci/gcp-vm-bootstrap-diagnostics\.sh' 'Roll back failed production rollout'
assert_before .github/workflows/build-ocr.yaml 'run: ./ci/verify-gcp-wif\.sh' 'Build and publish reviewed OCR image'
assert_before .github/workflows/backup-verification.yaml 'run: ./ci/verify-gcp-wif\.sh' 'Verify state, Vault, upload, and transfer recovery points'
assert_before .github/workflows/terraform-drift.yaml 'run: ./ci/verify-gcp-wif\.sh' 'terraform -chdir=terraform init'

for file in terraform-apply.yaml terraform-preview.yaml terraform-deploy.yaml terraform-drift.yaml build-ocr.yaml backup-verification.yaml; do
  rg -q 'WIF_EXPECTED_ENVIRONMENT:' ".github/workflows/$file" || fail "$file omits the protected environment preflight"
  rg -q 'WIF_IDENTITY_CLASS:' ".github/workflows/$file" || fail "$file omits the identity-class preflight"
  rg -q 'WIF_SERVICE_ACCOUNT:' ".github/workflows/$file" || fail "$file omits the exact service-account preflight"
done

assert_count 1 'run: ./ci/gcp-vm-bootstrap-diagnostics\.sh' .github/workflows/terraform-deploy.yaml
assert_count 1 'continue-on-error: true' .github/workflows/terraform-deploy.yaml
assert_count 4 'run-cloud-run-readiness\.sh' .github/workflows/terraform-deploy.yaml
if rg -q 'gcloud run jobs execute' .github/workflows/terraform-deploy.yaml; then
  fail "terraform-deploy.yaml bypasses the shared bounded Cloud Run readiness helper"
fi
assert_count 1 'inputs\.tf_workspace \}\}-backend-readiness-diagnostics\.log' .github/workflows/terraform-deploy.yaml
assert_count 1 'inputs\.tf_workspace \}\}-ocr-readiness-diagnostics\.log' .github/workflows/terraform-deploy.yaml
assert_count 1 'inputs\.tf_workspace \}\}-rollback-backend-readiness-diagnostics\.log' .github/workflows/terraform-deploy.yaml
assert_count 1 'inputs\.tf_workspace \}\}-rollback-ocr-readiness-diagnostics\.log' .github/workflows/terraform-deploy.yaml
diagnostics_block="$(sed -n '/name: Capture failed VM diagnostics/,/name: Roll back failed production rollout/p' .github/workflows/terraform-deploy.yaml)"
grep -Fq "if: failure() && inputs.mode == 'apply' && (steps.apply.outcome != 'skipped' || steps.apply_preview.outcome != 'skipped')" <<<"$diagnostics_block" ||
  fail "VM diagnostics must be failure-only and run after any started production or preview apply"
grep -Fq 'continue-on-error: true' <<<"$diagnostics_block" ||
  fail "VM diagnostics must not mask or block the original deployment failure"
grep -Fq ".github-artifacts/terraform/\${{ inputs.tf_workspace }}-vm-bootstrap-diagnostics.log" <<<"$diagnostics_block" ||
  fail "VM diagnostics must reuse the bounded Terraform log artifact"

for pattern in \
  'https://token.actions.githubusercontent.com' \
  'assertion\.repository' \
  'assertion\.workflow_ref' \
  'assertion\.ref' \
  'assertion\.environment' \
  'providers list' \
  'service-accounts get-iam-policy'; do
  rg -q -- "$pattern" ci/verify-gcp-wif.sh || fail "live preflight omits $pattern"
done

echo "Protected workflows fail closed unless their dedicated live GCP WIF boundary matches the reviewed claims."
