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

assert_count 2 'run: ./ci/verify-gcp-wif\.sh' .github/workflows/terraform-apply.yaml
assert_count 1 'run: ./ci/verify-gcp-wif\.sh' .github/workflows/terraform-deploy.yaml
assert_count 1 'run: ./ci/verify-gcp-wif\.sh' .github/workflows/terraform-drift.yaml
assert_count 2 'run: ./ci/verify-gcp-wif\.sh' .github/workflows/build-ocr.yaml
assert_count 1 'run: ./ci/verify-gcp-wif\.sh' .github/workflows/backup-verification.yaml

if rg -q 'secrets\.GCLOUD_OIDC_POOL' .github/workflows/build-ocr.yaml; then
  fail "OCR publishing must not share the deploy identity pool"
fi
if rg -q 'secrets\.GCLOUD_OIDC_POOL' .github/workflows/backup-verification.yaml; then
  fail "backup verification must not share the deploy identity pool"
fi
assert_count 4 'secrets\.OCR_GCLOUD_OIDC_POOL' .github/workflows/build-ocr.yaml
assert_count 2 'secrets\.BACKUP_GCLOUD_OIDC_POOL' .github/workflows/backup-verification.yaml

assert_before .github/workflows/terraform-apply.yaml 'run: ./ci/verify-gcp-wif\.sh' 'Plan or apply singleton project foundation'
assert_before .github/workflows/terraform-deploy.yaml 'run: ./ci/verify-gcp-wif\.sh' 'Promote the reviewed frontend digest to GAR'
assert_before .github/workflows/build-ocr.yaml 'run: ./ci/verify-gcp-wif\.sh' 'Build and publish reviewed OCR image'
assert_before .github/workflows/backup-verification.yaml 'run: ./ci/verify-gcp-wif\.sh' 'Verify state, Vault, upload, and transfer recovery points'
assert_before .github/workflows/terraform-drift.yaml 'run: ./ci/verify-gcp-wif\.sh' 'terraform -chdir=terraform init'

for file in terraform-apply.yaml terraform-deploy.yaml terraform-drift.yaml build-ocr.yaml backup-verification.yaml; do
  rg -q 'WIF_EXPECTED_ENVIRONMENT:' ".github/workflows/$file" || fail "$file omits the protected environment preflight"
  rg -q 'WIF_IDENTITY_CLASS:' ".github/workflows/$file" || fail "$file omits the identity-class preflight"
  rg -q 'WIF_SERVICE_ACCOUNT:' ".github/workflows/$file" || fail "$file omits the exact service-account preflight"
done

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
