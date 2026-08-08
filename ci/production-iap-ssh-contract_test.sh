#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

fail() {
  echo "production IAP SSH contract failed: $*" >&2
  exit 1
}

require_fixed() {
  local expected="$1"
  local file="$2"
  rg -Fq -- "${expected}" "${file}" || fail "${file} is missing required text: ${expected}"
}

require_pattern() {
  local pattern="$1"
  local file="$2"
  rg -q -- "${pattern}" "${file}" || fail "${file} is missing required pattern: ${pattern}"
}

access_file="terraform/production_browser_access.tf"
main_file="terraform/main.tf"

[ -f "${access_file}" ] || fail "${access_file} is missing"

# The protected production deploy workflow supplies exactly one Vault CI GSA.
# Terraform must fail closed on drift instead of selecting an arbitrary entry.
require_fixed 'one(var.vault_ci_service_account_emails),' "${access_file}"
require_fixed 'production_deploy_service_account_email = coalesce(' "${access_file}"
# shellcheck disable=SC2016 # Match the literal Terraform interpolation.
require_fixed '"scribe-browser-deploy-unused@${var.project_id}.iam.gserviceaccount.com",' "${access_file}"

# Production adds only Google's documented IAP TCP forwarding range. Dev and
# preview pass the operator SSH allowlist through byte-for-byte.
require_fixed 'production_iap_ssh_ipv4 = "35.235.240.0/20"' "${access_file}"
require_fixed 'local.is_prod_workspace ? distinct(concat(var.allowed_ssh_ipv4, [local.production_iap_ssh_ipv4])) : var.allowed_ssh_ipv4' "${access_file}"
require_fixed 'ssh_ipv4                 = local.effective_allowed_ssh_ipv4' "${main_file}"
test "$(rg -Fc -- '35.235.240.0/20' "${access_file}")" -eq 1 ||
  fail "the IAP TCP source range must have one canonical definition"
if rg -Fq -- '35.235.240.0/20' "${main_file}"; then
  fail "the production-only IAP range must not be duplicated at the module call site"
fi

# The IAP API and access grant exist only in the production workspace. API
# removal from state must never disable the project API.
api_resource="$(sed -n '/^resource "google_project_service" "iap_tcp_forwarding"/,/^}/p' "${access_file}")"
require_pattern '^resource "google_project_service" "iap_tcp_forwarding" \{' "${access_file}"
rg -Fq -- 'count = local.is_prod_workspace ? 1 : 0' <<<"${api_resource}" || fail "IAP API enablement is not production-only"
rg -Fq -- 'service            = "iap.googleapis.com"' <<<"${api_resource}" || fail "the IAP API service is not exact"
rg -Fq -- 'disable_on_destroy = false' <<<"${api_resource}" || fail "destroy could disable the IAP API"

# Use the non-authoritative instance member resource. Project- or tunnel-wide
# grants would let the deploy identity reach unrelated VMs.
instance_fence="$(sed -n '/^resource "terraform_data" "production_browser_instance"/,/^}/p' "${access_file}")"
require_pattern '^resource "terraform_data" "production_browser_instance" \{' "${access_file}"
rg -Fq -- 'count = local.is_prod_workspace ? 1 : 0' <<<"${instance_fence}" || fail "the instance replacement fence is not production-only"
rg -Fq -- 'triggers_replace = module.scribe.instance.id' <<<"${instance_fence}" || fail "the replacement fence is not keyed to the deployed VM identity"
rg -Fq -- 'precondition {' <<<"${instance_fence}" || fail "the production deploy identity singleton is only advisory"
rg -Fq -- 'condition     = length(var.vault_ci_service_account_emails) == 1' <<<"${instance_fence}" ||
  fail "the production deploy identity singleton is not a blocking resource precondition"

iam_resource="$(sed -n '/^resource "google_iap_tunnel_instance_iam_member" "production_browser"/,/^}/p' "${access_file}")"
require_pattern '^resource "google_iap_tunnel_instance_iam_member" "production_browser" \{' "${access_file}"
# shellcheck disable=SC2016 # These are literal Terraform source assertions.
for expected in \
  'count = local.is_prod_workspace ? 1 : 0' \
  'project  = var.project_id' \
  'zone     = var.zone' \
  'instance = var.name' \
  'role     = "roles/iap.tunnelResourceAccessor"' \
  'member   = "serviceAccount:${local.production_deploy_service_account_email}"' \
  'expression  = "destination.port == 22"'; do
  rg -Fq -- "${expected}" <<<"${iam_resource}" || fail "instance IAP grant is missing: ${expected}"
done
rg -Fq -- 'replace_triggered_by = [terraform_data.production_browser_instance[0]]' <<<"${iam_resource}" ||
  fail "same-name VM replacement would not recreate the instance IAM grant"
test "$(rg -Fc -- 'roles/iap.tunnelResourceAccessor' "${access_file}")" -eq 1 ||
  fail "IAP tunnel access must be granted once, on the exact instance"
if rg -q -- '^resource "google_(project|iap_tunnel)_iam_(member|binding|policy)"' "${access_file}"; then
  fail "IAP access must not be granted at project or all-tunnels scope"
fi

echo "Production IAP SSH access is exact, port-bound, and isolated from dev and preview."
