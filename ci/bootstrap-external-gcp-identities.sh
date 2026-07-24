#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly ROOT_DIR
readonly EXPECTED_REPOSITORY="lehigh-university-libraries/scribe"
readonly ATTRIBUTE_MAPPING="google.subject=assertion.sub,attribute.repository=assertion.repository,attribute.workflow_ref=assertion.workflow_ref,attribute.ref=assertion.ref,attribute.environment=assertion.environment"
readonly WIF_VIEWER_ROLE="roles/iam.workloadIdentityPoolViewer"
readonly SERVICE_ACCOUNT_VIEWER_ROLE="roles/iam.serviceAccountViewer"
readonly ARTIFACT_WRITER_ROLE="roles/artifactregistry.writer"
readonly PREVIEW_DEPLOY_ROLES_JSON='[
  "roles/compute.admin",
  "roles/iam.serviceAccountAdmin",
  "roles/iam.serviceAccountUser",
  "roles/pubsub.admin",
  "roles/resourcemanager.projectIamAdmin",
  "roles/run.admin",
  "roles/serviceusage.serviceUsageConsumer",
  "roles/storage.admin"
]'

fail() {
  echo "External GCP identity bootstrap failed: $*" >&2
  exit 1
}

usage() {
  cat >&2 <<'EOF'
Usage: GCLOUD_PROJECT=project-id SOURCE_DEPLOY_GSA=service-account-email \
         [SOURCE_DEPLOY_PROVIDER=full-provider-resource] \
         MONITORING_NOTIFICATION_EMAIL=operator@example.edu \
         [TF_STATE_BUCKET=bucket-name] bootstrap-external-gcp-identities.sh [plan|apply]

The default mode is plan. Plan performs every read-only safety check and emits
the exact resources and IAM role copies as JSON. Apply creates or reuses four
dedicated WIF identities, applies the reviewed bindings, optionally hardens and
shares the Terraform state bucket, and ensures the Scribe notification email.
EOF
}

mode="${1:-plan}"
case "$mode" in
  plan | apply) ;;
  -h | --help)
    usage
    exit 0
    ;;
  *)
    usage
    fail "mode must be plan or apply"
    ;;
esac
[ "$#" -le 1 ] || {
  usage
  fail "too many arguments"
}

: "${GCLOUD_PROJECT:?GCLOUD_PROJECT is required}"
: "${MONITORING_NOTIFICATION_EMAIL:?MONITORING_NOTIFICATION_EMAIL is required}"
SOURCE_DEPLOY_GSA="${SOURCE_DEPLOY_GSA:-${PRODUCTION_DEPLOY_GSA:-}}"
: "${SOURCE_DEPLOY_GSA:?SOURCE_DEPLOY_GSA is required}"
[[ "$GCLOUD_PROJECT" =~ ^[a-z][a-z0-9-]{4,28}[a-z0-9]$ ]] || fail "GCLOUD_PROJECT is not a valid project ID"
[[ "$SOURCE_DEPLOY_GSA" =~ ^[a-z][a-z0-9-]{4,28}[a-z0-9]@${GCLOUD_PROJECT}\.iam\.gserviceaccount\.com$ ]] ||
  fail "SOURCE_DEPLOY_GSA must be a service account in GCLOUD_PROJECT"
[[ "$MONITORING_NOTIFICATION_EMAIL" =~ ^[A-Za-z0-9][A-Za-z0-9._%+-]{0,63}@[A-Za-z0-9][A-Za-z0-9.-]{1,251}\.[A-Za-z]{2,63}$ ]] ||
  fail "MONITORING_NOTIFICATION_EMAIL is not a valid email address"
if [ -n "${TF_STATE_BUCKET:-}" ]; then
  [[ "$TF_STATE_BUCKET" =~ ^[a-z0-9][a-z0-9._-]{1,61}[a-z0-9]$ ]] || fail "TF_STATE_BUCKET is not a valid bucket name"
fi

for command in gcloud jq mktemp yq; do
  command -v "$command" >/dev/null 2>&1 || fail "$command is required"
done
[ -x "$ROOT_DIR/ci/verify-gcp-wif.sh" ] || fail "ci/verify-gcp-wif.sh must be executable"

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT
run_gcloud() {
  CLOUDSDK_CORE_DISABLE_PROMPTS=1 gcloud "$@" 2>"$tmp_dir/gcloud.stderr"
}

active_account="$(run_gcloud auth list --filter=status:ACTIVE --format='value(account)')" || fail "cannot inspect the active gcloud account"
[ "$active_account" = "$SOURCE_DEPLOY_GSA" ] ||
  fail "the active gcloud account must be exactly SOURCE_DEPLOY_GSA"

project_json="$(run_gcloud projects describe "$GCLOUD_PROJECT" --format=json)" || fail "cannot inspect GCLOUD_PROJECT"
project_number="$(jq -er '.projectNumber | tostring | select(test("^[0-9]+$"))' <<<"$project_json")" ||
  fail "project metadata does not contain a numeric project number"
backup_custom_role="projects/${GCLOUD_PROJECT}/roles/scribeBackupRestoreVerifier"
if [ -n "${SOURCE_DEPLOY_PROVIDER:-}" ]; then
  [[ "$SOURCE_DEPLOY_PROVIDER" =~ ^projects/${project_number}/locations/global/workloadIdentityPools/[a-z][a-z0-9-]{2,30}[a-z0-9]/providers/[a-z][a-z0-9-]{2,30}[a-z0-9]$ ]] ||
    fail "SOURCE_DEPLOY_PROVIDER must be a full provider resource in GCLOUD_PROJECT"
  WIF_EXPECTED_ENVIRONMENT=production \
    WIF_IDENTITY_CLASS=deploy \
    WIF_PROVIDER="$SOURCE_DEPLOY_PROVIDER" \
    WIF_SERVICE_ACCOUNT="$SOURCE_DEPLOY_GSA" \
    "$ROOT_DIR/ci/verify-gcp-wif.sh" >/dev/null 2>"$tmp_dir/wif.stderr" || fail "SOURCE_DEPLOY_PROVIDER does not satisfy the production deploy WIF contract"
fi

readonly provider_id="github-main"
readonly provider_location="global"
production_ocr_condition="$(WIF_EXPECTED_ENVIRONMENT=production WIF_IDENTITY_CLASS=ocr "$ROOT_DIR/ci/verify-gcp-wif.sh" --print-expected-condition)"
production_backup_condition="$(WIF_EXPECTED_ENVIRONMENT=production WIF_IDENTITY_CLASS=backup "$ROOT_DIR/ci/verify-gcp-wif.sh" --print-expected-condition)"
preview_deploy_condition="$(WIF_EXPECTED_ENVIRONMENT=preview WIF_IDENTITY_CLASS=deploy "$ROOT_DIR/ci/verify-gcp-wif.sh" --print-expected-condition)"
preview_ocr_condition="$(WIF_EXPECTED_ENVIRONMENT=preview WIF_IDENTITY_CLASS=ocr "$ROOT_DIR/ci/verify-gcp-wif.sh" --print-expected-condition)"
definitions_json="$(jq -cn \
  --arg project "$GCLOUD_PROJECT" \
  --arg number "$project_number" \
  --arg production_ocr_condition "$production_ocr_condition" \
  --arg production_backup_condition "$production_backup_condition" \
  --arg preview_deploy_condition "$preview_deploy_condition" \
  --arg preview_ocr_condition "$preview_ocr_condition" '
  [
    {key: "production_ocr", environment: "production", class: "ocr", account_id: "scribe-prod-ocr", pool_id: "scribe-production-ocr-wif", display_name: "Scribe production OCR publisher", attribute_condition: $production_ocr_condition},
    {key: "production_backup", environment: "production", class: "backup", account_id: "scribe-prod-backup", pool_id: "scribe-production-backup-wif", display_name: "Scribe prod backup verifier", attribute_condition: $production_backup_condition},
    {key: "preview_deploy", environment: "preview", class: "deploy", account_id: "scribe-preview-deploy", pool_id: "scribe-preview-deploy-wif", display_name: "Scribe preview deploy", attribute_condition: $preview_deploy_condition},
    {key: "preview_ocr", environment: "preview", class: "ocr", account_id: "scribe-preview-ocr", pool_id: "scribe-preview-ocr-wif", display_name: "Scribe preview OCR publisher", attribute_condition: $preview_ocr_condition}
  ]
  | map(. + {
      service_account: (.account_id + "@" + $project + ".iam.gserviceaccount.com"),
      pool: ("projects/" + $number + "/locations/global/workloadIdentityPools/" + .pool_id),
      provider: ("projects/" + $number + "/locations/global/workloadIdentityPools/" + .pool_id + "/providers/github-main")
    })
')" || fail "cannot construct the intended identity resources"
readonly definitions_json
jq -e '
  all(.[];
    (.account_id | test("^[a-z][a-z0-9-]{4,28}[a-z0-9]$")) and
    (.pool_id | test("^[a-z][a-z0-9-]{2,30}[a-z0-9]$")) and
    (.display_name | length >= 1 and length <= 32)
  )
' <<<"$definitions_json" >/dev/null || fail "generated identity names violate GCP resource limits"

service_accounts_json="$(run_gcloud iam service-accounts list --project="$GCLOUD_PROJECT" --format=json)" ||
  fail "cannot list service accounts"
jq -e 'type == "array"' <<<"$service_accounts_json" >/dev/null || fail "service-account list is not JSON"
jq -e --arg email "$SOURCE_DEPLOY_GSA" 'any(.[]; .email == $email and (.disabled // false) == false)' \
  <<<"$service_accounts_json" >/dev/null || fail "SOURCE_DEPLOY_GSA does not exist or is disabled"

pools_json="$(run_gcloud iam workload-identity-pools list \
  --location="$provider_location" \
  --project="$GCLOUD_PROJECT" \
  --show-deleted \
  --format=json)" || fail "cannot list workload identity pools"
jq -e 'type == "array"' <<<"$pools_json" >/dev/null || fail "workload identity pool list is not JSON"

project_policy="$(run_gcloud projects get-iam-policy "$GCLOUD_PROJECT" --format=json)" || fail "cannot inspect project IAM policy"
jq -e '.bindings | type == "array"' <<<"$project_policy" >/dev/null || fail "project IAM policy has no bindings array"
jq -e '.etag | type == "string" and length > 0' <<<"$project_policy" >/dev/null || fail "project IAM policy has no etag"
jq -e '([.bindings[]? | select(.condition != null)] | length == 0) or .version == 3' <<<"$project_policy" >/dev/null ||
  fail "project IAM policy contains conditions without policy version 3"

source_member="serviceAccount:${SOURCE_DEPLOY_GSA}"
preview_deploy_email="$(jq -er '.[] | select(.key == "preview_deploy") | .service_account' <<<"$definitions_json")"
preview_deploy_member="serviceAccount:${preview_deploy_email}"
source_project_bindings="$(jq -c --arg member "$source_member" '[
  .bindings[]? | select((.members // []) | index($member)) | {role, condition: (.condition // null)}
] | sort_by(.role)' <<<"$project_policy")" || fail "cannot inspect production deploy project roles"
[ "$(jq 'length' <<<"$source_project_bindings")" -gt 0 ] || fail "SOURCE_DEPLOY_GSA has no project IAM bindings to copy"
jq -e 'all(.[]; .condition == null)' <<<"$source_project_bindings" >/dev/null ||
  fail "SOURCE_DEPLOY_GSA has conditional project bindings; refusing to broaden or reinterpret them"
jq -e 'length == (unique_by(.role) | length)' <<<"$source_project_bindings" >/dev/null ||
  fail "SOURCE_DEPLOY_GSA has duplicate project role bindings"
source_project_roles="$(jq -c '[.[].role]' <<<"$source_project_bindings")"

fixed_roles_json="$(jq -cn \
  --arg wif "$WIF_VIEWER_ROLE" \
  --arg service_account "$SERVICE_ACCOUNT_VIEWER_ROLE" \
  --arg backup_custom "$backup_custom_role" \
  --argjson preview_deploy "$PREVIEW_DEPLOY_ROLES_JSON" \
  '{
    ocr: [$wif, $service_account] | sort,
    backup: [$wif, $service_account, "roles/storagetransfer.viewer", $backup_custom] | sort,
    preview_deploy: ($preview_deploy + [$wif, $service_account]) | unique | sort
  }')"

# Dedicated OCR publishers must not retain unrelated project permissions.
while IFS=$'\t' read -r key identity_class email; do
  member="serviceAccount:${email}"
  existing="$(jq -c --arg member "$member" '[
    .bindings[]? | select((.members // []) | index($member)) | {role, condition: (.condition // null)}
  ] | sort_by(.role)' <<<"$project_policy")"
  case "$identity_class" in
    ocr)
      jq -e --argjson allowed "$(jq -c '.ocr' <<<"$fixed_roles_json")" '
        all(.[]; .condition == null and (.role as $role | $allowed | index($role)))
      ' <<<"$existing" >/dev/null || fail "$key has project permissions outside the reviewed OCR publisher roles"
      ;;
    deploy)
      jq -e --argjson allowed "$(jq -c '.preview_deploy' <<<"$fixed_roles_json")" '
        all(.[]; .condition == null and (.role as $role | $allowed | index($role)))
      ' <<<"$existing" >/dev/null || fail "$key has project permissions outside the explicit preview deploy allowlist"
      ;;
    backup)
      jq -e --argjson allowed "$(jq -c '.backup' <<<"$fixed_roles_json")" '
        all(.[]; .condition == null and (.role as $role | $allowed | index($role)))
      ' <<<"$existing" >/dev/null || fail "$key has project permissions outside the bootstrap and Terraform-managed backup roles"
      ;;
  esac
done < <(jq -r '.[] | [.key, .class, .service_account] | @tsv' <<<"$definitions_json")

# shellcheck disable=SC2016 # This is a jq program; its variables must reach jq unchanged.
add_unconditional_member='def add_member($role; $member):
  if any(.bindings[]?; .role == $role and (.condition // null) == null) then
    .bindings |= map(if .role == $role and (.condition // null) == null
      then .members = (((.members // []) + [$member]) | unique)
      else . end)
  else
    .bindings += [{role: $role, members: [$member]}]
  end;'

project_policy_next="$(jq \
  --arg preview "$preview_deploy_member" \
  --arg prod_ocr "serviceAccount:$(jq -er '.[] | select(.key == "production_ocr") | .service_account' <<<"$definitions_json")" \
  --arg preview_ocr "serviceAccount:$(jq -er '.[] | select(.key == "preview_ocr") | .service_account' <<<"$definitions_json")" \
  --arg backup "serviceAccount:$(jq -er '.[] | select(.key == "production_backup") | .service_account' <<<"$definitions_json")" \
  --arg wif "$WIF_VIEWER_ROLE" \
  --arg service_account "$SERVICE_ACCOUNT_VIEWER_ROLE" \
  --argjson preview_roles "$(jq -c '.preview_deploy' <<<"$fixed_roles_json")" \
  "$add_unconditional_member
  add_member(\$wif; \$prod_ocr)
  | add_member(\$service_account; \$prod_ocr)
  | add_member(\$wif; \$preview_ocr)
  | add_member(\$service_account; \$preview_ocr)
  | add_member(\$wif; \$backup)
  | add_member(\$service_account; \$backup)
  | reduce \$preview_roles[] as \$role (. ; add_member(\$role; \$preview))
  | .bindings |= sort_by(.role, (.condition.expression // \"\"))" <<<"$project_policy")" || fail "cannot build the project IAM update"

if [ "$(jq -S -c . <<<"$project_policy")" = "$(jq -S -c . <<<"$project_policy_next")" ]; then
  project_policy_action="reuse"
else
  project_policy_action="update"
fi

bucket_json='null'
bucket_policy='null'
bucket_policy_next='null'
bucket_role_copies='[]'
bucket_versioning_action='not_requested'
bucket_soft_delete_action='not_requested'
bucket_policy_action='not_requested'
state_bucket_preview_roles='["roles/storage.legacyBucketReader","roles/storage.objectAdmin"]'
if [ -n "${TF_STATE_BUCKET:-}" ]; then
  # The Cloud SDK's normalized bucket shape omits projectNumber in recent
  # releases. Use the documented raw JSON API resource so ownership and the
  # retention fields are both stable and can be checked before any mutation.
  bucket_json="$(run_gcloud storage buckets describe "gs://${TF_STATE_BUCKET}" --raw --format=json)" || fail "cannot inspect TF_STATE_BUCKET"
  bucket_project_number="$(jq -er '.projectNumber | tostring | select(test("^[0-9]+$"))' <<<"$bucket_json")" ||
    fail "TF_STATE_BUCKET metadata has no project number"
  [ "$bucket_project_number" = "$project_number" ] || fail "TF_STATE_BUCKET belongs to a different project"
  bucket_policy="$(run_gcloud storage buckets get-iam-policy "gs://${TF_STATE_BUCKET}" --format=json)" || fail "cannot inspect TF_STATE_BUCKET IAM policy"
  jq -e '.bindings | type == "array"' <<<"$bucket_policy" >/dev/null || fail "TF_STATE_BUCKET IAM policy has no bindings array"
  jq -e '.etag | type == "string" and length > 0' <<<"$bucket_policy" >/dev/null || fail "TF_STATE_BUCKET IAM policy has no etag"
  jq -e '([.bindings[]? | select(.condition != null)] | length == 0) or .version == 3' <<<"$bucket_policy" >/dev/null ||
    fail "TF_STATE_BUCKET IAM policy contains conditions without policy version 3"
  bucket_role_copies="$(jq -c --arg member "$source_member" '[
    .bindings[]? | select((.members // []) | index($member)) | {role, condition: (.condition // null)}
  ] | sort_by(.role)' <<<"$bucket_policy")"
  jq -e 'all(.[]; .condition == null)' <<<"$bucket_role_copies" >/dev/null ||
    fail "SOURCE_DEPLOY_GSA has conditional TF_STATE_BUCKET bindings; refusing to broaden or reinterpret them"
  jq -e 'length == (unique_by(.role) | length)' <<<"$bucket_role_copies" >/dev/null ||
    fail "SOURCE_DEPLOY_GSA has duplicate TF_STATE_BUCKET role bindings"
  preview_bucket_bindings="$(jq -c --arg member "$preview_deploy_member" '[
    .bindings[]? | select((.members // []) | index($member)) | {role, condition: (.condition // null)}
  ] | sort_by(.role)' <<<"$bucket_policy")"
  jq -e --argjson allowed "$state_bucket_preview_roles" '
    all(.[]; .condition == null and (.role as $role | $allowed | index($role)))
  ' <<<"$preview_bucket_bindings" >/dev/null ||
    fail "preview deploy identity has TF_STATE_BUCKET permissions outside the explicit state-backend allowlist"
  bucket_policy_next="$(jq --arg preview "$preview_deploy_member" --argjson roles "$state_bucket_preview_roles" \
    "$add_unconditional_member
    reduce \$roles[] as \$role (. ; add_member(\$role; \$preview))
    | .bindings |= sort_by(.role, (.condition.expression // \"\"))
  " <<<"$bucket_policy")"
  bucket_policy_action="update"
  [ "$(jq -S -c . <<<"$bucket_policy")" != "$(jq -S -c . <<<"$bucket_policy_next")" ] || bucket_policy_action="reuse"
  if jq -e '.versioning.enabled == true' <<<"$bucket_json" >/dev/null; then
    bucket_versioning_action="reuse"
  else
    bucket_versioning_action="enable"
  fi
  retention_seconds="$(jq -r '(.softDeletePolicy.retentionDurationSeconds // 0) | tonumber' <<<"$bucket_json")" ||
    fail "TF_STATE_BUCKET soft-delete retention is invalid"
  if [ "$retention_seconds" -ge 1209600 ]; then
    bucket_soft_delete_action="reuse"
  else
    bucket_soft_delete_action="set_14d"
  fi
fi

ocr_publish_repositories="$(yq -o=json -I=0 '.artifact_registry.ocr_publish_repositories // []' "$ROOT_DIR/config/ocr.yaml")" ||
  fail "cannot read artifact_registry.ocr_publish_repositories from config/ocr.yaml"
jq -e '
  type == "array" and length > 0 and
  all(.[];
    (keys | sort) == ["location", "repository"] and
    (.location | type == "string" and test("^[a-z][a-z0-9-]{0,62}$")) and
    (.repository | type == "string" and test("^[a-z][a-z0-9._-]{1,62}[a-z0-9]$"))
  ) and
  length == (unique_by(.location, .repository) | length)
' <<<"$ocr_publish_repositories" >/dev/null ||
  fail "config/ocr.yaml must enumerate unique, valid OCR Artifact Registry repositories"

artifact_repositories_json="$(run_gcloud artifacts repositories list --project="$GCLOUD_PROJECT" --format=json)" ||
  fail "cannot list Artifact Registry repositories"
jq -e 'type == "array"' <<<"$artifact_repositories_json" >/dev/null || fail "Artifact Registry repository list is not JSON"

while IFS=$'\t' read -r configured_location configured_repository; do
  match_count="$(jq --arg suffix "/locations/${configured_location}/repositories/${configured_repository}" '[.[] | select(.name | endswith($suffix))] | length' <<<"$artifact_repositories_json")"
  [ "$match_count" -eq 1 ] || fail "configured OCR repository ${configured_location}/${configured_repository} must resolve exactly once"
done < <(jq -r '.[] | [.location, .repository] | @tsv' <<<"$ocr_publish_repositories")

production_ocr_member="serviceAccount:$(jq -er '.[] | select(.key == "production_ocr") | .service_account' <<<"$definitions_json")"
preview_ocr_member="serviceAccount:$(jq -er '.[] | select(.key == "preview_ocr") | .service_account' <<<"$definitions_json")"
artifact_writer_members="$(jq -cn --arg prod "$production_ocr_member" --arg preview "$preview_ocr_member" '[$prod, $preview] | sort')"
artifact_repo_admin_members="$(jq -cn --arg deploy "$preview_deploy_member" '[$deploy]')"
artifact_publisher_members="$(jq -cn --argjson writers "$artifact_writer_members" --argjson admins "$artifact_repo_admin_members" '$writers + $admins')"
preview_artifact_policy_role="projects/${GCLOUD_PROJECT}/roles/scribePreviewArtifactPolicy"
artifact_repository_actions='[]'
while IFS=$'\t' read -r repository_name repository_format; do
  [[ "$repository_name" =~ /locations/([a-z][a-z0-9-]{0,62})/repositories/([a-z][a-z0-9._-]{1,62}[a-z0-9])$ ]] ||
    fail "Artifact Registry returned an invalid repository resource"
  repository_location="${BASH_REMATCH[1]}"
  repository_id="${BASH_REMATCH[2]}"
  configured=false
  if jq -e --arg location "$repository_location" --arg repository "$repository_id" \
    'any(.[]; .location == $location and .repository == $repository)' <<<"$ocr_publish_repositories" >/dev/null; then
    configured=true
    [ "$repository_format" = "DOCKER" ] || fail "configured OCR repository ${repository_location}/${repository_id} is not a Docker repository"
  fi
  artifact_policy="$(run_gcloud artifacts repositories get-iam-policy "$repository_id" \
    --location="$repository_location" --project="$GCLOUD_PROJECT" --format=json)" ||
    fail "cannot inspect Artifact Registry IAM for ${repository_location}/${repository_id}"
  jq -e '.bindings | type == "array"' <<<"$artifact_policy" >/dev/null || fail "Artifact Registry IAM policy has no bindings array"
  jq -e '.etag | type == "string" and length > 0' <<<"$artifact_policy" >/dev/null || fail "Artifact Registry IAM policy has no etag"
  jq -e '([.bindings[]? | select(.condition != null)] | length == 0) or .version == 3' <<<"$artifact_policy" >/dev/null ||
    fail "Artifact Registry IAM policy contains conditions without policy version 3"
  existing_publisher_bindings="$(jq -c --argjson members "$artifact_publisher_members" '[
    .bindings[]? as $binding |
    $binding.members[]? as $member |
    select($members | index($member)) |
    {member: $member, role: $binding.role, condition: ($binding.condition // null)}
  ]' <<<"$artifact_policy")"
  if [ "$configured" = false ]; then
    [ "$(jq 'length' <<<"$existing_publisher_bindings")" -eq 0 ] ||
      fail "OCR or preview deploy identity has access to non-enumerated GAR repository ${repository_location}/${repository_id}"
    continue
  fi
  jq -e \
    --arg writer "$ARTIFACT_WRITER_ROLE" \
    --arg admin "roles/artifactregistry.repoAdmin" \
    --arg policy_manager "$preview_artifact_policy_role" \
    --arg deploy "$preview_deploy_member" '
    all(.[];
      .condition == null and
      (if .member == $deploy
       then (.role == $admin or .role == $policy_manager)
       else .role == $writer
       end)
    )
  ' \
    <<<"$existing_publisher_bindings" >/dev/null ||
    fail "OCR or preview deploy identity has an unexpected role on ${repository_location}/${repository_id}"
  artifact_policy_next="$(jq \
    --argjson writers "$artifact_writer_members" \
    --argjson admins "$artifact_repo_admin_members" \
    --arg writer "$ARTIFACT_WRITER_ROLE" \
    --arg admin "roles/artifactregistry.repoAdmin" \
    "$add_unconditional_member
    reduce \$writers[] as \$member (. ; add_member(\$writer; \$member))
    | reduce \$admins[] as \$member (. ; add_member(\$admin; \$member))
    | .bindings |= sort_by(.role, (.condition.expression // \"\"))
  " <<<"$artifact_policy")"
  artifact_action=update
  [ "$(jq -S -c . <<<"$artifact_policy")" != "$(jq -S -c . <<<"$artifact_policy_next")" ] || artifact_action=reuse
  artifact_policy_file="$tmp_dir/artifact-${repository_location}-${repository_id}.json"
  jq . <<<"$artifact_policy_next" >"$artifact_policy_file"
  artifact_repository_actions="$(jq -c \
    --arg location "$repository_location" \
    --arg repository "$repository_id" \
    --arg action "$artifact_action" \
    --arg policy_file "$artifact_policy_file" \
    --argjson writers "$artifact_writer_members" \
    --argjson admins "$artifact_repo_admin_members" \
    '. + [{location: $location, repository: $repository, action: $action, policy_file: $policy_file, writer_members: $writers, repo_admin_members: $admins}]' \
    <<<"$artifact_repository_actions")"
done < <(jq -r '.[] | [.name, (.format // "")] | @tsv' <<<"$artifact_repositories_json")

channels_json="$(run_gcloud beta monitoring channels list --project="$GCLOUD_PROJECT" --format=json)" ||
  fail "cannot list Cloud Monitoring notification channels"
jq -e 'type == "array"' <<<"$channels_json" >/dev/null || fail "notification-channel list is not JSON"
matching_enabled_channels="$(jq -c --arg email "$MONITORING_NOTIFICATION_EMAIL" '[
  .[] | select(.type == "email" and .labels.email_address == $email and (.enabled // true) == true)
]' <<<"$channels_json")"
matching_disabled_channels="$(jq -c --arg email "$MONITORING_NOTIFICATION_EMAIL" '[
  .[] | select(.type == "email" and .labels.email_address == $email and (.enabled // true) == false)
]' <<<"$channels_json")"
[ "$(jq 'length' <<<"$matching_enabled_channels")" -le 1 ] || fail "multiple enabled notification channels use ${MONITORING_NOTIFICATION_EMAIL}"
if [ "$(jq 'length' <<<"$matching_enabled_channels")" -eq 1 ]; then
  notification_action="reuse"
  notification_channel="$(jq -er '.[0].name' <<<"$matching_enabled_channels")"
elif [ "$(jq 'length' <<<"$matching_disabled_channels")" -eq 1 ]; then
  notification_action="enable"
  notification_channel="$(jq -er '.[0].name' <<<"$matching_disabled_channels")"
elif [ "$(jq 'length' <<<"$matching_disabled_channels")" -eq 0 ]; then
  notification_action="create"
  notification_channel=""
else
  fail "multiple disabled notification channels use ${MONITORING_NOTIFICATION_EMAIL}"
fi

identity_actions='{}'
while IFS=$'\t' read -r key email pool_name provider_name; do
  account_action="create"
  account_policy_action="bind"
  if jq -e --arg email "$email" 'any(.[]; .email == $email and (.disabled // false) == false)' <<<"$service_accounts_json" >/dev/null; then
    account_action="reuse"
    account_policy="$(run_gcloud iam service-accounts get-iam-policy "$email" --project="$GCLOUD_PROJECT" --format=json)" ||
      fail "cannot inspect ${key} service-account IAM policy"
    expected_member="principalSet://iam.googleapis.com/projects/${project_number}/locations/global/workloadIdentityPools/$(jq -er --arg key "$key" '.[] | select(.key == $key) | .pool_id' <<<"$definitions_json")/attribute.repository/${EXPECTED_REPOSITORY}"
    if jq -e '(.bindings // []) | length == 0' <<<"$account_policy" >/dev/null; then
      account_policy_action="bind"
    elif jq -e --arg member "$expected_member" '
      ([.bindings[]? | {role, members: ((.members // []) | sort), condition: (.condition // null)}] | sort_by(.role)) ==
      [{role: "roles/iam.workloadIdentityUser", members: [$member], condition: null}]
    ' <<<"$account_policy" >/dev/null; then
      account_policy_action="reuse"
    else
      fail "${key} service account has an unexpected IAM policy"
    fi
  elif jq -e --arg email "$email" 'any(.[]; .email == $email)' <<<"$service_accounts_json" >/dev/null; then
    fail "${key} service account exists but is disabled"
  fi

  pool_action="create"
  pool_state="$(jq -r --arg name "$pool_name" '[.[] | select(.name == $name)] | if length == 0 then "missing" elif length == 1 then (.[0].state // "ACTIVE") else "duplicate" end' <<<"$pools_json")"
  case "$pool_state" in
    missing) ;;
    ACTIVE) pool_action="reuse" ;;
    duplicate) fail "multiple workload identity pools resolved to ${pool_name}" ;;
    *) fail "${key} workload identity pool is not active; deleted or disabled pool IDs are not reused" ;;
  esac

  provider_action="create"
  if [ "$pool_action" = "reuse" ]; then
    pool_id="${pool_name##*/}"
    providers_json="$(run_gcloud iam workload-identity-pools providers list \
      --workload-identity-pool="$pool_id" \
      --location="$provider_location" \
      --project="$GCLOUD_PROJECT" \
      --show-deleted \
      --format=json)" || fail "cannot list providers for ${key}"
    active_other_count="$(jq --arg name "$provider_name" '[.[] | select((.state // "ACTIVE") == "ACTIVE" and (.disabled // false) == false and .name != $name)] | length' <<<"$providers_json")"
    [ "$active_other_count" -eq 0 ] || fail "${key} pool contains another active provider"
    provider_matches="$(jq -c --arg name "$provider_name" '[.[] | select(.name == $name)]' <<<"$providers_json")"
    [ "$(jq 'length' <<<"$provider_matches")" -le 1 ] || fail "multiple providers resolved to ${provider_name}"
    if [ "$(jq 'length' <<<"$provider_matches")" -eq 1 ]; then
      condition="$(jq -er --arg key "$key" '.[] | select(.key == $key) | .attribute_condition' <<<"$definitions_json")"
      jq -e --arg condition "$condition" '
        .[0] |
        (.state // "ACTIVE") == "ACTIVE" and
        (.disabled // false) == false and
        .oidc.issuerUri == "https://token.actions.githubusercontent.com" and
        (.oidc.allowedAudiences // []) == [] and
        .attributeMapping == {
          "google.subject": "assertion.sub",
          "attribute.environment": "assertion.environment",
          "attribute.ref": "assertion.ref",
          "attribute.repository": "assertion.repository",
          "attribute.workflow_ref": "assertion.workflow_ref"
        } and
        ((.attributeCondition | gsub("[[:space:]]"; "")) == ($condition | gsub("[[:space:]]"; "")))
      ' <<<"$provider_matches" >/dev/null || fail "${key} provider exists with unreviewed configuration"
      provider_action="reuse"
    fi
  fi
  identity_actions="$(jq -c --arg key "$key" --arg account "$account_action" --arg account_policy "$account_policy_action" --arg pool "$pool_action" --arg provider "$provider_action" \
    '. + {($key): {service_account: $account, service_account_policy: $account_policy, pool: $pool, provider: $provider}}' <<<"$identity_actions")"
done < <(jq -r '.[] | [.key, .service_account, .pool, .provider] | @tsv' <<<"$definitions_json")

emit_output() {
  jq -n \
    --arg mode "$mode" \
    --arg project_id "$GCLOUD_PROJECT" \
    --arg project_number "$project_number" \
    --arg source_deploy_provider "${SOURCE_DEPLOY_PROVIDER:-}" \
    --arg source_deploy_gsa "$SOURCE_DEPLOY_GSA" \
    --argjson definitions "$definitions_json" \
    --argjson identity_actions "$identity_actions" \
    --argjson project_role_copies "$source_project_roles" \
    --argjson fixed_roles "$fixed_roles_json" \
    --argjson state_bucket_preview_roles "$state_bucket_preview_roles" \
    --argjson artifact_repository_actions "$artifact_repository_actions" \
    --arg project_policy_action "$project_policy_action" \
    --arg state_bucket "${TF_STATE_BUCKET:-}" \
    --argjson bucket_role_bindings "$bucket_role_copies" \
    --arg bucket_versioning_action "$bucket_versioning_action" \
    --arg bucket_soft_delete_action "$bucket_soft_delete_action" \
    --arg bucket_policy_action "$bucket_policy_action" \
    --arg notification_email "$MONITORING_NOTIFICATION_EMAIL" \
    --arg notification_channel "$notification_channel" \
    --arg notification_action "$notification_action" '
      {
        mode: $mode,
        project_id: $project_id,
        project_number: $project_number,
        source_deploy: {
          provider: (if $source_deploy_provider == "" then null else $source_deploy_provider end),
          service_account: $source_deploy_gsa
        },
        identities: ($definitions | map({key, environment, class, provider, service_account, attribute_condition}) | map({key: .key, value: del(.key)}) | from_entries),
        attribute_mapping: {
          "google.subject": "assertion.sub",
          "attribute.environment": "assertion.environment",
          "attribute.ref": "assertion.ref",
          "attribute.repository": "assertion.repository",
          "attribute.workflow_ref": "assertion.workflow_ref"
        },
        project_role_grants: {
          production_ocr: $fixed_roles.ocr,
          production_backup: $fixed_roles.ocr,
          preview_deploy: $fixed_roles.preview_deploy,
          preview_ocr: $fixed_roles.ocr
        },
        permitted_existing_backup_project_roles: $fixed_roles.backup,
        actions: {
          identities: $identity_actions,
          project_policy: $project_policy_action,
          state_bucket: {
            name: (if $state_bucket == "" then null else $state_bucket end),
            versioning: $bucket_versioning_action,
            soft_delete: $bucket_soft_delete_action,
            policy: $bucket_policy_action
          },
          notification_channel: $notification_action
        },
        source_deploy_observed_roles: {
          project_roles: $project_role_copies,
          state_bucket_bindings: $bucket_role_bindings
        },
        preview_deploy_explicit_grants: {
          project_roles: $fixed_roles.preview_deploy,
          state_bucket_roles: $state_bucket_preview_roles
        },
        artifact_registry: {
          repositories: ($artifact_repository_actions | map(del(.policy_file)))
        },
        monitoring_notification_channel: {
          email: $notification_email,
          name: (if $notification_channel == "" then null else $notification_channel end)
        }
      }
    '
}

if [ "$mode" = "plan" ]; then
  emit_output
  exit 0
fi

while IFS=$'\t' read -r key account_id email pool_id display_name environment_name identity_class; do
  account_action="$(jq -er --arg key "$key" '.[$key].service_account' <<<"$identity_actions")"
  pool_action="$(jq -er --arg key "$key" '.[$key].pool' <<<"$identity_actions")"
  provider_action="$(jq -er --arg key "$key" '.[$key].provider' <<<"$identity_actions")"
  if [ "$account_action" = "create" ]; then
    run_gcloud iam service-accounts create "$account_id" \
      --project="$GCLOUD_PROJECT" \
      --display-name="$display_name" \
      --description="Dedicated external GitHub Actions identity for Scribe ${environment_name} ${identity_class}." \
      --quiet >/dev/null || fail "cannot create ${key} service account"
  fi
  if [ "$pool_action" = "create" ]; then
    run_gcloud iam workload-identity-pools create "$pool_id" \
      --location="$provider_location" \
      --project="$GCLOUD_PROJECT" \
      --display-name="$display_name" \
      --description="Dedicated Scribe ${environment_name} ${identity_class} GitHub Actions trust boundary." \
      --quiet >/dev/null || fail "cannot create ${key} workload identity pool"
  fi
  if [ "$provider_action" = "create" ]; then
    condition="$(jq -er --arg key "$key" '.[] | select(.key == $key) | .attribute_condition' <<<"$definitions_json")"
    run_gcloud iam workload-identity-pools providers create-oidc "$provider_id" \
      --workload-identity-pool="$pool_id" \
      --location="$provider_location" \
      --project="$GCLOUD_PROJECT" \
      --display-name="GitHub main" \
      --issuer-uri="https://token.actions.githubusercontent.com" \
      --attribute-mapping="$ATTRIBUTE_MAPPING" \
      --attribute-condition="$condition" \
      --quiet >/dev/null || fail "cannot create ${key} provider"
  fi
  expected_member="principalSet://iam.googleapis.com/projects/${project_number}/locations/global/workloadIdentityPools/${pool_id}/attribute.repository/${EXPECTED_REPOSITORY}"
  account_policy="$(run_gcloud iam service-accounts get-iam-policy "$email" --project="$GCLOUD_PROJECT" --format=json)" ||
    fail "cannot inspect newly available ${key} service-account policy"
  if ! jq -e --arg member "$expected_member" '
    ([.bindings[]? | {role, members: ((.members // []) | sort), condition: (.condition // null)}] | sort_by(.role)) ==
    [{role: "roles/iam.workloadIdentityUser", members: [$member], condition: null}]
  ' <<<"$account_policy" >/dev/null; then
    jq --arg member "$expected_member" '
      .bindings = [{role: "roles/iam.workloadIdentityUser", members: [$member]}]
      | .version = 1
    ' <<<"$account_policy" >"$tmp_dir/${key}-service-account-policy.json"
    run_gcloud iam service-accounts set-iam-policy "$email" "$tmp_dir/${key}-service-account-policy.json" \
      --project="$GCLOUD_PROJECT" --quiet >/dev/null || fail "cannot bind ${key} service account to its dedicated pool"
  fi
done < <(jq -r '.[] | [.key, .account_id, .service_account, .pool_id, .display_name, .environment, .class] | @tsv' <<<"$definitions_json")

if [ "$project_policy_action" = "update" ]; then
  jq . <<<"$project_policy_next" >"$tmp_dir/project-policy.json"
  run_gcloud projects set-iam-policy "$GCLOUD_PROJECT" "$tmp_dir/project-policy.json" --quiet >/dev/null ||
    fail "cannot atomically apply project IAM roles; rerun plan to refresh the policy etag"
fi

if [ -n "${TF_STATE_BUCKET:-}" ]; then
  bucket_update=(storage buckets update "gs://${TF_STATE_BUCKET}")
  [ "$bucket_versioning_action" != "enable" ] || bucket_update+=(--versioning)
  [ "$bucket_soft_delete_action" != "set_14d" ] || bucket_update+=(--soft-delete-duration=14d)
  if [ "${#bucket_update[@]}" -gt 4 ]; then
    run_gcloud "${bucket_update[@]}" --quiet >/dev/null || fail "cannot harden TF_STATE_BUCKET retention"
  fi
  if [ "$bucket_policy_action" = "update" ]; then
    fresh_bucket_policy="$(run_gcloud storage buckets get-iam-policy "gs://${TF_STATE_BUCKET}" --format=json)" ||
      fail "cannot refresh TF_STATE_BUCKET IAM policy after retention hardening"
    jq -e --argjson planned "$bucket_policy" '
      (.bindings | type == "array") and
      (.etag | type == "string" and length > 0) and
      (del(.etag) == ($planned | del(.etag)))
    ' <<<"$fresh_bucket_policy" >/dev/null ||
      fail "TF_STATE_BUCKET IAM policy changed during bootstrap; rerun plan before applying"
    fresh_bucket_etag="$(jq -er '.etag' <<<"$fresh_bucket_policy")"
    jq --arg etag "$fresh_bucket_etag" '.etag = $etag' <<<"$bucket_policy_next" >"$tmp_dir/bucket-policy.json"
    run_gcloud storage buckets set-iam-policy "gs://${TF_STATE_BUCKET}" "$tmp_dir/bucket-policy.json" --quiet >/dev/null ||
      fail "cannot atomically copy TF_STATE_BUCKET roles; rerun plan to refresh the policy etag"
  fi
fi

while IFS=$'\t' read -r repository_location repository_id artifact_action artifact_policy_file; do
  [ "$artifact_action" != "update" ] ||
    run_gcloud artifacts repositories set-iam-policy "$repository_id" "$artifact_policy_file" \
      --location="$repository_location" --project="$GCLOUD_PROJECT" --quiet >/dev/null ||
    fail "cannot atomically apply Artifact Registry IAM for ${repository_location}/${repository_id}; rerun plan to refresh the policy etag"
done < <(jq -r '.[] | [.location, .repository, .action, .policy_file] | @tsv' <<<"$artifact_repository_actions")

case "$notification_action" in
  create)
    channel_json="$(run_gcloud beta monitoring channels create \
      --project="$GCLOUD_PROJECT" \
      --display-name="Scribe production alerts" \
      --description="Operator notifications for Scribe production availability, capacity, and recovery alerts." \
      --type=email \
      --channel-labels="email_address=${MONITORING_NOTIFICATION_EMAIL}" \
      --enabled \
      --format=json \
      --quiet)" || fail "cannot create the notification channel"
    notification_channel="$(jq -er --arg email "$MONITORING_NOTIFICATION_EMAIL" 'select(.type == "email" and .labels.email_address == $email and (.enabled // true) == true) | .name' <<<"$channel_json")" ||
      fail "created notification channel does not match the reviewed email"
    ;;
  enable)
    channel_json="$(run_gcloud beta monitoring channels update "$notification_channel" \
      --project="$GCLOUD_PROJECT" --enabled --format=json --quiet)" || fail "cannot enable the notification channel"
    jq -e --arg name "$notification_channel" --arg email "$MONITORING_NOTIFICATION_EMAIL" '
      .name == $name and .type == "email" and .labels.email_address == $email and (.enabled // true) == true
    ' <<<"$channel_json" >/dev/null || fail "enabled notification channel does not match the reviewed email"
    ;;
  reuse) ;;
esac

while IFS=$'\t' read -r environment_name identity_class provider email; do
  WIF_EXPECTED_ENVIRONMENT="$environment_name" \
    WIF_IDENTITY_CLASS="$identity_class" \
    WIF_PROVIDER="$provider" \
    WIF_SERVICE_ACCOUNT="$email" \
    "$ROOT_DIR/ci/verify-gcp-wif.sh" >/dev/null 2>"$tmp_dir/wif.stderr" || fail "post-apply verification failed for ${environment_name}/${identity_class}"
done < <(jq -r '.[] | [.environment, .class, .provider, .service_account] | @tsv' <<<"$definitions_json")

emit_output
