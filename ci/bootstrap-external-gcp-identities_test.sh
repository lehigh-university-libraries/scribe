#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEST_DIR="$(mktemp -d)"
trap 'rm -rf "$TEST_DIR"' EXIT
mkdir -p "$TEST_DIR/bin" "$TEST_DIR/state/service-account-policies" "$TEST_DIR/state/providers" "$TEST_DIR/state/artifact-policies"

readonly PROJECT="scribe-test1"
readonly PROJECT_NUMBER="123456789012"
readonly SOURCE_GSA="scribe-deploy@${PROJECT}.iam.gserviceaccount.com"
readonly SOURCE_POOL="scribe-production-deploy-wif"
readonly SOURCE_PROVIDER="projects/${PROJECT_NUMBER}/locations/global/workloadIdentityPools/${SOURCE_POOL}/providers/github-main"
readonly SOURCE_MEMBER="serviceAccount:${SOURCE_GSA}"
readonly PREVIEW_MEMBER="serviceAccount:scribe-preview-deploy@${PROJECT}.iam.gserviceaccount.com"
readonly REPOSITORY_PRINCIPAL="principalSet://iam.googleapis.com/projects/${PROJECT_NUMBER}/locations/global/workloadIdentityPools/${SOURCE_POOL}/attribute.repository/lehigh-university-libraries/scribe"

rg -Uq '(?s)^artifact_registry:\n.*?ocr_publish_repositories:\n[[:space:]]+- location: us\n[[:space:]]+repository: internal' \
  "$ROOT_DIR/config/ocr.yaml" || {
  echo "config/ocr.yaml does not enumerate the reviewed OCR publisher repository" >&2
  exit 1
}

cat >"$TEST_DIR/state/project.json" <<EOF
{"projectId":"${PROJECT}","projectNumber":"${PROJECT_NUMBER}"}
EOF
jq -n --arg email "$SOURCE_GSA" '[{email: $email, disabled: false}]' >"$TEST_DIR/state/service-accounts.json"
jq -n --arg name "projects/${PROJECT_NUMBER}/locations/global/workloadIdentityPools/${SOURCE_POOL}" \
  '[{name: $name, state: "ACTIVE"}]' >"$TEST_DIR/state/pools.json"
jq -n --arg source "$SOURCE_MEMBER" '{
  version: 3,
  etag: "project-etag",
  bindings: [
    {role: "roles/editor", members: [$source]},
    {role: "roles/iam.serviceAccountViewer", members: [$source]},
    {role: "roles/iam.workloadIdentityPoolViewer", members: [$source]},
    {
      role: "roles/viewer",
      members: ["user:auditor@example.edu"],
      condition: {title: "audited-window", description: "Retained verbatim", expression: "request.time < timestamp(\"2030-01-01T00:00:00Z\")"}
    }
  ]
}' >"$TEST_DIR/state/project-policy.json"
jq -n --arg member "$REPOSITORY_PRINCIPAL" '{
  version: 1,
  etag: "source-account-etag",
  bindings: [{role: "roles/iam.workloadIdentityUser", members: [$member]}]
}' >"$TEST_DIR/state/service-account-policies/${SOURCE_GSA}.json"

source_condition="$(WIF_EXPECTED_ENVIRONMENT=production WIF_IDENTITY_CLASS=deploy \
  "$ROOT_DIR/ci/verify-gcp-wif.sh" --print-expected-condition)"
jq -n \
  --arg name "$SOURCE_PROVIDER" \
  --arg condition "$source_condition" '[{
    name: $name,
    state: "ACTIVE",
    disabled: false,
    oidc: {issuerUri: "https://token.actions.githubusercontent.com", allowedAudiences: []},
    attributeMapping: {
      "google.subject": "assertion.sub",
      "attribute.environment": "assertion.environment",
      "attribute.ref": "assertion.ref",
      "attribute.repository": "assertion.repository",
      "attribute.workflow_ref": "assertion.workflow_ref"
    },
    attributeCondition: $condition
  }]' >"$TEST_DIR/state/providers/${SOURCE_POOL}.json"

cat >"$TEST_DIR/state/bucket.json" <<EOF
{"name":"${PROJECT}-terraform","projectNumber":"${PROJECT_NUMBER}","versioning":{"enabled":false},"softDeletePolicy":{"retentionDurationSeconds":"604800"}}
EOF
jq -n --arg source "$SOURCE_MEMBER" '{
  version: 3,
  etag: "bucket-etag",
  bindings: [
    {role: "roles/storage.objectAdmin", members: [$source]},
    {
      role: "roles/storage.objectViewer",
      members: ["user:auditor@example.edu"],
      condition: {title: "audited-prefix", description: "Retained verbatim", expression: "resource.name.startsWith(\"projects/_/buckets/scribe-test1-terraform/objects/audit/\")"}
    }
  ]
}' >"$TEST_DIR/state/bucket-policy.json"
printf '[]\n' >"$TEST_DIR/state/channels.json"
jq -n --arg project "$PROJECT" '[{
  name: ("projects/" + $project + "/locations/us/repositories/internal"),
  format: "DOCKER"
}]' >"$TEST_DIR/state/artifact-repositories.json"
printf '{"version":1,"etag":"artifact-etag","bindings":[]}\n' >"$TEST_DIR/state/artifact-policies/us--internal.json"
: >"$TEST_DIR/gcloud.log"

# The real yq invocation has four arguments. Keep the parser strict while
# emitting the reviewed fixture repository as JSON.
cat >"$TEST_DIR/bin/yq" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[ "$#" -eq 4 ]
[ "$1" = "-o=json" ]
[ "$2" = "-I=0" ]
[ "$3" = '.artifact_registry.ocr_publish_repositories // []' ]
[ "$4" = "$FAKE_OCR_CONFIG" ]
printf '[{"location":"us","repository":"internal"}]\n'
EOF
chmod +x "$TEST_DIR/bin/yq"

cat >"$TEST_DIR/bin/gcloud" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

printf '%s\n' "$*" >>"$FAKE_GCLOUD_LOG"

flag_value() {
  local wanted="$1" arg
  shift
  for arg in "$@"; do
    case "$arg" in
      "${wanted}="*) printf '%s\n' "${arg#*=}"; return 0 ;;
    esac
  done
  return 1
}

has_flag() {
  local wanted="$1" arg
  shift
  for arg in "$@"; do
    [ "$arg" != "$wanted" ] || return 0
  done
  return 1
}

account_policy_file() {
  printf '%s/service-account-policies/%s.json\n' "$FAKE_GCLOUD_STATE" "$1"
}

case "${1:-} ${2:-} ${3:-} ${4:-}" in
  "auth list --filter=status:ACTIVE --format=value(account)")
    printf '%s\n' "$FAKE_ACTIVE_ACCOUNT"
    ;;
  "projects describe ${FAKE_PROJECT} --format=json")
    cat "$FAKE_GCLOUD_STATE/project.json"
    ;;
  "projects get-iam-policy ${FAKE_PROJECT}"*)
    cat "$FAKE_GCLOUD_STATE/project-policy.json"
    ;;
  "projects set-iam-policy ${FAKE_PROJECT}"*)
    cp "$4" "$FAKE_GCLOUD_STATE/project-policy.json"
    cat "$FAKE_GCLOUD_STATE/project-policy.json"
    ;;
  "iam service-accounts list"*)
    cat "$FAKE_GCLOUD_STATE/service-accounts.json"
    ;;
  "iam service-accounts create"*)
    account_id="$4"
    email="${account_id}@${FAKE_PROJECT}.iam.gserviceaccount.com"
    jq --arg email "$email" '. + [{email: $email, disabled: false}]' \
      "$FAKE_GCLOUD_STATE/service-accounts.json" >"$FAKE_GCLOUD_STATE/service-accounts.tmp"
    mv "$FAKE_GCLOUD_STATE/service-accounts.tmp" "$FAKE_GCLOUD_STATE/service-accounts.json"
    ;;
  "iam service-accounts get-iam-policy"*)
    email="$4"
    policy_file="$(account_policy_file "$email")"
    if [ -f "$policy_file" ]; then
      cat "$policy_file"
    else
      printf '{"version":1,"etag":"new-account-etag","bindings":[]}\n'
    fi
    ;;
  "iam service-accounts set-iam-policy"*)
    email="$4"
    cp "$5" "$(account_policy_file "$email")"
    cat "$(account_policy_file "$email")"
    ;;
  "iam workload-identity-pools list"*)
    cat "$FAKE_GCLOUD_STATE/pools.json"
    ;;
  "iam workload-identity-pools create"*)
    pool_id="$4"
    display_name="$(flag_value --display-name "$@")"
    [ "${#display_name}" -ge 1 ] && [ "${#display_name}" -le 32 ] || {
      echo "workload identity pool display name exceeds the GCP limit" >&2
      exit 2
    }
    name="projects/${FAKE_PROJECT_NUMBER}/locations/global/workloadIdentityPools/${pool_id}"
    jq --arg name "$name" '. + [{name: $name, state: "ACTIVE"}]' \
      "$FAKE_GCLOUD_STATE/pools.json" >"$FAKE_GCLOUD_STATE/pools.tmp"
    mv "$FAKE_GCLOUD_STATE/pools.tmp" "$FAKE_GCLOUD_STATE/pools.json"
    printf '[]\n' >"$FAKE_GCLOUD_STATE/providers/${pool_id}.json"
    ;;
  "iam workload-identity-pools providers list")
    pool_id="$(flag_value --workload-identity-pool "$@")"
    cat "$FAKE_GCLOUD_STATE/providers/${pool_id}.json"
    ;;
  "iam workload-identity-pools providers describe")
    provider_id="$5"
    pool_id="$(flag_value --workload-identity-pool "$@")"
    jq -e --arg suffix "/providers/${provider_id}" '.[] | select(.name | endswith($suffix))' \
      "$FAKE_GCLOUD_STATE/providers/${pool_id}.json"
    ;;
  "iam workload-identity-pools providers create-oidc")
    provider_id="$5"
    pool_id="$(flag_value --workload-identity-pool "$@")"
    condition="$(flag_value --attribute-condition "$@")"
    issuer="$(flag_value --issuer-uri "$@")"
    mapping="$(flag_value --attribute-mapping "$@")"
    [ "$issuer" = "https://token.actions.githubusercontent.com" ]
    [ "$mapping" = "google.subject=assertion.sub,attribute.repository=assertion.repository,attribute.workflow_ref=assertion.workflow_ref,attribute.ref=assertion.ref,attribute.environment=assertion.environment" ]
    case "$pool_id" in
      scribe-production-ocr-wif) environment=production; identity=ocr ;;
      scribe-production-backup-wif) environment=production; identity=backup ;;
      scribe-preview-deploy-wif) environment=preview; identity=deploy ;;
      scribe-preview-ocr-wif) environment=preview; identity=ocr ;;
      *) echo "unexpected provider pool: $pool_id" >&2; exit 2 ;;
    esac
    expected_condition="$(WIF_EXPECTED_ENVIRONMENT="$environment" WIF_IDENTITY_CLASS="$identity" \
      "$FAKE_ROOT_DIR/ci/verify-gcp-wif.sh" --print-expected-condition)"
    [ "$condition" = "$expected_condition" ]
    name="projects/${FAKE_PROJECT_NUMBER}/locations/global/workloadIdentityPools/${pool_id}/providers/${provider_id}"
    jq -n --arg name "$name" --arg condition "$condition" '{
      name: $name,
      state: "ACTIVE",
      disabled: false,
      oidc: {issuerUri: "https://token.actions.githubusercontent.com", allowedAudiences: []},
      attributeMapping: {
        "google.subject": "assertion.sub",
        "attribute.environment": "assertion.environment",
        "attribute.ref": "assertion.ref",
        "attribute.repository": "assertion.repository",
        "attribute.workflow_ref": "assertion.workflow_ref"
      },
      attributeCondition: $condition
    }' >"$FAKE_GCLOUD_STATE/provider.tmp"
    jq -s '.[0] + [.[1]]' "$FAKE_GCLOUD_STATE/providers/${pool_id}.json" "$FAKE_GCLOUD_STATE/provider.tmp" \
      >"$FAKE_GCLOUD_STATE/providers.tmp"
    mv "$FAKE_GCLOUD_STATE/providers.tmp" "$FAKE_GCLOUD_STATE/providers/${pool_id}.json"
    rm "$FAKE_GCLOUD_STATE/provider.tmp"
    ;;
  "storage buckets describe"*)
    cat "$FAKE_GCLOUD_STATE/bucket.json"
    ;;
  "storage buckets get-iam-policy"*)
    cat "$FAKE_GCLOUD_STATE/bucket-policy.json"
    ;;
  "storage buckets set-iam-policy"*)
    incoming_etag="$(jq -er '.etag' "$5")"
    current_etag="$(jq -er '.etag' "$FAKE_GCLOUD_STATE/bucket-policy.json")"
    [ "$incoming_etag" = "$current_etag" ] || {
      echo "stale fake bucket IAM policy etag" >&2
      exit 1
    }
    jq '.etag = "bucket-etag-after-policy"' "$5" >"$FAKE_GCLOUD_STATE/bucket-policy.tmp"
    mv "$FAKE_GCLOUD_STATE/bucket-policy.tmp" "$FAKE_GCLOUD_STATE/bucket-policy.json"
    cat "$FAKE_GCLOUD_STATE/bucket-policy.json"
    ;;
  "storage buckets update"*)
    bucket="$FAKE_GCLOUD_STATE/bucket.json"
    filter='.'
    case " $* " in *" --versioning "*) filter="${filter} | .versioning.enabled = true" ;; esac
    case " $* " in *" --soft-delete-duration=14d "*) filter="${filter} | .softDeletePolicy.retentionDurationSeconds = \"1209600\"" ;; esac
    jq "$filter" "$bucket" >"$FAKE_GCLOUD_STATE/bucket.tmp"
    mv "$FAKE_GCLOUD_STATE/bucket.tmp" "$bucket"
    jq '.etag = "bucket-etag-after-metadata"' "$FAKE_GCLOUD_STATE/bucket-policy.json" \
      >"$FAKE_GCLOUD_STATE/bucket-policy.tmp"
    mv "$FAKE_GCLOUD_STATE/bucket-policy.tmp" "$FAKE_GCLOUD_STATE/bucket-policy.json"
    cat "$bucket"
    ;;
  "artifacts repositories list"*)
    cat "$FAKE_GCLOUD_STATE/artifact-repositories.json"
    ;;
  "artifacts repositories get-iam-policy"*)
    repository="$4"
    location="$(flag_value --location "$@")"
    cat "$FAKE_GCLOUD_STATE/artifact-policies/${location}--${repository}.json"
    ;;
  "artifacts repositories set-iam-policy"*)
    repository="$4"
    policy_file="$5"
    location="$(flag_value --location "$@")"
    cp "$policy_file" "$FAKE_GCLOUD_STATE/artifact-policies/${location}--${repository}.json"
    cat "$FAKE_GCLOUD_STATE/artifact-policies/${location}--${repository}.json"
    ;;
  "beta monitoring channels list"*)
    cat "$FAKE_GCLOUD_STATE/channels.json"
    ;;
  "beta monitoring channels create"*)
    channel_type="$(flag_value --type "$@")"
    channel_labels="$(flag_value --channel-labels "$@")"
    has_flag --enabled "$@"
    [ "$channel_type" = "email" ]
    [[ "$channel_labels" =~ ^email_address=([A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,63})$ ]]
    channel_email="${BASH_REMATCH[1]}"
    [ "$channel_email" = "$FAKE_NOTIFICATION_EMAIL" ]
    jq -n --arg email "$channel_email" '{
      name: "projects/123456789012/notificationChannels/42",
      type: "email",
      enabled: true,
      labels: {email_address: $email},
      displayName: "Scribe production alerts"
    }' >"$FAKE_GCLOUD_STATE/channel.tmp"
    jq -s '.[0] + [.[1]]' "$FAKE_GCLOUD_STATE/channels.json" "$FAKE_GCLOUD_STATE/channel.tmp" \
      >"$FAKE_GCLOUD_STATE/channels.tmp"
    mv "$FAKE_GCLOUD_STATE/channels.tmp" "$FAKE_GCLOUD_STATE/channels.json"
    cat "$FAKE_GCLOUD_STATE/channel.tmp"
    rm "$FAKE_GCLOUD_STATE/channel.tmp"
    ;;
  "beta monitoring channels update"*)
    name="$5"
    jq --arg name "$name" 'map(if .name == $name then .enabled = true else . end)' \
      "$FAKE_GCLOUD_STATE/channels.json" >"$FAKE_GCLOUD_STATE/channels.tmp"
    mv "$FAKE_GCLOUD_STATE/channels.tmp" "$FAKE_GCLOUD_STATE/channels.json"
    jq -e --arg name "$name" '.[] | select(.name == $name)' "$FAKE_GCLOUD_STATE/channels.json"
    ;;
  *)
    echo "unexpected fake gcloud invocation: $*" >&2
    exit 2
    ;;
esac
EOF
chmod +x "$TEST_DIR/bin/gcloud"

run_bootstrap() {
  PATH="$TEST_DIR/bin:$PATH" \
    FAKE_ACTIVE_ACCOUNT="$SOURCE_GSA" \
    FAKE_GCLOUD_LOG="$TEST_DIR/gcloud.log" \
    FAKE_GCLOUD_STATE="$TEST_DIR/state" \
    FAKE_ROOT_DIR="$ROOT_DIR" \
    FAKE_OCR_CONFIG="$ROOT_DIR/config/ocr.yaml" \
    FAKE_NOTIFICATION_EMAIL="jjc223@lehigh.edu" \
    FAKE_PROJECT="$PROJECT" \
    FAKE_PROJECT_NUMBER="$PROJECT_NUMBER" \
    GCLOUD_PROJECT="$PROJECT" \
    SOURCE_DEPLOY_GSA="$SOURCE_GSA" \
    SOURCE_DEPLOY_PROVIDER="$SOURCE_PROVIDER" \
    MONITORING_NOTIFICATION_EMAIL="jjc223@lehigh.edu" \
    TF_STATE_BUCKET="${PROJECT}-terraform" \
    "$ROOT_DIR/ci/bootstrap-external-gcp-identities.sh" "$1"
}

assert_no_mutation() {
  if grep -Eq '(service-accounts create|set-iam-policy|workload-identity-pools create|providers create-oidc|buckets update|channels (create|update))' "$TEST_DIR/gcloud.log"; then
    echo "plan mode invoked a mutating gcloud command" >&2
    exit 1
  fi
}

plan_json="$(run_bootstrap plan)"
jq -e --arg provider "$SOURCE_PROVIDER" --arg gsa "$SOURCE_GSA" '
  .mode == "plan" and
  .source_deploy == {provider: $provider, service_account: $gsa} and
  .identities.production_ocr.provider == "projects/123456789012/locations/global/workloadIdentityPools/scribe-production-ocr-wif/providers/github-main" and
  .identities.production_backup.service_account == "scribe-prod-backup@scribe-test1.iam.gserviceaccount.com" and
  .actions.identities.production_ocr == {service_account: "create", service_account_policy: "bind", pool: "create", provider: "create"} and
  .actions.state_bucket.versioning == "enable" and
  .actions.state_bucket.soft_delete == "set_14d" and
  .actions.notification_channel == "create" and
  (.source_deploy_observed_roles.project_roles | index("roles/editor")) != null and
  (.preview_deploy_explicit_grants.project_roles | index("roles/editor")) == null and
  (.preview_deploy_explicit_grants.project_roles | index("roles/resourcemanager.projectIamAdmin")) != null and
  (.preview_deploy_explicit_grants.project_roles | index("roles/iam.serviceAccountKeyAdmin")) == null and
  .source_deploy_observed_roles.state_bucket_bindings == [{role: "roles/storage.objectAdmin", condition: null}] and
  .artifact_registry.repositories[0].action == "update"
' <<<"$plan_json" >/dev/null
assert_no_mutation
grep -Fq "storage buckets describe gs://${PROJECT}-terraform --raw --format=json" "$TEST_DIR/gcloud.log" || {
  echo "state bucket inspection must use the raw JSON API resource" >&2
  exit 1
}

: >"$TEST_DIR/gcloud.log"
apply_json="$(run_bootstrap apply)"
jq -e '
  .mode == "apply" and
  .monitoring_notification_channel.name == "projects/123456789012/notificationChannels/42"
' <<<"$apply_json" >/dev/null

for key in production_ocr production_backup preview_deploy preview_ocr; do
  jq -e --arg key "$key" '.actions.identities[$key] == {service_account: "create", service_account_policy: "bind", pool: "create", provider: "create"}' \
    <<<"$apply_json" >/dev/null
done
jq -e --arg preview "$PREVIEW_MEMBER" '
  ([.bindings[] | select((.members // []) | index($preview)) | .role] | sort) == [
    "roles/compute.admin",
    "roles/iam.serviceAccountAdmin",
    "roles/iam.serviceAccountUser",
    "roles/iam.serviceAccountViewer",
    "roles/iam.workloadIdentityPoolViewer",
    "roles/pubsub.admin",
    "roles/resourcemanager.projectIamAdmin",
    "roles/run.admin",
    "roles/serviceusage.serviceUsageConsumer",
    "roles/storage.admin"
  ] and
  all(.bindings[] | select((.members // []) | index($preview)); .role != "roles/editor" and .role != "roles/owner" and .role != "roles/iam.securityAdmin" and .role != "roles/iam.roleAdmin")
' "$TEST_DIR/state/project-policy.json" >/dev/null
jq -e --arg preview "$PREVIEW_MEMBER" '
  [.bindings[] | select((.members // []) | index($preview)) | .role] | sort == [
    "roles/storage.legacyBucketReader",
    "roles/storage.objectAdmin"
  ]
' "$TEST_DIR/state/bucket-policy.json" >/dev/null
jq -e '
  any(.bindings[]; .role == "roles/viewer" and .condition == {
    title: "audited-window",
    description: "Retained verbatim",
    expression: "request.time < timestamp(\"2030-01-01T00:00:00Z\")"
  })
' "$TEST_DIR/state/project-policy.json" >/dev/null
jq -e '
  any(.bindings[]; .role == "roles/storage.objectViewer" and .condition == {
    title: "audited-prefix",
    description: "Retained verbatim",
    expression: "resource.name.startsWith(\"projects/_/buckets/scribe-test1-terraform/objects/audit/\")"
  })
' "$TEST_DIR/state/bucket-policy.json" >/dev/null
jq -e '.versioning.enabled == true and (.softDeletePolicy.retentionDurationSeconds | tonumber) >= 1209600' \
  "$TEST_DIR/state/bucket.json" >/dev/null

for email in "scribe-prod-ocr@${PROJECT}.iam.gserviceaccount.com" "scribe-preview-ocr@${PROJECT}.iam.gserviceaccount.com"; do
  member="serviceAccount:${email}"
  jq -e --arg member "$member" '[.bindings[] | select((.members // []) | index($member)) | .role] | sort == [
    "roles/iam.serviceAccountViewer",
    "roles/iam.workloadIdentityPoolViewer"
  ]' "$TEST_DIR/state/project-policy.json" >/dev/null
  jq -e --arg member "$member" '
    [.bindings[] | select((.members // []) | index($member)) | .role] == ["roles/artifactregistry.writer"]
  ' "$TEST_DIR/state/artifact-policies/us--internal.json" >/dev/null
done
jq -e --arg member "$PREVIEW_MEMBER" '
  [.bindings[] | select((.members // []) | index($member)) | .role] == ["roles/artifactregistry.repoAdmin"]
' "$TEST_DIR/state/artifact-policies/us--internal.json" >/dev/null

# A later production Terraform apply owns these two backup-specific project
# roles. Bootstrap must accept exactly them while still rejecting broader roles.
backup_member="serviceAccount:scribe-prod-backup@${PROJECT}.iam.gserviceaccount.com"
jq --arg member "$backup_member" --arg custom "projects/${PROJECT}/roles/scribeBackupRestoreVerifier" '
  .bindings += [
    {role: "roles/storagetransfer.viewer", members: [$member]},
    {role: $custom, members: [$member]}
  ] |
  .bindings |= sort_by(.role, (.condition.expression // ""))
' "$TEST_DIR/state/project-policy.json" >"$TEST_DIR/state/project-policy.tmp"
mv "$TEST_DIR/state/project-policy.tmp" "$TEST_DIR/state/project-policy.json"

: >"$TEST_DIR/gcloud.log"
second_plan="$(run_bootstrap plan)"
jq -e '
  .actions.project_policy == "reuse" and
  .actions.state_bucket == {name: "scribe-test1-terraform", versioning: "reuse", soft_delete: "reuse", policy: "reuse"} and
  .actions.notification_channel == "reuse" and
  .artifact_registry.repositories[0].action == "reuse" and
  all(.actions.identities[]; . == {service_account: "reuse", service_account_policy: "reuse", pool: "reuse", provider: "reuse"})
' <<<"$second_plan" >/dev/null
assert_no_mutation

cp "$TEST_DIR/state/project-policy.json" "$TEST_DIR/state/project-policy.valid.json"
jq --arg source "$SOURCE_MEMBER" '
  .version = 3 |
  .bindings |= map(if (.members | index($source)) and .role == "roles/editor"
    then .condition = {title: "main-only", expression: "request.time < timestamp(\"2030-01-01T00:00:00Z\")"}
    else . end)
' "$TEST_DIR/state/project-policy.valid.json" >"$TEST_DIR/state/project-policy.json"
if run_bootstrap plan >"$TEST_DIR/conditional-project.out" 2>"$TEST_DIR/conditional-project.err"; then
  echo "bootstrap accepted a conditional source project binding" >&2
  exit 1
fi
grep -F 'conditional project bindings' "$TEST_DIR/conditional-project.err" >/dev/null
mv "$TEST_DIR/state/project-policy.valid.json" "$TEST_DIR/state/project-policy.json"

cp "$TEST_DIR/state/bucket-policy.json" "$TEST_DIR/state/bucket-policy.valid.json"
jq --arg source "$SOURCE_MEMBER" '
  .version = 3 |
  .bindings |= map(if .members | index($source)
    then .condition = {title: "unsafe-copy", expression: "resource.name.startsWith(\"projects/_/buckets/scribe-test1-terraform\")"}
    else . end)
' "$TEST_DIR/state/bucket-policy.valid.json" >"$TEST_DIR/state/bucket-policy.json"
if run_bootstrap plan >"$TEST_DIR/conditional-bucket.out" 2>"$TEST_DIR/conditional-bucket.err"; then
  echo "bootstrap accepted a conditional source state-bucket binding" >&2
  exit 1
fi
grep -F 'conditional TF_STATE_BUCKET bindings' "$TEST_DIR/conditional-bucket.err" >/dev/null
mv "$TEST_DIR/state/bucket-policy.valid.json" "$TEST_DIR/state/bucket-policy.json"

cp "$TEST_DIR/state/project-policy.json" "$TEST_DIR/state/project-policy.valid.json"
jq --arg member "$backup_member" '
  .bindings += [{role: "roles/editor", members: [$member]}] |
  .bindings |= sort_by(.role, (.condition.expression // ""))
' "$TEST_DIR/state/project-policy.valid.json" >"$TEST_DIR/state/project-policy.json"
if run_bootstrap plan >"$TEST_DIR/backup-overprivileged.out" 2>"$TEST_DIR/backup-overprivileged.err"; then
  echo "bootstrap accepted an overprivileged backup identity" >&2
  exit 1
fi
grep -F 'outside the bootstrap and Terraform-managed backup roles' "$TEST_DIR/backup-overprivileged.err" >/dev/null
mv "$TEST_DIR/state/project-policy.valid.json" "$TEST_DIR/state/project-policy.json"

jq '. + [{name: "projects/scribe-test1/locations/us/repositories/unreviewed", format: "DOCKER"}]' \
  "$TEST_DIR/state/artifact-repositories.json" >"$TEST_DIR/state/artifact-repositories.tmp"
mv "$TEST_DIR/state/artifact-repositories.tmp" "$TEST_DIR/state/artifact-repositories.json"
jq -n --arg member "serviceAccount:scribe-prod-ocr@${PROJECT}.iam.gserviceaccount.com" '{
  version: 1,
  etag: "unreviewed-etag",
  bindings: [{role: "roles/artifactregistry.reader", members: [$member]}]
}' >"$TEST_DIR/state/artifact-policies/us--unreviewed.json"
if run_bootstrap plan >"$TEST_DIR/unreviewed-artifact.out" 2>"$TEST_DIR/unreviewed-artifact.err"; then
  echo "bootstrap accepted OCR access to a non-enumerated Artifact Registry repository" >&2
  exit 1
fi
grep -F 'access to non-enumerated GAR repository' "$TEST_DIR/unreviewed-artifact.err" >/dev/null

echo "External GCP identity bootstrap plans safely, applies exact WIF boundaries, and is idempotent."
