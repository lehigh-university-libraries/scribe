#!/usr/bin/env bash

set -euo pipefail

readonly EXPECTED_REPOSITORY="lehigh-university-libraries/scribe"

fail() {
  local category="${1:-internal}"
  local diagnostic=""

  case "$category" in
    identity-selection) diagnostic="select a reviewed environment and identity class" ;;
    usage) diagnostic="invalid command usage" ;;
    provider-required) diagnostic="provider configuration is required" ;;
    service-account-required) diagnostic="service-account configuration is required" ;;
    provider-resource) diagnostic="provider resource format is invalid" ;;
    service-account-resource) diagnostic="service-account resource format is invalid" ;;
    gcloud-required) diagnostic="gcloud is required" ;;
    jq-required) diagnostic="jq is required" ;;
    provider-inspection) diagnostic="configured provider inspection failed" ;;
    pool-inspection) diagnostic="provider-pool inspection failed" ;;
    service-account-policy-inspection) diagnostic="service-account policy inspection failed" ;;
    provider-contract) diagnostic="configured provider does not match the reviewed lifecycle, issuer, audience, and mapping contract" ;;
    attribute-condition-missing) diagnostic="configured provider has no readable attribute condition" ;;
    attribute-condition-contract) diagnostic="configured provider attribute condition does not match the reviewed trust boundary" ;;
    pool-contract) diagnostic="provider pool does not match the reviewed exclusivity contract" ;;
    service-account-policy-contract) diagnostic="service-account policy does not match the reviewed binding contract" ;;
    *)
      category="internal"
      diagnostic="verification could not continue safely"
      ;;
  esac

  printf 'GCP WIF preflight failed [%s]: %s\n' "$category" "$diagnostic" >&2
  exit 1
}

environment_name="${WIF_EXPECTED_ENVIRONMENT:-}"
identity_class="${WIF_IDENTITY_CLASS:-}"

workflow_ref() {
  printf '%s/.github/workflows/%s@refs/heads/main' "$EXPECTED_REPOSITORY" "$1"
}

expected_condition() {
  local repository="assertion.repository == '${EXPECTED_REPOSITORY}'"
  local environment="assertion.environment == '${environment_name}'"
  local main_ref="assertion.ref == 'refs/heads/main'"
  local workflow_file=""
  local workflow_path=""
  local workflows=""

  case "${environment_name}:${identity_class}" in
    production:deploy)
      workflows="assertion.workflow_ref == '$(workflow_ref terraform-apply.yaml)' || assertion.workflow_ref == '$(workflow_ref terraform-drift.yaml)'"
      printf '%s && %s && %s && (%s)\n' "$repository" "$main_ref" "$environment" "$workflows"
      ;;
    production:ocr)
      workflows="assertion.workflow_ref == '$(workflow_ref terraform-apply.yaml)'"
      printf '%s && %s && %s && %s\n' "$repository" "$main_ref" "$environment" "$workflows"
      ;;
    production:backup)
      workflows="assertion.workflow_ref == '$(workflow_ref backup-verification.yaml)'"
      printf '%s && %s && %s && %s\n' "$repository" "$main_ref" "$environment" "$workflows"
      ;;
    preview:deploy|preview:ocr)
      workflow_file="terraform-preview.yaml"
      workflow_path="$(workflow_ref "$workflow_file")"
      workflows="assertion.workflow_ref == '${workflow_path}'"
      # pull_request_target and manual recovery both execute the reviewed main
      # orchestration file. PR head code never receives this OIDC token.
      printf '%s && %s && %s && %s\n' "$repository" "$main_ref" "$environment" "$workflows"
      ;;
    *) fail identity-selection ;;
  esac
}

if [ "${1:-}" = "--print-expected-condition" ]; then
  expected_condition
  exit 0
fi
[ "$#" -eq 0 ] || fail usage

[ -n "${WIF_PROVIDER:-}" ] || fail provider-required
[ -n "${WIF_SERVICE_ACCOUNT:-}" ] || fail service-account-required
[[ "$WIF_PROVIDER" =~ ^projects/([0-9]+)/locations/(global)/workloadIdentityPools/([a-z][a-z0-9-]{2,30}[a-z0-9])/providers/([a-z][a-z0-9-]{2,30}[a-z0-9])$ ]] ||
  fail provider-resource
provider_project_number="${BASH_REMATCH[1]}"
provider_location="${BASH_REMATCH[2]}"
pool_id="${BASH_REMATCH[3]}"
provider_id="${BASH_REMATCH[4]}"
[[ "$WIF_SERVICE_ACCOUNT" =~ ^[a-z][a-z0-9-]{4,28}[a-z0-9]@([a-z][a-z0-9-]{4,28}[a-z0-9])\.iam\.gserviceaccount\.com$ ]] ||
  fail service-account-resource
service_account_project="${BASH_REMATCH[1]}"

command -v gcloud >/dev/null 2>&1 || fail gcloud-required
command -v jq >/dev/null 2>&1 || fail jq-required

# WIF resource names use the immutable project number, but gcloud requires a
# project ID for these provider commands. Query through the validated target
# service account project, then verify the returned numeric resource name.
provider_json="$(gcloud iam workload-identity-pools providers describe "$provider_id" \
  --workload-identity-pool="$pool_id" \
  --location="$provider_location" \
  --project="$service_account_project" \
  --format=json 2>/dev/null)" || fail provider-inspection
providers_json="$(gcloud iam workload-identity-pools providers list \
  --workload-identity-pool="$pool_id" \
  --location="$provider_location" \
  --project="$service_account_project" \
  --format=json 2>/dev/null)" || fail pool-inspection
service_account_policy="$(gcloud iam service-accounts get-iam-policy "$WIF_SERVICE_ACCOUNT" \
  --project="$service_account_project" \
  --format=json 2>/dev/null)" || fail service-account-policy-inspection

expected_provider_name="projects/${provider_project_number}/locations/${provider_location}/workloadIdentityPools/${pool_id}/providers/${provider_id}"
jq -e --arg name "$expected_provider_name" '
  .name == $name and
  .state == "ACTIVE" and
  (.disabled // false) == false and
  .oidc.issuerUri == "https://token.actions.githubusercontent.com" and
  (.oidc.allowedAudiences // []) == [] and
  .attributeMapping == {
    "google.subject": "assertion.sub",
    "attribute.environment": "assertion.environment",
    "attribute.ref": "assertion.ref",
    "attribute.repository": "assertion.repository",
    "attribute.workflow_ref": "assertion.workflow_ref"
  }
' <<<"$provider_json" >/dev/null 2>&1 || fail provider-contract

actual_condition="$(jq -er '.attributeCondition | select(type == "string")' <<<"$provider_json" 2>/dev/null)" ||
  fail attribute-condition-missing
reviewed_condition="$(expected_condition)"
normalize_condition() {
  tr -d '[:space:]'
}
if [ "$(normalize_condition <<<"$actual_condition")" != "$(normalize_condition <<<"$reviewed_condition")" ]; then
  fail attribute-condition-contract
fi

jq -e --arg name "$expected_provider_name" '
  [.[] | select((.state // "ACTIVE") == "ACTIVE" and (.disabled // false) == false) | .name] == [$name]
' <<<"$providers_json" >/dev/null 2>&1 || fail pool-contract

expected_member="principalSet://iam.googleapis.com/projects/${provider_project_number}/locations/${provider_location}/workloadIdentityPools/${pool_id}/attribute.repository/${EXPECTED_REPOSITORY}"
jq -e --arg member "$expected_member" '
  ([.bindings[]? | {
    role: .role,
    members: ((.members // []) | sort),
    condition: (.condition // null)
  }] | sort_by(.role)) == [{
    role: "roles/iam.workloadIdentityUser",
    members: [$member],
    condition: null
  }]
' <<<"$service_account_policy" >/dev/null 2>&1 || fail service-account-policy-contract

echo "Verified dedicated ${environment_name}/${identity_class} WIF repository, workflow, ref, environment, and service-account binding."
