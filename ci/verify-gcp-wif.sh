#!/usr/bin/env bash

set -euo pipefail

readonly EXPECTED_REPOSITORY="lehigh-university-libraries/scribe"

fail() {
  echo "GCP WIF preflight failed: $*" >&2
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
    *) fail "WIF_EXPECTED_ENVIRONMENT and WIF_IDENTITY_CLASS must select production deploy/ocr/backup or preview deploy/ocr" ;;
  esac
}

if [ "${1:-}" = "--print-expected-condition" ]; then
  expected_condition
  exit 0
fi
[ "$#" -eq 0 ] || fail "usage: verify-gcp-wif.sh [--print-expected-condition]"

: "${WIF_PROVIDER:?WIF_PROVIDER is required}"
: "${WIF_SERVICE_ACCOUNT:?WIF_SERVICE_ACCOUNT is required}"
[[ "$WIF_PROVIDER" =~ ^projects/([0-9]+)/locations/(global)/workloadIdentityPools/([a-z][a-z0-9-]{2,30}[a-z0-9])/providers/([a-z][a-z0-9-]{2,30}[a-z0-9])$ ]] ||
  fail "WIF_PROVIDER must be a full numeric-project global provider resource"
provider_project_number="${BASH_REMATCH[1]}"
provider_location="${BASH_REMATCH[2]}"
pool_id="${BASH_REMATCH[3]}"
provider_id="${BASH_REMATCH[4]}"
[[ "$WIF_SERVICE_ACCOUNT" =~ ^[a-z][a-z0-9-]{4,28}[a-z0-9]@([a-z][a-z0-9-]{4,28}[a-z0-9])\.iam\.gserviceaccount\.com$ ]] ||
  fail "WIF_SERVICE_ACCOUNT must be a project-local Google service-account email"
service_account_project="${BASH_REMATCH[1]}"

command -v gcloud >/dev/null 2>&1 || fail "gcloud is required"
command -v jq >/dev/null 2>&1 || fail "jq is required"

provider_json="$(gcloud iam workload-identity-pools providers describe "$provider_id" \
  --workload-identity-pool="$pool_id" \
  --location="$provider_location" \
  --project="$provider_project_number" \
  --format=json)" || fail "cannot inspect the configured provider; grant the protected identity workloadIdentityPoolProviders.get"
providers_json="$(gcloud iam workload-identity-pools providers list \
  --workload-identity-pool="$pool_id" \
  --location="$provider_location" \
  --project="$provider_project_number" \
  --format=json)" || fail "cannot inspect the provider pool; grant the protected identity workloadIdentityPoolProviders.list"
service_account_policy="$(gcloud iam service-accounts get-iam-policy "$WIF_SERVICE_ACCOUNT" \
  --project="$service_account_project" \
  --format=json)" || fail "cannot inspect the target service-account policy; grant iam.serviceAccounts.getIamPolicy"

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
' <<<"$provider_json" >/dev/null ||
  fail "provider must be active, use GitHub's issuer/default audience, and expose only the reviewed subject, repository, workflow_ref, ref, and environment mappings"

actual_condition="$(jq -er '.attributeCondition' <<<"$provider_json")" || fail "provider attributeCondition is missing"
reviewed_condition="$(expected_condition)"
normalize_condition() {
  tr -d '[:space:]'
}
if [ "$(normalize_condition <<<"$actual_condition")" != "$(normalize_condition <<<"$reviewed_condition")" ]; then
  fail "provider attributeCondition is broader than the reviewed ${environment_name}/${identity_class} repository-workflow-ref-environment condition"
fi

jq -e --arg name "$expected_provider_name" '
  [.[] | select((.state // "ACTIVE") == "ACTIVE" and (.disabled // false) == false) | .name] == [$name]
' <<<"$providers_json" >/dev/null ||
  fail "the workload identity pool must contain exactly this one active provider"

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
' <<<"$service_account_policy" >/dev/null ||
  fail "target service account must have exactly one unconditional workloadIdentityUser binding for this pool's repository-scoped principal set"

echo "Verified dedicated ${environment_name}/${identity_class} WIF repository, workflow, ref, environment, and service-account binding."
