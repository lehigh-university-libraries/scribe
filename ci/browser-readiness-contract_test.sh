#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

fail() {
  echo "browser readiness contract failed: $*" >&2
  exit 1
}

require_fixed() {
  local pattern="$1" file="$2"
  rg -Fq -- "$pattern" "$file" || fail "$file is missing: $pattern"
}

require_pattern() {
  local pattern="$1" file="$2"
  rg -q -- "$pattern" "$file" || fail "$file is missing required pattern: $pattern"
}

forbid_pattern() {
  local pattern="$1" file="$2"
  if rg -qi -- "$pattern" "$file"; then
    fail "$file contains forbidden pattern: $pattern"
  fi
}

assert_before() {
  local file="$1" before_pattern="$2" after_pattern="$3" before_line after_line
  before_line="$(rg -n -m 1 -- "$before_pattern" "$file" | cut -d: -f1)"
  after_line="$(rg -n -m 1 -- "$after_pattern" "$file" | cut -d: -f1)"
  [ -n "$before_line" ] && [ -n "$after_line" ] && [ "$before_line" -lt "$after_line" ] ||
    fail "$file must place $before_pattern before $after_pattern"
}

playwright_version="$(jq -r '.devDependencies["@playwright/test"]' web/package.json)"
playwright_image="$(sed -nE 's/^PLAYWRIGHT_TEST_IMAGE="\$\{PLAYWRIGHT_TEST_IMAGE:-([^}]*)\}"$/\1/p' ci/test-browser.sh)"
[ -n "$playwright_version" ] && [ -n "$playwright_image" ] || fail "could not resolve the locked Playwright version"
case "$playwright_image" in
  "mcr.microsoft.com/playwright:v${playwright_version}-noble@sha256:"*) ;;
  *) fail "the local browser image does not match web's locked Playwright version" ;;
esac
[ "$(rg -c -F "FROM $playwright_image" Dockerfile.browser-readiness)" -eq 2 ] ||
  fail "the protected runner must pin both image stages to the reviewed Playwright digest"
require_fixed "USER pwuser" Dockerfile.browser-readiness
require_fixed 'ENTRYPOINT ["node", "/app/deployed-readiness.mjs"]' Dockerfile.browser-readiness
forbid_pattern 'latest|curl|wget|apt-get' Dockerfile.browser-readiness

node --check web/e2e/deployed-readiness.mjs
for forbidden in 'screenshot' 'tracing' 'recordVideo' 'storageState'; do
  forbid_pattern "$forbidden" web/e2e/deployed-readiness.mjs
done
# shellcheck disable=SC2016 # These are literal JavaScript source assertions.
for required in \
  'selectOption({ label: "Tesseract OCR" })' \
  'Batch transcription complete. Updated text is now available in the editor.' \
  '/presentation/v3/item-image-${itemImageID}/canvas/page-1/annotations' \
  'annotationPage.items.some(annotationHasText)' \
  'Document retranscribed. Save to persist this draft.' \
  'Saved page.' \
  'Edits published.' \
  'Publish edits' \
  'width: 360, height: 800' \
  'width: 768, height: 1024' \
  'width: 1440, height: 900' \
  'data-scribe-action-panel="true"' \
  'Copy workspace token' \
  'Copy token' \
  'navigator.clipboard.writeText("")' \
  'page.on("response"' \
  'page.on("requestfailed"' \
  'page.on("console"' \
  'page.on("dialog"'; do
  require_fixed "$required" web/e2e/deployed-readiness.mjs
done
require_fixed 'data-scribe-action-panel="true"' mirador-scribe/src/components/ScribeActionPanel.jsx
require_fixed 'startUploadPayload?.item?.id' web/e2e/deployed-readiness.mjs
require_fixed '/scribe.v1.AuthService/DeleteAPIKey' web/e2e/deployed-readiness.mjs
require_fixed '/scribe.v1.AuthService/ListAPIKeys' web/e2e/deployed-readiness.mjs
require_fixed 'listPayload.apiKeys.some' web/e2e/deployed-readiness.mjs
require_fixed 'apiKeyDelete.getAttribute("data-api-key-delete")' web/e2e/deployed-readiness.mjs
require_fixed 'findActionByValue(page, "data-item-delete", createdItemID)' web/e2e/deployed-readiness.mjs
require_fixed 'Math.abs(geometry.panelClientHeight - geometry.parentClientHeight) > 2' web/e2e/deployed-readiness.mjs
require_fixed 'geometry.panelScrollWidth > geometry.panelClientWidth + 1' web/e2e/deployed-readiness.mjs
require_fixed 'const clientCancellation = /ERR_ABORTED|cancell?ed/i.test' web/e2e/deployed-readiness.mjs
require_fixed 'await navigate("/", false);' web/e2e/deployed-readiness.mjs
require_fixed 'if (requireHealthy) assertBrowserHealthy();' web/e2e/deployed-readiness.mjs
require_fixed 'page.locator("[data-scribe-granularity]").count() !== 0' web/e2e/deployed-readiness.mjs
require_fixed 'if (await tokenField.inputValue() !== "")' web/e2e/deployed-readiness.mjs
forbid_pattern 'settings-api-key-form"\)\.count\(\) > 0' web/e2e/deployed-readiness.mjs
require_pattern '\^scribe-pr-\[1-9\]\[0-9\]\*-' web/e2e/deployed-readiness.mjs
[ "$(rg -c -F 'process.stderr.write(`browser readiness failed: ${failureCategory}\n`)' web/e2e/deployed-readiness.mjs)" -eq 1 ] ||
  fail "the runner must emit exactly one bounded failure marker"

require_fixed 'normalized_browser_readiness_image = trimspace(var.browser_readiness_image)' terraform/readiness.tf
require_pattern 'browser_readiness_enabled[[:space:]]+= local\.is_preview_workspace && local\.normalized_browser_readiness_image != ""' terraform/readiness.tf
require_fixed 'browser_readiness_image = local.normalized_browser_readiness_image' terraform/outputs.tf
require_fixed 'image = local.normalized_browser_readiness_image' terraform/readiness.tf
require_pattern 'browser_readiness_name_hash[[:space:]]+= substr\(sha256\("\$\{var\.name\}:\$\{local\.workspace_slug\}"\), 0, 8\)' terraform/readiness.tf
require_fixed 'substr(var.name, 0, 46)' terraform/readiness.tf
require_fixed 'substr(var.name, 0, 32)' terraform/readiness.tf
# shellcheck disable=SC2016 # This is a literal Terraform source assertion.
require_fixed 'substr("probe-browser-${local.workspace_slug}", 0, 21)' terraform/readiness.tf
for resource in google_compute_address google_compute_router google_compute_router_nat google_compute_subnetwork google_cloud_run_v2_job google_service_account; do
  require_pattern "resource \"${resource}\" \"browser_readiness\"" terraform/readiness.tf
done
for required in \
  'nat_ip_allocate_option             = "MANUAL_ONLY"' \
  'nat_ips                            = [google_compute_address.browser_readiness[0].self_link]' \
  'source_subnetwork_ip_ranges_to_nat = "LIST_OF_SUBNETWORKS"' \
  'name                    = google_compute_subnetwork.browser_readiness[0].self_link' \
  'source_ip_ranges_to_nat = ["ALL_IP_RANGES"]' \
  'service_account = google_service_account.browser_readiness[0].email' \
  'egress = "ALL_TRAFFIC"' \
  'subnetwork = local.browser_readiness_subnetwork_resource_name' \
  'value = local.public_base_url'; do
  require_fixed "$required" terraform/readiness.tf
done
require_fixed 'ip_cidr_range = var.browser_readiness_subnet_cidr' terraform/readiness.tf
require_fixed 'private_ip_google_access = false' terraform/readiness.tf
require_fixed 'check "browser_readiness_subnet_isolated"' terraform/readiness.tf
require_fixed 'setintersection(' terraform/readiness.tf
[ "$(rg -c -F 'subnetwork = local.readiness_subnetwork_resource_name' terraform/readiness.tf)" -eq 2 ] ||
  fail "only the backend and OCR probes may use the application subnet"
[ "$(rg -c -F 'subnetwork = local.browser_readiness_subnetwork_resource_name' terraform/readiness.tf)" -eq 1 ] ||
  fail "only the browser probe may use the NATed browser subnet"
require_fixed 'power_button_allowed_ips = distinct(concat(var.allowed_ips, local.browser_readiness_allowed_ips))' terraform/main.tf
[ "$(rg -l 'browser_readiness_allowed_ips' terraform/*.tf | wc -l)" -eq 2 ] ||
  fail "the derived browser /32 must be consumed only by readiness and this preview's PPB allowlist"
for iam_file in terraform/backup.tf terraform/iam.tf terraform/kraken.tf terraform/pubsub.tf terraform/storage.tf terraform/vault.tf; do
  [ ! -f "$iam_file" ] || forbid_pattern 'browser_readiness' "$iam_file"
done

for replay_file in \
  terraform/outputs.tf \
  terraform/deploy-local.sh \
  ci/resolve-destroy-inputs.sh \
  ci/resolve-refresh-inputs.sh \
  ci/resolve-rollback-inputs.sh; do
  require_fixed 'browser_readiness_image' "$replay_file"
  require_fixed 'browser_readiness_subnet_cidr' "$replay_file"
done
require_fixed '!endswith(local.normalized_browser_readiness_image, "sha256:0000000000000000000000000000000000000000000000000000000000000000")' terraform/outputs.tf
require_fixed 'has("browser_readiness_image") | not' ci/resolve-destroy-inputs.sh
require_fixed 'has("browser_readiness_image") | not' ci/resolve-rollback-inputs.sh

replay_test_dir="$(mktemp -d)"
trap 'rm -rf "$replay_test_dir"' EXIT
jq 'del(.browser_readiness_image)' ci/fixtures/deployment-inputs.json >"$replay_test_dir/legacy.json"
legacy_replay="$(GCLOUD_PROJECT=scribe-test1 ci/resolve-destroy-inputs.sh <"$replay_test_dir/legacy.json")"
[ "$(jq -r '.browser_readiness_image' <<<"$legacy_replay")" = "" ] ||
  fail "pre-bootstrap lifecycle state did not normalize to the historical empty browser image"
jq 'del(.configuration.browser_readiness_subnet_cidr)' ci/fixtures/deployment-inputs.json >"$replay_test_dir/legacy-subnet.json"
legacy_subnet_replay="$(GCLOUD_PROJECT=scribe-test1 ci/resolve-destroy-inputs.sh <"$replay_test_dir/legacy-subnet.json")"
[ "$(jq -r '.configuration.browser_readiness_subnet_cidr' <<<"$legacy_subnet_replay")" = "10.43.0.0/26" ] ||
  fail "pre-bootstrap lifecycle state did not normalize to the reviewed browser subnet default"
for invalid_filter in \
  '.browser_readiness_image = null' \
  '.browser_readiness_image = "us-docker.pkg.dev/scribe-test1/internal/scribe-browser-readiness@sha256:0000000000000000000000000000000000000000000000000000000000000000"' \
  '.configuration.browser_readiness_subnet_cidr = null' \
  '.configuration.browser_readiness_subnet_cidr = "10.43.0.0/24"'; do
  jq "$invalid_filter" ci/fixtures/deployment-inputs.json >"$replay_test_dir/invalid.json"
  if GCLOUD_PROJECT=scribe-test1 ci/resolve-destroy-inputs.sh <"$replay_test_dir/invalid.json" >/dev/null 2>&1; then
    fail "destroy replay accepted a null or placeholder browser readiness image"
  fi
done

require_fixed 'ref: refs/heads/main' .github/workflows/terraform-preview.yaml
# shellcheck disable=SC2016 # This is a literal GitHub expression assertion.
require_fixed 'checkout_ref: ${{ needs.prepare.outputs.base_sha }}' .github/workflows/terraform-preview.yaml
require_fixed "if: inputs.mode == 'apply' && inputs.pr_number != ''" .github/workflows/terraform-deploy.yaml
# shellcheck disable=SC2016 # This is a literal GitHub expression assertion.
require_fixed 'PROTECTED_SOURCE_SHA: ${{ inputs.checkout_ref }}' .github/workflows/terraform-deploy.yaml
require_fixed '--file Dockerfile.browser-readiness' .github/workflows/terraform-deploy.yaml
require_fixed '--provenance=true' .github/workflows/terraform-deploy.yaml
require_fixed '--sbom=true' .github/workflows/terraform-deploy.yaml
# shellcheck disable=SC2016 # This is a literal shell-source assertion.
require_fixed 'SCRIBE_BROWSER_READINESS_IMAGE=$readiness_image' .github/workflows/terraform-deploy.yaml
require_fixed 'name: Verify frontend, backend, and OCR readiness' .github/workflows/terraform-deploy.yaml
require_fixed 'inputs.tf_workspace }}-browser-readiness-diagnostics.log' .github/workflows/terraform-deploy.yaml
assert_before .github/workflows/terraform-deploy.yaml 'run: ./ci/verify-gcp-wif\.sh' 'name: Build protected preview browser readiness image'
assert_before .github/workflows/terraform-deploy.yaml 'name: Checkout repository' 'name: Build protected preview browser readiness image'

echo "Protected preview browser readiness is pinned, isolated, replayable, and categorical."
