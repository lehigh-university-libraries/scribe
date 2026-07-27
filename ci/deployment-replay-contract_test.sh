#!/usr/bin/env bash

set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root_dir"

fail() {
  echo "deployment replay contract failed: $*" >&2
  exit 1
}

require_fixed() {
  local value="$1"
  local file="$2"
  rg -Fq -- "$value" "$file" || fail "$file does not replay $value"
}

require_regex() {
  local value="$1"
  local file="$2"
  rg -q -- "$value" "$file" || fail "$file does not match replay contract: $value"
}

bash ci/resolve-rollback-inputs_test.sh
bash ci/select-current-ocr-images_test.sh
bash ci/verify-production-source-lineage_test.sh

require_fixed 'path: .rollback-source' .github/workflows/terraform-deploy.yaml
if rg -Fq -- 'working-directory: .rollback-source' .github/workflows/terraform-deploy.yaml; then
  fail "automatic rollback still executes retired infrastructure code"
fi
# shellcheck disable=SC2016 # Match literal workflow shell variables.
require_fixed './ci/verify-production-source-lineage.sh .rollback-source "$PREVIOUS_SHA" "$CURRENT_SHA"' .github/workflows/terraform-deploy.yaml
# shellcheck disable=SC2016 # Match a literal GitHub expression.
require_fixed 'DEPLOY_EVENT_FORCED: ${{ github.event.forced }}' .github/workflows/terraform-deploy.yaml
require_fixed 'current infrastructure safety code' .github/workflows/terraform-deploy.yaml
require_fixed 'ci/select-current-ocr-images.sh' .github/workflows/terraform-deploy.yaml
require_fixed 'name = "KRAKEN_TRANSCRIPTION_MODEL_ID"' terraform/kraken.tf
require_fixed 'name = "KRAKEN_SEGMENTATION_MODEL_ID"' terraform/kraken.tf
require_fixed "steps.apply.outcome != 'skipped'" .github/workflows/terraform-deploy.yaml
if rg -q "steps\.apply\.outcome == 'success'" .github/workflows/terraform-deploy.yaml; then
  fail "automatic rollback still excludes a partially failed Terraform apply"
fi

capture_line="$(rg -n 'name: Capture current production rollback inputs' .github/workflows/terraform-deploy.yaml | cut -d: -f1)"
apply_line="$(rg -n 'name: Run production Terraform$' .github/workflows/terraform-deploy.yaml | cut -d: -f1)"
[[ "$capture_line" =~ ^[0-9]+$ && "$apply_line" =~ ^[0-9]+$ && "$capture_line" -lt "$apply_line" ]] ||
  fail "production rollback inputs must be captured before the forward apply"

rollback_block="$(sed -n '/name: Roll back failed production rollout/,/name: Read production backup outputs/p' .github/workflows/terraform-deploy.yaml)"
rg -Fq 'SCRIBE_DATA_GENERATION="$(jq -r '\''.data_generation'\'' <<<"$previous")"' <<<"$rollback_block" ||
  fail "automatic rollback does not reload the recorded prior persistence generation"
rg -q '^[[:space:]]*export[[:space:]].*SCRIBE_DATA_GENERATION([[:space:]]|$)' <<<"$rollback_block" ||
  fail "automatic rollback does not export the recorded prior persistence generation"
if rg -q 'SCRIBE_DATA_GENERATION[=:][[:space:]]*canonical-v2' <<<"$rollback_block"; then
  fail "automatic rollback overrides the recorded prior persistence generation"
fi
require_fixed 'path: .deployed-source' .github/workflows/terraform-drift.yaml
require_fixed 'working-directory: .deployed-source' .github/workflows/terraform-drift.yaml
# shellcheck disable=SC2016 # Match literal workflow shell variables.
require_fixed 'merge-base --is-ancestor "$DEPLOYED_SHA" "$CURRENT_SHA"' .github/workflows/terraform-drift.yaml

while IFS='|' read -r environment_name configuration_name terraform_variable; do
  require_regex "${configuration_name}[[:space:]]*=[[:space:]]*var\\.${terraform_variable}" terraform/outputs.tf
  require_fixed "${environment_name}=\"\$(jq" .github/workflows/terraform-deploy.yaml
  require_regex "^[[:space:]]*export[[:space:]]+([A-Za-z_][A-Za-z0-9_]*[[:space:]]+)*${environment_name}([[:space:]]|$)" .github/workflows/terraform-deploy.yaml
  require_fixed ".configuration.${configuration_name}" .github/workflows/terraform-deploy.yaml
  require_fixed "echo \"${environment_name}=\$(jq" .github/workflows/terraform-drift.yaml
  require_fixed ".configuration.${configuration_name}" .github/workflows/terraform-drift.yaml
done <<'EOF'
SCRIBE_REGION|region|region
SCRIBE_ZONE|zone|zone
TF_VAR_backup_restore_service_account_email|backup_restore_service_account_email|backup_restore_service_account_email
ALLOWED_IPS|allowed_ips|allowed_ips
ALLOWED_SSH_IPV4|allowed_ssh_ipv4|allowed_ssh_ipv4
ALLOWED_SSH_IPV6|allowed_ssh_ipv6|allowed_ssh_ipv6
DEV_EXTERNAL_OCR_IMPERSONATORS|dev_external_ocr_impersonators|dev_external_ocr_impersonators
VAULT_ADMIN_EMAILS|vault_admin_emails|vault_admin_emails
VAULT_CI_SERVICE_ACCOUNT_EMAILS|vault_ci_service_account_emails|vault_ci_service_account_emails
TF_VAR_monitoring_notification_channels|monitoring_notification_channels|monitoring_notification_channels
TF_VAR_network_ip_cidr_range|network_ip_cidr_range|network_ip_cidr_range
TF_VAR_compose_network_cidr|compose_network_cidr|compose_network_cidr
TF_VAR_transcription_max_active_jobs_per_workspace|transcription_max_active_jobs_per_workspace|transcription_max_active_jobs_per_workspace
TF_VAR_storage_max_bytes_per_workspace|storage_max_bytes_per_workspace|storage_max_bytes_per_workspace
TF_VAR_storage_max_bytes_total|storage_max_bytes_total|storage_max_bytes_total
TF_VAR_storage_max_items_per_workspace|storage_max_items_per_workspace|storage_max_items_per_workspace
TF_VAR_storage_max_items_total|storage_max_items_total|storage_max_items_total
TF_VAR_storage_max_images_per_workspace|storage_max_images_per_workspace|storage_max_images_per_workspace
TF_VAR_storage_max_images_total|storage_max_images_total|storage_max_images_total
TF_VAR_storage_reservation_ttl|storage_reservation_ttl|storage_reservation_ttl
TF_VAR_storage_normalization_cache_max_bytes|storage_normalization_cache_max_bytes|storage_normalization_cache_max_bytes
TF_VAR_storage_normalization_cache_max_age|storage_normalization_cache_max_age|storage_normalization_cache_max_age
TF_VAR_iiif_max_manifest_canvases|iiif_max_manifest_canvases|iiif_max_manifest_canvases
TF_VAR_iiif_max_manifest_import_bytes|iiif_max_manifest_import_bytes|iiif_max_manifest_import_bytes
EOF

echo "deployment rollback and drift replay contracts passed"
