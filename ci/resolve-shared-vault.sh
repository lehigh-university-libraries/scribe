#!/usr/bin/env bash

set -euo pipefail

usage() {
  echo "usage: $0 <dev|prod> [--allow-runtime-identity-drift]" >&2
}

workspace="${1:-}"
identity_mode="${2:-}"
case "$workspace" in
  dev)
    service_name="vault-server-dev"
    ;;
  prod)
    service_name="vault-server-prod"
    ;;
  *)
    usage
    exit 2
    ;;
esac
case "$identity_mode" in
  "")
    require_expected_identity=true
    ;;
  --allow-runtime-identity-drift)
    require_expected_identity=false
    ;;
  *)
    usage
    exit 2
    ;;
esac

: "${GCLOUD_PROJECT:?GCLOUD_PROJECT is required}"
region="${SCRIBE_REGION:-${TF_VAR_region:-us-east5}}"
[[ "$GCLOUD_PROJECT" =~ ^[a-z][a-z0-9-]{4,28}[a-z0-9]$ ]] || {
  echo "GCLOUD_PROJECT is invalid." >&2
  exit 1
}
[[ "$region" =~ ^[a-z][a-z0-9-]{1,30}[0-9]$ ]] || {
  echo "SCRIBE_REGION is invalid." >&2
  exit 1
}

# Neither gcloud nor jq needs the recovered Vault credential.
unset VAULT_TOKEN VAULT_ADDR

if ! project_json="$(gcloud projects describe "$GCLOUD_PROJECT" --format=json 2>/dev/null)"; then
  echo "Failed to resolve the configured Google Cloud project." >&2
  exit 1
fi
if ! project_id="$(jq -er '.projectId | strings | select(length > 0)' <<<"$project_json" 2>/dev/null)" ||
  ! project_number="$(jq -er '.projectNumber | tostring | select(length > 0)' <<<"$project_json" 2>/dev/null)"; then
  echo "The configured Google Cloud project description was incomplete." >&2
  exit 1
fi
if [ "$project_id" != "$GCLOUD_PROJECT" ]; then
  echo "The resolved Google Cloud project identity did not match GCLOUD_PROJECT." >&2
  exit 1
fi
[[ "$project_number" =~ ^[1-9][0-9]{5,19}$ ]] || {
  echo "The resolved Google Cloud project number was invalid." >&2
  exit 1
}

if ! service_json="$(gcloud run services describe "$service_name" \
  --project "$GCLOUD_PROJECT" \
  --region "$region" \
  --format=json 2>/dev/null)"; then
  echo "Failed to resolve the shared Vault service." >&2
  exit 1
fi
if ! resolved_service_name="$(jq -er '.metadata.name | strings | select(length > 0)' <<<"$service_json" 2>/dev/null)" ||
  ! reported_addr="$(jq -er '.status.url | strings | select(length > 0)' <<<"$service_json" 2>/dev/null)" ||
  ! runtime_gsa="$(jq -er '.spec.template.spec.serviceAccountName | strings | select(length > 0)' <<<"$service_json" 2>/dev/null)"; then
  echo "The shared Vault service description was incomplete." >&2
  exit 1
fi
resolved_service_name="$(printf '%s' "$resolved_service_name" | tr -d '\r\n')"
reported_addr="$(printf '%s' "$reported_addr" | tr -d '\r\n')"
runtime_gsa="$(printf '%s' "$runtime_gsa" | tr -d '\r\n')"
if [ "$resolved_service_name" != "$service_name" ]; then
  echo "The resolved shared Vault service identity was unexpected." >&2
  exit 1
fi
if [[ ! "$reported_addr" =~ ^https://[a-z0-9][a-z0-9.-]*[a-z0-9]\.run\.app$ ]]; then
  echo "The shared Vault service did not expose a valid default HTTPS origin." >&2
  exit 1
fi

# Cloud Run assigns both deterministic project-number URLs and stable
# non-deterministic URLs. Depending on the API/client version, status.url can
# report either one. Clients use the independently derived deterministic
# origin after verifying the exact project-scoped service. Google ID tokens
# must retain the reported origin as their audience because the Terraform-owned
# Vault JWT role is bound to that exact service URI.
deterministic_label="${service_name}-${project_number}"
if [ "${#deterministic_label}" -gt 63 ]; then
  echo "The shared Vault service name is too long for a deterministic origin." >&2
  exit 1
fi
expected_addr="https://${service_name}-${project_number}.${region}.run.app"
expected_gsa="${service_name}@${GCLOUD_PROJECT}.iam.gserviceaccount.com"
if [ "$require_expected_identity" = "true" ] && [ "$runtime_gsa" != "$expected_gsa" ]; then
  echo "The shared Vault service does not use its expected runtime identity." >&2
  exit 1
fi

jq -cn \
  --arg vault_addr "$expected_addr" \
  --arg vault_audience "$reported_addr" \
  --arg project_number "$project_number" \
  --arg service_account "$runtime_gsa" \
  '{
    vault_addr: $vault_addr,
    vault_audience: $vault_audience,
    project_number: $project_number,
    service_account: $service_account
  }'
