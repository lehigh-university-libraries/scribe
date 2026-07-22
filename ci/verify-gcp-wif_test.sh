#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEST_DIR="$(mktemp -d)"
trap 'rm -rf "$TEST_DIR"' EXIT
mkdir -p "$TEST_DIR/bin"

readonly PROVIDER="projects/123456789012/locations/global/workloadIdentityPools/deploy-pool/providers/github-main"
readonly PROVIDER_NAME="$PROVIDER"
readonly SERVICE_ACCOUNT="scribe-deploy@example-project.iam.gserviceaccount.com"
readonly EXPECTED_MEMBER="principalSet://iam.googleapis.com/projects/123456789012/locations/global/workloadIdentityPools/deploy-pool/attribute.repository/lehigh-university-libraries/scribe"

cat >"$TEST_DIR/bin/gcloud" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
case "$*" in
  "iam workload-identity-pools providers describe "*) cat "$FAKE_PROVIDER_JSON" ;;
  "iam workload-identity-pools providers list "*) cat "$FAKE_PROVIDERS_JSON" ;;
  "iam service-accounts get-iam-policy "*) cat "$FAKE_SERVICE_ACCOUNT_POLICY" ;;
  *) echo "unexpected fake gcloud invocation: $*" >&2; exit 2 ;;
esac
EOF
chmod +x "$TEST_DIR/bin/gcloud"

expected_condition() {
  WIF_EXPECTED_ENVIRONMENT="$1" WIF_IDENTITY_CLASS="$2" \
    "$ROOT_DIR/ci/verify-gcp-wif.sh" --print-expected-condition
}

write_provider() {
  local condition="$1"
  jq -n \
    --arg name "$PROVIDER_NAME" \
    --arg condition "$condition" '
      {
        name: $name,
        state: "ACTIVE",
        disabled: false,
        oidc: {
          issuerUri: "https://token.actions.githubusercontent.com",
          allowedAudiences: []
        },
        attributeMapping: {
          "google.subject": "assertion.sub",
          "attribute.environment": "assertion.environment",
          "attribute.ref": "assertion.ref",
          "attribute.repository": "assertion.repository",
          "attribute.workflow_ref": "assertion.workflow_ref"
        },
        attributeCondition: $condition
      }
    ' >"$TEST_DIR/provider.json"
  jq -s '.' "$TEST_DIR/provider.json" >"$TEST_DIR/providers.json"
}

write_policy() {
  jq -n --arg member "$EXPECTED_MEMBER" '
    {bindings: [{role: "roles/iam.workloadIdentityUser", members: [$member]}]}
  ' >"$TEST_DIR/policy.json"
}

write_valid_fixture() {
  write_provider "$(expected_condition "$1" "$2")"
  write_policy
}

run_preflight() {
  PATH="$TEST_DIR/bin:$PATH" \
    FAKE_PROVIDER_JSON="$TEST_DIR/provider.json" \
    FAKE_PROVIDERS_JSON="$TEST_DIR/providers.json" \
    FAKE_SERVICE_ACCOUNT_POLICY="$TEST_DIR/policy.json" \
    WIF_EXPECTED_ENVIRONMENT="$1" \
    WIF_IDENTITY_CLASS="$2" \
    WIF_PROVIDER="$PROVIDER" \
    WIF_SERVICE_ACCOUNT="$SERVICE_ACCOUNT" \
    "$ROOT_DIR/ci/verify-gcp-wif.sh"
}

expect_failure() {
  local environment="$1" identity="$2" expected="$3"
  if run_preflight "$environment" "$identity" >"$TEST_DIR/failure.out" 2>"$TEST_DIR/failure.err"; then
    echo "WIF preflight accepted an invalid ${environment}/${identity} fixture" >&2
    exit 1
  fi
  grep -F "$expected" "$TEST_DIR/failure.err" >/dev/null
}

write_valid_fixture production deploy
run_preflight production deploy >/dev/null

canonical="$(expected_condition production deploy)"
old="assertion.repository == 'lehigh-university-libraries/scribe'"
new="assertion.repository.startsWith('lehigh-university-libraries/')"
write_provider "${canonical/"$old"/"$new"}"
expect_failure production deploy "repository-workflow-ref-environment condition"

old="terraform-drift.yaml"
new="unreviewed-workflow.yaml"
write_provider "${canonical/"$old"/"$new"}"
expect_failure production deploy "repository-workflow-ref-environment condition"

old="assertion.ref == 'refs/heads/main'"
new="assertion.ref.startsWith('refs/heads/')"
write_provider "${canonical/"$old"/"$new"}"
expect_failure production deploy "repository-workflow-ref-environment condition"

old="assertion.environment == 'production'"
new="assertion.environment in ['production', 'preview']"
write_provider "${canonical/"$old"/"$new"}"
expect_failure production deploy "repository-workflow-ref-environment condition"

write_valid_fixture production deploy
jq '.oidc.issuerUri = "https://untrusted.example"' "$TEST_DIR/provider.json" >"$TEST_DIR/provider.tmp"
mv "$TEST_DIR/provider.tmp" "$TEST_DIR/provider.json"
expect_failure production deploy "GitHub's issuer/default audience"

write_valid_fixture production deploy
jq '.oidc.allowedAudiences = ["https://example.invalid/broad-audience"]' \
  "$TEST_DIR/provider.json" >"$TEST_DIR/provider.tmp"
mv "$TEST_DIR/provider.tmp" "$TEST_DIR/provider.json"
expect_failure production deploy "GitHub's issuer/default audience"

write_valid_fixture production deploy
jq '.attributeMapping["attribute.actor"] = "assertion.actor"' "$TEST_DIR/provider.json" >"$TEST_DIR/provider.tmp"
mv "$TEST_DIR/provider.tmp" "$TEST_DIR/provider.json"
expect_failure production deploy "expose only the reviewed"

write_valid_fixture production deploy
jq '. + [{name: "projects/123456789012/locations/global/workloadIdentityPools/deploy-pool/providers/second-active", state: "ACTIVE"}]' \
  "$TEST_DIR/providers.json" >"$TEST_DIR/providers.tmp"
mv "$TEST_DIR/providers.tmp" "$TEST_DIR/providers.json"
expect_failure production deploy "exactly this one active provider"

write_valid_fixture production deploy
jq --arg extra "${EXPECTED_MEMBER%/scribe}/other" \
  '.bindings[0].members += [$extra]' "$TEST_DIR/policy.json" >"$TEST_DIR/policy.tmp"
mv "$TEST_DIR/policy.tmp" "$TEST_DIR/policy.json"
expect_failure production deploy "exactly one unconditional workloadIdentityUser binding"

write_valid_fixture production deploy
jq --arg extra "serviceAccount:untrusted@example-project.iam.gserviceaccount.com" '
  .bindings += [{role: "roles/iam.serviceAccountTokenCreator", members: [$extra]}]
' "$TEST_DIR/policy.json" >"$TEST_DIR/policy.tmp"
mv "$TEST_DIR/policy.tmp" "$TEST_DIR/policy.json"
expect_failure production deploy "exactly one unconditional workloadIdentityUser binding"

write_valid_fixture production deploy
jq '.bindings[0].condition = {title: "unexpected", expression: "true"}' \
  "$TEST_DIR/policy.json" >"$TEST_DIR/policy.tmp"
mv "$TEST_DIR/policy.tmp" "$TEST_DIR/policy.json"
expect_failure production deploy "exactly one unconditional workloadIdentityUser binding"

write_valid_fixture preview deploy
run_preflight preview deploy >/dev/null
preview_condition="$(expected_condition preview deploy)"
grep -F "terraform-preview.yaml@refs/heads/main" <<<"$preview_condition" >/dev/null
grep -F "assertion.ref == 'refs/heads/main'" <<<"$preview_condition" >/dev/null

grep -F "terraform-apply.yaml@refs/heads/main" <<<"$(expected_condition production ocr)" >/dev/null
grep -F "backup-verification.yaml@refs/heads/main" <<<"$(expected_condition production backup)" >/dev/null

echo "GCP WIF preflight accepts only the reviewed repository, workflow, ref, environment, provider, and service-account bindings."
